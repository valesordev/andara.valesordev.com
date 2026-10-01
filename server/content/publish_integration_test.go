// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

//go:build integration

package content

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"
	"go.opentelemetry.io/otel/trace"

	"github.com/valesordev/andara/content/core"
	adminv1 "github.com/valesordev/andara/gen/go/andara/admin/v1"
	contentv1 "github.com/valesordev/andara/gen/go/andara/content/v1"
	"github.com/valesordev/andara/internal/eventually"
	"github.com/valesordev/andara/server/auth"
	"github.com/valesordev/andara/server/recordlog"
)

// publishTopics is the three content topics and an audit topic under
// test-local names. The blobs topic allows a record past content.max_blob_bytes,
// as deploy/kafka/topics.yaml must (docs/feedback/AW-SRV-013-publish-path.md,
// For SRE 1). The versions topic compacts within a test's deadline, which the
// local broker's log_segment_ms_min of 1 s permits (AW-SRV-019 AC-5).
func publishTopics(t *testing.T) (Topics, string, *kgo.Client) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cl, err := kgo.NewClient(kgo.SeedBrokers(brokers(t)...), kgo.RecordPartitioner(kgo.ManualPartitioner()))
	if err != nil {
		t.Fatal(err)
	}
	adm := kadm.NewClient(cl)
	stamp := time.Now().UnixNano()
	tp := Topics{
		Blobs:    fmt.Sprintf("andara.test.publish.blobs.%d", stamp),
		Versions: fmt.Sprintf("andara.test.publish.versions.%d", stamp),
		Active:   fmt.Sprintf("andara.test.publish.active.%d", stamp),
	}
	audit := fmt.Sprintf("andara.test.publish.audit.%d", stamp)
	compact := func(extra map[string]string) map[string]*string {
		m := map[string]*string{"cleanup.policy": kadm.StringPtr("compact")}
		for k, v := range extra {
			m[k] = kadm.StringPtr(v)
		}
		return m
	}
	for name, cfg := range map[string]map[string]*string{
		tp.Blobs:    compact(map[string]string{"max.message.bytes": "9437184"}),
		tp.Versions: compact(map[string]string{"segment.ms": "1000", "min.cleanable.dirty.ratio": "0.01", "delete.retention.ms": "1000"}),
		tp.Active:   compact(nil),
		audit:       {"cleanup.policy": kadm.StringPtr("delete")},
	} {
		if _, err := adm.CreateTopic(ctx, 6, 1, cfg, name); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		dctx, dcancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer dcancel()
		_, _ = adm.DeleteTopics(dctx, tp.Blobs, tp.Versions, tp.Active, audit)
		cl.Close()
	})
	return tp, audit, cl
}

type brokerPublish struct {
	t        *testing.T
	tp       Topics
	auditTop string
	cl       *kgo.Client
	logs     []recordlog.Log
	reg      *Registry
	resolver *KafkaResolver
	loader   *Loader
	admin    *Admin
	// holder is the Account store the publish path authorizes on; nil is
	// the fixed alice/bob map.
	holder PackHolder
	// pm and tracer, when set, are the publish path's metrics and tracer, for
	// a test that asserts them.
	pm     *PublishMetrics
	tracer trace.Tracer
}

func (b *brokerPublish) open(topic string, maxRecord int32) recordlog.Log {
	b.t.Helper()
	l, err := recordlog.NewKafka(context.Background(), recordlog.KafkaOptions{Brokers: brokers(b.t), Topic: topic, MaxRecordBytes: maxRecord})
	if err != nil {
		b.t.Fatal(err)
	}
	b.logs = append(b.logs, l)
	b.t.Cleanup(func() { _ = l.Close() })
	return l
}

// start is a server boot against the topics: the registry replayed, the
// resolver and Loader over the store, and the publish path.
func (b *brokerPublish) start() {
	b.t.Helper()
	blobs, versions, active, audit := b.open(b.tp.Blobs, 9<<20), b.open(b.tp.Versions, 0), b.open(b.tp.Active, 0), b.open(b.auditTop, 0)
	var err error
	b.reg, err = OpenRegistry(context.Background(), RegistryOptions{Blobs: blobs, Versions: versions, Active: active, Audit: audit, Cache: BlobCache{Dir: b.t.TempDir()}})
	if err != nil {
		b.t.Fatal(err)
	}
	b.resolver, err = NewKafkaResolver(KafkaOptions{Brokers: brokers(b.t), Topics: b.tp, Cache: BlobCache{Dir: b.t.TempDir()}, MaxBlobBytes: 8 << 20})
	if err != nil {
		b.t.Fatal(err)
	}
	b.loader = NewLoader(LoaderOptions{Store: b.resolver, Packs: []string{CorePack, "town"}, Metrics: NewMetrics(nil), Tracer: b.tracer})
	attachEngine(b.loader)
	auditor := auth.NewAuditor(audit, slog.New(slog.DiscardHandler), nil, nil)
	var holder PackHolder = packHolders{alice: {"town"}, bob: {"town"}}
	if b.holder != nil {
		holder = b.holder
	}
	b.admin, err = NewAdmin(AdminOptions{
		Registry: b.reg, Loader: b.loader, Blobs: b.resolver,
		Accounts: holder, Metrics: b.pm, Tracer: b.tracer,
		Auditor: auditor, MaxBlobBytes: 8 << 20, MaxPackBytes: 256 << 20, OperatorSelfApproval: true,
	})
	if err != nil {
		b.t.Fatal(err)
	}
	if _, err := BootCore(context.Background(), CoreBootOptions{Registry: b.reg, Auditor: auditor, Pack: CorePack, Version: core.Version(), Blobs: core.Blobs(), Build: "integration"}); err != nil {
		b.t.Fatal(err)
	}
	b.follow(CorePack)
}

// follow brings a pack's active pointer, read back from the broker, into
// effect through the Loader.
func (b *brokerPublish) follow(pack string) {
	b.t.Helper()
	active, err := b.resolver.Active(context.Background())
	if err != nil {
		b.t.Fatal(err)
	}
	if rej := b.loader.Apply(context.Background(), PointerMove{Pack: pack, Version: active[pack]}); len(rej) > 0 {
		b.t.Fatalf("load %s@%d: %v", pack, active[pack], rej[0].Err)
	}
}

func (b *brokerPublish) publish(files map[string][]byte) uint64 {
	b.t.Helper()
	pv, err := publishThrough(b.admin, builder(alice), "town", files, b.reg.Newest("town"))
	if err != nil {
		b.t.Fatal(err)
	}
	if _, err := b.admin.ApproveVersion(builder(bob), &adminv1.ApproveVersionRequest{PackId: "town", Version: pv.GetVersion()}); err != nil {
		b.t.Fatal(err)
	}
	return pv.GetVersion()
}

func publishThrough(a *Admin, ctx context.Context, pack string, files map[string][]byte, parent uint64) (*adminv1.PublishVersionResponse, error) {
	var paths []string
	for p := range files {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	req := &adminv1.PublishVersionRequest{PackId: pack, ParentVersion: parent}
	for _, p := range paths {
		if _, err := a.PublishBlob(ctx, blobStream(pack, p, files[p], BlobChunkBytes).Receive); err != nil {
			return nil, err
		}
		sum := sha256.Sum256(files[p])
		req.Blobs = append(req.Blobs, blobRef(p, sum[:], len(files[p])))
	}
	return a.PublishVersion(ctx, req)
}

// The publish path against a broker, end to end: every write is read back
// through the store the Loader reads, and a restart rebuilds what the
// publish path answers from, after compaction has removed the records an
// approval superseded (AC-2, AC-5, AC-6, AC-10, AC-12, AC-15, AC-18).
func TestPublishPath_AgainstABroker(t *testing.T) {
	tp, audit, cl := publishTopics(t)
	b := &brokerPublish{t: t, tp: tp, auditTop: audit, cl: cl}
	b.start()
	ctx := context.Background()

	// AC-2 and AC-5: publish, approve, activate; the Loader follows.
	v1 := b.publish(townFiles(t))
	if _, err := b.admin.ActivateVersion(builder(alice), &adminv1.ActivateVersionRequest{PackId: "town", Version: v1}); err != nil {
		t.Fatal(err)
	}
	b.follow("town")
	if got := b.loader.Versions()["town"]; got != v1 {
		t.Fatalf("serving town@%d, want %d", got, v1)
	}

	// AC-12: a 5 MiB blob goes through the broker and comes back through a
	// resolver with a cold cache; one past the limit produces nothing.
	files := townFiles(t)
	big := bytes.Repeat([]byte("andara "), (5<<20)/7)
	files["README"] = big
	v2 := b.publish(files)
	cold, err := NewKafkaResolver(KafkaOptions{Brokers: brokers(t), Topics: tp, Cache: BlobCache{Dir: t.TempDir()}, MaxBlobBytes: 8 << 20})
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(big)
	got, err := cold.Blobs(ctx, []*contentv1.BlobRef{blobRef("README", sum[:], len(big))})
	if err != nil || !bytes.Equal(got["README"], big) {
		t.Fatalf("the 5 MiB blob read back: %d bytes, %v", len(got["README"]), err)
	}
	over := bytes.Repeat([]byte("x"), (8<<20)+1)
	_, err = b.admin.PublishBlob(builder(alice), blobStream("town", "HUGE", over, BlobChunkBytes).Receive)
	adminError(t, err, CodeResourceExhausted, ErrReasonBlobTooLarge)
	overSum := sha256.Sum256(over)
	if b.reg.HasBlob(overSum[:]) {
		t.Fatal("a blob over the limit was written")
	}

	// AC-6: forward to 2, then back to 1 with no new approval.
	if _, err := b.admin.ActivateVersion(builder(alice), &adminv1.ActivateVersionRequest{PackId: "town", Version: v2}); err != nil {
		t.Fatal(err)
	}
	b.follow("town")
	if av, err := b.admin.ActivateVersion(builder(bob), &adminv1.ActivateVersionRequest{PackId: "town", Version: v1}); err != nil || !av.GetRollback() {
		t.Fatalf("rollback %v %v", av, err)
	}
	b.follow("town")
	if got := b.loader.Versions()["town"]; got != v1 {
		t.Fatalf("after rollback serving town@%d", got)
	}

	// AC-10: each approval rewrote its manifest under the same key. Force
	// compaction until the older record of town@1 is gone, then restart.
	key := []byte(ManifestKey("town", v1))
	eventually.Observed(t, 90*time.Second, "town@1's superseded manifest compacted away", func() (bool, string) {
		for p := range int32(6) {
			if err := cl.ProduceSync(ctx, &kgo.Record{Topic: tp.Versions, Partition: p, Key: []byte("test:filler")}).FirstErr(); err != nil {
				t.Fatal(err)
			}
		}
		n := countKey(t, tp.Versions, key)
		return n == 1, fmt.Sprintf("%d records keyed %s", n, key)
	})
	b.start()
	list, err := b.admin.ListVersions(builder(alice), &adminv1.ListVersionsRequest{PackId: "town"})
	if err != nil {
		t.Fatal(err)
	}
	var versions []uint64
	for _, v := range list.GetVersions() {
		versions = append(versions, v.GetVersion())
		if v.GetApprovedBy() != bob {
			t.Errorf("town@%d lost its approval: %v", v.GetVersion(), v)
		}
	}
	if !slices.Equal(versions, []uint64{v2, v1}) || list.GetActiveVersion() != v1 || len(list.GetActivations()) != 3 {
		t.Fatalf("after compaction and a restart: versions %v, active %d, %d activations", versions, list.GetActiveVersion(), len(list.GetActivations()))
	}

	// AC-18 on the restarted server: the big blob streams back from town@2.
	var body []byte
	if err := b.admin.GetBlob(builder(alice), &adminv1.GetBlobRequest{PackId: "town", Version: v2, Hash: sum[:]}, func(r *adminv1.GetBlobResponse) error {
		body = append(body, r.GetData()...)
		return nil
	}); err != nil || !bytes.Equal(body, big) {
		t.Fatalf("GetBlob after restart: %d bytes, %v", len(body), err)
	}
}

func blobRef(path string, hash []byte, size int) *contentv1.BlobRef {
	return &contentv1.BlobRef{Path: path, Hash: hash, SizeBytes: uint64(size)}
}

// countKey reads a whole topic raw and counts the records with key.
func countKey(t *testing.T, topic string, key []byte) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	adm, err := kgo.NewClient(kgo.SeedBrokers(brokers(t)...))
	if err != nil {
		t.Fatal(err)
	}
	defer adm.Close()
	ends, err := kadm.NewClient(adm).ListEndOffsets(ctx, topic)
	if err != nil {
		t.Fatal(err)
	}
	want := map[int32]int64{}
	ends.Each(func(o kadm.ListedOffset) {
		if o.Offset > 0 {
			want[o.Partition] = o.Offset
		}
	})
	cl, err := kgo.NewClient(kgo.SeedBrokers(brokers(t)...), kgo.ConsumeTopics(topic), kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()))
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	n := 0
	for len(want) > 0 {
		fs := cl.PollFetches(ctx)
		if err := fs.Err0(); err != nil {
			t.Fatal(err)
		}
		fs.EachRecord(func(r *kgo.Record) {
			if bytes.Equal(r.Key, key) {
				n++
			}
			if end, ok := want[r.Partition]; ok && r.Offset+1 >= end {
				delete(want, r.Partition)
			}
		})
	}
	return n
}
