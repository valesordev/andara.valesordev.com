// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package tickloop

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/simtest"
)

// lossyPublisher is the Kafka publisher's delivery contract on the memory
// publisher: a boundary is acknowledged lag ticks after Publish, and at
// lossAt the boundaries not yet acknowledged are lost — the first of them
// reported through BoundaryLost, as KafkaPublisher.OnBoundaryLost does, and
// every later Publish refused with ErrBoundaryLost.
type lossyPublisher struct {
	*MemoryPublisher
	loop   *Loop
	lag    sim.Tick
	lossAt sim.Tick
	acked  sim.Tick
	lost   bool
	// onClose, if set, runs in Close: a boundary the shutdown's flush fails.
	onClose func()
}

func (p *lossyPublisher) Close() error {
	if p.onClose != nil {
		p.onClose()
	}
	return p.MemoryPublisher.Close()
}

var errBrokerGone = errors.New("broker gone")

func (p *lossyPublisher) Publish(ctx context.Context, events []sim.Event, tc sim.TickCompleted) error {
	if err := p.MemoryPublisher.Publish(ctx, events, sim.TickCompleted{}); err != nil {
		return err
	}
	if tc.Tick == 0 {
		return nil
	}
	if !p.lost && p.lossAt > 0 && tc.Tick >= p.lossAt {
		p.lost = true
		p.loop.BoundaryLost(p.acked+1, errBrokerGone)
	}
	if p.lost {
		return ErrBoundaryLost
	}
	p.mu.Lock()
	p.Completed = append(p.Completed, tc)
	p.mu.Unlock()
	if tc.Tick > p.lag {
		p.acked = tc.Tick - p.lag
		p.loop.BoundaryAcked(p.acked)
	}
	return nil
}

// newLossyHarness is a loop over one Look per tick on town, so every tick
// moves town's offset, publishing through a lossyPublisher.
func newLossyHarness(t *testing.T, lag, lossAt sim.Tick) (*harness, *lossyPublisher) {
	t.Helper()
	pub := &lossyPublisher{MemoryPublisher: &MemoryPublisher{}, lag: lag, lossAt: lossAt}
	h := newHarness(t, func(o *Options) {
		o.Publisher = pub
		o.AwaitBoundaryAck = true
		o.MaxPerTick = 1
	})
	pub.loop = h.loop
	h.pub = pub.MemoryPublisher
	for range 40 {
		h.source.Push(simtest.Look("town", "a"))
	}
	return h, pub
}

func boundaryAt(t *testing.T, pub *lossyPublisher, tick sim.Tick) sim.TickCompleted {
	t.Helper()
	_, cs, _ := pub.Snapshot()
	for _, c := range cs {
		if c.Tick == tick {
			return c
		}
	}
	t.Fatalf("no boundary for tick %d among %d", tick, len(cs))
	return sim.TickCompleted{}
}

// AC-1, AC-3: a boundary lost at tick 13, with deliveries two ticks behind,
// stops the loop after the tick that learned of it; the stop names tick 11
// lost and tick 10 delivered, SimulationStopped says boundary_lost, the
// counter is 1, the error line comes before the drain's, the tick's span is
// marked and kept — and the offsets committed are tick 10's, not tick 13's.
func TestLoop_BoundaryLostStopsIntoRecovery(t *testing.T) {
	h, pub := newLossyHarness(t, 2, 13)
	err := h.runFor(10 * time.Second)

	var lost *BoundaryLostError
	if !errors.As(err, &lost) || lost.Lost != 11 || lost.LastDelivered != 10 {
		t.Fatalf("Run returned %v, want boundary 11 lost after 10", err)
	}
	if !errors.Is(err, ErrBoundaryLost) || !errors.Is(err, errBrokerGone) {
		t.Fatalf("%v does not wrap ErrBoundaryLost and the delivery failure", err)
	}
	if got := h.engine.Tick(); got != 13 {
		t.Fatalf("engine stopped at tick %d, want 13: the tick that learned of the loss, and no later one", got)
	}
	if got := counter(h.loop.Metrics().BoundaryLost); got != 1 {
		t.Fatalf("andara_tick_boundary_lost_total %v, want 1", got)
	}

	evs, _, _ := pub.Snapshot()
	last := evs[len(evs)-1]
	if last.Type != sim.EvSimulationStopped || last.Envelope.GetSimulationStopped().GetReason() != StopReasonBoundaryLost {
		t.Fatalf("last Event %v %q, want SimulationStopped boundary_lost", last.Type, last.Envelope.GetSimulationStopped().GetReason())
	}

	line := findLog(t, h.logs, "tick boundary lost; exiting into recovery")
	if line["level"] != "ERROR" || line["lost_tick"] != float64(11) || line["last_delivered_tick"] != float64(10) || line["err"] != errBrokerGone.Error() {
		t.Fatalf("loss line %v", line)
	}
	logs := h.logs.String()
	at, draining, stopped := strings.Index(logs, "exiting into recovery"), strings.Index(logs, `"tick loop draining"`), strings.Index(logs, `"tick loop stopped"`)
	if at < 0 || draining < at || stopped < draining {
		t.Fatalf("want the loss line, then draining, then stopped:\n%s", logs)
	}

	var marked []int64
	for _, s := range h.spans.Ended() {
		if s.Name() != "sim.tick" {
			continue
		}
		attrs := attribute.NewSet(s.Attributes()...)
		if v, ok := attrs.Value("boundary_lost"); ok && v.AsBool() {
			tick, _ := attrs.Value("tick")
			keep, _ := attrs.Value("andara.keep")
			if !keep.AsBool() {
				t.Fatal("the boundary_lost tick span is not kept")
			}
			marked = append(marked, tick.AsInt64())
		}
	}
	if len(marked) != 1 || marked[0] != 13 {
		t.Fatalf("sim.tick spans marked boundary_lost: %v, want [13]", marked)
	}

	// AC-3: nothing past the last delivered boundary was committed.
	committed, _ := h.source.Committed()
	want := boundaryAt(t, pub, 10).Offsets
	town := sim.PartitionFor("town")
	if committed[town] != want[town] || committed[town] != 10 {
		t.Fatalf("committed town at %d, want %d: the last delivered boundary's", committed[town], want[town])
	}
}

// AC-1's "within one tick interval": a loss reported while the loop sleeps
// between ticks is acted on by the next tick, which completes and stops it.
func TestLoop_BoundaryLostBetweenTicks(t *testing.T) {
	h, pub := newLossyHarness(t, 0, 0)
	var lossTime time.Time
	stop := h.clock.Now().Add(10 * time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.clock.OnSleep = func(until time.Time) bool {
		if lossTime.IsZero() && pub.acked == 7 {
			// Delivered through 7, and 8 is lost while the loop waits to
			// run it: the publisher refuses tick 8's boundary.
			lossTime = h.clock.Now()
			pub.lost = true
			h.loop.BoundaryLost(8, errBrokerGone)
		}
		if !until.Before(stop) {
			cancel()
			return false
		}
		return true
	}
	err := h.loop.Run(ctx)
	var lost *BoundaryLostError
	if !errors.As(err, &lost) || lost.Lost != 8 || lost.LastDelivered != 7 {
		t.Fatalf("Run returned %v, want boundary 8 lost after 7", err)
	}
	if got := h.engine.Tick(); got != 8 {
		t.Fatalf("engine stopped at tick %d, want 8", got)
	}
	if elapsed := h.clock.Now().Sub(lossTime); elapsed > h.loop.Interval() {
		t.Fatalf("stopped %v after the loss, more than one tick interval (%v)", elapsed, h.loop.Interval())
	}
}

// Under AwaitBoundaryAck a checkpoint waits for its boundary's delivery: with
// none acknowledged nothing is committed, and the acknowledgement of a
// checkpoint tick commits exactly that tick's offsets.
func TestLoop_CheckpointWaitsForDelivery(t *testing.T) {
	h, pub := newLossyHarness(t, 1000, 0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ticks := 0
	h.loop.opts.OnTick = func(res sim.StepResult, _ time.Duration) {
		ticks++
		switch res.Tick {
		case 12:
			if _, n := h.source.Committed(); n != 0 {
				t.Errorf("%d commits with no boundary delivered", n)
			}
			h.loop.BoundaryAcked(10)
		case 13:
			committed, n := h.source.Committed()
			if n != 1 || committed[sim.PartitionFor("town")] != boundaryAt(t, pub, 10).Offsets[sim.PartitionFor("town")] {
				t.Errorf("after tick 10's ack: %d commits, town at %d", n, committed[sim.PartitionFor("town")])
			}
			cancel()
		}
	}
	h.clock.OnSleep = func(time.Time) bool { return ctx.Err() == nil }
	if err := h.loop.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if ticks < 13 {
		t.Fatalf("ran %d ticks", ticks)
	}
	// The drain flushed nothing more: ticks 11.. were never acknowledged,
	// so the drain's checkpoint is still tick 10's.
	committed, _ := h.source.Committed()
	if got := committed[sim.PartitionFor("town")]; got != 10 {
		t.Fatalf("drain committed town at %d, want 10", got)
	}
}

// AC-5: the memory publisher cannot lose a boundary. A memory loop drains as
// it always has: Run returns nil, SimulationStopped says draining, the
// counter stays 0, and the drain commits the last tick's offsets.
func TestLoop_MemoryPublisherUnchanged(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.MaxPerTick = 1 })
	for range 7 {
		h.source.Push(simtest.Look("town", "a"))
	}
	// Stopped after tick 7, which is not a checkpoint tick (every 5): only
	// the drain's own checkpoint can commit 7.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.loop.opts.OnTick = func(res sim.StepResult, _ time.Duration) {
		if res.Tick == 7 {
			cancel()
		}
	}
	h.clock.OnSleep = func(time.Time) bool { return ctx.Err() == nil }
	if err := h.loop.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if got := h.engine.Tick(); got != 7 {
		t.Fatalf("stopped at tick %d, want 7", got)
	}
	if got := counter(h.loop.Metrics().BoundaryLost); got != 0 {
		t.Fatalf("andara_tick_boundary_lost_total %v", got)
	}
	evs, cs, _ := h.pub.Snapshot()
	if r := evs[len(evs)-1].Envelope.GetSimulationStopped().GetReason(); r != "draining" {
		t.Fatalf("SimulationStopped reason %q", r)
	}
	// The drain's own record is the zero TickCompleted, which is not written.
	last := cs[len(cs)-2]
	committed, _ := h.source.Committed()
	if got, want := committed[sim.PartitionFor("town")], last.Offsets[sim.PartitionFor("town")]; got != want || got != 7 || last.Tick != 7 || cs[len(cs)-1].Tick != 0 {
		t.Fatalf("drain committed town at %d, tick %d's boundary at %d", got, last.Tick, want)
	}
}

// Defense in depth behind boundarySeq: an acknowledgement for the lost tick
// or a later one, which the publisher should never report, moves neither
// what the stop calls delivered nor what is committed. Three layers hold
// this: BoundaryAcked's guard, stopLost's min, and commitAcked's cap. This
// test fails only with both of the first two removed; the cap has its own
// test below.
func TestLoop_AckAtOrPastTheLossIsIgnored(t *testing.T) {
	h, pub := newLossyHarness(t, 0, 0)
	h.loop.opts.OnTick = func(res sim.StepResult, _ time.Duration) {
		if res.Tick == 10 {
			// Tick 10's boundary is acknowledged by the stub. Then 11 is
			// lost, and a stray acknowledgement for 12 arrives.
			pub.lost = true
			h.loop.BoundaryLost(11, errBrokerGone)
			h.loop.BoundaryAcked(12)
		}
	}
	err := h.runFor(10 * time.Second)
	var lost *BoundaryLostError
	if !errors.As(err, &lost) || lost.Lost != 11 || lost.LastDelivered != 10 {
		t.Fatalf("Run returned %v, want boundary 11 lost after 10", err)
	}
	committed, _ := h.source.Committed()
	if got := committed[sim.PartitionFor("town")]; got != 10 {
		t.Fatalf("committed town at %d, want 10", got)
	}
}

// commitAcked's cap on its own: with the acknowledged tick already past the
// loss, which BoundaryAcked's guard otherwise prevents, nothing past the
// last tick before the loss is committed.
func TestLoop_CommitNeverPassesTheLoss(t *testing.T) {
	h := newHarness(t, nil)
	offsets := func(n int64) map[int32]int64 { return map[int32]int64{sim.PartitionFor("town"): n} }
	h.loop.pending = []sim.TickCompleted{{Tick: 10, Offsets: offsets(10)}, {Tick: 12, Offsets: offsets(12)}}
	h.loop.BoundaryLost(11, errBrokerGone)
	h.loop.acked.Store(12)
	h.loop.commitAcked(context.Background())
	committed, _ := h.source.Committed()
	if got := committed[sim.PartitionFor("town")]; got != 10 {
		t.Fatalf("committed town at %d, want 10: tick 12 is past the lost 11", got)
	}
}

// A loss recorded while the loop sleeps, followed by a shutdown before the
// next tick, still stops as a loss: SimulationStopped says boundary_lost and
// Run returns the BoundaryLostError, so the process exits 5, not 0 (review of
// #355).
func TestLoop_LossThenShutdownBeforeTheNextTick(t *testing.T) {
	h, pub := newLossyHarness(t, 0, 0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.clock.OnSleep = func(time.Time) bool {
		if pub.acked == 7 {
			pub.lost = true
			h.loop.BoundaryLost(8, errBrokerGone)
			cancel()
			return false
		}
		return true
	}
	err := h.loop.Run(ctx)
	var lost *BoundaryLostError
	if !errors.As(err, &lost) || lost.Lost != 8 || lost.LastDelivered != 7 {
		t.Fatalf("Run returned %v, want boundary 8 lost after 7", err)
	}
	evs, _, _ := pub.Snapshot()
	if r := evs[len(evs)-1].Envelope.GetSimulationStopped().GetReason(); r != StopReasonBoundaryLost {
		t.Fatalf("SimulationStopped reason %q", r)
	}
	if got := counter(h.loop.Metrics().BoundaryLost); got != 1 {
		t.Fatalf("andara_tick_boundary_lost_total %v", got)
	}
}

// The same through the top of the loop: a tick that overran skips the sleep,
// so a loss and a shutdown during it reach the loop's own context check.
func TestLoop_LossThenShutdownDuringAnOverrunTick(t *testing.T) {
	h, pub := newLossyHarness(t, 0, 0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.clock.OnSleep = func(time.Time) bool { return true }
	h.loop.opts.OnTick = func(res sim.StepResult, _ time.Duration) {
		if res.Tick == 7 {
			h.clock.Advance(2 * h.loop.Interval()) // overrun: no sleep follows
			pub.lost = true
			h.loop.BoundaryLost(8, errBrokerGone)
			cancel()
		}
	}
	err := h.loop.Run(ctx)
	var lost *BoundaryLostError
	if !errors.As(err, &lost) || lost.Lost != 8 || lost.LastDelivered != 7 {
		t.Fatalf("Run returned %v, want boundary 8 lost after 7", err)
	}
	if got := h.engine.Tick(); got != 7 {
		t.Fatalf("stopped at tick %d, want 7: the shutdown came before tick 8", got)
	}
	evs, _, _ := pub.Snapshot()
	if r := evs[len(evs)-1].Envelope.GetSimulationStopped().GetReason(); r != StopReasonBoundaryLost {
		t.Fatalf("SimulationStopped reason %q", r)
	}
	if got := counter(h.loop.Metrics().BoundaryLost); got != 1 {
		t.Fatalf("andara_tick_boundary_lost_total %v", got)
	}
}

// A boundary the shutdown's own flush fails is still reported as a loss:
// the error line, the counter, and Run's BoundaryLostError, so the process
// exits 5. Nothing past it is committed.
func TestLoop_LossDuringTheShutdownFlush(t *testing.T) {
	h, pub := newLossyHarness(t, 2, 0)
	pub.onClose = func() { h.loop.BoundaryLost(pub.acked+1, errBrokerGone) }
	err := h.runFor(time.Second)
	var lost *BoundaryLostError
	if !errors.As(err, &lost) || lost.Lost != pub.acked+1 || lost.LastDelivered != pub.acked {
		t.Fatalf("Run returned %v, want boundary %d lost after %d", err, pub.acked+1, pub.acked)
	}
	findLog(t, h.logs, "tick boundary lost; exiting into recovery")
	if got := counter(h.loop.Metrics().BoundaryLost); got != 1 {
		t.Fatalf("andara_tick_boundary_lost_total %v", got)
	}
	committed, _ := h.source.Committed()
	if got := committed[sim.PartitionFor("town")]; got > int64(lost.LastDelivered) {
		t.Fatalf("committed town at %d, past the last delivered tick %d", got, lost.LastDelivered)
	}
}
