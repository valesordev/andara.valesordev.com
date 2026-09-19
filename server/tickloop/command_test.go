// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package tickloop

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	logv1 "github.com/valesordev/andara/gen/go/andara/log/v1"
	"github.com/valesordev/andara/server/auth"
	"github.com/valesordev/andara/server/command"
	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/simtest"
)

// verbHarness is the loop over CrossingWorld with the real verb handlers,
// the pre-log pipeline in front of it, and the memory publisher feeding
// cross-Zone Commands back into the source — the whole pipeline with a
// fake log, which is what andara-cli sim repl drives.
type verbHarness struct {
	*harness
	pipeline *command.Pipeline
	metrics  *command.Metrics
}

func newVerbHarness(t *testing.T) *verbHarness {
	t.Helper()
	h := &harness{t: t, clock: NewSteppedClock(time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC)), source: NewMemorySource(), pub: &MemoryPublisher{}, logs: &syncBuffer{}, spans: tracetest.NewSpanRecorder()}
	h.pub.OnProduce = func(c *logv1.LoggedCommand) { h.source.Push(c) }
	e, err := simtest.NewVerbEngine(3)
	if err != nil {
		t.Fatal(err)
	}
	simtest.Place(e, "alice", "town", "plaza")
	simtest.Place(e, "bob", "town", "plaza")
	reg := prometheus.NewRegistry()
	table := command.Builtin()
	names := []string{}
	for _, v := range table.Verbs() {
		names = append(names, v.Name)
	}
	metrics := command.NewMetrics(reg, names)
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(h.spans))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	loop, err := New(Options{
		Engine: e, Source: h.source, Publisher: h.pub, Clock: h.clock,
		TickRate: 10, TickBudget: 50 * time.Millisecond, MaxPerTick: 8, DrainTimeout: 2 * time.Second, CheckpointEvery: 5,
		Log:      slog.New(slog.NewJSONHandler(h.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Tracer:   tp.Tracer("test"),
		Registry: reg,
		Commands: metrics,
	})
	if err != nil {
		t.Fatal(err)
	}
	e.SetObserver(loop)
	h.engine, h.loop = e, loop
	bindings := map[string]command.Binding{"s-alice": {Actor: "alice", Zone: "town"}, "s-bob": {Actor: "bob", Zone: "town"}}
	vh := &verbHarness{harness: h, metrics: metrics}
	vh.pipeline = &command.Pipeline{
		Table: table,
		Bindings: command.BinderFunc(func(_ context.Context, id string) (command.Binding, error) {
			if b, ok := bindings[id]; ok {
				return b, nil
			}
			return command.Binding{}, command.ErrNoBinding
		}),
		Log: command.ProducerFunc(func(_ context.Context, c *logv1.LoggedCommand) (command.Accepted, error) {
			r := h.source.Push(c)
			return command.Accepted{Partition: r.Partition, Offset: r.Offset}, nil
		}),
		Metrics: metrics,
		Tracer:  tp.Tracer("test"),
		Clock:   h.clock,
	}
	return vh
}

// runTicks runs the loop for n ticks of stepped time.
func (vh *verbHarness) runTicks(n int) {
	vh.t.Helper()
	if err := vh.runFor(time.Duration(n) * 100 * time.Millisecond); err != nil {
		vh.t.Fatal(err)
	}
}

func (vh *verbHarness) submit(session, raw string) error {
	_, err := vh.pipeline.Submit(context.Background(), command.Intent{SessionID: session, Raw: raw, ClientRef: raw}, playerPrincipal)
	return err
}

// The whole pipeline: an accepted move is applied by the next tick, a
// rejected one is counted post-log with its stage and code, and a parse
// failure never reaches the source.
func TestPipeline_EndToEnd(t *testing.T) {
	vh := newVerbHarness(t)
	if err := vh.submit("s-alice", "look"); err != nil {
		t.Fatal(err)
	}
	if err := vh.submit("s-alice", "west"); err != nil {
		t.Fatal(err)
	}
	if err := vh.submit("s-bob", "frobnicate"); !command.IsPreLog(err) {
		t.Fatalf("frobnicate: %v", err)
	}
	if err := vh.submit("s-alice", "north"); err != nil {
		t.Fatal(err)
	}
	if vh.source.Pending() != 3 {
		t.Fatalf("pending = %d: a pre-log rejection reached the log", vh.source.Pending())
	}
	vh.runTicks(2)

	events, _, _ := vh.pub.Snapshot()
	var types []string
	for _, ev := range events {
		types = append(types, string(ev.Type))
	}
	want := "room_described command_rejected character_left character_arrived"
	if got := strings.Join(types[:4], " "); got != want {
		t.Fatalf("events = %v", types)
	}
	if events[1].Envelope.GetCommandRejected().GetCode() != sim.CodeNoSuchExit || events[1].Envelope.GetClientRef() != "west" {
		t.Fatalf("rejection = %v", events[1].Envelope)
	}
	if vh.engine.State().Zones["town"].Entities["alice"].Room != "hall" {
		t.Fatal("alice did not move")
	}

	// AC-10: rejected by stage and code, pre_log distinguishing the sides.
	if got := testutil.ToFloat64(vh.metrics.Rejected.WithLabelValues("validate", "no_such_exit", "false")); got != 1 {
		t.Fatalf("rejected{validate,no_such_exit,false} = %v", got)
	}
	if got := testutil.ToFloat64(vh.metrics.Rejected.WithLabelValues("parse", "unknown_verb", "true")); got != 1 {
		t.Fatalf("rejected{parse,unknown_verb,true} = %v", got)
	}
	if got := testutil.ToFloat64(vh.metrics.Commands.WithLabelValues("look")); got != 1 {
		t.Fatalf("commands{look} = %v", got)
	}
	if got := testutil.ToFloat64(vh.metrics.Commands.WithLabelValues("frobnicate")); got != 0 {
		t.Fatal("an unknown verb reached andara_commands_total")
	}
	post := histogramCount(t, vh.metrics.Duration, map[string]string{"verb": "move", "phase": "post_log"})
	pre := histogramCount(t, vh.metrics.Duration, map[string]string{"verb": "look", "phase": "pre_log"})
	if post != 2 || pre != 1 {
		t.Fatalf("duration counts post_log/move=%d pre_log/look=%d", post, pre)
	}

	// The debug line per applied Command.
	var lines []map[string]any
	for _, l := range strings.Split(vh.logs.String(), "\n") {
		var m map[string]any
		if json.Unmarshal([]byte(l), &m) == nil && m["msg"] == "command applied" {
			lines = append(lines, m)
		}
	}
	if len(lines) != 3 {
		t.Fatalf("command applied lines = %d", len(lines))
	}
	for _, key := range []string{"verb", "actor", "tick", "partition", "offset", "duration_ms", "session_id", "trace_id"} {
		if _, ok := lines[0][key]; !ok {
			t.Errorf("debug line lacks %s: %v", key, lines[0])
		}
	}
	if lines[1]["code"] != "no_such_exit" || lines[1]["stage"] != "validate" {
		t.Errorf("rejected line = %v", lines[1])
	}

	// Traces: command.apply continues the Gateway's trace and links the tick.
	var applied []sdktrace.ReadOnlySpan
	var execTraces = map[trace.TraceID]bool{}
	for _, s := range vh.spans.Ended() {
		switch s.Name() {
		case "command.apply":
			applied = append(applied, s)
		case "command.execute":
			execTraces[s.SpanContext().TraceID()] = true
		}
	}
	if len(applied) != 3 {
		t.Fatalf("command.apply spans = %d", len(applied))
	}
	for _, s := range applied {
		if !execTraces[s.SpanContext().TraceID()] {
			t.Errorf("command.apply is not in a command.execute trace")
		}
		if len(s.Links()) != 1 {
			t.Errorf("command.apply has %d links, want the tick", len(s.Links()))
		}
		attrs := map[string]any{}
		for _, kv := range s.Attributes() {
			attrs[string(kv.Key)] = kv.Value.AsInterface()
		}
		if attrs["pre_log"] != false || attrs["verb"] == nil || attrs["partition"] == nil || attrs["offset"] == nil {
			t.Errorf("attrs = %v", attrs)
		}
		if attrs["verb"] == "move" && attrs["offset"] == int64(1) && attrs["stage_failed"] != "validate" {
			t.Errorf("rejected span attrs = %v", attrs)
		}
	}
}

// AC-9 through the loop: the memory publisher feeds the Arrive back into
// the source and the arrival resolves on the tick after the departure.
func TestPipeline_CrossZoneResolvesNextTick(t *testing.T) {
	vh := newVerbHarness(t)
	if err := vh.submit("s-alice", "east"); err != nil {
		t.Fatal(err)
	}
	var arrivedAt, leftAt sim.Tick
	vh.loop.opts.OnTick = func(res sim.StepResult, _ time.Duration) {
		for _, ev := range res.Events {
			switch ev.Type {
			case sim.EvCharacterLeft:
				leftAt = res.Tick
			case sim.EvCharacterArrived:
				arrivedAt = res.Tick
			}
		}
	}
	vh.runTicks(3)
	if leftAt == 0 || arrivedAt != leftAt+1 {
		t.Fatalf("left at tick %d, arrived at tick %d; the arrival resolves exactly one tick later", leftAt, arrivedAt)
	}
	if got := vh.engine.State().Zones["wilds"].Entities["alice"]; got == nil || got.Room != "trail" {
		t.Fatalf("alice = %+v", got)
	}
	_, _, produced := vh.pub.Snapshot()
	if len(produced) != 1 || produced[0].GetZoneId() != "wilds" {
		t.Fatalf("produced = %v", produced)
	}
}

var playerPrincipal = auth.Principal{AccountID: "acct-alice", Roles: []auth.Role{auth.RolePlayer}}

func histogramCount(t *testing.T, h *prometheus.HistogramVec, labels map[string]string) uint64 {
	t.Helper()
	obs, err := h.GetMetricWith(labels)
	if err != nil {
		t.Fatal(err)
	}
	var m dto.Metric
	if err := obs.(prometheus.Metric).Write(&m); err != nil {
		t.Fatal(err)
	}
	return m.GetHistogram().GetSampleCount()
}
