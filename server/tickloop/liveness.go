// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package tickloop

import (
	"context"
	"log/slog"
	"time"

	"github.com/valesordev/andara/server/sim"
)

// Wedged reports whether the running loop has gone longer than
// sim.tick_budget_ms × 100 without completing a tick: what /livez answers
// 503 for (AW-SRV-066). A loop that hasn't begun, or is stopping on purpose,
// is never wedged. The first call to find a wedge logs it, once per wedge.
// Safe from any goroutine.
func (l *Loop) Wedged() bool {
	if !l.live() {
		return false
	}
	now := l.clock.Now()
	l.wedgeMu.Lock()
	since := now.Sub(l.lastDone)
	if since <= l.wedgeAfter {
		l.wedgeMu.Unlock()
		return false
	}
	first := !l.wedged
	l.wedged = true
	last := l.lastTick
	l.wedgeMu.Unlock()
	if first {
		l.log.LogAttrs(context.Background(), slog.LevelWarn, "tick loop wedged",
			slog.Uint64("last_tick", uint64(last)), slog.Float64("since_seconds", since.Seconds()))
	}
	return true
}

// Draining marks the loop as stopping on purpose before its context ends: the
// boot calls it when the drain begins (AW-SRV-042), so a tick that stalls
// behind the drain doesn't read as a wedge. Safe from any goroutine.
func (l *Loop) Draining() { l.stopping.Store(true) }

// live is the predicate /livez and the lag at scrape share: the loop has
// begun and isn't stopping.
func (l *Loop) live() bool { return l.running.Load() && !l.stopping.Load() }

// completed records a completed tick, and ends a wedge a probe found.
func (l *Loop) completed(ctx context.Context, tick sim.Tick) {
	now := l.clock.Now()
	l.wedgeMu.Lock()
	prev, was := l.lastDone, l.wedged
	l.lastDone, l.lastTick, l.wedged = now, tick, false
	l.wedgeMu.Unlock()
	if was {
		l.log.LogAttrs(ctx, slog.LevelInfo, "tick loop resumed",
			slog.Uint64("last_tick", uint64(tick)), slog.Float64("wedged_seconds", now.Sub(prev).Seconds()))
	}
}

// lagNow is andara_simulation_lag_seconds at scrape (AW-SRV-066 AC-9): the
// value the loop set, or how far past due the next tick is if that's more, so
// a World frozen mid-tick shows as lag rising. Once the loop ticks again its
// own value is at least the larger one.
func (l *Loop) lagNow(set float64) float64 {
	if !l.live() {
		return set
	}
	behind := l.clock.Now().Sub(time.Unix(0, l.nextDue.Load())).Seconds()
	return max(set, behind)
}
