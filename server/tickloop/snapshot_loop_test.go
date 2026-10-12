// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package tickloop

import (
	"testing"
	"time"

	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/store"
)

// A snapshot for a tick whose boundary was never published cannot be
// verified: AW-SRV-007 checks a round against the TickCompleted for the same
// tick, and there is none. Worse, the round reports success, so the metrics
// show healthy snapshots throughout a boundary outage and SnapshotStale stays
// silent — the one time an operator needs it to speak.
//
// Every error path out of KafkaPublisher.Publish leaves the tick without a
// boundary: an Events send that fails returns before the boundary is
// attempted, and ErrBoundaryLost means the process has stopped publishing
// them altogether. So the loop gates on the error, not on its kind.
func TestLoop_NoSnapshotWhenTheBoundaryWasNotPublished(t *testing.T) {
	rounds := make(chan sim.Tick, 16)
	snap, err := NewSnapshotter(SnapshotOptions{
		Store:         store.NewFS(t.TempDir()),
		Interval:      time.Nanosecond, // due on every boundary
		MaxStall:      5 * time.Millisecond,
		UploadTimeout: 30 * time.Second,
		OnRound:       func(tick sim.Tick, _ error) { rounds <- tick },
	})
	if err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, func(o *Options) { o.Snapshotter = snap })
	h.pub.Fail = ErrBoundaryLost

	if err := h.runFor(time.Second); err != nil { // ten ticks
		t.Fatalf("run: %v", err)
	}
	snap.Wait()
	if n := len(rounds); n != 0 {
		t.Fatalf("%d round(s) ran with no boundary published", n)
	}
	if got := counter(snap.Metrics().Rounds.WithLabelValues("complete")); got != 0 {
		t.Errorf("rounds_total{complete} = %v, want 0", got)
	}
	// Not silently: every round that fell due is counted abandoned (AC-8).
	// The cadence is a nanosecond, so that is every tick.
	if got := counter(snap.Metrics().Failures.WithLabelValues("boundary")); got != 10 {
		t.Errorf("failures_total{boundary} = %v, want 10, one per tick a round fell due", got)
	}
}

// AW-SRV-054: pre-stop's round checkpoints its own tick. With
// sim.checkpoint_every_ticks far past the run, the only commit the loop makes
// while it ticks is the forced round's, on the boundary after it.
func TestLoop_ForcedRoundCommitsItsOwnTick(t *testing.T) {
	snap, err := NewSnapshotter(SnapshotOptions{
		Store:         store.NewFS(t.TempDir()),
		Interval:      time.Hour, // no periodic round inside the run
		MaxStall:      5 * time.Millisecond,
		UploadTimeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	var committed []sim.Tick
	var h *harness
	h = newHarness(t, func(o *Options) {
		o.Snapshotter = snap
		o.CheckpointEvery = 1000
		o.OnTick = func(sim.StepResult, time.Duration) { committed = append(committed, h.loop.CommittedThrough()) }
	})
	ch := snap.Force()
	if err := h.runFor(time.Second); err != nil {
		t.Fatalf("run: %v", err)
	}
	snap.Wait()
	var r ForcedRound
	select {
	case r = <-ch:
	default:
		t.Fatal("no forced round was taken")
	}
	if r.Err != nil || r.Tick == 0 {
		t.Fatalf("forced round tick %d err %v", r.Tick, r.Err)
	}
	var seen sim.Tick
	for _, c := range committed {
		seen = max(seen, c)
	}
	if seen != r.Tick {
		t.Fatalf("committed through %d while ticking (%v), want the forced round's tick %d", seen, committed, r.Tick)
	}
}

// The gate is per tick, not a latch: once boundaries are published again, so
// are snapshots.
func TestLoop_SnapshotsResumeWhenTheBoundaryReturns(t *testing.T) {
	rounds := make(chan sim.Tick, 16)
	snap, err := NewSnapshotter(SnapshotOptions{
		Store:         store.NewFS(t.TempDir()),
		Interval:      time.Nanosecond,
		MaxStall:      5 * time.Millisecond,
		UploadTimeout: 30 * time.Second,
		OnRound:       func(tick sim.Tick, _ error) { rounds <- tick },
	})
	if err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, func(o *Options) { o.Snapshotter = snap })
	if err := h.runFor(time.Second); err != nil {
		t.Fatalf("run: %v", err)
	}
	snap.Wait()
	if len(rounds) == 0 {
		t.Fatal("no round ran while boundaries were being published")
	}
	if got := counter(snap.Metrics().Rounds.WithLabelValues("complete")); got == 0 {
		t.Error("no complete round recorded")
	}
}
