// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

// Package fault holds the fault injectors a `-tags fault` build of
// andara-server wires in (AW-SRV-066). Only that build imports it: the
// production binary's dependencies never include this package, which a test
// in cmd/andara-server holds.
package fault

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// WedgeTickAfter is a tick loop BeforeTick (tickloop.Options.BeforeTick) that
// lets ticks run for d of the loop's clock, measured from its first call, and
// then blocks the next tick until ctx ends: a World frozen while the process
// still answers HTTP, for the liveness probe to find. It logs one warn when it
// wedges.
func WedgeTickAfter(d time.Duration, log *slog.Logger) func(context.Context, time.Time) {
	var (
		once  sync.Once
		start time.Time
	)
	return func(ctx context.Context, now time.Time) {
		once.Do(func() { start = now })
		if now.Sub(start) < d {
			return
		}
		log.LogAttrs(ctx, slog.LevelWarn, "fault: wedging the tick loop", slog.String("after", d.String()))
		<-ctx.Done()
	}
}
