// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package boot

import (
	"context"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel/attribute"

	"github.com/valesordev/andara/server/config"
	"github.com/valesordev/andara/server/content"
	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/telemetry"
)

const (
	ExitOK   = 0
	ExitFail = 1
)

// Runtime is one boot of andara-server.
type Runtime struct {
	// Cfg is the resolved process configuration.
	Cfg config.Config
	// Tel is logs, metrics, and traces for this process.
	Tel *telemetry.Telemetry
	// World is the loaded topology; nil until LoadContent succeeds.
	World *sim.World
	ready atomic.Bool
}

// New constructs a Runtime. Telemetry must already be set up.
func New(cfg config.Config, tel *telemetry.Telemetry) *Runtime {
	return &Runtime{Cfg: cfg, Tel: tel}
}

// LoadContent reads ZoneDefinitions, validates them, and records metrics.
// Every finding is logged before the function returns. ExitFail if the World
// cannot be served.
func (rt *Runtime) LoadContent(ctx context.Context) int {
	start := time.Now()
	ctx, span := rt.Tel.Tracer.Start(ctx, "content.load")
	defer func() {
		rt.Tel.Metrics.LoadDuration.Observe(time.Since(start).Seconds())
		span.End()
	}()

	inputs, loadErrs := content.Load(rt.Cfg.ContentSource, rt.Cfg.ContentPath)
	loadFatal := false
	errorCount := 0
	warnCount := 0
	for _, e := range loadErrs {
		if rt.recordFinding(ctx, e) {
			warnCount++
			continue
		}
		loadFatal = true
		errorCount++
	}

	opts := sim.Options{
		StrictOrphans: rt.Cfg.StrictOrphans,
		Source:        rt.Cfg.ContentSourceName(),
	}

	// error_count on content.validate covers both phases. A boot that failed in
	// the source adapter never reaches BuildWorld, and a span reporting zero
	// errors on a failed boot is worse than no span.
	ctx, vspan := rt.Tel.Tracer.Start(ctx, "content.validate")
	var (
		world *sim.World
		errs  []sim.ValidationError
	)
	if len(inputs) > 0 {
		world, errs = sim.BuildWorld(inputs, opts)
	}
	buildFatal := false
	for _, e := range errs {
		if rt.recordFinding(ctx, e) {
			warnCount++
			continue
		}
		buildFatal = true
		errorCount++
	}
	// Component and Direction validation are attributes on the existing span,
	// not spans of their own: a span per Room would be one span per Room
	// (AW-SRV-021 observability).
	vspan.SetAttributes(
		attribute.Int("error_count", errorCount),
		attribute.Int("warning_count", warnCount),
	)
	vspan.End()

	zoneCount, roomCount, componentCount := 0, 0, 0
	ok := world != nil && !loadFatal && !buildFatal
	if ok {
		zoneCount = len(world.Zones)
		rt.Tel.Metrics.ZonesLoaded.Set(float64(zoneCount))
		for id, z := range world.Zones {
			n := len(z.Rooms)
			roomCount += n
			rt.Tel.Metrics.RoomsLoaded.WithLabelValues(string(id)).Set(float64(n))
			componentCount += rt.countComponents(z.Components)
			for _, r := range z.Rooms {
				componentCount += rt.countComponents(r.Components)
			}
		}
		rt.World = world
		rt.ready.Store(true)
	}
	span.SetAttributes(
		attribute.Int("zone_count", zoneCount),
		attribute.Int("room_count", roomCount),
		attribute.Int("component_count", componentCount),
	)
	if !ok {
		return ExitFail
	}
	return ExitOK
}

// recordFinding logs one finding and counts it, and reports whether it was
// advisory. Both loops share it so a warning can never be logged at warn and
// counted as an error, which is the way these two drift apart.
func (rt *Runtime) recordFinding(ctx context.Context, e sim.ValidationError) bool {
	telemetry.LogFinding(ctx, rt.Tel.Log, e, rt.Cfg.StrictOrphans)
	rt.Tel.Metrics.ValidationErrors.WithLabelValues(string(e.Code)).Inc()
	if !sim.IsWarning(e, rt.Cfg.StrictOrphans) {
		return false
	}
	rt.Tel.Metrics.LoadWarnings.WithLabelValues(string(e.Code)).Inc()
	return true
}

// countComponents records one Component set against the by-type counter and
// returns its size.
func (rt *Runtime) countComponents(set []sim.Component) int {
	for _, c := range set {
		rt.Tel.Metrics.Components.WithLabelValues(string(c.Type)).Inc()
	}
	return len(set)
}

// Handler serves /livez, /readyz, and /metrics.
func (rt *Runtime) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/livez", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if !rt.ready.Load() {
			http.Error(w, "not ready\n", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.Handle("/metrics", promhttp.HandlerFor(rt.Tel.Reg, promhttp.HandlerOpts{}))
	return mux
}

// Ready reports whether a World has been loaded and the process is not
// draining.
func (rt *Runtime) Ready() bool {
	return rt.ready.Load()
}

// Drain flips readiness off. The gateway calls it when Shutdown begins, so
// a load balancer stops routing here while in-flight work finishes
// (AW-SRV-005 AC-8).
func (rt *Runtime) Drain() {
	rt.ready.Store(false)
}
