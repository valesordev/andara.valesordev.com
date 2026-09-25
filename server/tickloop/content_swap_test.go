// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package tickloop

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/valesordev/andara/server/command"
	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/simtest"
)

// AW-SRV-012 §8 (2026-09-25): content.swap is a child of the content.load
// that produced the swap — through the record's trace_id, a W3C traceparent —
// and carries a link to the sim.tick that applied it.
func TestContentSwap_SpanIsAChildOfTheLoadAndLinksTheTick(t *testing.T) {
	spans := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	tracer := tp.Tracer("test")

	c, err := simtest.CrossingContent()
	if err != nil {
		t.Fatal(err)
	}
	engine := sim.NewEngine(sim.EmptyWorld(), nil, sim.Config{Seed: 11, Partitions: simtest.AllPartitions(), Handlers: sim.Handlers(), Content: c})
	source := NewMemorySource()
	clock := NewSteppedClock(time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC))
	var stalls int
	loop, err := New(Options{
		Engine: engine, Source: source, Publisher: &MemoryPublisher{}, Clock: clock,
		TickRate: 10, TickBudget: 50 * time.Millisecond, MaxPerTick: 8, DrainTimeout: 2 * time.Second, CheckpointEvery: 5,
		Log: slog.New(slog.DiscardHandler), Registry: prometheus.NewRegistry(), Tracer: tracer,
		OnSwap: func(time.Duration) { stalls++ },
	})
	if err != nil {
		t.Fatal(err)
	}
	engine.SetObserver(loop)

	// The Loader's side: a content.load span, and the swap it produced
	// carrying that span's traceparent.
	ctx, load := tracer.Start(context.Background(), "content.load")
	genesis := c.Genesis()
	genesis.TraceId = command.TraceParent(ctx)
	load.End()
	source.Push(genesis)

	runCtx, cancel := context.WithCancel(context.Background())
	stop := clock.Now().Add(time.Second)
	clock.OnSleep = func(until time.Time) bool {
		if !until.Before(stop) {
			cancel()
			return false
		}
		return true
	}
	if err := loop.Run(runCtx); err != nil {
		t.Fatal(err)
	}
	if v, _ := engine.Content(); v[simtest.FixturePack] != 1 || stalls != 1 {
		t.Fatalf("in effect %v, %d stall observations; want the genesis swap applied once", v, stalls)
	}

	var swap sdktrace.ReadOnlySpan
	ticks := map[string]bool{}
	for _, s := range spans.Ended() {
		switch s.Name() {
		case "content.swap":
			swap = s
		case "sim.tick":
			ticks[s.SpanContext().SpanID().String()] = true
		}
	}
	if swap == nil {
		t.Fatal("no content.swap span")
	}
	if swap.Parent().SpanID() != load.SpanContext().SpanID() || swap.SpanContext().TraceID() != load.SpanContext().TraceID() {
		t.Fatalf("content.swap parent %v, want content.load %v", swap.Parent(), load.SpanContext())
	}
	linked := false
	for _, l := range swap.Links() {
		linked = linked || ticks[l.SpanContext.SpanID().String()]
	}
	if !linked {
		t.Fatalf("content.swap links %v name no sim.tick span", swap.Links())
	}
}
