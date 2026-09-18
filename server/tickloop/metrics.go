// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package tickloop

import (
	"github.com/prometheus/client_golang/prometheus"
)

// Metrics is AW-SRV-002's observability contract: the three tick SLIs,
// consumer lag, and the loop's bookkeeping. Entity, Room, Session, and
// Command IDs are never labels.
type Metrics struct {
	TickDuration     prometheus.Histogram     // andara_tick_duration_seconds
	ZoneTickDuration *prometheus.HistogramVec // andara_zone_tick_duration_seconds{zone}
	Ticks            prometheus.Counter
	Overruns         prometheus.Counter
	Lag              prometheus.Gauge
	AppliedRecords   prometheus.Counter
	DeferredRecords  prometheus.Gauge
	InputStarved     prometheus.Counter
	ConsumerLag      *prometheus.GaugeVec // {partition}
	CheckpointAge    prometheus.Gauge
	ZoneFaults       *prometheus.CounterVec // {zone}
	PublishFailures  *prometheus.CounterVec // {kind}: events, commands, checkpoint, boundary
}

// TickBuckets place the 50 ms Tick Budget on a boundary (ADR-0008), so the
// SLI is measured rather than interpolated (AC-16).
var TickBuckets = []float64{0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1}

// NewMetrics registers the tick metrics on reg (nil registers nothing).
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		TickDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "andara_tick_duration_seconds", Help: "Time to process one tick.", Buckets: TickBuckets,
		}),
		// A separate name rather than a label on the tick histogram: the
		// unlabeled series is the SLI, and a labeled variant with a sum
		// across Zones would double-count it.
		ZoneTickDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "andara_zone_tick_duration_seconds", Help: "Time one tick spent applying Commands in each Zone. Cardinality: Zones.", Buckets: TickBuckets,
		}, []string{"zone"}),
		Ticks:           prometheus.NewCounter(prometheus.CounterOpts{Name: "andara_ticks_total", Help: "Ticks completed."}),
		Overruns:        prometheus.NewCounter(prometheus.CounterOpts{Name: "andara_tick_overruns_total", Help: "Ticks whose processing exceeded the Tick Budget."}),
		Lag:             prometheus.NewGauge(prometheus.GaugeOpts{Name: "andara_simulation_lag_seconds", Help: "How far behind schedule the loop is — the player-visible symptom."}),
		AppliedRecords:  prometheus.NewCounter(prometheus.CounterOpts{Name: "andara_tick_applied_records_total", Help: "Commands applied."}),
		DeferredRecords: prometheus.NewGauge(prometheus.GaugeOpts{Name: "andara_tick_deferred_records", Help: "Buffered Commands not yet applied — the backlog beyond max_per_tick."}),
		InputStarved:    prometheus.NewCounter(prometheus.CounterOpts{Name: "andara_tick_input_starved_total", Help: "Ticks that applied nothing because the broker was unreachable."}),
		ConsumerLag:     prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "andara_consumer_lag", Help: "Broker end offset minus next-to-read, per Partition. Cardinality 64."}, []string{"partition"}),
		CheckpointAge:   prometheus.NewGauge(prometheus.GaugeOpts{Name: "andara_checkpoint_age_ticks", Help: "Ticks since offsets were last committed — the re-apply cost of a restart."}),
		ZoneFaults:      prometheus.NewCounterVec(prometheus.CounterOpts{Name: "andara_tick_zone_faults_total", Help: "Zones quarantined by a panic inside a tick."}, []string{"zone"}),
		PublishFailures: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "andara_tick_publish_failures_total", Help: "Ticks whose Events, cross-Zone Commands, or checkpoint could not be written."}, []string{"kind"}),
	}
	for _, k := range []string{"events", "commands", "checkpoint", "boundary"} {
		m.PublishFailures.WithLabelValues(k)
	}
	if reg != nil {
		reg.MustRegister(m.TickDuration, m.ZoneTickDuration, m.Ticks, m.Overruns, m.Lag, m.AppliedRecords,
			m.DeferredRecords, m.InputStarved, m.ConsumerLag, m.CheckpointAge, m.ZoneFaults, m.PublishFailures)
	}
	return m
}
