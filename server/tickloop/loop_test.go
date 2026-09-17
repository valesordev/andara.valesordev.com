// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package tickloop

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"flag"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	logv1 "github.com/valesordev/andara/gen/go/andara/log/v1"
	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/simtest"
)

var update = flag.Bool("update", false, "rewrite golden files")

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type harness struct {
	t      *testing.T
	clock  *SteppedClock
	source *MemorySource
	pub    *MemoryPublisher
	engine *sim.Engine
	loop   *Loop
	logs   *syncBuffer
	spans  *tracetest.SpanRecorder
	// cost is how long each Command "takes"; a handler advances the clock.
	cost time.Duration
	mu   sync.Mutex
}

func (h *harness) setCost(d time.Duration) {
	h.mu.Lock()
	h.cost = d
	h.mu.Unlock()
}

func newHarness(t *testing.T, mutate func(*Options)) *harness {
	t.Helper()
	h := &harness{t: t, clock: NewSteppedClock(time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC)), source: NewMemorySource(), pub: &MemoryPublisher{}, logs: &syncBuffer{}, spans: tracetest.NewSpanRecorder()}
	w, err := simtest.World()
	if err != nil {
		t.Fatal(err)
	}
	reg, err := simtest.Templates()
	if err != nil {
		t.Fatal(err)
	}
	handlers := simtest.Handlers(reg)
	// Every Command costs h.cost of stepped time.
	for k, apply := range handlers {
		apply := apply
		handlers[k] = func(a *sim.ApplyContext, cmd *logv1.LoggedCommand) error {
			h.mu.Lock()
			c := h.cost
			h.mu.Unlock()
			h.clock.Advance(c)
			return apply(a, cmd)
		}
	}
	o := Options{
		Source: h.source, Publisher: h.pub, Clock: h.clock,
		TickRate: 10, TickBudget: 50 * time.Millisecond, MaxPerTick: 8, DrainTimeout: 2 * time.Second, CheckpointEvery: 5,
		Log:      slog.New(slog.NewJSONHandler(h.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Registry: prometheus.NewRegistry(),
	}
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(h.spans))
	o.Tracer = tp.Tracer("test")
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	if mutate != nil {
		mutate(&o)
	}
	// The engine needs the loop as its ZoneTimer, and the loop needs the
	// engine: build the loop with a placeholder and swap.
	cfg := sim.Config{Seed: 11, Partitions: simtest.AllPartitions(), Handlers: handlers}
	h.engine = sim.NewEngine(w, reg, cfg)
	o.Engine = h.engine
	loop, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	h.loop = loop
	cfg.ZoneTimer = loop
	h.engine = sim.NewEngine(w, reg, cfg)
	loop.opts.Engine = h.engine
	return h
}

// runFor runs the loop until stepped time reaches d past the start.
func (h *harness) runFor(d time.Duration) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := h.clock.Now().Add(d)
	h.clock.OnSleep = func(until time.Time) bool {
		if !until.Before(stop) {
			cancel()
			return false
		}
		return true
	}
	return h.loop.Run(ctx)
}

func counter(c prometheus.Counter) float64 { return testutil.ToFloat64(c) }

// AC-6, AC-16: at 10 Hz, ten seconds of stepped time is exactly 100 ticks,
// no tick number skipped, the interval 100 ms and the budget 50 ms.
func TestLoop_ScheduleAdherence(t *testing.T) {
	h := newHarness(t, nil)
	if h.loop.Interval() != 100*time.Millisecond {
		t.Fatalf("interval %s", h.loop.Interval())
	}
	if err := h.runFor(10 * time.Second); err != nil {
		t.Fatal(err)
	}
	if got := h.engine.Tick(); got != 100 {
		t.Fatalf("ticks = %d, want 100", got)
	}
	if counter(h.loop.Metrics().Ticks) != 100 || counter(h.loop.Metrics().Overruns) != 0 {
		t.Errorf("ticks_total=%v overruns=%v", counter(h.loop.Metrics().Ticks), counter(h.loop.Metrics().Overruns))
	}
	_, completed, _ := h.pub.Snapshot()
	for i, tc := range completed[:100] {
		if tc.Tick != sim.Tick(i+1) {
			t.Fatalf("boundary %d is tick %d", i, tc.Tick)
		}
	}
	// The budget is on a histogram boundary.
	found := false
	for _, b := range TickBuckets {
		if b == 0.05 {
			found = true
		}
	}
	if !found {
		t.Error("no 0.05 bucket")
	}
	if !strings.Contains(h.logs.String(), `"msg":"tick"`) {
		t.Error("no once-per-second summary line")
	}
	// Drain emitted SimulationStopped and checkpointed.
	events, _, _ := h.pub.Snapshot()
	if last := events[len(events)-1]; last.Type != sim.EvSimulationStopped {
		t.Errorf("last event %v", last.Type)
	}
	committed, n := h.source.Committed()
	if n < 20 || len(committed) != int(sim.PartitionCount) {
		t.Errorf("commits=%d partitions=%d", n, len(committed))
	}
}

// AC-7: a tick over budget increments the overrun counter and logs at warn
// with the tick and its duration; AC-8: sustained 3× overload raises lag
// monotonically, never spins, and never drops input.
func TestLoop_OverrunAndLag(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.MaxPerTick = 1 })
	// 150 ms per Command, one Command per tick: every tick is 3× budget.
	h.setCost(150 * time.Millisecond)
	for i := 0; i < 40; i++ {
		h.source.Push(simtest.Look("town", "a"))
	}
	var lags []float64
	h.loop.opts.OnTick = func(res sim.StepResult, _ time.Duration) {
		lags = append(lags, testutil.ToFloat64(h.loop.Metrics().Lag))
	}
	if err := h.runFor(6 * time.Second); err != nil {
		t.Fatal(err)
	}
	ticks := h.engine.Tick()
	if ticks == 0 || counter(h.loop.Metrics().Overruns) < 30 {
		t.Fatalf("ticks=%d overruns=%v", ticks, counter(h.loop.Metrics().Overruns))
	}
	// Lag rises monotonically while the backlog lasts and every Command is
	// applied — none dropped, none skipped.
	for i := 1; i < 40 && i < len(lags); i++ {
		if lags[i] < lags[i-1] {
			t.Fatalf("lag fell at tick %d: %v -> %v", i+1, lags[i-1], lags[i])
		}
	}
	if lags[39] <= lags[0] {
		t.Fatalf("lag did not rise under 3x load: %v", lags[:40])
	}
	if counter(h.loop.Metrics().AppliedRecords) != 40 || h.source.Pending() != 0 {
		t.Errorf("applied=%v pending=%d", counter(h.loop.Metrics().AppliedRecords), h.source.Pending())
	}
	line := findLog(t, h.logs, "tick overran its budget")
	if line["budget_ms"] != float64(50) || line["duration_ms"].(float64) < 150 || line["zone"] != "town" {
		t.Errorf("overrun line %v", line)
	}
	// The overrun tick's span says so.
	overrun := 0
	for _, s := range h.spans.Ended() {
		if s.Name() == "sim.tick" {
			for _, a := range s.Attributes() {
				if string(a.Key) == "overrun" && a.Value.AsBool() {
					overrun++
				}
			}
		}
	}
	if overrun < 30 {
		t.Errorf("%d overrun spans", overrun)
	}
}

// AC-9: with the broker gone the loop keeps its schedule applying nothing,
// counts starvation, and does not crash; when input returns it is applied.
func TestLoop_Starvation(t *testing.T) {
	h := newHarness(t, nil)
	h.source.SetUnavailable(true)
	h.loop.opts.OnTick = func(res sim.StepResult, _ time.Duration) {
		if res.Tick == 20 {
			h.source.SetUnavailable(false)
			h.source.Push(simtest.Look("town", "a"))
		}
	}
	if err := h.runFor(3 * time.Second); err != nil {
		t.Fatal(err)
	}
	if h.engine.Tick() != 30 || counter(h.loop.Metrics().InputStarved) != 20 {
		t.Fatalf("ticks=%d starved=%v", h.engine.Tick(), counter(h.loop.Metrics().InputStarved))
	}
	if counter(h.loop.Metrics().AppliedRecords) != 1 {
		t.Errorf("applied %v after the broker returned", counter(h.loop.Metrics().AppliedRecords))
	}
	if !strings.Contains(h.logs.String(), "tick input starved") {
		t.Error("no starvation warning")
	}
}

// max_per_tick defers, round-robin across Partitions, and the backlog gauge
// reports what waits.
func TestLoop_DeferralRoundRobin(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.MaxPerTick = 4 })
	for i := 0; i < 10; i++ {
		h.source.Push(simtest.Look("town", "a"))
	}
	for i := 0; i < 2; i++ {
		h.source.Push(simtest.Look("docks", "b"))
	}
	var firstTick sim.StepResult
	h.loop.opts.OnTick = func(res sim.StepResult, _ time.Duration) {
		if res.Tick == 1 {
			firstTick = res
		}
	}
	if err := h.runFor(500 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	// Tick 1 took two from each Partition, not four from town.
	if firstTick.Completed.CommandsApplied != 4 {
		t.Fatalf("tick 1 applied %d", firstTick.Completed.CommandsApplied)
	}
	if got := firstTick.Completed.Offsets[sim.PartitionFor("docks")]; got != 2 {
		t.Errorf("docks after tick 1: %d, the busy Zone starved it", got)
	}
	if counter(h.loop.Metrics().AppliedRecords) != 12 || h.source.Pending() != 0 {
		t.Errorf("applied=%v pending=%d", counter(h.loop.Metrics().AppliedRecords), h.source.Pending())
	}
}

func TestSelectRoundRobin(t *testing.T) {
	rec := func(p int32, o int64) sim.Record { return sim.Record{Partition: p, Offset: o} }
	buffers := map[int32][]sim.Record{
		1: {rec(1, 0), rec(1, 1), rec(1, 2)},
		2: {rec(2, 0)},
		3: {rec(3, 0), rec(3, 1)},
	}
	got := selectRoundRobin(buffers, []int32{3}, 3)
	want := []sim.Record{rec(1, 0), rec(1, 1), rec(2, 0)}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("got %v", got)
	}
	if len(buffers[1]) != 1 || len(buffers[2]) != 0 || len(buffers[3]) != 2 {
		t.Errorf("buffers after: %v", buffers)
	}
	requeue(buffers, []sim.Record{rec(1, 1)})
	if buffers[1][0].Offset != 1 || buffers[1][1].Offset != 2 {
		t.Errorf("requeue: %v", buffers[1])
	}
}

// AC-12 at the loop: a faulted Zone's Partition is frozen and its records
// wait; the fault is counted and logged; other Zones keep going.
func TestLoop_ZoneFault(t *testing.T) {
	h := newHarness(t, nil)
	h.source.Push(simtest.Look("town", "a"))
	h.source.Push(simtest.Move("town", "a", "panic"))
	h.source.Push(simtest.Look("town", "b"))
	h.source.Push(simtest.Look("docks", "c"))
	if err := h.runFor(time.Second); err != nil {
		t.Fatal(err)
	}
	if got := testutil.ToFloat64(h.loop.Metrics().ZoneFaults.WithLabelValues("town")); got != 1 {
		t.Fatalf("zone_faults{town} = %v", got)
	}
	line := findLog(t, h.logs, "zone faulted: partition frozen")
	if line["zone"] != "town" || line["level"] != "ERROR" || !strings.Contains(line["panic"].(string), "blew up") {
		t.Errorf("fault line %v", line)
	}
	if h.source.Pending() != 2 {
		t.Errorf("pending %d, want the panicking record and the one behind it", h.source.Pending())
	}
	committed, _ := h.source.Committed()
	if committed[sim.PartitionFor("town")] != 1 || committed[sim.PartitionFor("docks")] != 1 {
		t.Errorf("committed %v", committed)
	}
	if h.engine.Tick() != 10 {
		t.Errorf("the loop stopped ticking: %d", h.engine.Tick())
	}
}

// AC-15: a drain that outlasts the timeout exits with the tick named.
func TestLoop_DrainTimeout(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.DrainTimeout = 50 * time.Millisecond })
	block := make(chan struct{})
	h.engine = nil
	// A handler that never returns: the wedged tick.
	w, _ := simtest.World()
	reg, _ := simtest.Templates()
	handlers := simtest.Handlers(reg)
	handlers["look"] = func(*sim.ApplyContext, *logv1.LoggedCommand) error { <-block; return nil }
	h.loop.opts.Engine = sim.NewEngine(w, reg, sim.Config{Seed: 1, Partitions: simtest.AllPartitions(), Handlers: handlers})
	h.source.Push(simtest.Look("town", "a"))
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	err := h.loop.Run(ctx)
	var dt *DrainTimeoutError
	if !errors.As(err, &dt) || dt.Tick != 1 {
		t.Fatalf("got %v", err)
	}
	close(block)
}

// An offset gap from the source stops the loop rather than skipping history.
func TestLoop_OffsetGapIsFatal(t *testing.T) {
	h := newHarness(t, nil)
	h.source.Push(simtest.Look("town", "a"))
	// Corrupt the buffer: the next record claims offset 5.
	h.source.mu.Lock()
	p := sim.PartitionFor("town")
	h.source.buffers[p] = append(h.source.buffers[p], sim.Record{Partition: p, Offset: 5, Command: simtest.Look("town", "b")})
	h.source.mu.Unlock()
	err := h.runFor(time.Second)
	if !errors.Is(err, sim.ErrOffsetGap) {
		t.Fatalf("got %v", err)
	}
}

// AC-1: 1000 ticks over the scripted log hash to a golden sequence.
func TestLoop_GoldenHashSequence(t *testing.T) {
	e, err := simtest.NewEngine(7)
	if err != nil {
		t.Fatal(err)
	}
	remaining := simtest.Script(250)
	var got []string
	for tick := 1; tick <= 1000; tick++ {
		res, err := e.Step(simtest.Batch(remaining, 1))
		if err != nil {
			t.Fatal(err)
		}
		if res.Tick != sim.Tick(tick) {
			t.Fatalf("tick %d advanced to %d", tick, res.Tick)
		}
		got = append(got, hex.EncodeToString(res.Completed.StateHash[:]))
	}
	path := filepath.Join("testdata", "golden_hashes.txt")
	if *update {
		if err := os.WriteFile(path, []byte(strings.Join(got, "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update to write it)", err)
	}
	wantLines := strings.Split(strings.TrimSpace(string(want)), "\n")
	if len(wantLines) != len(got) {
		t.Fatalf("golden has %d hashes, produced %d", len(wantLines), len(got))
	}
	for i := range got {
		if got[i] != wantLines[i] {
			t.Fatalf("tick %d: hash %s, golden %s — a determinism regression, or an intended state change that needs -update and a state_version decision", i+1, got[i][:16], wantLines[i][:16])
		}
	}
}

func findLog(t *testing.T, logs *syncBuffer, msg string) map[string]any {
	t.Helper()
	for _, line := range strings.Split(logs.String(), "\n") {
		if !strings.Contains(line, `"msg":"`+msg+`"`) {
			continue
		}
		var m map[string]any
		if err := jsonUnmarshal([]byte(line), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	t.Fatalf("no log line %q in:\n%s", msg, logs.String())
	return nil
}
