// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

//go:build integration

package boot

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/valesordev/andara/content/core"
	contentv1 "github.com/valesordev/andara/gen/go/andara/content/v1"
	logv1 "github.com/valesordev/andara/gen/go/andara/log/v1"
	"github.com/valesordev/andara/server/content"
	"github.com/valesordev/andara/server/sim"
)

// AW-SRV-042's #299 amendment: an empty store's no_zones_found is held from
// LoadContent until the boot's decision. Each case runs the real path
// through LoadContent on the local stack's Redpanda, over throwaway topics,
// with the tick loop on the memory source and the World log in memory.

// noZonesLines is every log line whose code is no_zones_found.
func noZonesLines(logs interface{ String() string }) int {
	n := 0
	for l := range strings.SplitSeq(logs.String(), "\n") {
		var m map[string]any
		if json.Unmarshal([]byte(l), &m) == nil && m["code"] == string(sim.ErrEmptyContent) {
			n++
		}
	}
	return n
}

func noZonesCount(rt *Runtime) float64 {
	return testutil.ToFloat64(rt.Tel.Metrics.ValidationErrors.WithLabelValues(string(sim.ErrEmptyContent)))
}

// heldServer boots a server over the store: LoadContent (publishing
// andara.core), then the tick loop on the memory source over log.
func heldServer(t *testing.T, ctx context.Context, tp storeTopicsT, log *memLog) (*Runtime, *syncBuffer) {
	t.Helper()
	rt, logs := storeRuntime(t, tp.tp, tp.audit, tp.brokers)
	if code := rt.LoadContent(ctx); code != ExitOK || rt.World != nil {
		t.Fatalf("an empty store: exit %d, World %v\n%s", code, rt.World, logs.String())
	}
	rt.replay = log
	startMemoryLoop(t, rt)
	return rt, logs
}

type storeTopicsT struct {
	tp      content.Topics
	audit   string
	brokers []string
}

func newStore(t *testing.T) storeTopicsT {
	tp, audit, _, brokers := storeTopics(t)
	return storeTopicsT{tp: tp, audit: audit, brokers: brokers}
}

// The wait drops it: no line, no count, no recovery warn.
func TestHeldNoZones_TheWaitDropsIt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	rt, logs := heldServer(t, ctx, newStore(t), worldLog())
	if code := rt.ReconcileContent(ctx); code != ExitOK || !rt.Waiting() {
		t.Fatalf("exit %d, waiting %t\n%s", code, rt.Waiting(), logs.String())
	}
	rt.SettleHeld(ExitOK) // main's, on a later clean exit: nothing to revive
	if n := noZonesLines(logs); n != 0 {
		t.Errorf("%d no_zones_found lines on a wait\n%s", n, logs.String())
	}
	if n := len(logLines(t, logs, recoveringLine)); n != 0 {
		t.Errorf("%d recovery warns on a wait", n)
	}
	if c := noZonesCount(rt); c != 0 {
		t.Errorf("andara_content_validation_errors_total{code=no_zones_found} = %v, want 0", c)
	}
}

// An exit in reconcile (AC-3: the log applied a swap with Zones, and the
// store names none) logs and counts it once.
func TestHeldNoZones_AnExitReportsItOnce(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	rt, logs := heldServer(t, ctx, newStore(t), worldLog(swapRec("town", 4, 0, 2, "")))
	if code := rt.ReconcileContent(ctx); code != ExitFail {
		t.Fatalf("exit %d, want %d\n%s", code, ExitFail, logs.String())
	}
	rt.SettleHeld(ExitFail) // main's, on the way out: already settled
	if n := noZonesLines(logs); n != 1 {
		t.Errorf("%d no_zones_found lines on an exit, want 1\n%s", n, logs.String())
	}
	if c := noZonesCount(rt); c != 1 {
		t.Errorf("andara_content_validation_errors_total{code=no_zones_found} = %v, want 1", c)
	}
}

// An exit 1 elsewhere before the decision, as main's deferred SettleHeld
// sees it (a tick-loop start failure, say), reports it once; a signal drops
// it.
func TestHeldNoZones_MainSettlesIt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	st := newStore(t)
	for _, tc := range []struct {
		code      int
		wantLines int
	}{{ExitFail, 1}, {ExitOK, 0}} {
		rt, logs := storeRuntime(t, st.tp, st.audit, st.brokers)
		if code := rt.LoadContent(ctx); code != ExitOK {
			t.Fatalf("load: exit %d\n%s", code, logs.String())
		}
		rt.SettleHeld(tc.code)
		rt.SettleHeld(tc.code)
		if n := noZonesLines(logs); n != tc.wantLines {
			t.Errorf("exit %d: %d no_zones_found lines, want %d\n%s", tc.code, n, tc.wantLines, logs.String())
		}
		if c := noZonesCount(rt); c != float64(tc.wantLines) {
			t.Errorf("exit %d: count %v, want %d", tc.code, c, tc.wantLines)
		}
	}
}

// Served from the log: town@1 is in the store, published and never
// activated, and the World log applies a swap of it. No no_zones_found,
// and the recovery warn exactly once.
func TestHeldNoZones_AServeFromTheLogWarnsOnce(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	st := newStore(t)
	seed, slogs := storeRuntime(t, st.tp, st.audit, st.brokers)
	if code := seed.LoadContent(ctx); code != ExitOK || seed.core == nil {
		t.Fatalf("the seeding boot: exit %d\n%s", code, slogs.String())
	}
	body := []byte(`{"formatVersion":1,"id":"town","name":"Town","fallbackRoom":"plaza",
		"rooms":[{"id":"plaza","title":"Plaza","description":"A square."}]}`)
	sum := sha256.Sum256(body)
	if _, err := seed.registry.PutBlob(ctx, sum[:], "application/json", body); err != nil {
		t.Fatal(err)
	}
	cv := &contentv1.ContentVersion{PackId: "town", Author: "acct-builder", CoreVersion: core.Version(),
		Blobs: []*contentv1.BlobRef{{Path: "town.json", Hash: sum[:], SizeBytes: uint64(len(body))}}}
	if _, err := seed.registry.Publish(ctx, cv, 0, "acct-builder"); err != nil {
		t.Fatal(err)
	}

	rt, logs := heldServer(t, ctx, st, worldLog())
	swap := &logv1.ContentSwap{PackId: "town", Version: 1, ZoneCount: 1}
	topo, err := rt.Content.Prepare(nil, swap)
	if err != nil {
		t.Fatal(err)
	}
	d := sim.ContentDigest(topo)
	swap.WorldDigest = d[:]
	rt.memSource.Push(&logv1.LoggedCommand{Command: &logv1.LoggedCommand_ContentSwap{ContentSwap: swap}})

	if code := rt.ReconcileContent(ctx); code != ExitOK || rt.Waiting() || len(rt.Engine.World().Zones) == 0 {
		t.Fatalf("exit %d, waiting %t\n%s", code, rt.Waiting(), logs.String())
	}
	rt.SettleHeld(ExitOK)
	if n := noZonesLines(logs); n != 0 {
		t.Errorf("%d no_zones_found lines on a serve\n%s", n, logs.String())
	}
	warns := logLines(t, logs, recoveringLine)
	if len(warns) != 1 || warns[0]["error_count"] != float64(0) || warns[0]["trace_id"] == "" {
		t.Errorf("recovery warns: %v, want one with error_count 0 and a trace_id", warns)
	}
	if c := noZonesCount(rt); c != 0 {
		t.Errorf("andara_content_validation_errors_total{code=no_zones_found} = %v, want 0", c)
	}
}

// A pointer naming a version with no manifest is a rejection, not an empty
// store: LoadContent logs and counts it as it always did. Between its start
// and its return, two lines: the rejection's own and the appended one.
func TestHeldNoZones_AMissingManifestIsReportedAtLoad(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	st := newStore(t)
	seed, slogs := storeRuntime(t, st.tp, st.audit, st.brokers)
	if code := seed.LoadContent(ctx); code != ExitOK {
		t.Fatalf("the seeding boot: exit %d\n%s", code, slogs.String())
	}
	if _, err := seed.registry.MovePointer(ctx, "town", 5, "acct-operator"); err != nil {
		t.Fatal(err)
	}
	rt, logs := storeRuntime(t, st.tp, st.audit, st.brokers)
	if code := rt.LoadContent(ctx); code != ExitOK {
		t.Fatalf("load: exit %d\n%s", code, logs.String())
	}
	if n := noZonesLines(logs); n != 2 {
		t.Errorf("%d no_zones_found lines from LoadContent, want 2\n%s", n, logs.String())
	}
	if c := noZonesCount(rt); c != 2 {
		t.Errorf("andara_content_validation_errors_total{code=no_zones_found} = %v, want 2", c)
	}
	rt.replay = worldLog()
	startMemoryLoop(t, rt)
	_ = rt.ReconcileContent(ctx)
	rt.SettleHeld(ExitOK)
	if c := noZonesCount(rt); c != 2 {
		t.Errorf("over the whole boot: count %v, want 2", c)
	}
}
