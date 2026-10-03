// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package boot

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"go.opentelemetry.io/otel/trace"

	"github.com/valesordev/andara/server/command"
	"github.com/valesordev/andara/server/content"
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

// WaitForContent is a read-only consumer's wait (#326). The state projector
// can't scope a snapshot round until the Active Pointers name Zones, so on a
// store without them it waits, as the server does (AW-SRV-042), rather than
// exiting: the same warn line once, then a reload of the content on every
// Active Pointer move, until one builds a World. It writes nothing, and
// returns nil once rt.World is set, or ctx's error when ctx ends first.
func (rt *Runtime) WaitForContent(ctx context.Context) error {
	if rt.World != nil {
		return nil
	}
	if rt.Content == nil || rt.Content.Resolver() == nil {
		rt.reportHeld(ctx)
		return fmt.Errorf("content: what the source names does not load, and it has no Active Pointers to wait on")
	}
	ctx, span := rt.Tel.Tracer.Start(ctx, "content.wait")
	defer span.End()
	// The watch is the first Content's, from the position it pinned before
	// reading the pointers, so no move between that read and this is missed.
	// Each reload opens a Content of its own, pinned afresh. The watch lives
	// only as long as the wait: Close doesn't stop it, its context does
	// (review of #334).
	watcher := rt.Content
	wctx, stop := context.WithCancel(ctx)
	defer stop()
	moves, err := watcher.Resolver().Watch(wctx)
	if err != nil {
		// An exit before the wait reports the held finding, as the
		// server's does (AW-SRV-042, #299); a signal drops it.
		if ctx.Err() != nil {
			rt.dropHeld()
		} else {
			rt.reportHeld(ctx)
		}
		return err
	}
	defer func() {
		if rt.Content != watcher {
			_ = watcher.Close()
		}
	}()
	rt.enterWait(ctx)
	// The projector's decision: waiting, so the boot's held finding is
	// dropped (AW-SRV-042, #299).
	rt.dropHeld()
	return rt.waitLoop(ctx, moves, func() (bool, error) {
		prev := rt.Content
		if code := rt.LoadContent(ctx); code != ExitOK {
			return false, fmt.Errorf("content: reloading while waiting failed")
		}
		if prev != watcher && prev != rt.Content {
			_ = prev.Close()
		}
		return rt.World != nil, nil
	}, func() int { return len(rt.World.Zones) })
}

// The wait's retry: a reload with no pointer move, after waitRetryMin,
// doubling to waitRetryMax, and back to waitRetryMin on a move.
const (
	waitRetryMin = time.Second
	waitRetryMax = time.Minute
)

// waitLoop reloads on every pointer move, and on a backoff between them,
// until reload reports a World. The backoff is what recovers from a load
// that failed transiently: a non-validation store-backed load that can't
// read the store returns no World and no error, and the pointer move that
// triggered it is already consumed. Without the retry, the projector would
// wait for a move that may never come (review of #334). The leaving line
// names the last move seen, if any.
func (rt *Runtime) waitLoop(ctx context.Context, moves <-chan content.PointerMove, reload func() (bool, error), zones func() int) error {
	first := rt.waitRetry
	if first == 0 {
		first = waitRetryMin
	}
	retry := first
	timer := time.NewTimer(retry)
	defer timer.Stop()
	var trigger string
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case m, ok := <-moves:
			if !ok {
				if err := ctx.Err(); err != nil {
					return err
				}
				return fmt.Errorf("content: the Active Pointer watch ended")
			}
			trigger = fmt.Sprintf("%s@%d", m.Pack, m.Version)
			retry = first
		case <-timer.C:
			retry = min(retry*2, max(waitRetryMax, first))
		}
		// The reload's debug line names the backoff before the next one.
		rt.nextRetry = retry
		done, err := reload()
		if err != nil {
			return err
		}
		if !done {
			timer.Reset(retry)
			continue
		}
		rt.waiting.Store(false)
		rt.Tel.Log.LogAttrs(ctx, slog.LevelInfo, "content in effect: leaving the wait",
			slog.Int("zones", zones()), slog.String("pack", trigger),
			slog.String("trace_id", telemetry.TraceID(ctx)))
		return nil
	}
}
