// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package egress

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/valesordev/andara/server/sim"
)

// Drop reasons, the `reason` label of andara_session_egress_drops_total:
// why a stream ended other than by the client closing it cleanly.
const (
	// ReasonBufferFull: the client trailed by more than egress.buffer, or
	// the fan-out dropped the Session's subscription for the same reason.
	ReasonBufferFull = "buffer_full"
	// ReasonClientGone: the client went away, or the Session ended.
	ReasonClientGone = "client_gone"
	// ReasonDraining: the server is shutting down.
	ReasonDraining = "draining"
)

// Frame types with no EventType: the `type` label values this package
// adds to the enum.
const (
	TypeHeartbeat = "heartbeat"
	TypeResync    = "resync"
)

// Resync reasons, Resync.reason and the `reason` label of
// andara_stream_resyncs_total.
const (
	ResyncWindowExceeded = "resume_window_exceeded"
	ResyncNoHistory      = "no_history"
)

// Metrics is the stream instrumentation (AW-SRV-011). No label carries a
// Session ID or an Entity ID. The fan-out's own series — the subscriber
// gauge, the fan-out histogram and span, the fan-out's drops — are
// events.Metrics and are not duplicated here.
type Metrics struct {
	// Streams is open Subscribe streams.
	Streams prometheus.Gauge
	// Sent counts frames written to streams, by type.
	Sent *prometheus.CounterVec
	// Drops counts streams ended by the server, by reason.
	Drops *prometheus.CounterVec
	// BufferDepth is how far behind the ring a stream's cursor was when
	// an Event was appended: sampled across every Session, never per one.
	BufferDepth prometheus.Histogram
	// Resyncs counts resumes the server could not honor, by reason. A
	// rising rate means egress.resume_window is too small.
	Resyncs *prometheus.CounterVec
}

// NewMetrics registers the stream metrics on reg (nil registers nothing)
// and pre-seeds every enumerable label.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		Streams: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "andara_stream_subscribers",
			Help: "Open Game.Subscribe streams.",
		}),
		Sent: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "andara_stream_events_sent_total",
			Help: "Frames written to Subscribe streams, by type: the EventType enum plus heartbeat and resync.",
		}, []string{"type"}),
		Drops: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "andara_session_egress_drops_total",
			Help: "Subscribe streams the server ended, by reason.",
		}, []string{"reason"}),
		BufferDepth: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "andara_stream_buffer_depth",
			Help:    "Events a stream had unsent when another was appended for it, sampled across Sessions.",
			Buckets: []float64{0, 1, 2, 4, 8, 16, 32, 64, 128, 256, 512, 1024, 2048},
		}),
		Resyncs: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "andara_stream_resyncs_total",
			Help: "Resumes the server could not honor, by reason. Rising means egress.resume_window is too small.",
		}, []string{"reason"}),
	}
	for _, t := range []sim.EventType{sim.EvRoomDescribed, sim.EvCharacterArrived, sim.EvCharacterLeft, sim.EvCommandRejected, sim.EvZoneFaulted, sim.EvSubscriberDropped, sim.EvSimulationStopped} {
		m.Sent.WithLabelValues(string(t))
	}
	m.Sent.WithLabelValues(TypeHeartbeat)
	m.Sent.WithLabelValues(TypeResync)
	for _, r := range []string{ReasonBufferFull, ReasonClientGone, ReasonDraining} {
		m.Drops.WithLabelValues(r)
	}
	for _, r := range []string{ResyncWindowExceeded, ResyncNoHistory} {
		m.Resyncs.WithLabelValues(r)
	}
	if reg != nil {
		reg.MustRegister(m.Streams, m.Sent, m.Drops, m.BufferDepth, m.Resyncs)
	}
	return m
}
