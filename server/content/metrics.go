// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package content

import (
	"sort"
	"strconv"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

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
	// Named andara_content_load_phase_duration_seconds, beside AW-SRV-001's
	// unlabelled andara_content_load_duration_seconds: one says how long a
	// load took, this one where the time went. Adding a label to the published
	// name would have broken every query written against it, so both are kept
	// (decided by architecture, 2026-09-24; the story's metric list is
	// amended to match).
	LoadDuration *prometheus.HistogramVec
	// LoadFailures counts versions refused, by reason. The previous version
	// keeps serving after each one, so this is not an availability signal —
	// it is a content-freshness signal, and the SLO architecture writes for
	// this story is built on it.
	LoadFailures *prometheus.CounterVec
	// CacheHits counts blob lookups, hit and miss.
	CacheHits *prometheus.CounterVec
	// Relocations counts Entities a content swap moved to their Zone's
	// fallback Room, by Zone (AC-3). Zones are bounded by content.
	Relocations *prometheus.CounterVec
	// ReloadStall is the in-tick cost of applying one swap (AC-9): what the
	// tick spent inside it, which must stay under sim.tick_budget_ms / 2.
	ReloadStall prometheus.Histogram
	// BuildInfo is andara_build_info: always 1, one series per pack in effect
	// naming its content_version, with the build's version, commit and env.
	// Before any content is in effect, one series with both empty.
	BuildInfo *prometheus.GaugeVec

	mu      sync.Mutex
	build   [3]string // version, commit, env
	content map[string]uint64
	// pendingFn reads andara_content_pending_seconds at scrape time: a gauge
	// of seconds-since cannot be set once and left, so it is computed.
	pendingFn func() map[string]float64
	pendingD  *prometheus.Desc
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
		Relocations: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "andara_content_relocations_total",
			Help: "Entities moved to their Zone's fallback Room by a content swap, by Zone.",
		}, []string{"zone"}),
		ReloadStall: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "andara_content_reload_stall_seconds",
			Help:    "In-tick cost of applying one content swap.",
			Buckets: []float64{0.0001, 0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1},
		}),
		BuildInfo: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "andara_build_info",
			Help: "Always 1: the running build, and the content version of each pack in effect.",
		}, []string{"version", "commit", "env", "pack", "content_version"}),
		pendingD: prometheus.NewDesc("andara_content_pending_seconds",
			"Seconds since a pack's Active Pointer moved to a version that is neither in effect nor refused for a Builder's reason; 0 otherwise.",
			[]string{"pack"}, nil),
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
		reg.MustRegister(m.ActiveVersion, m.LoadDuration, m.LoadFailures, m.CacheHits,
			m.Relocations, m.ReloadStall, m.BuildInfo, pendingCollector{m})
	}
	m.buildInfo(nil)
	return m
}

// SetBuild names the running build on andara_build_info.
func (m *Metrics) SetBuild(version, commit, env string) {
	m.mu.Lock()
	m.build = [3]string{version, commit, env}
	content := m.content
	m.mu.Unlock()
	m.buildInfo(content)
}

// buildInfo rewrites andara_build_info for the content in effect.
func (m *Metrics) buildInfo(content map[string]uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.content = content
	m.BuildInfo.Reset()
	b := m.build
	if len(content) == 0 {
		m.BuildInfo.WithLabelValues(b[0], b[1], b[2], "", "").Set(1)
		return
	}
	packs := make([]string, 0, len(content))
	for p := range content {
		packs = append(packs, p)
	}
	sort.Strings(packs)
	for _, p := range packs {
		m.BuildInfo.WithLabelValues(b[0], b[1], b[2], p, strconv.FormatUint(content[p], 10)).Set(1)
	}
}

// pending sets what andara_content_pending_seconds reads.
func (m *Metrics) pending(fn func() map[string]float64) {
	m.mu.Lock()
	m.pendingFn = fn
	m.mu.Unlock()
}

// pendingCollector computes andara_content_pending_seconds at scrape time.
type pendingCollector struct{ m *Metrics }

func (c pendingCollector) Describe(ch chan<- *prometheus.Desc) { ch <- c.m.pendingD }

func (c pendingCollector) Collect(ch chan<- prometheus.Metric) {
	c.m.mu.Lock()
	fn := c.m.pendingFn
	c.m.mu.Unlock()
	if fn == nil {
		return
	}
	for pack, v := range fn() {
		ch <- prometheus.MustNewConstMetric(c.m.pendingD, prometheus.GaugeValue, v, pack)
	}
}
