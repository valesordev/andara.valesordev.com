// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

//go:build integration

package boot

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/valesordev/andara/content/core"
	contentv1 "github.com/valesordev/andara/gen/go/andara/content/v1"
	"github.com/valesordev/andara/internal/eventually"
	"github.com/valesordev/andara/server/content"
)

// storeTopics creates the content store's three topics and an audit topic
// under throwaway names, so neither the code under test nor a regression of
// it writes into the store the dev stack serves from.
func storeTopics(t *testing.T) (content.Topics, string, *kadm.Client, []string) {
	t.Helper()
	v := os.Getenv("ANDARA_KAFKA_BROKERS")
	if v == "" {
		t.Skip("ANDARA_KAFKA_BROKERS not set; run `make up` and `make test-integration`")
	}
	brokers := strings.Split(v, ",")
	cl, err := kgo.NewClient(kgo.SeedBrokers(brokers...))
	if err != nil {
		t.Fatal(err)
	}
	adm := kadm.NewClient(cl)
	stamp := time.Now().UnixNano()
	tp := content.Topics{
		Blobs:    fmt.Sprintf("andara.test.projector.blobs.%d", stamp),
		Versions: fmt.Sprintf("andara.test.projector.versions.%d", stamp),
		Active:   fmt.Sprintf("andara.test.projector.active.%d", stamp),
	}
	audit := fmt.Sprintf("andara.test.projector.audit.%d", stamp)
	compact := map[string]*string{"cleanup.policy": kadm.StringPtr("compact")}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for name, cfg := range map[string]map[string]*string{
		tp.Blobs:    {"cleanup.policy": kadm.StringPtr("compact"), "max.message.bytes": kadm.StringPtr("9437184")},
		tp.Versions: compact, tp.Active: compact,
		audit: {"cleanup.policy": kadm.StringPtr("delete")},
	} {
		if _, err := adm.CreateTopic(ctx, 1, 1, cfg, name); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		dctx, dcancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer dcancel()
		_, _ = adm.DeleteTopics(dctx, tp.Blobs, tp.Versions, tp.Active, audit)
		cl.Close()
	})
	return tp, audit, adm, brokers
}

// storeRuntime is a content.source=kafka boot over the throwaway topics.
func storeRuntime(t *testing.T, tp content.Topics, audit string, brokers []string) (*Runtime, *syncBuffer) {
	t.Helper()
	rt, _ := runtime(t, "", false)
	logs := liveLogs(rt)
	rt.Cfg.ContentSource = content.SourceKafka
	rt.Cfg.ContentPath = ""
	rt.Cfg.KafkaBrokers = brokers
	rt.Cfg.ContentPacks = []string{content.AllPacks}
	rt.Cfg.ContentCorePack = core.Pack
	rt.Cfg.ContentCacheDir = t.TempDir()
	rt.Cfg.ContentMaxBlobBytes = 8 << 20
	rt.Cfg.CharacterSpawnRoom = "town/plaza"
	rt.ContentTopics, rt.AuditTopicName = tp, audit
	return rt, logs
}

// #326: the state projector loads content read-only, and waits on a store
// without Zones rather than exiting.
//   - Booted on an empty store, it writes nothing to the content topics or
//     the audit topic. The window closes at its wait line: LoadContent, where
//     the core boot ran, has returned by then.
//   - The server's boot then publishes andara.core, and a Builder's town is
//     activated. The same projector Runtime leaves the wait on the town, with
//     no restart, holding the town's World.
func TestProjectorBoot_ReadOnlyAndWaitsForZones(t *testing.T) {
	tp, audit, adm, brokers := storeTopics(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	proj, plogs := storeRuntime(t, tp, audit, brokers)
	proj.ContentReadOnly = true
	// Only pointer moves end this wait, so the leaving line names the
	// town's; the retry between moves is TestWaitLoop_RetriesWithoutAMove's.
	proj.waitRetry = time.Hour
	if code := proj.LoadContent(ctx); code != ExitOK || proj.World != nil {
		t.Fatalf("an empty store: exit %d, World %v\n%s", code, proj.World, plogs.String())
	}
	waited := make(chan error, 1)
	go func() { waited <- proj.WaitForContent(ctx) }()
	const waitLine = "waiting for content: no Zones in effect; publish and activate a pack"
	eventually.Observed(t, 30*time.Second, "the projector's wait line", func() (bool, string) {
		n := len(logLines(t, plogs, waitLine))
		return n == 1, fmt.Sprintf("%d wait lines", n)
	})
	ends, err := adm.ListEndOffsets(ctx, tp.Blobs, tp.Versions, tp.Active, audit)
	if err != nil {
		t.Fatal(err)
	}
	ends.Each(func(o kadm.ListedOffset) {
		if o.Err != nil || o.Offset != 0 {
			t.Errorf("the projector's boot wrote to %s[%d]: high-water mark %d (%v)", o.Topic, o.Partition, o.Offset, o.Err)
		}
	})
	if !proj.Waiting() {
		t.Fatal("not waiting")
	}

	// The server's boot publishes and activates the core. Still no Zones.
	srv, slogs := storeRuntime(t, tp, audit, brokers)
	if code := srv.LoadContent(ctx); code != ExitOK || srv.core == nil {
		t.Fatalf("the server's boot: exit %d\n%s", code, slogs.String())
	}
	// A town, published and activated as the Admin path would.
	body := []byte(`{"formatVersion":1,"id":"town","name":"Town","fallbackRoom":"plaza",
		"rooms":[{"id":"plaza","title":"Plaza","description":"A square."}]}`)
	sum := sha256.Sum256(body)
	if _, err := srv.registry.PutBlob(ctx, sum[:], "application/json", body); err != nil {
		t.Fatal(err)
	}
	cv := &contentv1.ContentVersion{PackId: "town", Author: "acct-builder", CoreVersion: core.Version(),
		Blobs: []*contentv1.BlobRef{{Path: "town.json", Hash: sum[:], SizeBytes: uint64(len(body))}}}
	if _, err := srv.registry.Publish(ctx, cv, 0, "acct-builder"); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.registry.MovePointer(ctx, "town", 1, "acct-operator"); err != nil {
		t.Fatal(err)
	}
	// The server's writes are done. From here the topics hold only what
	// the projector writes, which must be nothing.
	seeded := endOffsets(ctx, t, adm, tp, audit)

	select {
	case err := <-waited:
		if err != nil {
			t.Fatalf("WaitForContent: %v\n%s", err, plogs.String())
		}
	case <-time.After(60 * time.Second):
		t.Fatalf("still waiting after the town was activated\n%s", plogs.String())
	}
	if proj.Waiting() || proj.World == nil || proj.World.Zones["town"] == nil {
		t.Fatalf("waiting %t, World %v", proj.Waiting(), proj.World)
	}
	left := logLines(t, plogs, "content in effect: leaving the wait")
	if len(left) != 1 || left[0]["zones"] != float64(1) || left[0]["pack"] != "town@1" || left[0]["trace_id"] == "" {
		t.Fatalf("the leaving line: %v", left)
	}
	if n := len(logLines(t, plogs, waitLine)); n != 1 {
		t.Errorf("%d wait lines; the warn is once", n)
	}
	// Leaving the wait wrote nothing either: WaitForContent returned after
	// its last reload, so its window is closed (review of #334).
	for tp, end := range endOffsets(ctx, t, adm, tp, audit) {
		if end != seeded[tp] {
			t.Errorf("leaving the wait wrote to %s: high-water mark %d, %d after the seed", tp, end, seeded[tp])
		}
	}
}

// endOffsets is each topic's high-water mark, summed over its Partitions.
func endOffsets(ctx context.Context, t *testing.T, adm *kadm.Client, tp content.Topics, audit string) map[string]int64 {
	t.Helper()
	ends, err := adm.ListEndOffsets(ctx, tp.Blobs, tp.Versions, tp.Active, audit)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]int64{}
	ends.Each(func(o kadm.ListedOffset) {
		if o.Err != nil {
			t.Fatalf("%s[%d]: %v", o.Topic, o.Partition, o.Err)
		}
		out[o.Topic] += o.Offset
	})
	return out
}
