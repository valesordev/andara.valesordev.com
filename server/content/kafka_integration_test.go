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

	"github.com/prometheus/client_golang/prometheus/testutil"
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
	return fmt.Sprintf(`{"formatVersion":1,"id":%q,"name":"Zone","fallbackRoom":%q,"rooms":[{"id":%q,"title":"T","description":"d"}]}`, id, room, room)
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
	attachEngine(l)
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
	m := NewMetrics(nil)
	r, err := NewKafkaResolver(KafkaOptions{Brokers: brokers(t), Cache: cache, Topics: topics, Metrics: m})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	cold, err := Resolve(ctx, r, "town", 1)
	if err != nil {
		t.Fatalf("cold: %v", err)
	}
	// andara_content_cache_hits_total (§8, 2026-09-25): the cold resolve
	// misses on every blob and hits none.
	misses := testutil.ToFloat64(m.CacheHits.WithLabelValues(OutcomeMiss))
	if misses < 1 || testutil.ToFloat64(m.CacheHits.WithLabelValues(OutcomeHit)) != 0 {
		t.Fatalf("cold: %v misses, %v hits", misses, testutil.ToFloat64(m.CacheHits.WithLabelValues(OutcomeHit)))
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
	// The warm resolve hits on every blob the cold one missed, and misses none.
	if hits := testutil.ToFloat64(m.CacheHits.WithLabelValues(OutcomeHit)); hits != misses || testutil.ToFloat64(m.CacheHits.WithLabelValues(OutcomeMiss)) != misses {
		t.Fatalf("warm: %v hits for %v cold misses", hits, misses)
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
	// Watch starts at the end of the topic, so history is not replayed and a
	// move produced before the consumer has taken its position is missed.
	// Rather than sleep a guessed amount and hope — which is a race that only
	// shows up on a loaded CI box — publish the pointer repeatedly until the
	// watch reports it. Re-activating the same version is a legal, idempotent
	// write, so the retry costs nothing but a record.
	p.publish("town", 2, 0, map[string]string{"town.json": intZone("town", "square")})

	deadline := time.After(90 * time.Second)
	retry := time.NewTicker(time.Second)
	defer retry.Stop()
	p.activate("town", 2)
	for {
		select {
		case m := <-moves:
			if m.Pack != "town" || m.Version != 2 {
				t.Fatalf("move = %+v", m)
			}
			return
		case <-retry.C:
			p.activate("town", 2)
		case <-deadline:
			t.Fatal("no pointer move observed")
		}
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

// The store is content-addressed, so the record key is a checksum the reader
// can verify for itself. This is the "wrong hash" case the story's test plan
// names. Without the check a damaged record is trusted once and built into a
// World; the on-disk cache would only catch it on the *second* read, having
// already served the bad body.
func TestKafkaResolver_BlobThatDoesNotHashToItsKeyIsRefused(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	topics, cl := throwawayTopics(t)
	p := &publisher{t: t, cl: cl, topics: topics}

	good := []byte(intZone("town", "square"))
	sum := sha256.Sum256(good)
	// The manifest names the hash of the good body; the record stored under
	// that key carries a different one.
	damaged, err := proto.Marshal(&contentv1.Blob{Hash: sum[:], Body: []byte(intZone("town", "TAMPERED"))})
	if err != nil {
		t.Fatal(err)
	}
	res := cl.ProduceSync(ctx, &kgo.Record{Topic: topics.Blobs, Key: sum[:], Value: damaged})
	if err := res.FirstErr(); err != nil {
		t.Fatal(err)
	}
	mf, err := proto.Marshal(&contentv1.ContentVersion{
		PackId: "town", Version: 1,
		Blobs: []*contentv1.BlobRef{{Path: "town.json", Hash: sum[:], SizeBytes: uint64(len(good))}},
	})
	if err != nil {
		t.Fatal(err)
	}
	p.produce(topics.Versions, ManifestKey("town", 1), mf)
	p.activate("town", 1)

	r, err := NewKafkaResolver(KafkaOptions{
		Brokers: brokers(t), Cache: BlobCache{Dir: t.TempDir()}, Topics: topics,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	_, rerr := Resolve(ctx, r, "town", 1)
	if rerr == nil {
		t.Fatal("a blob that does not hash to its own key must be refused")
	}
	if got := Reason(rerr); got != ReasonBlobCorrupt {
		t.Fatalf("reason = %q, want %q (err: %v)", got, ReasonBlobCorrupt, rerr)
	}
	// blob_corrupt is its own reason: not store_unavailable, and not one of
	// the content faults either. A damaged record says something about the
	// store, but it is not the store being unreachable.
	if IsStoreFault(rerr) {
		t.Errorf("a damaged blob record must not be reported as an unreachable store")
	}
}

// The scan-to-watch gap, which is the one failure here that production would
// hit and the test suite would not.
//
// A consumer configured AtEnd takes its position when it first polls, not when
// it is constructed. So a pointer written between the initial Active() scan
// and that first poll lands behind the consumer and is treated as history —
// never delivered, old version serving, until some later write. A publisher
// writes the pointer once.
//
// Pin captures the end offsets up front, so this test produces the move BEFORE
// Watch is ever called and still expects it. Under the old AtEnd behaviour the
// move is unreachable and this times out.
func TestKafkaResolver_PinnedWatchSeesAMoveMadeBeforeItStarted(t *testing.T) {
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

	// Pin, then resolve, then publish — the exact ordering a boot has.
	if err := r.Pin(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Active(ctx); err != nil {
		t.Fatal(err)
	}
	p.publish("town", 2, 0, map[string]string{"town.json": intZone("town", "square")})
	p.activate("town", 2)

	// Only now does the watch start. The move is already in the past.
	moves, err := r.Watch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case m := <-moves:
		if m.Pack != "town" || m.Version != 2 {
			t.Fatalf("move = %+v", m)
		}
	case <-time.After(45 * time.Second):
		t.Fatal("a pointer move made after the pin was never delivered")
	}
}
