// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package tickloop

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	logv1 "github.com/valesordev/andara/gen/go/andara/log/v1"
	"github.com/valesordev/andara/server/command"
	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/simtest"
)

// AW-SRV-028 through the live loop: the retry pass runs after Step, outside
// it; recovery goes through RecoverFrom, which replays and never retries.

// recLog is the records a lossy log held, by Partition: the record source a
// recovery reads. A record the producer dropped isn't in it, as a lost
// Arrive isn't in the log.
type recLog struct {
	mu   sync.Mutex
	recs map[int32][]sim.Record
}

func (l *recLog) add(r sim.Record) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.recs == nil {
		l.recs = map[int32][]sim.Record{}
	}
	l.recs[r.Partition] = append(l.recs[r.Partition], r)
}

func (l *recLog) Fetch(p int32, from, to int64) ([]sim.Record, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if int64(len(l.recs[p])) < to {
		return nil, fmt.Errorf("partition %d has %d records, want up to %d", p, len(l.recs[p]), to)
	}
	return append([]sim.Record(nil), l.recs[p][from:to]...), nil
}

// lossyHarness is the verb harness with a log that records every record that
// reaches it and drops each cross-Zone Command drop says to.
func lossyHarness(t *testing.T, drop func(*logv1.LoggedCommand) bool) (*verbHarness, *recLog) {
	t.Helper()
	vh := newVerbHarness(t)
	log := &recLog{}
	push := vh.pipeline.Log
	vh.pipeline.Log = command.ProducerFunc(func(ctx context.Context, c *logv1.LoggedCommand) (command.Accepted, error) {
		a, err := push.Produce(ctx, c)
		if err == nil {
			// The pipeline's own push assigned the offset; record what it did.
			log.add(sim.Record{Partition: a.Partition, Offset: a.Offset, Command: c})
		}
		return a, err
	})
	vh.pub.OnProduce = func(c *logv1.LoggedCommand) {
		if drop != nil && drop(c) {
			return
		}
		log.add(vh.source.Push(c))
	}
	return vh, log
}

func gauge(g prometheus.Gauge) float64 { return testutil.ToFloat64(g) }

func arrives(cmds []*logv1.LoggedCommand) []*logv1.LoggedCommand {
	var out []*logv1.LoggedCommand
	for _, c := range cmds {
		if c.GetArrive() != nil {
			out = append(out, c)
		}
	}
	return out
}

// A lost Arrive is produced again by the loop after the retry interval, the
// retry is counted, and the World converges: one alice, in wilds, the record
// gone, the gauge back to 0.
func TestHandoffLoop_ALostArriveIsRetriedAndTheWorldConverges(t *testing.T) {
	dropped := false
	vh, _ := lossyHarness(t, func(c *logv1.LoggedCommand) bool {
		if c.GetArrive() != nil && !dropped {
			dropped = true
			return true
		}
		return false
	})
	if err := vh.submit("s-alice", "east"); err != nil {
		t.Fatal(err)
	}
	vh.runTicks(3)
	if n := gauge(vh.loop.metrics.HandoffsInTransit); n != 1 {
		t.Fatalf("andara_handoffs_in_transit = %v after the Arrive was lost, want 1", n)
	}
	if got := counter(vh.loop.metrics.HandoffRetries); got != 0 {
		t.Fatalf("a retry before the interval: %v", got)
	}
	vh.runTicks(20)

	if got := counter(vh.loop.metrics.HandoffRetries); got != 1 {
		t.Fatalf("andara_handoff_retries_total = %v, want 1", got)
	}
	_, _, produced := vh.pub.Snapshot()
	as := arrives(produced)
	if len(as) != 2 || as[0].GetArrive().GetHandoffSeq() != 1 || as[1].GetArrive().GetHandoffSeq() != 1 {
		t.Fatalf("produced Arrives = %v, want the first and one retry, both seq 1", as)
	}
	st := vh.engine.State()
	if st.Zones["wilds"].Entities["alice"] == nil || st.Zones["town"].Entities["alice"] != nil || len(st.Zones["town"].Transit) != 0 {
		t.Fatalf("the World did not converge: wilds %v, town %v, transit %v", st.Zones["wilds"].Entities, st.Zones["town"].Entities, st.Zones["town"].Transit)
	}
	if n := gauge(vh.loop.metrics.HandoffsInTransit); n != 0 {
		t.Fatalf("andara_handoffs_in_transit = %v after the ack, want 0", n)
	}
	if n := gauge(vh.loop.metrics.HandoffPlacedEntries); n != 1 {
		t.Fatalf("andara_handoff_placed_entries = %v, want wilds' one mark", n)
	}
	logs := vh.logs.String()
	if !strings.Contains(logs, "handoffs retried") || !strings.Contains(logs, `"oldest_attempt":2`) {
		t.Errorf("no warn for the tick that produced a retry with the count and the oldest attempt:\n%s", logs)
	}
	if !strings.Contains(logs, `"msg":"handoff retried"`) || !strings.Contains(logs, `"entity_id":"alice"`) {
		t.Errorf("no debug line per retry naming the Entity")
	}
}

// AC-2, through tickloop.RecoverFrom (which uses ReplayEach, as the state
// projector does) and not only Replay: the process dies with alice in
// transit and the Arrive lost; the recovered World has her in Transit at the
// recovered hash, recovery produced no retry and counted none, and the
// recovered loop's first ticks retry her once, so she arrives in wilds
// exactly once.
func TestHandoffLoop_RecoveryRetriesFromTheRecoveredTransit(t *testing.T) {
	vh, log := lossyHarness(t, func(c *logv1.LoggedCommand) bool { return c.GetArrive() != nil })
	if err := vh.submit("s-alice", "east"); err != nil {
		t.Fatal(err)
	}
	vh.runTicks(14) // past the retry interval: every retry is dropped too
	if len(vh.engine.State().Zones["town"].Transit) != 1 {
		t.Fatal("setup: alice should be in transit")
	}
	if got := counter(vh.loop.metrics.HandoffRetries); got < 1 {
		t.Fatalf("setup: the original process never retried (%v)", got)
	}
	_, completed, _ := vh.pub.Snapshot()
	var boundaries []sim.TickCompleted
	for _, b := range completed {
		if b.Tick > 0 { // the loop's shutdown writes a zero-tick record
			boundaries = append(boundaries, b)
		}
	}
	hashAtDeath := vh.engine.StateHash()

	// A fresh process: the same starting World, the log replayed.
	e2, err := simtest.NewVerbEngine(3)
	if err != nil {
		t.Fatal(err)
	}
	simtest.Place(e2, "alice", "town", "plaza")
	simtest.Place(e2, "bob", "town", "plaza")
	var outbound int
	replayed, err := RecoverFrom(boundaries, log, e2, func(res sim.StepResult) error {
		outbound += len(arrives(res.Outbound))
		return nil
	})
	if err != nil {
		t.Fatalf("RecoverFrom: %v", err)
	}
	if replayed == 0 || e2.StateHash() != hashAtDeath {
		t.Fatalf("the recovered World does not match: replayed %d ticks, hashes equal %v", replayed, e2.StateHash() == hashAtDeath)
	}
	if len(e2.State().Zones["town"].Transit) != 1 || e2.State().Zones["wilds"].Entities["alice"] != nil {
		t.Fatal("alice should still be in town's Transit after recovery")
	}

	// The recovered loop, with a log that now delivers: its first live ticks
	// retry. Nothing before that did.
	src := NewMemorySource()
	for p, off := range e2.State().Offsets {
		src.next[p], src.end[p] = off, off
	}
	pub := &MemoryPublisher{}
	pub.OnProduce = func(c *logv1.LoggedCommand) { src.Push(c) }
	loop2 := recoveredLoop(t, e2, src, pub)
	if got := counter(loop2.metrics.HandoffRetries); got != 0 {
		t.Fatalf("recovery counted %v retries; replay never retries", got)
	}
	// Within its first ticks, not eventually: a record found after a
	// recovery is due on the first live call. (A replay that ran the retry
	// pass would have refreshed the schedule, and the retry would wait out a
	// whole interval.)
	if err := runLoopFor(loop2, 300*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if got := counter(loop2.metrics.HandoffRetries); got != 1 {
		t.Fatalf("the recovered loop's retries after 3 ticks = %v, want one for the one record in transit", got)
	}
	if err := runLoopFor(loop2, 3*time.Second); err != nil {
		t.Fatal(err)
	}
	if got := counter(loop2.metrics.HandoffRetries); got != 1 {
		t.Fatalf("the recovered loop's retries = %v, want exactly one", got)
	}
	st := e2.State()
	if st.Zones["wilds"].Entities["alice"] == nil || st.Zones["town"].Entities["alice"] != nil || len(st.Zones["town"].Transit) != 0 {
		t.Fatalf("alice did not arrive exactly once: wilds %v, town %v, transit %v", st.Zones["wilds"].Entities, st.Zones["town"].Entities, st.Zones["town"].Transit)
	}
}

// recoveredLoop is a loop over a recovered engine and a primed source.
func recoveredLoop(t *testing.T, e *sim.Engine, src *MemorySource, pub *MemoryPublisher) *Loop {
	t.Helper()
	tp := sdktrace.NewTracerProvider()
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	loop, err := New(Options{
		Engine: e, Source: src, Publisher: pub, Clock: loopClock,
		TickRate: 10, TickBudget: 50 * time.Millisecond, MaxPerTick: 64, DrainTimeout: 2 * time.Second, CheckpointEvery: 5,
		Log:      slog.New(slog.NewJSONHandler(&syncBuffer{}, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Tracer:   tp.Tracer("test"),
		Registry: prometheus.NewRegistry(),
	})
	if err != nil {
		t.Fatal(err)
	}
	e.SetObserver(loop)
	return loop
}

// loopClock is the stepped clock recoveredLoop's loop runs on.
var loopClock = NewSteppedClock(time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC))

// runLoopFor runs loop for d of stepped time.
func runLoopFor(loop *Loop, d time.Duration) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := loopClock.Now().Add(d)
	loopClock.OnSleep = func(until time.Time) bool {
		if !until.Before(stop) {
			cancel()
			return false
		}
		return true
	}
	return loop.Run(ctx)
}

// The loop counts a stale Arrive (andara_handoff_stale_arrivals_total) and
// logs the Commands no well-formed producer writes, or no sequence of marks
// explains, at error rather than at the debug line every command gets.
func TestHandoffLoop_AStaleArriveIsCountedAndAnImpossibleOneIsLoggedAtError(t *testing.T) {
	vh, _ := lossyHarness(t, nil)
	vh.engine.State().Zones["wilds"].Placed = map[sim.EntityID]sim.PlacedMark{"zed": {Seq: 5}}
	arrive := func(seq, entitySeq uint64) *logv1.LoggedCommand {
		e := sim.EntityState{ID: "zed", Template: "andara.core.Character", ContentVersion: "core@1", HandoffSeq: entitySeq}
		return &logv1.LoggedCommand{ZoneId: "wilds", ActorId: "zed", Command: &logv1.LoggedCommand_Arrive{Arrive: &logv1.Arrive{
			RoomId: "trail", Entity: e.Proto(), OriginZoneId: "town", OriginRoomId: "plaza", HandoffSeq: seq,
		}}}
	}
	vh.source.Push(arrive(3, 3)) // at or below the mark: stale
	vh.runTicks(2)
	if got := counter(vh.loop.metrics.HandoffStaleArrivals); got != 1 {
		t.Fatalf("andara_handoff_stale_arrivals_total = %v, want 1", got)
	}
	if strings.Contains(vh.logs.String(), `"level":"ERROR"`) {
		t.Fatalf("a stale Arrive is not an error:\n%s", vh.logs.String())
	}
	vh.source.Push(arrive(0, 0)) // seq 0: malformed
	vh.runTicks(2)
	logs := vh.logs.String()
	if !strings.Contains(logs, `"level":"ERROR"`) || !strings.Contains(logs, "handoff or bind refused: invalid_arrival") || !strings.Contains(logs, `"entity_id":"zed"`) {
		t.Fatalf("no error line naming invalid_arrival and the Entity:\n%s", logs)
	}
	if got := counter(vh.loop.metrics.HandoffStaleArrivals); got != 1 {
		t.Fatalf("a malformed Arrive was counted stale: %v", got)
	}
}
