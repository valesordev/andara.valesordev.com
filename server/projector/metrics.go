// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package projector

import (
	"github.com/prometheus/client_golang/prometheus"

	statev1 "github.com/valesordev/andara/gen/go/andara/state/v1"
)

// Metrics is the state projector's instrument set (AW-SRV-019). No label
// carries an aggregate ID: kind and phase are bounded enums.
type Metrics struct {
	// LagSeconds is now minus when the last verified tick's boundary was
	// produced.
	LagSeconds prometheus.Gauge
	// LagBudgetSeconds is projector.state.lag_budget, exported so the
	// ProjectionStale rule compares the lag with the configured budget
	// rather than a number copied into the rule (SnapshotStale's pattern).
	LagBudgetSeconds prometheus.Gauge
	// Tick is the last verified tick.
	Tick prometheus.Gauge
	// RecordsProduced counts acknowledged records by kind; tombstones
	// included, and counted again on Tombstones.
	RecordsProduced *prometheus.CounterVec
	Tombstones      prometheus.Counter
	// DigestMismatches must stay 0. It reaches 1 on the first divergence,
	// and the binary then holds /metrics up for a minute before exiting 2,
	// because a restart starts it at 0 and an immediate exit would never be
	// scraped (StateProjectorDiverged).
	DigestMismatches prometheus.Counter
	// RebuildDuration is the bootstrap, by phase: bootstrap (load the round
	// and write the dump) and replay (catch up to the log head).
	RebuildDuration *prometheus.HistogramVec
	// TopicBytes is andara.state.v1's size on the broker, summed over
	// partitions (one replica each), from broker metadata.
	TopicBytes prometheus.Gauge
}

// NewMetrics registers the set on reg; nil registers nothing, for tests.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		LagSeconds: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "andara_state_projector_lag_seconds",
			Help: "Seconds between now and when the last verified tick's boundary was produced.",
		}),
		LagBudgetSeconds: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "andara_state_projector_lag_budget_seconds",
			Help: "projector.state.lag_budget: the lag ProjectionStale fires above.",
		}),
		Tick: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "andara_state_projector_tick",
			Help: "The last tick the state projector verified.",
		}),
		RecordsProduced: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "andara_state_records_produced_total",
			Help: "andara.state.v1 records acknowledged by the broker, by aggregate kind.",
		}, []string{"kind"}),
		Tombstones: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "andara_state_tombstones_total",
			Help: "Tombstones acknowledged on andara.state.v1.",
		}),
		DigestMismatches: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "andara_state_digest_mismatches_total",
			Help: "Ticks whose replayed State Hash differed from the recorded one. Must stay 0.",
		}),
		RebuildDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "andara_state_rebuild_duration_seconds",
			Help:    "Duration of a state projector bootstrap, by phase.",
			Buckets: []float64{0.1, 0.5, 1, 5, 15, 30, 60, 120, 300, 600, 1800},
		}, []string{"phase"}),
		TopicBytes: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "andara_state_topic_bytes",
			Help: "Size of andara.state.v1 on the broker, one replica per partition.",
		}),
	}
	// Every kind exists from the start, so a rate() over a kind that has not
	// been written yet reads 0 rather than absent.
	for _, k := range []statev1.AggregateKind{statev1.AggregateKind_CHARACTER, statev1.AggregateKind_NPC,
		statev1.AggregateKind_ITEM, statev1.AggregateKind_ROOM, statev1.AggregateKind_ZONE} {
		m.RecordsProduced.WithLabelValues(KindLabel(k))
	}
	m.RebuildDuration.WithLabelValues("bootstrap")
	m.RebuildDuration.WithLabelValues("replay")
	if reg != nil {
		reg.MustRegister(m.LagSeconds, m.LagBudgetSeconds, m.Tick, m.RecordsProduced, m.Tombstones, m.DigestMismatches, m.RebuildDuration, m.TopicBytes)
	}
	return m
}

// KindLabel is the kind label's value: the key prefix.
func KindLabel(k statev1.AggregateKind) string {
	switch k {
	case statev1.AggregateKind_CHARACTER:
		return "character"
	case statev1.AggregateKind_NPC:
		return "npc"
	case statev1.AggregateKind_ITEM:
		return "item"
	case statev1.AggregateKind_ROOM:
		return "room"
	case statev1.AggregateKind_ZONE:
		return "zone"
	}
	return "unknown"
}
