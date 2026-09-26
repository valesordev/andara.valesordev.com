// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package tickloop_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/simtest"
	"github.com/valesordev/andara/server/tickloop"
)

// AC-8, as amended: acknowledgement, not enqueue. These drive the Snapshotter
// the way the boot wires it to KafkaPublisher, whose Publish returns nil once
// the boundary is enqueued and reports its fate on a callback later.

func awaitingAcks(o *tickloop.SnapshotOptions) { o.AwaitBoundaryAck = true }

// noObjects asserts the store holds nothing for any Zone and no manifest was
// written: an abandoned round made nothing durable.
func noObjects(t *testing.T, h *roundHarness) {
	t.Helper()
	for _, z := range simtest.Zones {
		keys, err := h.fs.List(context.Background(), sim.ZoneID(z))
		if err != nil {
			t.Fatalf("List %s: %v", z, err)
		}
		if len(keys) != 0 {
			t.Errorf("zone %s: %v written for a round whose boundary was not acknowledged", z, keys)
		}
	}
	if recs := h.man.all(); len(recs) != 0 {
		t.Errorf("%d SnapshotWritten records for an abandoned round", len(recs))
	}
}

// The round copies at the boundary and then waits: nothing is encoded or
// written until its own tick is acknowledged. An acknowledgement for an
// earlier tick does not release it.
func TestARoundWritesNothingBeforeItsBoundaryIsAcknowledged(t *testing.T) {
	t.Parallel()
	h := newRoundHarness(t, awaitingAcks)
	e := snapshotEngine(t)
	h.advance(60 * time.Second)
	h.s.Maybe(context.Background(), e)
	h.s.OnBoundaryAcked(41)

	// An absence cannot be polled to a deadline; this bounds how long the
	// round had to misbehave, and the acknowledgement below anchors it.
	time.Sleep(100 * time.Millisecond)
	h.fail.mu.Lock()
	puts := h.fail.puts
	h.fail.mu.Unlock()
	if puts != 0 {
		t.Fatalf("%d Put(s) before the boundary was acknowledged", puts)
	}
	select {
	case r := <-h.done:
		t.Fatalf("the round finished at tick %d (%v) before its boundary was acknowledged", r.tick, r.err)
	default:
	}

	h.s.OnBoundaryAcked(42)
	if r := h.awaitTick(t, 42); r.err != nil {
		t.Fatalf("round: %v", r.err)
	}
	if n := len(h.man.all()); n != len(simtest.Zones) {
		t.Fatalf("%d manifest records, want one per Zone", n)
	}
	assertFailure(t, h.s, "boundary", 0)
	assertFailure(t, h.s, "timeout", 0)
}

// An acknowledgement that arrives before the round starts waiting still
// counts: the callback can beat the loop to Maybe.
func TestAnAcknowledgementBeforeTheRoundStartsReleasesIt(t *testing.T) {
	t.Parallel()
	h := newRoundHarness(t, awaitingAcks)
	h.s.OnBoundaryAcked(42)
	h.advance(60 * time.Second)
	h.s.Maybe(context.Background(), snapshotEngine(t))
	if r := h.awaitTick(t, 42); r.err != nil {
		t.Fatalf("round: %v", r.err)
	}
}

// The real shape of a broker outage: Publish returned nil, and the delivery
// failure arrives on the callback afterwards. The round is abandoned, writes
// nothing, and counts reason=boundary once. A loss at an earlier tick
// abandons it too: the Publisher publishes no boundary after a lost one.
func TestALostBoundaryAbandonsTheRound(t *testing.T) {
	t.Parallel()
	for name, lostAt := range map[string]sim.Tick{"its own tick": 42, "an earlier tick": 40} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newRoundHarness(t, awaitingAcks)
			h.advance(60 * time.Second)
			h.s.Maybe(context.Background(), snapshotEngine(t))
			h.s.OnBoundaryLost(lostAt, errors.New("broker gone"))

			r := h.awaitTick(t, 42)
			if !errors.Is(r.err, tickloop.ErrBoundaryLost) {
				t.Fatalf("round error %v, want ErrBoundaryLost", r.err)
			}
			noObjects(t, h)
			assertFailure(t, h.s, "boundary", 1)
			assertFailure(t, h.s, "timeout", 0)
			assertRounds(t, h.s, 0, 0)
		})
	}
}

// Neither outcome before snapshot.upload_timeout: the round is abandoned on
// its deadline rather than hanging, and counts reason=timeout once. The common
// case when a broker is simply gone, since the delivery timeout is longer.
func TestAnUnacknowledgedBoundaryTimesTheRoundOut(t *testing.T) {
	t.Parallel()
	h := newRoundHarness(t, func(o *tickloop.SnapshotOptions) {
		o.AwaitBoundaryAck = true
		o.UploadTimeout = 50 * time.Millisecond
	})
	h.advance(60 * time.Second)
	h.s.Maybe(context.Background(), snapshotEngine(t))

	r := h.awaitTick(t, 42)
	if r.err == nil || errors.Is(r.err, tickloop.ErrBoundaryLost) {
		t.Fatalf("round error %v, want the acknowledgement timeout", r.err)
	}
	noObjects(t, h)
	assertFailure(t, h.s, "timeout", 1)
	assertFailure(t, h.s, "boundary", 0)
	assertRounds(t, h.s, 0, 0)
}

// Both outcome labels exist before the first failure (CLAUDE.md §7).
func TestBoundaryFailureIsPreCreated(t *testing.T) {
	t.Parallel()
	h := newRoundHarness(t, nil)
	assertFailure(t, h.s, "boundary", 0)
}

func assertRounds(t *testing.T, s *tickloop.Snapshotter, complete, incomplete float64) {
	t.Helper()
	if got := counterValue(t, s.Metrics().Rounds, "outcome", "complete"); got != complete {
		t.Errorf("rounds_total{complete} = %v, want %v", got, complete)
	}
	if got := counterValue(t, s.Metrics().Rounds, "outcome", "incomplete"); got != incomplete {
		t.Errorf("rounds_total{incomplete} = %v, want %v", got, incomplete)
	}
}
