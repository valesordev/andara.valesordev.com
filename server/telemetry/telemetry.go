// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package telemetry

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkresource "go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.34.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/valesordev/andara/server/config"
	"github.com/valesordev/andara/server/sim"
)

// Telemetry holds process-wide logs, metrics, and traces for boot.
type Telemetry struct {
	Log     *slog.Logger
	Metrics *Metrics
	Tracer  trace.Tracer
	TP      *sdktrace.TracerProvider
	Reg     *prometheus.Registry
	// LP is the OTLP log provider (AW-SRV-024); nil when export is off.
	LP *sdklog.LoggerProvider
	// LogExport is the bounded queue behind LP, for tests; nil when off.
	LogExport *queueProcessor
	// SetupErr is set when an exporter could not be built — a
	// configuration error the caller treats as fatal.
	SetupErr error
}

// Metrics is the content-load instrument set.
type Metrics struct {
	ZonesLoaded      prometheus.Gauge
	RoomsLoaded      *prometheus.GaugeVec
	LoadDuration     prometheus.Histogram
	ValidationErrors *prometheus.CounterVec
	Components       *prometheus.CounterVec
	LoadWarnings     *prometheus.CounterVec
	TemplatesLoaded  *prometheus.GaugeVec // AW-SRV-022; label pack, bounded by the packs loaded
}

// Setup builds a JSON slog logger, a Prometheus registry, and a tracer.
// An empty OTLP endpoint disables export; traces still have IDs for logs.
func Setup(cfg config.Config, stderr io.Writer) *Telemetry {
	level := slog.LevelInfo
	switch strings.ToLower(cfg.LogLevel) {
	case "debug":
		level = slog.LevelDebug
	case "warn", "warning":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	h := slog.NewJSONHandler(stderr, &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey {
				return slog.String("ts", a.Value.Time().UTC().Format(time.RFC3339Nano))
			}
			return a
		},
	})
	reg := prometheus.NewRegistry()
	m := newMetrics()
	reg.MustRegister(m.ZonesLoaded, m.RoomsLoaded, m.LoadDuration, m.ValidationErrors,
		m.Components, m.LoadWarnings, m.TemplatesLoaded)

	// Logs go to stderr and, with an endpoint, to the collector as the
	// same records (AW-SRV-024). The exporter's own failures go to the
	// stderr handler alone, so they cannot loop.
	stderrLog := slog.New(h).With("service", cfg.ServiceName, "env", cfg.Environment)
	var handler slog.Handler = h
	otelHandler, lp, proc, err := newLogExport(cfg, stderrLog, reg, stderr)
	if otelHandler != nil {
		handler = newFanout(h, otelHandler)
	}
	log := slog.New(handler).With(
		"service", cfg.ServiceName,
		"env", cfg.Environment,
	)

	tp := newTracerProvider(cfg)
	otel.SetTracerProvider(tp)
	tr := tp.Tracer("andara-server")

	return &Telemetry{Log: log, Metrics: m, Tracer: tr, TP: tp, Reg: reg, LP: lp, LogExport: proc, SetupErr: err}
}

// bootResource identifies this process to a trace backend.
//
// Without it the Go SDK falls back to `unknown_service:<executable>`, which is
// what Tempo recorded until this was added: the spans arrived, carried their
// attributes, and were unfindable by the service name every dashboard and
// TraceQL query uses. The slog handler has always stamped `service` and `env` on
// every line, so logs and traces disagreed about the identity they were meant to
// correlate on. An in-memory SpanRecorder cannot see this — resource attributes
// are attached on export — which is why CLAUDE.md §8 asks for instrumentation
// verified against a real backend rather than merely registered.
func bootResource(cfg config.Config) *sdkresource.Resource {
	return sdkresource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceName(cfg.ServiceName),
		semconv.DeploymentEnvironmentName(cfg.Environment),
	)
}

// newTracerProvider builds the boot tracer. extra exists so a test can attach a
// span processor and read back what a real exporter would see; the SDK does not
// expose a provider's resource, and a resource that is silently dropped is the
// defect this seam is here to keep caught.
func newTracerProvider(cfg config.Config, extra ...sdktrace.TracerProviderOption) *sdktrace.TracerProvider {
	opts := append([]sdktrace.TracerProviderOption{
		sdktrace.WithResource(bootResource(cfg)),
		// The head decision (AW-SRV-010): Game/Submit roots at
		// telemetry.trace_sample_ratio, a client's own decision honored,
		// every other root sampled. SpanFilter below is the tail half.
		sdktrace.WithSampler(NewSampler(cfg.TraceSampleRatio)),
	}, extra...)
	if cfg.OTLPEndpoint == "" {
		return sdktrace.NewTracerProvider(opts...)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	exp, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(cfg.OTLPEndpoint),
		otlptracegrpc.WithInsecure(),
	)
	if err != nil {
		return sdktrace.NewTracerProvider(opts...)
	}
	opts = append(opts, sdktrace.WithSpanProcessor(NewSpanFilter(sdktrace.NewBatchSpanProcessor(exp))))
	return sdktrace.NewTracerProvider(opts...)
}

func newMetrics() *Metrics {
	return &Metrics{
		ZonesLoaded: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "andara",
			Name:      "content_zones_loaded",
			Help:      "Number of Zones in the loaded World topology.",
		}),
		RoomsLoaded: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "andara",
			Name:      "content_rooms_loaded",
			Help:      "Number of Rooms loaded, labeled by Zone.",
		}, []string{"zone"}),
		LoadDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: "andara",
			Name:      "content_load_duration_seconds",
			Help:      "Wall time to load and validate content.",
		}),
		ValidationErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "andara",
			Name:      "content_validation_errors_total",
			Help:      "Validation findings by ErrCode.",
		}, []string{"code"}),
		// component_type is safe as a label only because the Component
		// registry is closed by construction (ADR-0010 decision 7): the
		// cardinality bound is the number of types in the server binary, and
		// content cannot add one. This is the same reason room_id is not a
		// label anywhere in this file (CLAUDE.md §7).
		Components: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "andara",
			Name:      "content_components_total",
			Help:      "Components in the loaded World, by registered Component type.",
		}, []string{"component_type"}),
		// kind is bounded by sim's warningCodes. Warnings also appear in
		// content_validation_errors_total, which counts every finding by code;
		// this one exists so an operator can ask "is this content pack sloppy"
		// without knowing which codes happen to be advisory.
		LoadWarnings: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "andara",
			Name:      "content_load_warnings_total",
			Help:      "Advisory content findings by kind: content loaded, but something looks unfinished.",
		}, []string{"kind"}),
		// pack is bounded by the Content Packs the server follows — a handful,
		// named in configuration — not by anything a player does.
		TemplatesLoaded: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "andara",
			Name:      "content_templates_loaded",
			Help:      "Templates in the loaded registry, by Content Pack.",
		}, []string{"pack"}),
	}
}

// Shutdown flushes the tracer provider.
func (t *Telemetry) Shutdown(ctx context.Context) {
	if t == nil {
		return
	}
	// Logs first, so the last lines — including a fatal one — reach the
	// sink before the trace exporter they correlate with closes.
	if t.LP != nil {
		lctx, cancel := context.WithTimeout(ctx, logExportTimeout)
		_ = t.LP.Shutdown(lctx)
		cancel()
	}
	if t.TP != nil {
		_ = t.TP.Shutdown(ctx)
	}
}

// TraceID returns the current span's trace id, or empty.
func TraceID(ctx context.Context) string {
	sc := trace.SpanFromContext(ctx).SpanContext()
	if !sc.HasTraceID() {
		return ""
	}
	return sc.TraceID().String()
}

// LogFinding writes one structured line per validation finding. A finding that
// does not refuse the load logs at warn; everything else at error.
func LogFinding(ctx context.Context, log *slog.Logger, e sim.ValidationError, strict bool) {
	level := slog.LevelError
	if sim.IsWarning(e, strict) {
		level = slog.LevelWarn
	}
	attrs := []any{
		"code", string(e.Code),
		"file", e.File,
		"zone", string(e.Zone),
		"room", string(e.Room),
		"template", string(e.Template),
		"detail", e.Detail,
		"trace_id", TraceID(ctx),
	}
	if e.Line > 0 {
		attrs = append(attrs, "line", e.Line)
	}
	log.Log(ctx, level, e.Detail, attrs...)
}
