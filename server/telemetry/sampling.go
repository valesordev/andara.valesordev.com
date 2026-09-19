// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package telemetry

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// Sampling, in two halves that answer two different questions.
//
// Sampler is the head decision: whether a trace is sampled at all, made
// where its root span starts and carried in the sampled flag from there —
// through the Gateway's spans, into LoggedCommand.trace_id, and out to the
// tick's command.apply, which inherits it as a remote parent. That is
// AW-SRV-010's rule (confirmed 2026-09-18): the Game/Submit root is
// sampled at telemetry.trace_sample_ratio; a client that sent a traceparent
// made the decision itself and is honored, sampled or not; every other root
// is sampled, because none of them is per-keystroke.
//
// SpanFilter is the tail decision for spans OTel cannot sample at the head:
// sim.tick and sim.zone_tick, always started and kept one in a hundred or on
// overrun (AW-SRV-002); command.apply on a tick-produced record, which has
// no Gateway root and keeps its tick's one-in-a-hundred; and a rejected
// command.execute under an unsampled root, which is kept whatever the head
// said — every rejection is exported. Those are marked with KeepAttr, or by
// the rejection's stage_failed attribute, and the filter forwards or drops.

// KeepAttr is the attribute a span sets to override the head decision:
// true on a sim.tick, sim.zone_tick, or tick-produced command.apply span
// to export it, false to drop it.
const KeepAttr = attribute.Key("andara.keep")

// StageFailedAttr marks a command.execute span that rejected the Intent;
// the filter keeps it even under an unsampled root.
const StageFailedAttr = attribute.Key("stage_failed")

// SubmitSpan is the name the Gateway's trace interceptor gives the
// Game/Submit RPC span, the root the ratio applies to.
const SubmitSpan = "andara.game.v1.Game/Submit"

// Sampler implements sdktrace.Sampler as described above.
type Sampler struct {
	ratio float64
	inner sdktrace.Sampler
}

// NewSampler builds the head sampler for trace_sample_ratio in [0, 1].
func NewSampler(ratio float64) *Sampler {
	return &Sampler{ratio: ratio, inner: sdktrace.TraceIDRatioBased(ratio)}
}

// ShouldSample implements sdktrace.Sampler.
func (s *Sampler) ShouldSample(p sdktrace.SamplingParameters) sdktrace.SamplingResult {
	psc := trace.SpanContextFromContext(p.ParentContext)
	state := psc.TraceState()
	if psc.IsValid() {
		if psc.IsSampled() {
			return sdktrace.SamplingResult{Decision: sdktrace.RecordAndSample, Tracestate: state}
		}
		// Under an unsampled root only command.execute records, so that a
		// rejection has its attributes when the filter keeps it.
		if p.Name == "command.execute" {
			return sdktrace.SamplingResult{Decision: sdktrace.RecordOnly, Tracestate: state}
		}
		return sdktrace.SamplingResult{Decision: sdktrace.Drop, Tracestate: state}
	}
	if p.Name == SubmitSpan {
		return s.inner.ShouldSample(p)
	}
	return sdktrace.SamplingResult{Decision: sdktrace.RecordAndSample, Tracestate: state}
}

// Description implements sdktrace.Sampler.
func (s *Sampler) Description() string {
	return fmt.Sprintf("AndaraSampler{submit=%g,rejections=always,parent-based}", s.ratio)
}

// SpanFilter is the span processor that honors the tail decisions.
type SpanFilter struct {
	next sdktrace.SpanProcessor
}

// NewSpanFilter wraps next.
func NewSpanFilter(next sdktrace.SpanProcessor) *SpanFilter { return &SpanFilter{next: next} }

// OnStart implements SpanProcessor.
func (t *SpanFilter) OnStart(ctx context.Context, s sdktrace.ReadWriteSpan) { t.next.OnStart(ctx, s) }

// OnEnd implements SpanProcessor.
func (t *SpanFilter) OnEnd(s sdktrace.ReadOnlySpan) {
	keep, marked := attr(s, KeepAttr)
	switch s.Name() {
	case "sim.tick", "sim.zone_tick":
		if !keep {
			return
		}
	case "command.apply":
		// Marked only on a tick-produced record; one from the Gateway
		// carries the head decision in its sampled flag.
		if marked && !keep {
			return
		}
	case "command.execute":
		if !s.SpanContext().IsSampled() {
			if _, rejected := attr(s, StageFailedAttr); !rejected {
				return
			}
			// Kept after the head said no: the exporter refuses an
			// unsampled span, so it goes out claiming the flag.
			t.next.OnEnd(kept{s})
			return
		}
	}
	t.next.OnEnd(s)
}

// Shutdown implements SpanProcessor.
func (t *SpanFilter) Shutdown(ctx context.Context) error { return t.next.Shutdown(ctx) }

// ForceFlush implements SpanProcessor.
func (t *SpanFilter) ForceFlush(ctx context.Context) error { return t.next.ForceFlush(ctx) }

// attr reads one attribute: its boolean value, and whether it is set.
func attr(s sdktrace.ReadOnlySpan, key attribute.Key) (value, set bool) {
	for _, kv := range s.Attributes() {
		if kv.Key == key {
			return kv.Value.Type() != attribute.BOOL || kv.Value.AsBool(), true
		}
	}
	return false, false
}

// kept is a ReadOnlySpan whose context reports the sampled flag set.
type kept struct{ sdktrace.ReadOnlySpan }

func (k kept) SpanContext() trace.SpanContext {
	sc := k.ReadOnlySpan.SpanContext()
	return sc.WithTraceFlags(sc.TraceFlags() | trace.FlagsSampled)
}
