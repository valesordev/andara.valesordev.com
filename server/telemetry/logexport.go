// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package telemetry

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	sdklog "go.opentelemetry.io/otel/sdk/log"

	"github.com/valesordev/andara/server/config"
)

// Log export (AW-SRV-024): the same records stderr carries, over OTLP to
// the collector, with trace context as record fields so Loki's
// trace-to-logs link works. The queue is bounded and never blocks the
// caller: when the collector is away, records are dropped and counted, and
// the process neither stalls nor grows.

// Log export bounds. Nothing here is player-visible.
const (
	LogQueueSize      = 2048
	LogExportInterval = time.Second
	LogExportBatch    = 512
	logExportTimeout  = 5 * time.Second
)

// LogExportMetrics is the exporter's own instrument set.
type LogExportMetrics struct {
	Dropped   prometheus.Counter // andara_log_export_dropped_total
	QueueSize prometheus.Gauge   // andara_log_export_queue_size
}

func newLogExportMetrics(reg prometheus.Registerer) *LogExportMetrics {
	m := &LogExportMetrics{
		Dropped:   prometheus.NewCounter(prometheus.CounterOpts{Name: "andara_log_export_dropped_total", Help: "Log records discarded because the export queue was full — the collector was away longer than the queue could absorb."}),
		QueueSize: prometheus.NewGauge(prometheus.GaugeOpts{Name: "andara_log_export_queue_size", Help: "Log records waiting to be exported."}),
	}
	if reg != nil {
		reg.MustRegister(m.Dropped, m.QueueSize)
	}
	return m
}

// queueProcessor is an sdklog.Processor with an explicit bounded queue.
// The SDK's BatchProcessor also drops on a full queue, but does not say
// how many; the count is the point (AC-4), so the queue is ours.
type queueProcessor struct {
	exporter sdklog.Exporter
	queue    chan sdklog.Record
	metrics  *LogExportMetrics
	warn     func(error) // rate-limited, stderr only
	stop     chan struct{}
	done     chan struct{}
	dropped  atomic.Uint64
	once     sync.Once
}

func newQueueProcessor(exp sdklog.Exporter, m *LogExportMetrics, warn func(error)) *queueProcessor {
	p := &queueProcessor{exporter: exp, queue: make(chan sdklog.Record, LogQueueSize), metrics: m, warn: warn, stop: make(chan struct{}), done: make(chan struct{})}
	go p.run()
	return p
}

// OnEmit implements sdklog.Processor without blocking.
func (p *queueProcessor) OnEmit(_ context.Context, r *sdklog.Record) error {
	select {
	case p.queue <- r.Clone():
		p.metrics.QueueSize.Set(float64(len(p.queue)))
	default:
		p.dropped.Add(1)
		p.metrics.Dropped.Inc()
	}
	return nil
}

// Enabled implements sdklog.FilterProcessor: everything the level allows.
func (p *queueProcessor) Enabled(context.Context, sdklog.EnabledParameters) bool { return true }

func (p *queueProcessor) run() {
	defer close(p.done)
	ticker := time.NewTicker(LogExportInterval)
	defer ticker.Stop()
	batch := make([]sdklog.Record, 0, LogExportBatch)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), logExportTimeout)
		err := p.exporter.Export(ctx, batch)
		cancel()
		if err != nil {
			p.warn(err)
		}
		batch = batch[:0]
		p.metrics.QueueSize.Set(float64(len(p.queue)))
	}
	for {
		select {
		case r := <-p.queue:
			batch = append(batch, r)
			if len(batch) >= LogExportBatch {
				flush()
			}
		case <-ticker.C:
			flush()
		case <-p.stop:
			for {
				select {
				case r := <-p.queue:
					batch = append(batch, r)
					if len(batch) >= LogExportBatch {
						flush()
					}
				default:
					flush()
					return
				}
			}
		}
	}
}

// Shutdown implements sdklog.Processor: drains the queue, then the exporter.
func (p *queueProcessor) Shutdown(ctx context.Context) error {
	p.once.Do(func() { close(p.stop) })
	select {
	case <-p.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	return p.exporter.Shutdown(ctx)
}

// ForceFlush implements sdklog.Processor. The run loop flushes on its
// interval; a caller who needs it now waits one interval.
func (p *queueProcessor) ForceFlush(ctx context.Context) error {
	select {
	case <-time.After(LogExportInterval + 100*time.Millisecond):
	case <-ctx.Done():
		return ctx.Err()
	}
	return p.exporter.ForceFlush(ctx)
}

// Dropped is how many records the queue discarded, for tests.
func (p *queueProcessor) Dropped() uint64 { return p.dropped.Load() }

// rateLimitedWarn writes the exporter's own failures to stderr at most once
// a minute. An exporter that logs its failure through itself is a loop.
func rateLimitedWarn(stderr *slog.Logger) func(error) {
	var mu sync.Mutex
	var last time.Time
	return func(err error) {
		mu.Lock()
		defer mu.Unlock()
		if time.Since(last) < time.Minute {
			return
		}
		last = time.Now()
		stderr.Warn("otlp log export failed", "detail", err.Error())
	}
}

// newLogExport builds the OTLP log exporter and the slog handler that feeds
// it. A nil handler means export is disabled (no endpoint). An endpoint the
// exporter cannot be built for is a configuration error, fatal like the
// trace exporter's.
func newLogExport(cfg config.Config, stderr *slog.Logger, reg prometheus.Registerer, _ io.Writer) (slog.Handler, *sdklog.LoggerProvider, *queueProcessor, error) {
	if cfg.OTLPEndpoint == "" {
		return nil, nil, nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	exp, err := otlploggrpc.New(ctx,
		otlploggrpc.WithEndpoint(cfg.OTLPEndpoint),
		otlploggrpc.WithInsecure(),
	)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("telemetry: otlp log exporter: %w", err)
	}
	proc := newQueueProcessor(exp, newLogExportMetrics(reg), rateLimitedWarn(stderr))
	lp := sdklog.NewLoggerProvider(
		sdklog.WithResource(bootResource(cfg)),
		sdklog.WithProcessor(proc),
	)
	h := otelslog.NewHandler("andara-server", otelslog.WithLoggerProvider(lp), otelslog.WithSource(false))
	return h, lp, proc, nil
}
