// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package gateway

import "github.com/prometheus/client_golang/prometheus"

// Session outcomes for andara_sessions_total. A bounded enum: every way a
// Session can end maps to one of these four, and nothing else is ever a
// label value.
const (
	OutcomeClosed          = "closed"           // CloseSession, or the server closed it (drain)
	OutcomeDropped         = "dropped"          // the connection went away first
	OutcomeRejectedVersion = "rejected_version" // never established: version outside the range
	OutcomeRejectedAuth    = "rejected_auth"    // never established: token rejected
)

// Metrics is the AW-SRV-005 instrument set. Method labels are bounded by the
// service definition and code labels by the gRPC code enum. Session ID,
// remote address, and token are never labels.
type Metrics struct {
	SessionsActive  prometheus.Gauge
	SessionsTotal   *prometheus.CounterVec
	SessionDuration prometheus.Histogram
	RequestsTotal   *prometheus.CounterVec
	RequestDuration *prometheus.HistogramVec
}

// NewMetrics builds and registers the gateway instruments. A nil registerer
// builds them unregistered, which a test can read with testutil.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		SessionsActive: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "andara",
			Name:      "sessions_active",
			Help:      "Sessions currently established on this process.",
		}),
		SessionsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "andara",
			Name:      "sessions_total",
			Help:      "Sessions ended or rejected, by outcome.",
		}, []string{"outcome"}),
		SessionDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: "andara",
			Name:      "session_duration_seconds",
			Help:      "Lifetime of a Session from OpenSession to teardown.",
			Buckets:   []float64{1, 10, 60, 300, 900, 1800, 3600, 7200, 14400},
		}),
		RequestsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "andara",
			Name:      "grpc_requests_total",
			Help:      "RPCs completed, by method and gRPC code.",
		}, []string{"method", "code"}),
		RequestDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "andara",
			Name:      "grpc_request_duration_seconds",
			Help:      "RPC wall time from interceptor entry to completion, by method.",
			Buckets:   prometheus.DefBuckets,
		}, []string{"method"}),
	}
	// Every outcome is present from the first scrape, so a rate() over one
	// that has not happened yet is zero rather than absent.
	for _, o := range []string{OutcomeClosed, OutcomeDropped, OutcomeRejectedVersion, OutcomeRejectedAuth} {
		m.SessionsTotal.WithLabelValues(o)
	}
	if reg != nil {
		reg.MustRegister(m.SessionsActive, m.SessionsTotal, m.SessionDuration, m.RequestsTotal, m.RequestDuration)
	}
	return m
}
