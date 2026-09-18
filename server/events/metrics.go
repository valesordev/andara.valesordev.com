// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package events

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/valesordev/andara/server/sim"
)

// Metrics is the fan-out's instrumentation (AW-SRV-004). No label carries
// an Entity ID, a Room ID, or a subscription ID: `type` is the EventType
// enum and `reason` the drop-reason enum.
type Metrics struct {
	// Emitted counts Events the sim published, by type.
	Emitted *prometheus.CounterVec
	// FanoutDuration is the time to deliver one batch — one tick's worth
	// under load — to every subscriber, measured outside the tick.
	FanoutDuration prometheus.Histogram
	// Subscribers is the live subscription count.
	Subscribers prometheus.Gauge
	// Drops counts subscriptions ended, by reason.
	Drops *prometheus.CounterVec
	// Redactions counts deliveries in the redacted form, by type.
	Redactions *prometheus.CounterVec
	// InboundDropped counts Events the fan-out could not queue: its own
	// goroutine was starved. Any value above zero is a process problem.
	InboundDropped prometheus.Counter
	// PublishLag is how far the Event producer trails the tick: the time
	// from publishing a Tick Boundary Record to its acknowledgement.
	PublishLag prometheus.Gauge
}

// NewMetrics registers the fan-out metrics on reg (nil registers nothing)
// and pre-seeds every enumerable label.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		Emitted: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "andara_events_emitted_total",
			Help: "Events the simulation emitted, by type. Bounded by the EventType enum.",
		}, []string{"type"}),
		FanoutDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "andara_event_fanout_duration_seconds",
			Help:    "Time to deliver one batch of Events to every subscriber, outside the tick.",
			Buckets: []float64{0.0001, 0.00025, 0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1},
		}),
		Subscribers: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "andara_subscribers",
			Help: "Live Event subscriptions.",
		}),
		Drops: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "andara_subscriber_drops_total",
			Help: "Subscriptions ended, by reason.",
		}, []string{"reason"}),
		Redactions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "andara_event_scope_redactions_total",
			Help: "Deliveries in the redacted form — privileged detail withheld from an unprivileged observer — by type.",
		}, []string{"type"}),
		InboundDropped: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "andara_event_fanout_dropped_total",
			Help: "Events dropped before delivery because the fan-out goroutine was not keeping up. Any value above zero is a process problem.",
		}),
		PublishLag: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "andara_event_publish_lag_seconds",
			Help: "How far the Event producer trails the tick: time from publishing a Tick Boundary Record to its acknowledgement, as last observed.",
		}),
	}
	for _, t := range []sim.EventType{sim.EvRoomDescribed, sim.EvCharacterArrived, sim.EvCharacterLeft, sim.EvCommandRejected, sim.EvZoneFaulted, sim.EvSubscriberDropped, sim.EvSimulationStopped} {
		m.Emitted.WithLabelValues(string(t))
		m.Redactions.WithLabelValues(string(t))
	}
	for _, r := range []string{ReasonBufferFull, ReasonUnsubscribed, ReasonShutdown} {
		m.Drops.WithLabelValues(r)
	}
	if reg != nil {
		reg.MustRegister(m.Emitted, m.FanoutDuration, m.Subscribers, m.Drops, m.Redactions, m.InboundDropped, m.PublishLag)
	}
	return m
}

// Metrics exposes the Hub's metrics, for the loop to set PublishLag.
func (h *Hub) Metrics() *Metrics { return h.metrics }
