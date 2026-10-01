// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package boot

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"go.opentelemetry.io/otel/trace"

	"github.com/valesordev/andara/server/command"
	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/telemetry"
)

// Waiting for content (AW-SRV-042). A store-backed World that has never had
// Zones is a normal first state: every new environment starts in it once.
// The server waits in it, started but unready, serving Admin so the first
// content can be published, and refusing play, until a swap brings Zones.
// The state is derived at boot from the log (worldHadZones) and nothing
// persists it. A World that has had Zones never waits again.

// Waiting reports whether the server is waiting for its first content.
func (rt *Runtime) Waiting() bool { return rt.waiting.Load() }

// Started reports whether recovery has finished and the gRPC listener serves:
// /startedz, which stays 200 until exit, waiting or not.
func (rt *Runtime) Started() bool { return rt.started.Load() }

// MarkStarted is main's, once the Gateway serves. A server that isn't
// waiting is ready from here; one that is becomes ready when it leaves the
// wait.
func (rt *Runtime) MarkStarted() {
	rt.started.Store(true)
	if !rt.waiting.Load() && !rt.draining.Load() {
		rt.MarkReady()
	}
}

// enterWait starts the wait, with its warn line, once.
func (rt *Runtime) enterWait(ctx context.Context) {
	if !rt.waiting.CompareAndSwap(false, true) {
		return
	}
	rt.Tel.Log.LogAttrs(ctx, slog.LevelWarn, "waiting for content: no Zones in effect; publish and activate a pack",
		slog.String("content_source", rt.Cfg.ContentSource),
		slog.String("packs", strings.Join(rt.Cfg.ContentPacks, ",")),
		slog.Uint64("core_version", rt.coreVersion()),
		slog.String("trace_id", telemetry.TraceID(ctx)),
	)
}

func (rt *Runtime) coreVersion() uint64 {
	if rt.core == nil {
		return 0
	}
	return rt.core.Active
}

// leaveWait is contentApplied's, on the loop goroutine: the first swap that
// brings Zones ends the wait, and the server becomes ready once it serves.
// The spawn Room is checked as a boot checks it; the Loader refuses a first
// version without it (AC-8), so a failure here is a defect, and the server
// stays unready, saying so, rather than going ready with nowhere to spawn.
func (rt *Runtime) leaveWait(w *sim.World, templates *sim.TemplateRegistry, swaps []sim.SwapApplied) {
	if !rt.waiting.Load() || len(w.Zones) == 0 {
		return
	}
	var trigger, traceParent string
	if n := len(swaps); n > 0 {
		trigger = fmt.Sprintf("%s@%d", swaps[n-1].Pack, swaps[n-1].Version)
		traceParent = swaps[n-1].TraceParent
	}
	ctx := command.ParentFrom(context.Background(), traceParent)
	traceID := ""
	if sc := trace.SpanContextFromContext(ctx); sc.HasTraceID() {
		traceID = sc.TraceID().String()
	}
	if err := rt.checkSpawnIn(w, templates); err != nil {
		rt.Tel.Log.LogAttrs(ctx, slog.LevelError, "content in effect lacks the spawn Room; still waiting",
			slog.String("detail", err.Error()), slog.String("pack", trigger), slog.String("trace_id", traceID))
		return
	}
	rt.waiting.Store(false)
	rt.Tel.Log.LogAttrs(ctx, slog.LevelInfo, "content in effect: leaving the wait",
		slog.Int("zones", len(w.Zones)), slog.String("pack", trigger), slog.String("trace_id", traceID))
	if rt.started.Load() && !rt.draining.Load() {
		rt.MarkReady()
	}
}
