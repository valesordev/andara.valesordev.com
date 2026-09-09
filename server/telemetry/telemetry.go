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
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
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
}

// Metrics is the AW-SRV-001 content-load instrument set.
type Metrics struct {
	ZonesLoaded      prometheus.Gauge
	RoomsLoaded      *prometheus.GaugeVec
	LoadDuration     prometheus.Histogram
	ValidationErrors *prometheus.CounterVec
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
	log := slog.New(h).With(
		"service", cfg.ServiceName,
		"env", cfg.Environment,
	)

	reg := prometheus.NewRegistry()
	m := newMetrics()
	reg.MustRegister(m.ZonesLoaded, m.RoomsLoaded, m.LoadDuration, m.ValidationErrors)

	tp := newTracerProvider(cfg)
	otel.SetTracerProvider(tp)
	tr := tp.Tracer("andara-server")

	return &Telemetry{Log: log, Metrics: m, Tracer: tr, TP: tp, Reg: reg}
}

func newTracerProvider(cfg config.Config) *sdktrace.TracerProvider {
	opts := []sdktrace.TracerProviderOption{}
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
	opts = append(opts, sdktrace.WithBatcher(exp))
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
	}
}

// Shutdown flushes the tracer provider.
func (t *Telemetry) Shutdown(ctx context.Context) {
	if t == nil || t.TP == nil {
		return
	}
	_ = t.TP.Shutdown(ctx)
}

// TraceID returns the current span's trace id, or empty.
func TraceID(ctx context.Context) string {
	sc := trace.SpanFromContext(ctx).SpanContext()
	if !sc.HasTraceID() {
		return ""
	}
	return sc.TraceID().String()
}

// LogFinding writes one structured line per validation finding.
func LogFinding(ctx context.Context, log *slog.Logger, e sim.ValidationError, strict bool) {
	level := slog.LevelError
	if e.Code == sim.ErrOrphanRoom && !strict {
		level = slog.LevelWarn
	}
	attrs := []any{
		"code", string(e.Code),
		"file", e.File,
		"zone", string(e.Zone),
		"room", string(e.Room),
		"detail", e.Detail,
		"trace_id", TraceID(ctx),
	}
	if e.Line > 0 {
		attrs = append(attrs, "line", e.Line)
	}
	log.Log(ctx, level, e.Detail, attrs...)
}
