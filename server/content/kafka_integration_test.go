// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

//go:build integration

// Against throwaway topics on a running broker (`make up`, then
// `make test-integration`). Build-tagged so `make test` never needs a broker.
//
// The unit tests cover every rejection, because a rejection is a property of a
// manifest and not of Kafka. What only a broker can show is the part this file
// tests: that a *compacted, keyed, multi-partition* topic scans to the newest
// value per key, that a pointer move is seen by a watcher, and that the
// on-disk cache lets a second resolve never touch the blob topic at all.
package content

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"

	contentv1 "github.com/valesordev/andara/gen/go/andara/content/v1"
)

func brokers(t *testing.T) []string {
	t.Helper()
	v := os.Getenv("ANDARA_KAFKA_BROKERS")
	if v == "" {
		t.Skip("ANDARA_KAFKA_BROKERS not set; run `make up` and `make test-integration`")
	}
	return strings.Split(v, ",")
}

// throwawayTopics creates the three content topics under a test-local name,
// compacted and multi-partition like the real ones, deleted when the test ends.
func throwawayTopics(t *testing.T) (Topics, *kgo.Client) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cl, err := kgo.NewClient(kgo.SeedBrokers(brokers(t)...))
	if err != nil {
		t.Fatal(err)
	}
	adm := kadm.NewClient(cl)
	stamp := time.Now().UnixNano()
	tp := Topics{
		Blobs:    fmt.Sprintf("andara.test.content.blobs.%d", stamp),
		Versions: fmt.Sprintf("andara.test.content.versions.%d", stamp),
		Active:   fmt.Sprintf("andara.test.content.active.%d", stamp),
	}
	configs := map[string]*string{"cleanup.policy": kadm.StringPtr("compact")}
	// Six partitions, as deploy/kafka/topics.yaml declares: a single-partition
	// test would not exercise the per-partition end-offset rule the scan
	// terminates on.
	for _, name := range []string{tp.Blobs, tp.Versions, tp.Active} {
		if _, err := adm.CreateTopic(ctx, 6, 1, configs, name); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		dctx, dcancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer dcancel()
		for _, name := range []string{tp.Blobs, tp.Versions, tp.Active} {
			_, _ = adm.DeleteTopic(dctx, name)
		}
		cl.Close()
	})
	return tp, cl
}

// publisher writes into the throwaway topics the way AW-SRV-013 will.
type publisher struct {
	t      *testing.T
	cl     *kgo.Client
	topics Topics
}

func (p *publisher) produce(topic string, key string, value []byte) {
	p.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res := p.cl.ProduceSync(ctx, &kgo.Record{Topic: topic, Key: []byte(key), Value: value})
	if err := res.FirstErr(); err != nil {
		p.t.Fatal(err)
	}
}

func (p *publisher) publish(pack string, version, core uint64, files map[string]string) {
	p.t.Helper()
	mf := &contentv1.ContentVersion{PackId: pack, Version: version, CoreVersion: core}
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		body := []byte(files[path])
		sum := sha256.Sum256(body)
		blob, err := proto.Marshal(&contentv1.Blob{Hash: sum[:], Body: body, MediaType: "application/json"})
		if err != nil {
			p.t.Fatal(err)
		}
		// The key is the raw hash, exactly as content.proto says.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		res := p.cl.ProduceSync(ctx, &kgo.Record{Topic: p.topics.Blobs, Key: sum[:], Value: blob})
		cancel()
		if err := res.FirstErr(); err != nil {
			p.t.Fatal(err)
		}
		mf.Blobs = append(mf.Blobs, &contentv1.BlobRef{Path: path, Hash: sum[:], SizeBytes: uint64(len(body))})
	}
	b, err := proto.Marshal(mf)
	if err != nil {
		p.t.Fatal(err)
	}
	p.produce(p.topics.Versions, ManifestKey(pack, version), b)
}

func (p *publisher) activate(pack string, version uint64) {
	p.t.Helper()
	b, err := proto.Marshal(&contentv1.ActiveVersion{PackId: pack, Version: version})
	if err != nil {
		p.t.Fatal(err)
	}
	p.produce(p.topics.Active, pack, b)
}

func intZone(id, room string) string {
	return fmt.Sprintf(`{"formatVersion":1,"id":%q,"name":"Zone","rooms":[{"id":%q,"title":"T","description":"d"}]}`, id, room)
}

// AC-1: boot from the Active Pointers over a real broker.
func TestKafkaResolver_ResolvesFromActivePointers(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	topics, cl := throwawayTopics(t)
	p := &publisher{t: t, cl: cl, topics: topics}

	p.publish("andara.core", 3, 0, map[string]string{"core.json": intZone("core", "void")})
	p.activate("andara.core", 3)
	p.publish("town", 7, 3, map[string]string{
		"town.json": intZone("town", "square"),
		"town.aw":   "zone town \"Town\" {\n  room square \"Square\" {}\n}\n",
	})
	p.activate("town", 7)

	r, err := NewKafkaResolver(KafkaOptions{
		Brokers: brokers(t), Cache: BlobCache{Dir: t.TempDir()}, Topics: topics,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	active, err := r.Active(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if active["andara.core"] != 3 || active["town"] != 7 {
		t.Fatalf("active = %v", active)
	}

	l := NewLoader(LoaderOptions{Store: r, Packs: []string{AllPacks}})
	rejects, err := l.LoadAll(ctx)
	if err != nil || len(rejects) != 0 {
		t.Fatalf("rejects = %+v err = %v", rejects, err)
	}
	if v := l.Versions(); v["andara.core"] != 3 || v["town"] != 7 {
		t.Fatalf("serving %v", v)
	}
	zones, _ := l.Inputs()
	if len(zones) != 2 {
		t.Fatalf("zones = %d", len(zones))
	}
}

// Compaction keeps the newest value per key, but until it runs the topic still
// holds every write. A scan that stopped at the first value for a key would
// resolve a pack to a version it has moved off.
func TestKafkaResolver_NewestPointerWinsBeforeCompaction(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	topics, cl := throwawayTopics(t)
	p := &publisher{t: t, cl: cl, topics: topics}

	for v := uint64(1); v <= 5; v++ {
		p.publish("andara.core", v, 0, map[string]string{"core.json": intZone("core", "void")})
		p.activate("andara.core", v)
	}
	// A rollback is one more write of the pointer, to an older version.
	p.activate("andara.core", 2)

	r, err := NewKafkaResolver(KafkaOptions{Brokers: brokers(t), Topics: topics})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	active, err := r.Active(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if active["andara.core"] != 2 {
		t.Fatalf("active = %v; the newest write wins, and a rollback is a write", active)
	}
}

// AC-6 over the broker: a manifest naming a hash the blob topic never carried.
func TestKafkaResolver_MissingBlobIsRefusedNamingIt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	topics, cl := throwawayTopics(t)
	p := &publisher{t: t, cl: cl, topics: topics}

	body := []byte(intZone("ghost", "room"))
	sum := sha256.Sum256(body)
	mf := &contentv1.ContentVersion{PackId: "ghost", Version: 1, Blobs: []*contentv1.BlobRef{
		{Path: "ghost.json", Hash: sum[:], SizeBytes: uint64(len(body))},
	}}
	b, err := proto.Marshal(mf)
	if err != nil {
		t.Fatal(err)
	}
	p.produce(topics.Versions, ManifestKey("ghost", 1), b) // manifest, but no blob
	p.activate("ghost", 1)

	r, err := NewKafkaResolver(KafkaOptions{Brokers: brokers(t), Topics: topics})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	_, err = Resolve(ctx, r, "ghost", 1)
	var bm *ErrBlobMissing
	if !asErr(err, &bm) {
		t.Fatalf("err = %v, want ErrBlobMissing", err)
	}
	if bm.Path != "ghost.json" {
		t.Errorf("path = %q", bm.Path)
	}
}

// AC-7 over the broker: a warm cache resolves without reading the blob topic.
// Asserted by deleting the blob topic between the two resolves — if the second
// one touched it, it could not succeed.
func TestKafkaResolver_WarmCacheDoesNotReadTheBlobTopic(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	topics, cl := throwawayTopics(t)
	p := &publisher{t: t, cl: cl, topics: topics}
	p.publish("town", 1, 0, map[string]string{"town.json": intZone("town", "square")})
	p.activate("town", 1)

	cache := BlobCache{Dir: t.TempDir()}
	r, err := NewKafkaResolver(KafkaOptions{Brokers: brokers(t), Cache: cache, Topics: topics})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	cold, err := Resolve(ctx, r, "town", 1)
	if err != nil {
		t.Fatalf("cold: %v", err)
	}

	adm := kadm.NewClient(cl)
	if _, err := adm.DeleteTopic(ctx, topics.Blobs); err != nil {
		t.Fatal(err)
	}

	warm, err := Resolve(ctx, r, "town", 1)
	if err != nil {
		t.Fatalf("warm resolve must come entirely from the cache: %v", err)
	}
	if len(cold.Zones) != len(warm.Zones) || cold.Zones[0].Def.GetId() != warm.Zones[0].Def.GetId() {
		t.Fatal("cold and cached must resolve to the same content (AC-7)")
	}
}

// A pointer move reaches a watcher, which is what AC-2's reload is built on.
func TestKafkaResolver_WatchSeesAPointerMove(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	topics, cl := throwawayTopics(t)
	p := &publisher{t: t, cl: cl, topics: topics}
	p.publish("town", 1, 0, map[string]string{"town.json": intZone("town", "square")})
	p.activate("town", 1)

	r, err := NewKafkaResolver(KafkaOptions{Brokers: brokers(t), Topics: topics})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	moves, err := r.Watch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Watch starts at the end, so history is not replayed; only the move made
	// after it starts should arrive. Give the consumer a moment to take its
	// position before producing.
	time.Sleep(2 * time.Second)
	p.publish("town", 2, 0, map[string]string{"town.json": intZone("town", "square")})
	p.activate("town", 2)

	select {
	case m := <-moves:
		if m.Pack != "town" || m.Version != 2 {
			t.Fatalf("move = %+v", m)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("no pointer move observed")
	}
}

// asErr is errors.As without importing errors into a file that only needs it
// once, kept explicit so the test reads as a type assertion on the taxonomy.
func asErr(err error, target **ErrBlobMissing) bool {
	e, ok := err.(*ErrBlobMissing)
	if ok {
		*target = e
	}
	return ok
}
