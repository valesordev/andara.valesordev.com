// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package telemetry

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// KeepAttr is the attribute the tick loop sets on a sim.tick or
// sim.zone_tick span it wants exported: one tick in every hundred as a head
// sample, and every tick that overran its budget (AW-SRV-002 observability).
// OTel cannot sample retroactively, so the loop always starts the span and
// decides at the end; TickSampler is the processor that honors the decision.
const KeepAttr = attribute.Key("andara.keep")

// TickSampler drops sim.tick and sim.zone_tick spans without KeepAttr and
// forwards everything else unchanged. Ten spans a second per Zone, all
// exported, would be the collector's biggest customer for the least
// interesting data.
type TickSampler struct {
	next sdktrace.SpanProcessor
}

// NewTickSampler wraps next.
func NewTickSampler(next sdktrace.SpanProcessor) *TickSampler { return &TickSampler{next: next} }

// OnStart implements SpanProcessor.
func (t *TickSampler) OnStart(ctx context.Context, s sdktrace.ReadWriteSpan) { t.next.OnStart(ctx, s) }

// OnEnd implements SpanProcessor.
func (t *TickSampler) OnEnd(s sdktrace.ReadOnlySpan) {
	if name := s.Name(); name == "sim.tick" || name == "sim.zone_tick" {
		keep := false
		for _, kv := range s.Attributes() {
			if kv.Key == KeepAttr && kv.Value.AsBool() {
				keep = true
				break
			}
		}
		if !keep {
			return
		}
	}
	t.next.OnEnd(s)
}

// Shutdown implements SpanProcessor.
func (t *TickSampler) Shutdown(ctx context.Context) error { return t.next.Shutdown(ctx) }

// ForceFlush implements SpanProcessor.
func (t *TickSampler) ForceFlush(ctx context.Context) error { return t.next.ForceFlush(ctx) }
