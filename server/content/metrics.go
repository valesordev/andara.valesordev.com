// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package content

import "github.com/prometheus/client_golang/prometheus"

// Metrics is the content pipeline's instrumentation (AW-SRV-012).
//
// `pack` is a label because the pack set is bounded by content and is the one
// dimension an operator needs to answer "which pack is stale". `reason` and
// `phase` and `outcome` are closed enumerations declared in this package, not
// free text, and every value is pre-seeded below so a dashboard shows a zero
// rather than a gap.
type Metrics struct {
	// ActiveVersion is the version each pack is serving.
	ActiveVersion *prometheus.GaugeVec
	// LoadDuration is the cost of one load, by phase.
	//
	// Named andara_content_load_phase_duration_seconds, not the
	// andara_content_load_duration_seconds this story's Observability section
	// asks for: AW-SRV-001 already publishes that name with no labels and
	// states "Labels: none. Cardinality: 1 series." Adding a label to a
	// published metric silently breaks every query written against it, and
	// which of the two contracts gives way is architecture's to decide
	// (docs/feedback/AW-SRV-012-content-resolution-and-reload.md §9). A new
	// name is the reversible choice.
	LoadDuration *prometheus.HistogramVec
	// LoadFailures counts versions refused, by reason. The previous version
	// keeps serving after each one, so this is not an availability signal —
	// it is a content-freshness signal, and the SLO architecture writes for
	// this story is built on it.
	LoadFailures *prometheus.CounterVec
	// CacheHits counts blob lookups, hit and miss.
	CacheHits *prometheus.CounterVec
}

// Load phases, the values of the `phase` label.
const (
	PhaseResolve  = "resolve"
	PhaseValidate = "validate"
	PhaseBuild    = "build"
	PhaseSwap     = "swap"
)

// Cache outcomes, the values of the `outcome` label.
const (
	OutcomeHit  = "hit"
	OutcomeMiss = "miss"
)

// NewMetrics registers the content metrics on reg (nil registers nothing) and
// pre-seeds every enumerable label.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		ActiveVersion: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "andara_content_active_version",
			Help: "The content version each pack is currently serving.",
		}, []string{"pack"}),
		LoadDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "andara_content_load_phase_duration_seconds",
			Help:    "Cost of one content load, by phase.",
			Buckets: []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
		}, []string{"phase"}),
		LoadFailures: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "andara_content_load_failures_total",
			Help: "Content versions refused, by reason. The previously loaded version keeps serving after each one.",
		}, []string{"reason"}),
		CacheHits: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "andara_content_cache_hits_total",
			Help: "Blob cache lookups, by outcome.",
		}, []string{"outcome"}),
	}
	for _, p := range []string{PhaseResolve, PhaseValidate, PhaseBuild, PhaseSwap} {
		m.LoadDuration.WithLabelValues(p)
	}
	for _, r := range []string{
		ReasonFormatVersion, ReasonCoreVersion, ReasonValidation,
		ReasonBlobMissing, ReasonBlobCorrupt, ReasonFallbackRoom,
		ReasonPackMismatch, ReasonManifestAbsent, ReasonStoreUnavailable,
		ReasonBlobTooLarge,
	} {
		m.LoadFailures.WithLabelValues(r)
	}
	for _, o := range []string{OutcomeHit, OutcomeMiss} {
		m.CacheHits.WithLabelValues(o)
	}
	if reg != nil {
		reg.MustRegister(m.ActiveVersion, m.LoadDuration, m.LoadFailures, m.CacheHits)
	}
	return m
}
