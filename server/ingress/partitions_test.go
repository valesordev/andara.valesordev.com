// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package ingress

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/valesordev/andara/server/sim"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type change struct {
	partition int32
	degraded  bool
	cause     string
	name      string
}

type healthFixture struct {
	h       *partitionHealth
	clock   *fakeClock
	mu      sync.Mutex
	changes []change
}

const testHold = 120 * time.Second

func newHealthFixture() *healthFixture {
	f := &healthFixture{clock: &fakeClock{t: time.Unix(1_000_000, 0)}}
	f.h = newPartitionHealth(f.clock.now, testHold, func(_ context.Context, p int32, degraded bool, cause, name string) {
		f.mu.Lock()
		f.changes = append(f.changes, change{p, degraded, cause, name})
		f.mu.Unlock()
	})
	return f
}

func (f *healthFixture) degradedSet() []int32 {
	var out []int32
	for p := range int32(sim.PartitionCount) {
		if f.h.Degraded(p) {
			out = append(out, p)
		}
	}
	return out
}

func (f *healthFixture) lastChange(t *testing.T) change {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.changes) == 0 {
		t.Fatal("no state change was reported")
	}
	return f.changes[len(f.changes)-1]
}

// healthy is a view of every Partition led, with a full ISR of three.
func healthy() *topicView {
	v := &topicView{Partitions: map[int32]partitionView{}}
	for p := range int32(sim.PartitionCount) {
		v.Partitions[p] = partitionView{Leader: 1, ISR: 3}
	}
	return v
}

func (v *topicView) with(p int32, pv partitionView) *topicView {
	v.Partitions[p] = pv
	return v
}

func TestHealth_StartsHealthy(t *testing.T) {
	f := newHealthFixture()
	if got := f.degradedSet(); len(got) != 0 {
		t.Fatalf("degraded at boot: %v", got)
	}
}

// AC-1's state change: a produce error marks that Partition and no other.
func TestHealth_ProduceErrorMarksOnlyThatPartition(t *testing.T) {
	for _, name := range produceErrorNames {
		t.Run(name, func(t *testing.T) {
			f := newHealthFixture()
			f.h.MarkProduceError(context.Background(), 9, name)
			if got := f.degradedSet(); len(got) != 1 || got[0] != 9 {
				t.Fatalf("degraded = %v, want [9]", got)
			}
			if c := f.lastChange(t); c != (change{9, true, CauseProduceError, name}) {
				t.Fatalf("change = %+v", c)
			}
		})
	}
}

// AC-3: a leader absent on one probe marks nothing; on two consecutive
// probes it marks the Partition, and a probe in between that finds a
// leader starts the count again.
func TestHealth_LeaderAbsentNeedsTwoConsecutiveProbes(t *testing.T) {
	f := newHealthFixture()
	gone := healthy().with(4, partitionView{Leader: -1, ISR: 0})

	f.h.ObserveProbe(context.Background(), gone, 2)
	if got := f.degradedSet(); len(got) != 0 {
		t.Fatalf("one probe marked %v", got)
	}
	f.h.ObserveProbe(context.Background(), healthy(), 2)
	f.h.ObserveProbe(context.Background(), gone, 2)
	if got := f.degradedSet(); len(got) != 0 {
		t.Fatalf("a probe between reset nothing: %v", got)
	}
	f.h.ObserveProbe(context.Background(), gone, 2)
	if got := f.degradedSet(); len(got) != 1 || got[0] != 4 {
		t.Fatalf("two consecutive probes: degraded = %v, want [4]", got)
	}
	if c := f.lastChange(t); c != (change{4, true, CauseProbe, ErrNameLeaderAbsent}) {
		t.Fatalf("change = %+v", c)
	}
}

// AC-4: an ISR below min.insync.replicas marks at the first probe that
// sees it, and the first probe that sees it full again clears it.
func TestHealth_ISRBelowMinMarksAtOnceAndClearsOnRecovery(t *testing.T) {
	f := newHealthFixture()
	short := healthy().with(7, partitionView{Leader: 2, ISR: 1})
	f.h.ObserveProbe(context.Background(), short, 2)
	if got := f.degradedSet(); len(got) != 1 || got[0] != 7 {
		t.Fatalf("degraded = %v, want [7]", got)
	}
	if c := f.lastChange(t); c != (change{7, true, CauseProbe, ErrNameISRBelowMin}) {
		t.Fatalf("change = %+v", c)
	}
	f.h.ObserveProbe(context.Background(), healthy(), 2)
	if got := f.degradedSet(); len(got) != 0 {
		t.Fatalf("not cleared on recovery: %v", got)
	}
	if c := f.lastChange(t); c.partition != 7 || c.degraded {
		t.Fatalf("change = %+v", c)
	}
}

// AC-4: min.insync.replicas is the topic's, not 2.
func TestHealth_ISRIsComparedWithTheTopicsMin(t *testing.T) {
	f := newHealthFixture()
	two := healthy().with(7, partitionView{Leader: 2, ISR: 2})
	f.h.ObserveProbe(context.Background(), two, 2)
	if got := f.degradedSet(); len(got) != 0 {
		t.Fatalf("an ISR of 2 with min 2 marked %v", got)
	}
	f.h.ObserveProbe(context.Background(), two, 3)
	if got := f.degradedSet(); len(got) != 1 || got[0] != 7 {
		t.Fatalf("an ISR of 2 with min 3: degraded = %v, want [7]", got)
	}
}

// AC-6: a metadata failure on two consecutive probes degrades all 64; one
// degrades none; the first answer clears them.
func TestHealth_MetadataFailureNeedsTwoConsecutiveProbes(t *testing.T) {
	f := newHealthFixture()
	f.h.ObserveProbe(context.Background(), nil, 2)
	if got := f.degradedSet(); len(got) != 0 {
		t.Fatalf("one failure degraded %d", len(got))
	}
	f.h.ObserveProbe(context.Background(), healthy(), 2)
	f.h.ObserveProbe(context.Background(), nil, 2)
	if got := f.degradedSet(); len(got) != 0 {
		t.Fatalf("a success between did not reset: %d degraded", len(got))
	}
	f.h.ObserveProbe(context.Background(), nil, 2)
	if got := f.degradedSet(); len(got) != int(sim.PartitionCount) {
		t.Fatalf("two consecutive failures degraded %d, want %d", len(got), sim.PartitionCount)
	}
	if c := f.lastChange(t); !c.degraded || c.cause != CauseProbe || c.name != ErrNameMetadataUnavailable {
		t.Fatalf("change = %+v", c)
	}
	f.h.ObserveProbe(context.Background(), healthy(), 2)
	if got := f.degradedSet(); len(got) != 0 {
		t.Fatalf("not cleared by the first answer: %d degraded", len(got))
	}
}

// AC-8: a produce-error mark holds across probes that show a full ISR
// until the hold has elapsed, measured from the most recent produce-error
// mark, and clears on the next healthy probe after it.
func TestHealth_ProduceErrorHoldsUntilTheHoldAndAHealthyProbe(t *testing.T) {
	f := newHealthFixture()
	ctx := context.Background()
	f.h.MarkProduceError(ctx, 3, "REQUEST_TIMED_OUT")

	f.clock.advance(testHold - time.Second)
	f.h.ObserveProbe(ctx, healthy(), 2)
	if !f.h.Degraded(3) {
		t.Fatal("cleared before the hold elapsed")
	}

	// A fresh mark restarts the hold: it runs from the most recent one.
	f.h.MarkProduceError(ctx, 3, "REQUEST_TIMED_OUT")
	f.clock.advance(testHold - time.Second)
	f.h.ObserveProbe(ctx, healthy(), 2)
	if !f.h.Degraded(3) {
		t.Fatal("the hold ran from the first mark, not the most recent")
	}

	f.clock.advance(time.Second)
	// The hold has elapsed but no probe has run since: still degraded.
	if !f.h.Degraded(3) {
		t.Fatal("cleared without a healthy probe")
	}
	f.h.ObserveProbe(ctx, healthy(), 2)
	if f.h.Degraded(3) {
		t.Fatal("not cleared by the first healthy probe after the hold")
	}
	if c := f.lastChange(t); c.partition != 3 || c.degraded {
		t.Fatalf("change = %+v", c)
	}
}

// AC-8: a probe mark on the same Partition does not shorten the hold; the
// Partition leaves when both the hold and a healthy probe have occurred.
func TestHealth_ProbeMarkDoesNotShortenTheHold(t *testing.T) {
	f := newHealthFixture()
	ctx := context.Background()
	f.h.MarkProduceError(ctx, 3, "NOT_ENOUGH_REPLICAS")
	f.h.ObserveProbe(ctx, healthy().with(3, partitionView{Leader: 1, ISR: 1}), 2)
	f.h.ObserveProbe(ctx, healthy(), 2) // the probe mark ends; the hold has not
	if !f.h.Degraded(3) {
		t.Fatal("the probe's recovery cleared a produce-error mark inside its hold")
	}
	f.clock.advance(testHold)
	f.h.ObserveProbe(ctx, healthy(), 2)
	if f.h.Degraded(3) {
		t.Fatal("not cleared after the hold and a healthy probe")
	}
}

// AC-8: the hold is no shortcut for a Partition the probe still finds
// unhealthy.
func TestHealth_HoldElapsedDoesNotClearAnUnhealthyPartition(t *testing.T) {
	f := newHealthFixture()
	ctx := context.Background()
	f.h.MarkProduceError(ctx, 3, "NOT_ENOUGH_REPLICAS")
	f.clock.advance(testHold + time.Second)
	f.h.ObserveProbe(ctx, healthy().with(3, partitionView{Leader: 1, ISR: 1}), 2)
	if !f.h.Degraded(3) {
		t.Fatal("cleared while the ISR was still below min")
	}
}

// AC-9: under a persistent fault with a full ISR in metadata, the gauge
// reads 1 for the whole hold (so for longer than the alert's for: plus two
// scrapes), then 0 until the next Submit rediscovers it.
func TestHealth_PersistentFaultReadsOneForTheWholeHold(t *testing.T) {
	f := newHealthFixture()
	ctx := context.Background()
	f.h.MarkProduceError(ctx, 3, "REQUEST_TIMED_OUT")
	const alertFor, scrape = 30 * time.Second, 15 * time.Second
	need := alertFor + 2*scrape
	if testHold <= need {
		t.Fatalf("test hold %v does not exceed for+2 scrapes %v", testHold, need)
	}
	for elapsed := time.Duration(0); elapsed < testHold-time.Second; elapsed += time.Second {
		if !f.h.Degraded(3) {
			t.Fatalf("read 0 after %v of a %v hold", elapsed, testHold)
		}
		f.clock.advance(time.Second)
		f.h.ObserveProbe(ctx, healthy(), 2)
	}
	f.clock.advance(time.Second)
	f.h.ObserveProbe(ctx, healthy(), 2)
	if f.h.Degraded(3) {
		t.Fatal("still degraded after the hold with a healthy probe")
	}
	// The next Submit rediscovers it.
	f.h.MarkProduceError(ctx, 3, "REQUEST_TIMED_OUT")
	if !f.h.Degraded(3) {
		t.Fatal("a rediscovery did not mark")
	}
}

// A change is reported once per transition, not once per probe.
func TestHealth_ReportsTransitionsOnce(t *testing.T) {
	f := newHealthFixture()
	ctx := context.Background()
	short := healthy().with(7, partitionView{Leader: 2, ISR: 1})
	for range 3 {
		f.h.ObserveProbe(ctx, short, 2)
	}
	f.h.MarkProduceError(ctx, 7, "NOT_ENOUGH_REPLICAS") // already degraded
	f.mu.Lock()
	n := len(f.changes)
	f.mu.Unlock()
	if n != 1 {
		t.Fatalf("%d changes reported, want 1", n)
	}
}

// A Partition the topic does not have is not marked: the probe judges the
// Partitions the broker lists.
func TestHealth_PartitionsTheTopicLacksAreNotMarked(t *testing.T) {
	f := newHealthFixture()
	v := &topicView{Partitions: map[int32]partitionView{0: {Leader: 1, ISR: 3}}}
	for range 3 {
		f.h.ObserveProbe(context.Background(), v, 2)
	}
	if got := f.degradedSet(); len(got) != 0 {
		t.Fatalf("degraded = %v", got)
	}
}

// A topic the broker reports as missing is an outage of every Partition.
func TestHealth_MissingTopicIsAnOutageOfAll(t *testing.T) {
	f := newHealthFixture()
	for range 2 {
		f.h.ObserveProbe(context.Background(), &topicView{Missing: true}, 2)
	}
	if got := f.degradedSet(); len(got) != int(sim.PartitionCount) {
		t.Fatalf("degraded %d, want %d", len(got), sim.PartitionCount)
	}
}

// One probe without a leader marks nothing and does not clear a mark that
// stands: not an ISR mark, and not a produce mark past its hold.
func TestHealth_OneLeaderlessProbeDoesNotClearAMark(t *testing.T) {
	ctx := context.Background()
	f := newHealthFixture()
	f.h.ObserveProbe(ctx, healthy().with(7, partitionView{Leader: 2, ISR: 1}), 2)
	f.h.ObserveProbe(ctx, healthy().with(7, partitionView{Leader: -1}), 2)
	if !f.h.Degraded(7) {
		t.Fatal("a probe with no leader cleared an ISR mark")
	}
	f.h.ObserveProbe(ctx, healthy(), 2)

	f.h.MarkProduceError(ctx, 9, "REQUEST_TIMED_OUT")
	f.clock.advance(testHold + time.Second)
	f.h.ObserveProbe(ctx, healthy().with(9, partitionView{Leader: -1}), 2)
	if !f.h.Degraded(9) {
		t.Fatal("a probe with no leader released a produce mark past its hold")
	}
}

// A produce mark on a Partition the topic does not list still ends after the
// hold and a probe.
func TestHealth_AProduceMarkOnAnUnlistedPartitionEnds(t *testing.T) {
	ctx := context.Background()
	f := newHealthFixture()
	v := &topicView{Partitions: map[int32]partitionView{0: {Leader: 1, ISR: 3}}}
	f.h.MarkProduceError(ctx, 40, "REQUEST_TIMED_OUT")
	f.clock.advance(testHold + time.Second)
	f.h.ObserveProbe(ctx, v, 2)
	if f.h.Degraded(40) {
		t.Fatal("the mark outlived its hold")
	}
}

// Two leaderless observations separated by a failed metadata probe are not
// consecutive probes.
func TestHealth_AFailedProbeBreaksALeaderlessStreak(t *testing.T) {
	ctx := context.Background()
	f := newHealthFixture()
	gone := healthy().with(5, partitionView{Leader: -1})
	f.h.ObserveProbe(ctx, gone, 2)
	f.h.ObserveProbe(ctx, nil, 2)
	f.h.ObserveProbe(ctx, gone, 2)
	if f.h.Degraded(5) {
		t.Fatal("a streak of two was counted across a failed probe")
	}
}

// The check and the action in ifDegraded are one step: a recovery cannot land
// between them.
func TestHealth_IfDegradedHoldsTheLockAcrossTheAction(t *testing.T) {
	ctx := context.Background()
	f := newHealthFixture()
	f.h.ObserveProbe(ctx, healthy().with(3, partitionView{Leader: 2, ISR: 1}), 2)
	var actionDone, recovered atomic.Int64
	in := make(chan struct{})
	go func() {
		<-in
		f.h.ObserveProbe(ctx, healthy(), 2)
		recovered.Store(time.Now().UnixNano())
	}()
	ran := f.h.ifDegraded(3, func() {
		close(in)
		time.Sleep(150 * time.Millisecond)
		actionDone.Store(time.Now().UnixNano())
	})
	if !ran {
		t.Fatal("the action did not run on a degraded Partition")
	}
	waitFor(t, func() bool { return recovered.Load() != 0 }, "the recovery")
	if recovered.Load() < actionDone.Load() {
		t.Fatal("the Partition recovered in the middle of the action")
	}
	if f.h.ifDegraded(3, func() { t.Fatal("ran on a healthy Partition") }) {
		t.Fatal("reported running on a healthy Partition")
	}
}
