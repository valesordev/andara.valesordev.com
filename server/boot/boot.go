// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package boot

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel/attribute"

	"github.com/valesordev/andara/server/auth"
	"github.com/valesordev/andara/server/command"
	"github.com/valesordev/andara/server/config"
	"github.com/valesordev/andara/server/content"
	"github.com/valesordev/andara/server/egress"
	"github.com/valesordev/andara/server/events"
	"github.com/valesordev/andara/server/ingress"
	"github.com/valesordev/andara/server/recovery"
	"github.com/valesordev/andara/server/roster"
	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/telemetry"
	"github.com/valesordev/andara/server/tickloop"
)

const (
	ExitOK   = 0
	ExitFail = 1
	// ExitBoundaryLost: a Tick Boundary Record was not delivered, and the
	// process exits so its restart recovers exactly to the last delivered
	// one (AW-SRV-026). Distinct from 1, the drain timeout.
	ExitBoundaryLost = 5
)

// LoopExit is the exit code for a tick loop that stopped on its own: 5 for a
// lost boundary, 1 for anything else.
func LoopExit(err error) int {
	var lost *tickloop.BoundaryLostError
	if errors.As(err, &lost) {
		return ExitBoundaryLost
	}
	return ExitFail
}

// Runtime is one boot of andara-server.
type Runtime struct {
	// Cfg is the resolved process configuration.
	Cfg config.Config
	// Tel is logs, metrics, and traces for this process.
	Tel *telemetry.Telemetry
	// World is the loaded topology; nil until LoadContent succeeds.
	World *sim.World
	// Content is the open content source (AW-SRV-012): a directory, or the
	// content store on the broker with its resolver, blob cache and Active
	// Pointer watch. Nil until LoadContent runs.
	Content *content.Content
	// ContentMetrics is the content pipeline's instrumentation.
	ContentMetrics *content.Metrics
	// BuildVersion is the running build's version, for the boot's core
	// audit records (reason "boot <build version>", AW-SRV-013).
	BuildVersion string
	// ContentReadOnly marks a content consumer that isn't the server: the
	// state projector (#326). Only the server writes andara.core, so
	// LoadContent runs no core boot, and nothing here opens the content
	// store's write side or the audit topic.
	ContentReadOnly bool
	// ContentTopics and AuditTopicName override the content store's topics and
	// the audit topic, for a test against a live broker that must not write
	// into the stack's. The zero values are the real ones.
	ContentTopics  content.Topics
	AuditTopicName string
	// waitRetry is WaitForContent's first retry interval; zero is
	// waitRetryMin. A test shortens it.
	waitRetry time.Duration
	// registry is the content store's write side, opened by LoadContent
	// on a content.source=kafka server so the boot can publish its core,
	// and reused by the publish path (AW-SRV-013). core is what that did.
	registry       *content.Registry
	publishMetrics *content.PublishMetrics
	core           *content.CoreBoot
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
	// Egress is the Subscribe path (AW-SRV-011); nil until StartEgress.
	Egress *egress.Egress
	// Roster is the Character roster and binding (AW-SRV-014); nil until
	// StartRoster.
	Roster *roster.Roster
	// producer is the Kafka producer behind Ingress, closed by
	// CloseIngress; memSource is the loopback for sim.source=memory,
	// built by StartIngress and consumed by StartTickLoop.
	producer  *ingress.KafkaProducer
	memSource *tickloop.MemorySource
	// commandLog is the producer behind Ingress, whichever source: the
	// roster produces its BindCharacter and UnbindCharacter through it.
	commandLog command.Producer
	ready      atomic.Bool
	// started, waiting and draining are AW-SRV-042's: the Gateway serves;
	// the World is waiting for its first Zones; SIGTERM has begun the drain.
	started atomic.Bool
	waiting atomic.Bool
	// roomsLabeled is the Zones andara_content_rooms_loaded has a series for,
	// so setTopologyGauges deletes only departed ones (#287). The boot's load
	// and the loop's swaps both set the gauges, one after the other today;
	// gaugeMu is defensive, so a later caller on another goroutine can't
	// race the map.
	gaugeMu      sync.Mutex
	roomsLabeled map[sim.ZoneID]struct{}
	// applied, if set, is called in place of Content.Applied: a test wraps
	// the call itself, so wherever contentApplied makes it, the test checks
	// the gauges are already set at that moment, with no timing involved
	// (#287). Nil outside tests.
	applied  func([]sim.SwapApplied)
	draining atomic.Bool
	// held is an empty store's no_zones_found finding, held by LoadContent
	// until the boot's decision settles it (AW-SRV-042, #299 amendment):
	// dropped on a wait, the recovery warn logged once on a serve, logged
	// and counted on an exit 1. heldCtx carries content.load's trace for an
	// exit outside any span. Nil once settled.
	heldMu  sync.Mutex
	held    *sim.ValidationError
	heldCtx context.Context
	// nextRetry is the wait's backoff before the next reload, for the
	// reload's debug line.
	nextRetry time.Duration
	// worldNext is the tick loop's next-to-read offset on the World
	// Partition, for worldBarrier; written on the loop's goroutine.
	worldNext atomic.Int64
	// replay, when set, is the log recovery reads instead of Kafka's: a test
	// drives StartTickLoop's recovery with it.
	replay replayLog
	// recMetrics is the recovery instrument set, registered on first use.
	recMetrics *recovery.Metrics
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

	if rt.ContentMetrics == nil {
		rt.ContentMetrics = content.NewMetrics(rt.Tel.Reg)
	}
	// The candidate content: what the source names now, validated. It is
	// not yet in effect — the log is the source of that (AW-SRV-012), and
	// ReconcileContent brings it in through a ContentSwap — but a boot whose
	// content cannot load at all still fails here, before anything starts.
	src, loadErrs := content.Open(ctx, rt.contentOptions())
	rt.Content = src
	// The server's own core, before the candidates are read: a store
	// without andara.core has no Template a Builder pack could extend
	// (AW-SRV-013 AC-15). --validate-only writes nothing, and neither does a
	// read-only consumer (#326).
	if src != nil && len(loadErrs) == 0 && src.Loader() != nil && !rt.Cfg.ValidateOnly && !rt.ContentReadOnly {
		if err := rt.bootCore(ctx); err != nil {
			rt.Tel.Log.LogAttrs(ctx, slog.LevelError, "content core not published", slog.String("detail", err.Error()))
			return ExitFail
		}
	}
	var (
		inputs     []sim.Input
		candidates []sim.TemplateInput
	)
	if src != nil {
		zones, templates, cerrs := src.Candidates(ctx)
		inputs, candidates = zones, templates
		loadErrs = append(loadErrs, cerrs...)
	}
	// AW-SRV-042's #299 amendment: an empty store's no_zones_found is the
	// expected first state of every fresh environment, not a fault, so on a
	// store-backed source it's held until the boot's decision settles it,
	// unless another finding refuses the load after all. The directory
	// source and --validate-only report it as before.
	emptyStore := rt.Cfg.ContentSource == content.SourceKafka && !rt.Cfg.ValidateOnly && src != nil && content.IsEmptyStore(loadErrs)
	// A reload during the projector's wait, in the same state: one debug
	// line and nothing else.
	reloading := emptyStore && rt.waiting.Load()
	var heldFinding *sim.ValidationError
	loadFatal := false
	errorCount := 0
	warnCount := 0
	for _, e := range loadErrs {
		if emptyStore {
			heldFinding = &e
			loadFatal = true
			continue
		}
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
	var (
		tinputs []sim.TemplateInput
		terrs   []sim.ValidationError
	)
	if src != nil {
		tinputs = candidates
	}
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
			level := slog.LevelInfo
			if reloading {
				level = slog.LevelDebug
			}
			rt.Tel.Log.LogAttrs(ctx, level, "templates loaded",
				slog.String("pack", pack),
				slog.Int("templates", n),
				slog.String("trace_id", telemetry.TraceID(ctx)),
			)
		}
	}

	if heldFinding != nil && errorCount > 0 {
		// Another finding refuses the load: not the empty-store case after
		// all. Reported now, as it always was.
		rt.recordFinding(ctx, *heldFinding)
		errorCount++
		heldFinding = nil
	}

	zoneCount, roomCount, componentCount := 0, 0, 0
	ok := world != nil && !loadFatal && !buildFatal && !templateFatal
	if ok {
		zoneCount = len(world.Zones)
		rt.setTopologyGauges(world)
		for _, z := range world.Zones {
			roomCount += len(z.Rooms)
			componentCount += rt.countComponents(z.Components)
			for _, r := range z.Rooms {
				componentCount += rt.countComponents(r.Components)
			}
		}
		rt.World = world
		rt.Templates = templates
	}
	span.SetAttributes(
		attribute.Int("zone_count", zoneCount),
		attribute.Int("room_count", roomCount),
		attribute.Int("component_count", componentCount),
		attribute.Int("template_count", templateCount),
	)
	if !ok {
		if rt.Cfg.ContentSource == content.SourceKafka && !rt.Cfg.ValidateOnly && src != nil {
			// The candidate is what the pointers name now; the log may
			// already hold content that loads. A bad activation must not
			// survive a restart as an outage: recovery brings back what the
			// log recorded, reconcile refuses the candidate, and only a boot
			// that ends with nothing in effect exits (ReconcileContent).
			switch {
			case heldFinding != nil && reloading:
				rt.Tel.Log.LogAttrs(ctx, slog.LevelDebug, "content reload: no Zones in effect yet",
					slog.String("next_retry", rt.nextRetry.String()), slog.String("trace_id", telemetry.TraceID(ctx)))
			case heldFinding != nil:
				// The recovery warn is held with the finding: a wait drops
				// both, and a serve logs the warn (serveHeld).
				rt.hold(ctx, *heldFinding)
			default:
				rt.Tel.Log.LogAttrs(ctx, slog.LevelWarn, recoveringLine,
					slog.Int("error_count", errorCount), slog.String("trace_id", telemetry.TraceID(ctx)))
			}
			return ExitOK
		}
		return ExitFail
	}
	return ExitOK
}

// MarkReady sets /readyz to 200: called once the Gateway serves with content
// in effect (AW-SRV-012, review of #88).
func (rt *Runtime) MarkReady() { rt.ready.Store(true) }

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

// Handler serves /livez, /startedz, /readyz, and /metrics.
func (rt *Runtime) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/livez", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	// /startedz: recovery has finished and the gRPC listener serves, whether
	// or not the World waits for content. It stays 200 until exit, through
	// the drain (AW-SRV-042): the probe a slow first start is judged by.
	mux.HandleFunc("/startedz", func(w http.ResponseWriter, _ *http.Request) {
		if !rt.started.Load() {
			http.Error(w, "not started\n", http.StatusServiceUnavailable)
			return
		}
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
	rt.draining.Store(true)
	rt.ready.Store(false)
}

// contentOptions flattens the content.* configuration for the adapter, the
// way store.Options is flattened out of snapshot.*.
func (rt *Runtime) contentOptions() content.Options {
	return content.Options{
		Source:        rt.Cfg.ContentSource,
		Path:          rt.Cfg.ContentPath,
		Brokers:       rt.Cfg.KafkaBrokers,
		Packs:         rt.Cfg.ContentPacks,
		CacheDir:      rt.Cfg.ContentCacheDir,
		MaxBlobBytes:  rt.Cfg.ContentMaxBlobBytes,
		Debounce:      rt.Cfg.ContentReloadDebounce,
		StrictOrphans: rt.Cfg.StrictOrphans,
		SpawnRoom:     rt.spawnRoom(),
		Topics:        rt.ContentTopics,
		Metrics:       rt.ContentMetrics,
		Log:           rt.Tel.Log,
		Tracer:        rt.Tel.Tracer,
	}
}
