// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package content

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	logv1 "github.com/valesordev/andara/gen/go/andara/log/v1"
	"github.com/valesordev/andara/server/sim"
)

// The review of #88: a swap built on a World the log moved past between
// evaluation and apply — another process's swap landing first — is refused
// as stale, and the Loader evaluates again against what is now in effect and
// brings the version in. It never poisons the log.
func TestLoader_AStaleSwapIsEvaluatedAgain(t *testing.T) {
	s := newFakeStore()
	s.publish("andara.core", 3, 0, map[string]string{"core.json": zoneJSON("core", "void")})
	s.publish("town", 7, 3, map[string]string{"town.json": zoneJSON("town", "square")})
	l, _, h := harnessLoader(t, s, nil, AllPacks)
	if rejects, err := l.LoadAll(context.Background()); err != nil || len(rejects) != 0 {
		t.Fatalf("boot: %v %v", rejects, err)
	}

	// Another process's core@4, built on what is in effect now, lands just
	// ahead of this Loader's town@8. Its content differs from core@3's: the
	// digest is over content, so a version with identical content would be a
	// World town@8's base still describes, and nothing would be stale.
	s.publish("andara.core", 4, 0, map[string]string{"core.json": zoneJSON("core", "hollow")})
	other := NewLoader(LoaderOptions{Store: s, Packs: []string{AllPacks}})
	inEffect, base := l.InEffect()
	after := map[string]uint64{"andara.core": 4, "town": inEffect["town"]}
	topo, err := sim.PrepareContent(other, after)
	if err != nil {
		t.Fatal(err)
	}
	d := sim.ContentDigest(topo)
	h.first = &logv1.LoggedCommand{Command: &logv1.LoggedCommand_ContentSwap{ContentSwap: &logv1.ContentSwap{
		PackId: "andara.core", Version: 4, WorldDigest: d[:], BaseDigest: base[:]}}}

	s.publish("town", 8, 3, map[string]string{"town.json": zoneJSON("town", "square")})
	if rejects := l.Apply(context.Background(), PointerMove{Pack: "town", Version: 8}); len(rejects) != 0 {
		t.Fatalf("rejects %v", rejects)
	}
	if v := l.Versions(); v["andara.core"] != 4 || v["town"] != 8 {
		t.Fatalf("in effect %v, want core@4 (the other swap) and town@8 (evaluated again)", v)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.refusals) != 1 || h.refusals[0].Pack != "town" || h.refusals[0].Reason != sim.SwapStaleBase {
		t.Fatalf("refusals %v; want town@8's first swap refused as stale", h.refusals)
	}
}

// An ambiguous produce waits for its fate and is never counted as not
// written before it settles: written, it comes into effect; not written, it
// is store_unavailable; unknown, the Loader waits for the World Partition to
// be consumed past it and then knows.
func TestLoader_AnAmbiguousProduceWaitsForItsFate(t *testing.T) {
	for _, tc := range []struct {
		name    string
		outcome error
		lands   bool
		want    string // "" when it comes into effect
	}{
		{"written", nil, true, ""},
		{"not written", ErrSwapNotWritten, false, ReasonStoreUnavailable},
		{"unknown, landed", ErrSwapOutcomeUnknown, true, ""},
		{"unknown, not landed", ErrSwapOutcomeUnknown, false, ReasonStoreUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newFakeStore()
			s.publish("andara.core", 1, 0, map[string]string{"core.json": zoneJSON("core", "void")})
			l, _, h := harnessLoader(t, s, nil, "andara.core")
			if _, err := l.LoadAll(context.Background()); err != nil {
				t.Fatal(err)
			}
			settle := make(chan func() error, 1)
			h.mu.Lock()
			h.pending = settle
			h.mu.Unlock()
			if tc.lands {
				l.SetBarrier(h.landHeld)
			} else {
				l.SetBarrier(func(context.Context) error { h.mu.Lock(); h.held = nil; h.mu.Unlock(); return nil })
			}
			s.publish("andara.core", 2, 0, map[string]string{"core.json": zoneJSON("core", "void")})
			got := make(chan []Rejection, 1)
			go func() { got <- l.Apply(context.Background(), PointerMove{Pack: "andara.core", Version: 2}) }()
			select {
			case r := <-got:
				t.Fatalf("returned %v before the produce settled", r)
			case <-time.After(50 * time.Millisecond):
			}
			if tc.outcome == nil {
				// Written: it reaches the Engine as the log is consumed.
				settle <- func() error { return nil }
				waitUntil(t, func() bool { h.mu.Lock(); defer h.mu.Unlock(); return h.held != nil }, "the held record")
				if err := h.landHeld(context.Background()); err != nil {
					t.Fatal(err)
				}
			} else {
				settle <- func() error { return tc.outcome }
			}
			rejects := <-got
			switch {
			case tc.want == "" && (len(rejects) != 0 || serving(t, l, "andara.core") != 2):
				t.Fatalf("rejects %v serving %d; want core@2 in effect", rejects, serving(t, l, "andara.core"))
			case tc.want != "" && (len(rejects) != 1 || rejects[0].Reason != tc.want || serving(t, l, "andara.core") != 1):
				t.Fatalf("rejects %v serving %d; want %s with core@1 kept", rejects, serving(t, l, "andara.core"), tc.want)
			}
		})
	}
}

// A swap produced and never applied — Partition 0 faulted — stops blocking
// after the bounded wait: store_unavailable, so the retry applies, and other
// moves keep draining.
func TestLoader_TheWaitForApplyIsBounded(t *testing.T) {
	s := newFakeStore()
	s.publish("andara.core", 1, 0, map[string]string{"core.json": zoneJSON("core", "void")})
	m := NewMetrics(nil)
	l := NewLoader(LoaderOptions{Store: s, Packs: []string{"andara.core"}, Metrics: m, ApplyWait: 50 * time.Millisecond})
	h := attachEngine(l)
	h.swallow = true
	start := time.Now()
	rejects, err := l.LoadAll(context.Background())
	var te *ErrApplyTimeout
	if err != nil || len(rejects) != 1 || rejects[0].Reason != ReasonStoreUnavailable || !errors.As(rejects[0].Err, &te) {
		t.Fatalf("rejects %v err %v", rejects, err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("the wait was not bounded: %s", time.Since(start))
	}
}

// Transition findings: a version that removes a Zone in effect is refused
// zone_removed, and one whose World loses character.spawn_room is refused
// spawn_room_removed, both validation — the Builder's.
func TestLoader_TransitionsTheVersionAloneCannotShow(t *testing.T) {
	s := newFakeStore()
	s.publish("andara.core", 1, 0, map[string]string{"core.json": zoneJSON("core", "void"), "town.json": zoneJSON("town", "square")})
	m := NewMetrics(nil)
	l := NewLoader(LoaderOptions{Store: s, Packs: []string{"andara.core"}, Metrics: m, SpawnRoom: sim.RoomRef{Zone: "town", Room: "square"}})
	attachEngine(l)
	if rejects, err := l.LoadAll(context.Background()); err != nil || len(rejects) != 0 {
		t.Fatalf("boot: %v %v", rejects, err)
	}

	s.publish("andara.core", 2, 0, map[string]string{"core.json": zoneJSON("core", "void")})
	rejects := l.Apply(context.Background(), PointerMove{Pack: "andara.core", Version: 2})
	if len(rejects) != 1 || rejects[0].Reason != ReasonValidation || !hasFinding(rejects[0].Err, sim.ErrZoneRemoved) {
		t.Fatalf("removing town: %v", rejects)
	}

	s.publish("andara.core", 3, 0, map[string]string{"core.json": zoneJSON("core", "void"), "town.json": zoneJSON("town", "plaza")})
	rejects = l.Apply(context.Background(), PointerMove{Pack: "andara.core", Version: 3})
	if len(rejects) != 1 || rejects[0].Reason != ReasonValidation || !hasFinding(rejects[0].Err, sim.ErrSpawnRoomRemoved) {
		t.Fatalf("losing the spawn Room: %v", rejects)
	}
	if got := serving(t, l, "andara.core"); got != 1 {
		t.Fatalf("serving %d", got)
	}
	// Builder reasons are not pending.
	if p := l.Pending()["andara.core"]; p != 0 {
		t.Fatalf("pending %v", p)
	}
	_ = testutil.ToFloat64(m.LoadFailures.WithLabelValues(ReasonValidation))
}

func hasFinding(err error, code sim.ErrCode) bool {
	for _, f := range Findings(err) {
		if f.Code == code {
			return true
		}
	}
	return false
}

// The pending clock starts when the move is read, not after the debounce.
func TestLoader_PendingStartsWhenTheMoveIsRead(t *testing.T) {
	s := newFakeStore()
	s.publish("andara.core", 1, 0, map[string]string{"core.json": zoneJSON("core", "void")})
	l, _, _ := harnessLoader(t, s, nil, "andara.core")
	if _, err := l.LoadAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.publish("andara.core", 2, 0, map[string]string{"core.json": zoneJSON("core", "void")})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := &fakeWatcher{ch: make(chan PointerMove, 1)}
	done := make(chan struct{})
	go func() { defer close(done); _ = l.Follow(ctx, w, time.Hour, nil) }()
	w.ch <- PointerMove{Pack: "andara.core", Version: 2}
	waitUntil(t, func() bool { return l.Pending()["andara.core"] > 0 }, "the pending clock to start within the debounce")
	cancel()
	<-done
}

// What reconcile could not load for the store's sake is retried by Follow:
// the watch starts past the pointer that named it.
func TestLoader_FollowRetriesWhatReconcileCouldNotLoad(t *testing.T) {
	prevI, prevM := retryInitial, retryMax
	retryInitial, retryMax = 10*time.Millisecond, 40*time.Millisecond
	t.Cleanup(func() { retryInitial, retryMax = prevI, prevM })
	s := newFakeStore()
	s.publish("andara.core", 1, 0, map[string]string{"core.json": zoneJSON("core", "void")})
	l, _, h := harnessLoader(t, s, nil, "andara.core")
	h.fail = errors.New("broker unreachable")
	if rejects, _ := l.LoadAll(context.Background()); len(rejects) != 1 || rejects[0].Reason != ReasonStoreUnavailable {
		t.Fatalf("reconcile: %v", rejects)
	}
	h.mu.Lock()
	h.fail = nil
	h.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); _ = l.Follow(ctx, &fakeWatcher{ch: make(chan PointerMove)}, time.Hour, nil) }()
	waitUntil(t, func() bool { return l.Versions()["andara.core"] == 1 }, "the retry to load core@1")
	cancel()
	<-done
}

// A held version the batch supersedes is dropped before core releases it, so
// an obsolete intermediate version never enters the World.
func TestLoader_ASupersededHeldVersionIsNeverApplied(t *testing.T) {
	s := newFakeStore()
	s.publish("andara.core", 3, 0, map[string]string{"core.json": zoneJSON("core", "void")})
	l, _, h := harnessLoader(t, s, nil, AllPacks)
	if _, err := l.LoadAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.publish("abbey", 2, 4, map[string]string{"abbey.json": zoneJSON("abbey", "nave")})
	if r := l.Apply(context.Background(), PointerMove{Pack: "abbey", Version: 2}); len(r) != 1 || r[0].Reason != ReasonCoreVersion {
		t.Fatalf("abbey@2 %v", r)
	}
	s.publish("andara.core", 4, 0, map[string]string{"core.json": zoneJSON("core", "void")})
	s.publish("abbey", 3, 4, map[string]string{"abbey.json": zoneJSON("abbey", "nave")})
	var applied []uint64 // written under h.mu, in stepLocked
	h.mu.Lock()
	h.onApply = func(swaps []sim.SwapApplied) {
		for _, sw := range swaps {
			if sw.Pack == "abbey" {
				applied = append(applied, sw.Version)
			}
		}
	}
	h.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := &fakeWatcher{ch: make(chan PointerMove, 2)}
	done := make(chan struct{})
	go func() { defer close(done); _ = l.Follow(ctx, w, 20*time.Millisecond, nil) }()
	w.ch <- PointerMove{Pack: "abbey", Version: 3}
	w.ch <- PointerMove{Pack: "andara.core", Version: 4}
	waitUntil(t, func() bool { return l.Versions()["abbey"] == 3 }, "abbey@3")
	cancel()
	<-done
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, v := range applied {
		if v == 2 {
			t.Fatalf("abbey@2 was swapped in on the way to abbey@3: %v", applied)
		}
	}
}
