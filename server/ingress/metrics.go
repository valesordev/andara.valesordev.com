// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package ingress

import (
	"strconv"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/valesordev/andara/server/sim"
)

// Submit outcomes, the `outcome` label of andara_ingress_submits_total.
// Session ID, actor ID, and raw Intent text are never labels.
const (
	OutcomeProduced      = "produced"
	OutcomeRejectedParse = "rejected_parse"
	OutcomeRejectedAuthz = "rejected_authz"
	OutcomeRateLimited   = "rate_limited"
	OutcomePendingFull   = "pending_full"
	OutcomeInTransit     = "in_transit"
	OutcomeUnavailable   = "unavailable"
	OutcomeDeadline      = "deadline"
	OutcomeCanceled      = "canceled"
	OutcomeInternal      = "internal"
)

// Outcomes is every outcome, for pre-seeding.
var Outcomes = []string{OutcomeProduced, OutcomeRejectedParse, OutcomeRejectedAuthz, OutcomeRateLimited, OutcomePendingFull, OutcomeInTransit, OutcomeUnavailable, OutcomeDeadline, OutcomeCanceled, OutcomeInternal}

// Metrics is the ingress instrumentation (AW-SRV-010).
type Metrics struct {
	// Submits counts Submit calls by outcome.
	Submits *prometheus.CounterVec
	// ProduceDuration is enqueue to acknowledgement: the latency a player
	// feels before the ack.
	ProduceDuration prometheus.Histogram
	// ProduceRetries counts produce requests the client had to send again
	// after a transport failure.
	ProduceRetries prometheus.Counter
	// Pending is Submits in flight on this process.
	Pending prometheus.Gauge
	// Held is Intents waiting for their Character to arrive in the next
	// Zone.
	Held prometheus.Gauge
	// Degraded is 1 while the Command log is unreachable and the World is
	// read-only. AW-INF-005 alerts on it.
	Degraded prometheus.Gauge
	// PartitionSkew is Commands produced to each Partition since boot; the
	// spread across the 64 reveals a hot Zone long before it is a tick
	// problem (ADR-0001).
	PartitionSkew *prometheus.GaugeVec
}

// NewMetrics registers the ingress metrics on reg (nil registers nothing)
// and pre-seeds every enumerable label.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		Submits: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "andara_ingress_submits_total",
			Help: "Submit calls by outcome. Bounded enum.",
		}, []string{"outcome"}),
		ProduceDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "andara_ingress_produce_duration_seconds",
			Help:    "Time from enqueue to the Command log's acknowledgement: what a player waits for the ack.",
			Buckets: []float64{0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5},
		}),
		ProduceRetries: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "andara_ingress_produce_retries_total",
			Help: "Produce requests sent again after a transport failure; the idempotent producer keeps them from duplicating.",
		}),
		Pending: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "andara_ingress_pending",
			Help: "Submits in flight on this process.",
		}),
		Held: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "andara_ingress_held_intents",
			Help: "Intents held while their Character is between Zones.",
		}),
		Degraded: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "andara_ingress_degraded",
			Help: "1 while the Command log is unreachable and the World is read-only.",
		}),
		PartitionSkew: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "andara_ingress_partition_skew",
			Help: "Commands produced to each Partition since boot. Cardinality 64.",
		}, []string{"partition"}),
	}
	for _, o := range Outcomes {
		m.Submits.WithLabelValues(o)
	}
	for p := range int32(sim.PartitionCount) {
		m.PartitionSkew.WithLabelValues(strconv.Itoa(int(p)))
	}
	if reg != nil {
		reg.MustRegister(m.Submits, m.ProduceDuration, m.ProduceRetries, m.Pending, m.Held, m.Degraded, m.PartitionSkew)
	}
	return m
}
