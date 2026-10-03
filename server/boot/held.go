// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package boot

import (
	"context"
	"log/slog"

	"go.opentelemetry.io/otel/trace"

	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/telemetry"
)

// An empty store's no_zones_found, held until the boot's decision settles it
// (AW-SRV-042, #299 amendment). LoadContent holds it; then exactly one of:
//   - a wait drops it (dropHeld), with no line and no count;
//   - a serve from the log logs the recovery warn once (serveHeld);
//   - an exit 1 logs and counts the finding once (reportHeld);
//   - a signal drops it.
//
// Once settled it stays settled: a later exit doesn't revive it.

// recoveringLine is the warn a store-backed boot logs when what the pointers
// name doesn't load and the log is what's brought back.
const recoveringLine = "the content the Active Pointers name does not load; recovering what the log recorded"

func (rt *Runtime) hold(ctx context.Context, f sim.ValidationError) {
	rt.heldMu.Lock()
	defer rt.heldMu.Unlock()
	rt.held, rt.heldCtx = &f, ctx
}

// takeHeld settles the held finding, returning it and its load's context, or
// nil when there is none or it's already settled.
func (rt *Runtime) takeHeld() (*sim.ValidationError, context.Context) {
	rt.heldMu.Lock()
	defer rt.heldMu.Unlock()
	f, ctx := rt.held, rt.heldCtx
	rt.held, rt.heldCtx = nil, nil
	return f, ctx
}

// dropHeld settles it unreported: a wait, or a signal.
func (rt *Runtime) dropHeld() { _, _ = rt.takeHeld() }

// reportHeld settles it as an exit 1 does: logged once with its fields, and
// counted. The trace is ctx's span's, or content.load's where the exit runs
// outside any span.
func (rt *Runtime) reportHeld(ctx context.Context) {
	f, loadCtx := rt.takeHeld()
	if f == nil {
		return
	}
	if !trace.SpanContextFromContext(ctx).IsValid() {
		ctx = loadCtx
	}
	rt.recordFinding(ctx, *f)
}

// exitHeld is reconcile's, on an exit 1: it reports the held finding, unless
// reconcile failed because its context was canceled. That's a signal or the
// loop stopping, and main's SettleHeld then decides by the process's real
// exit code: 1 reports it, anything else drops it.
func (rt *Runtime) exitHeld(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	rt.reportHeld(ctx)
}

// serveHeld settles it when the World is served from the log with no Zones
// the pointers name: not the finding, but the recovery warn, once, because
// Zones in the log that no pointer names is usually a store fault.
func (rt *Runtime) serveHeld(ctx context.Context) {
	if f, _ := rt.takeHeld(); f == nil {
		return
	}
	rt.Tel.Log.LogAttrs(ctx, slog.LevelWarn, recoveringLine,
		slog.Int("error_count", 0), slog.String("trace_id", telemetry.TraceID(ctx)))
}

// SettleHeld is main's, on its way out: an exit 1 before the decision
// reports the held finding, and any other exit (0 on a signal, 5 on a lost
// boundary) drops it.
func (rt *Runtime) SettleHeld(code int) {
	if code == ExitFail {
		rt.reportHeld(context.Background())
		return
	}
	rt.dropHeld()
}
