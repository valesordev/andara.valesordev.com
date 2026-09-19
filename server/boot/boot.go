// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package boot

import (
	"context"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel/attribute"

	"github.com/valesordev/andara/server/auth"
	"github.com/valesordev/andara/server/command"
	"github.com/valesordev/andara/server/config"
	"github.com/valesordev/andara/server/content"
	"github.com/valesordev/andara/server/events"
	"github.com/valesordev/andara/server/ingress"
	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/telemetry"
	"github.com/valesordev/andara/server/tickloop"
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
	// Templates is the loaded Template registry (AW-SRV-022); nil until
	// LoadContent succeeds.
	Templates *sim.TemplateRegistry
	// Engine is the running simulation (AW-SRV-002); nil until StartTickLoop.
	// Its State is mutated by the loop goroutine and is safe to read only
	// from there (a handler, or tickloop.Options.OnTick) — a reader on
	// another goroutine, a probe or a projector, is a data race. What they
	// need is on /metrics or, later, a snapshot.
	Engine *sim.Engine
	// Verbs is the verb table (AW-SRV-003): built in, or the file
	// command.verb_table_path names. Commands is the pipeline's metrics,
	// one instance for the Gateway's pre-log half and the loop's post-log
	// half. Both are nil until LoadVerbs.
	Verbs    *command.VerbTable
	Commands *command.Metrics
	// Events is the fan-out behind the Engine's sink (AW-SRV-004): what a
	// Session stream, the CLI's tap, or a projector subscribes to. Built
	// by StartTickLoop; the drain closes it after SimulationStopped.
	Events *events.Hub
	// Accounts is the account store (AW-SRV-008); nil until OpenAccounts.
	Accounts *auth.Store
	// Ingress is the Submit path and Bindings its routing table
	// (AW-SRV-010); both nil until StartIngress.
	Ingress  *ingress.Ingress
	Bindings *ingress.Bindings
	// producer is the Kafka producer behind Ingress, closed by
	// CloseIngress; memSource is the loopback for sim.source=memory,
	// built by StartIngress and consumed by StartTickLoop.
	producer  *ingress.KafkaProducer
	memSource *tickloop.MemorySource
	ready     atomic.Bool
}

// LoadVerbs builds the verb table and the command metrics. A verb table
// file that does not parse is a boot failure: a Gateway that accepts a
// different vocabulary than the operator configured is worse than one
// that does not start.
func (rt *Runtime) LoadVerbs(ctx context.Context) error {
	table := command.Builtin()
	source := "builtin"
	if path := rt.Cfg.VerbTablePath; path != "" {
		t, err := command.LoadVerbTable(path)
		if err != nil {
			return err
		}
		table, source = t, path
	}
	names := make([]string, 0, len(table.Verbs()))
	for _, v := range table.Verbs() {
		names = append(names, v.Name)
	}
	rt.Verbs = table
	rt.Commands = command.NewMetrics(rt.Tel.Reg, names)
	rt.Tel.Log.LogAttrs(ctx, slog.LevelInfo, "verb table loaded",
		slog.String("source", source), slog.Int("verbs", len(names)), slog.Int("max_intent_bytes", rt.Cfg.MaxIntentBytes))
	return nil
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

	// Templates (AW-SRV-022): loaded and validated under the same span, after
	// the Zones, with the same finding discipline. A refused Template refuses
	// the boot the way a dangling Exit does.
	tinputs, terrs := content.LoadTemplates(rt.Cfg.ContentSource, rt.Cfg.ContentPath)
	templateFatal := false
	for _, e := range terrs {
		if rt.recordFinding(ctx, e) {
			warnCount++
			continue
		}
		templateFatal = true
		errorCount++
	}
	var templates *sim.TemplateRegistry
	if !templateFatal {
		var berrs []sim.ValidationError
		templates, berrs = sim.BuildTemplates(tinputs, sim.TemplateOptions{})
		for _, e := range berrs {
			if rt.recordFinding(ctx, e) {
				warnCount++
				continue
			}
			templateFatal = true
			errorCount++
		}
	}
	templateCount := 0
	if templates != nil {
		templateCount = templates.Len()
		for _, pack := range templates.Packs() {
			n := len(templates.Pack(pack))
			rt.Tel.Metrics.TemplatesLoaded.WithLabelValues(pack).Set(float64(n))
			rt.Tel.Log.LogAttrs(ctx, slog.LevelInfo, "templates loaded",
				slog.String("pack", pack),
				slog.Int("templates", n),
				slog.String("trace_id", telemetry.TraceID(ctx)),
			)
		}
	}

	zoneCount, roomCount, componentCount := 0, 0, 0
	ok := world != nil && !loadFatal && !buildFatal && !templateFatal
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
		rt.Templates = templates
		rt.ready.Store(true)
	}
	span.SetAttributes(
		attribute.Int("zone_count", zoneCount),
		attribute.Int("room_count", roomCount),
		attribute.Int("component_count", componentCount),
		attribute.Int("template_count", templateCount),
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
