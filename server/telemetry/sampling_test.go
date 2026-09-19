// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package telemetry

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func sampledProvider(ratio float64) (*sdktrace.TracerProvider, *tracetest.InMemoryExporter) {
	exp := tracetest.NewInMemoryExporter()
	// The simple processor refuses unsampled spans exactly as the batch
	// one does, so what reaches exp is what a collector would.
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(NewSampler(ratio)),
		sdktrace.WithSpanProcessor(NewSpanFilter(sdktrace.NewSimpleSpanProcessor(exp))),
	)
	return tp, exp
}

func names(exp *tracetest.InMemoryExporter) []string {
	var out []string
	for _, s := range exp.GetSpans() {
		out = append(out, s.Name)
	}
	return out
}

func traceparentOf(ctx context.Context) string {
	c := propagation.MapCarrier{}
	propagation.TraceContext{}.Inject(ctx, c)
	return c.Get("traceparent")
}

// AW-SRV-010 inherited item 2: the Submit root is head-sampled at the
// ratio, the decision rides the traceparent the record carries, and a
// rejection is kept whatever the head said.
func TestSampler_SubmitRootAtRatio(t *testing.T) {
	tp, exp := sampledProvider(0)
	tracer := tp.Tracer("t")

	// Produced under an unsampled root: nothing exported, flag 00.
	ctx, root := tracer.Start(context.Background(), SubmitSpan)
	ectx, exec := tracer.Start(ctx, "command.execute")
	_, parse := tracer.Start(ectx, "command.parse")
	parse.End()
	tpar := traceparentOf(ectx)
	exec.End()
	root.End()
	if got := names(exp); len(got) != 0 {
		t.Fatalf("exported %v under an unsampled root", got)
	}
	if tpar[len(tpar)-2:] != "00" {
		t.Fatalf("traceparent %s carries the sampled flag", tpar)
	}

	// The tick side inherits it: command.apply on that traceparent is
	// dropped too.
	rctx := propagation.TraceContext{}.Extract(context.Background(), propagation.MapCarrier{"traceparent": tpar})
	_, applySpan := tracer.Start(rctx, "command.apply")
	applySpan.End()
	if got := names(exp); len(got) != 0 {
		t.Fatalf("command.apply exported under an unsampled parent: %v", got)
	}

	// A rejection under an unsampled root is kept, with its attributes.
	ctx, root = tracer.Start(context.Background(), SubmitSpan)
	_, exec = tracer.Start(ctx, "command.execute")
	exec.SetAttributes(attribute.String("stage_failed", "parse"), attribute.String("code", "unknown_verb"))
	exec.End()
	root.End()
	got := exp.GetSpans()
	if len(got) != 1 || got[0].Name != "command.execute" || !got[0].SpanContext.IsSampled() {
		t.Fatalf("rejection not kept: %v", names(exp))
	}
	found := false
	for _, kv := range got[0].Attributes {
		if kv.Key == "code" && kv.Value.AsString() == "unknown_verb" {
			found = true
		}
	}
	if !found {
		t.Fatal("kept rejection lost its attributes")
	}
	exp.Reset()

	// Ratio 1: everything, flag 01, and the tick side follows.
	tp1, exp1 := sampledProvider(1)
	tracer = tp1.Tracer("t")
	ctx, root = tracer.Start(context.Background(), SubmitSpan)
	ectx, exec = tracer.Start(ctx, "command.execute")
	tpar = traceparentOf(ectx)
	exec.End()
	root.End()
	rctx = propagation.TraceContext{}.Extract(context.Background(), propagation.MapCarrier{"traceparent": tpar})
	_, applySpan = tracer.Start(rctx, "command.apply")
	applySpan.End()
	if got := names(exp1); len(got) != 3 || tpar[len(tpar)-2:] != "01" {
		t.Fatalf("ratio 1 exported %v, traceparent %s", got, tpar)
	}
}

// A client that sent a traceparent decided already: sampled is honored
// under a ratio of zero, unsampled under a ratio of one.
func TestSampler_HonorsRemoteParent(t *testing.T) {
	tp, exp := sampledProvider(0)
	remote := propagation.TraceContext{}.Extract(context.Background(), propagation.MapCarrier{"traceparent": "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"})
	ctx, root := tp.Tracer("t").Start(remote, SubmitSpan)
	_, exec := tp.Tracer("t").Start(ctx, "command.execute")
	exec.End()
	root.End()
	if got := names(exp); len(got) != 2 {
		t.Fatalf("sampled client trace exported %v", got)
	}
	tp1, exp1 := sampledProvider(1)
	remote = propagation.TraceContext{}.Extract(context.Background(), propagation.MapCarrier{"traceparent": "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-00"})
	_, root = tp1.Tracer("t").Start(remote, SubmitSpan)
	root.End()
	if got := names(exp1); len(got) != 0 {
		t.Fatalf("unsampled client trace exported %v", got)
	}
}

// Every other root is sampled; the tick's own spans keep their tail rule,
// and a tick-produced command.apply keeps its tick's decision.
func TestSpanFilter_TickRules(t *testing.T) {
	tp, exp := sampledProvider(0)
	tracer := tp.Tracer("t")
	_, s := tracer.Start(context.Background(), "session.lifetime")
	s.End()
	ctx, tick := tracer.Start(context.Background(), "sim.tick")
	_, drop := tracer.Start(ctx, "command.apply", trace.WithAttributes(attribute.Bool("andara.keep", false)))
	drop.End()
	_, keep := tracer.Start(ctx, "command.apply", trace.WithAttributes(attribute.Bool("andara.keep", true)))
	keep.End()
	_, unmarked := tracer.Start(ctx, "command.apply")
	unmarked.End()
	tick.SetAttributes(attribute.Bool("andara.keep", false))
	tick.End()
	_, tick2 := tracer.Start(context.Background(), "sim.tick", trace.WithAttributes(attribute.Bool("andara.keep", true)))
	tick2.End()
	got := names(exp)
	want := []string{"session.lifetime", "command.apply", "command.apply", "sim.tick"}
	if len(got) != len(want) {
		t.Fatalf("exported %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("exported %v, want %v", got, want)
		}
	}
}
