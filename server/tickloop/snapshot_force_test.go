// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package tickloop_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/simtest"
	"github.com/valesordev/andara/server/store"
	"github.com/valesordev/andara/server/tickloop"
)

func awaitForced(t *testing.T, ch <-chan tickloop.ForcedRound) tickloop.ForcedRound {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("the forced round did not finish")
		return tickloop.ForcedRound{}
	}
}

// AW-SRV-054 AC-1: a Force makes the next boundary take a round whatever the
// cadence, and its result names the tick, the State Hash at it, and the keys
// written — every Zone's, readable as a complete round.
func TestForceTakesARoundAtTheNextBoundary(t *testing.T) {
	t.Parallel()
	h := newRoundHarness(t, nil)
	e := snapshotEngine(t)
	ch := h.s.Force()
	// The interval has not elapsed: only the Force makes this round due.
	if !h.s.Maybe(context.Background(), e) {
		t.Fatal("Maybe did not report taking the forced round")
	}
	r := awaitForced(t, ch)
	if r.Err != nil {
		t.Fatalf("forced round: %v", r.Err)
	}
	if r.Tick != 42 || r.Hash != e.StateHash() {
		t.Fatalf("forced round tick %d hash %x, want 42 and the engine's %x", r.Tick, r.Hash, e.StateHash())
	}
	if len(r.Keys) != len(simtest.Zones) {
		t.Fatalf("keys %v, want one per Zone", r.Keys)
	}
	zones := make([]sim.ZoneID, len(simtest.Zones))
	for i, z := range simtest.Zones {
		zones[i] = sim.ZoneID(z)
	}
	round, _, ok, err := store.NewestComplete(context.Background(), h.fs, zones, nil)
	if err != nil || !ok || round.Tick != 42 {
		t.Fatalf("NewestComplete = tick %d ok %v err %v, want the forced round at 42", round.Tick, ok, err)
	}
	// Taken: the next boundary is back on the cadence.
	h.step(t, e, 43)
	if h.s.Maybe(context.Background(), e) {
		t.Fatal("a second boundary took a forced round nobody asked for")
	}
}

// A Force that finds a round in flight waits for a later boundary instead of
// being skipped; the round it gets is a later tick's.
func TestForceWaitsForTheRoundInFlight(t *testing.T) {
	t.Parallel()
	h := newRoundHarness(t, nil)
	h.fail.delay = 200 * time.Millisecond
	e := snapshotEngine(t)
	h.advance(60 * time.Second)
	h.s.Maybe(context.Background(), e) // the periodic round, now in flight
	ch := h.s.Force()
	h.step(t, e, 43)
	if h.s.Maybe(context.Background(), e) {
		t.Fatal("the forced round started while another was in flight")
	}
	h.awaitTick(t, 42)
	h.step(t, e, 44)
	if !h.s.Maybe(context.Background(), e) {
		t.Fatal("the forced round was not taken once the previous one finished")
	}
	if r := awaitForced(t, ch); r.Err != nil || r.Tick != 44 {
		t.Fatalf("forced round tick %d err %v, want 44 and complete", r.Tick, r.Err)
	}
}

// AC-5: a store that times out the round reports a deadline, which pre-stop
// reads as TIMEOUT; a store error is reported as itself.
func TestForcedRoundReportsItsFailure(t *testing.T) {
	t.Parallel()
	h := newRoundHarness(t, func(o *tickloop.SnapshotOptions) { o.UploadTimeout = 50 * time.Millisecond })
	h.fail.delay = time.Second
	e := snapshotEngine(t)
	ch := h.s.Force()
	h.s.Maybe(context.Background(), e)
	if r := awaitForced(t, ch); !errors.Is(r.Err, context.DeadlineExceeded) {
		t.Fatalf("forced round err %v, want a deadline", r.Err)
	}

	h2 := newRoundHarness(t, nil)
	h2.fail.fail["town"] = sim.ErrStoreUnavailable
	ch = h2.s.Force()
	h2.s.Maybe(context.Background(), e)
	if r := awaitForced(t, ch); !errors.Is(r.Err, sim.ErrStoreUnavailable) || errors.Is(r.Err, context.DeadlineExceeded) {
		t.Fatalf("forced round err %v, want the store's error", r.Err)
	}
}

// A withdrawn Force is not taken at the next boundary.
func TestCancelForceWithdrawsTheRequest(t *testing.T) {
	t.Parallel()
	h := newRoundHarness(t, nil)
	e := snapshotEngine(t)
	h.s.CancelForce(h.s.Force())
	if h.s.Maybe(context.Background(), e) {
		t.Fatal("a withdrawn Force was taken")
	}
}

// A withdrawal that arrives while the boundary is claiming the request waits
// for the claim, finds the request taken, and reports withdrawn=false; the
// round goes to the request. Deterministic: the withdrawal is started inside
// the claim's window and given 50ms to reach into it.
func TestCancelForceCannotReachIntoTheClaim(t *testing.T) {
	t.Parallel()
	h := newRoundHarness(t, func(o *tickloop.SnapshotOptions) { o.OnRound = nil })
	e := snapshotEngine(t)
	ch := h.s.Force()
	var withdrawn bool
	done := make(chan struct{})
	h.s.SetInClaim(func() {
		go func() { withdrawn = h.s.CancelForce(ch); close(done) }()
		select {
		case <-done:
		case <-time.After(50 * time.Millisecond):
		}
	})
	forced := h.s.Maybe(context.Background(), e)
	<-done
	h.s.Wait()
	if !forced || withdrawn {
		t.Fatalf("forced %v, withdrawn %v: a withdrawal reached into the claim", forced, withdrawn)
	}
	select {
	case <-ch:
	default:
		t.Fatal("the claimed round did not report to its request")
	}
}

// A withdrawal racing the boundary either wins, and no round is taken, or
// loses, and the round taken reports to the request it was taken for: a
// boundary never takes a forced round whose request was withdrawn. This is
// a stress test, so it hits the window only sometimes;
// TestCancelForceCannotReachIntoTheClaim holds the window open on purpose.
func TestCancelForceRacingTheBoundary(t *testing.T) {
	t.Parallel()
	// No OnRound: the harness's channel would fill long before the loop ends.
	h := newRoundHarness(t, func(o *tickloop.SnapshotOptions) { o.OnRound = nil })
	e := snapshotEngine(t)
	for i := range 300 {
		ch := h.s.Force()
		var wg sync.WaitGroup
		var withdrawn bool
		wg.Add(1)
		go func() { defer wg.Done(); withdrawn = h.s.CancelForce(ch) }()
		forced := h.s.Maybe(context.Background(), e)
		wg.Wait()
		h.s.Wait()
		// Exactly one side wins: no periodic round is due (the harness's
		// clock never advances), so a round taken here is the forced one.
		if forced == withdrawn {
			t.Fatalf("iteration %d: forced %v, withdrawn %v: a withdrawn request's round was taken, or a request was neither taken nor withdrawn", i, forced, withdrawn)
		}
		if !forced {
			continue
		}
		select {
		case <-ch:
		default:
			t.Fatalf("iteration %d: a forced round was taken with no request to report to", i)
		}
	}
}

func TestForceWithSnapshotsOffIsRefused(t *testing.T) {
	t.Parallel()
	s, err := tickloop.NewSnapshotter(tickloop.SnapshotOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if r := awaitForced(t, s.Force()); !errors.Is(r.Err, tickloop.ErrSnapshotsDisabled) {
		t.Fatalf("Force with snapshots off: %v", r.Err)
	}
}
