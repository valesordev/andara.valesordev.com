// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package recovery

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

// Phases of andara_recovery_duration_seconds: a bounded enum.
const (
	PhaseLoad   = "load"
	PhaseSeek   = "seek"
	PhaseReplay = "replay"
	PhaseVerify = "verify"
	PhaseTotal  = "total"
)

// Callers of andara_restore_total that recovery owns (AW-SRV-043): boot
// recovery, and the verification paths (`recover --verify` and
// Admin.VerifySnapshotRound).
const (
	CallerRecovery = "recovery"
	CallerVerify   = "verify"
)

// Metrics is recovery's instrument set (AW-SRV-007). No label carries a tick,
// a Zone or any other unbounded value: phase, reason, caller and outcome are
// bounded enums.
type Metrics struct {
	// Duration is the phases of one recovery, and its total.
	Duration *prometheus.HistogramVec
	// ReplayedTicks is the ticks the last recovery replayed past its round.
	ReplayedTicks prometheus.Gauge
	// Failures counts refused recoveries by reason (the exit-code table).
	Failures *prometheus.CounterVec
	// RoundTick is the tick of the round the last recovery restored from, 0
	// for a cold start.
	RoundTick prometheus.Gauge
	// Restores is andara_restore_total, AW-SRV-043's instrument, pre-seeded
	// here for the two callers this story owns.
	Restores *prometheus.CounterVec

	reg  prometheus.Registerer
	once sync.Once
	// hashMatch is registered on its first set, never before: a registered
	// Gauge exposes 0 at once, and a pre-seeded 0 would fire
	// RecoveryStateMismatch on every normal boot (SRE's amendment, 2026-10-02).
	hashMatch prometheus.Gauge
}

// NewMetrics registers the set on reg; nil registers nothing, for tests that
// read the instruments directly.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		Duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "andara_recovery_duration_seconds",
			Help:    "Duration of a recovery by phase: load, seek, replay, verify, and total.",
			Buckets: []float64{0.01, 0.05, 0.1, 0.5, 1, 2.5, 5, 10, 30, 60, 90, 120, 300},
		}, []string{"phase"}),
		ReplayedTicks: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "andara_recovery_replayed_ticks",
			Help: "Ticks the last recovery replayed past the round it restored from.",
		}),
		Failures: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "andara_recovery_failures_total",
			Help: "Recoveries refused, by reason: hash, restore, gap, version, round, store.",
		}, []string{"reason"}),
		RoundTick: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "andara_recovery_round_tick",
			Help: "Tick of the snapshot round the last recovery restored from; 0 for a replay from offset zero.",
		}),
		Restores: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "andara_restore_total",
			Help: "Restores from a Snapshot Round by caller and outcome.",
		}, []string{"caller", "outcome"}),
		reg: reg,
	}
	for _, r := range Reasons {
		m.Failures.WithLabelValues(r)
	}
	for _, c := range []string{CallerRecovery, CallerVerify} {
		for _, o := range []string{"ok", "hash_mismatch", "seed_mismatch"} {
			m.Restores.WithLabelValues(c, o)
		}
	}
	if reg != nil {
		reg.MustRegister(m.Duration, m.ReplayedTicks, m.Failures, m.RoundTick, m.Restores)
	}
	return m
}

// SetHashMatch sets andara_recovery_state_hash_match, registering it on the
// first call. 1 is a recovery whose every boundary matched, 0 a hash or restore
// mismatch. It is set once per recovery.
func (m *Metrics) SetHashMatch(match bool) {
	m.once.Do(func() {
		m.hashMatch = prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "andara_recovery_state_hash_match",
			Help: "1 when the last recovery reproduced every recorded State Hash, 0 when it did not. Absent until a recovery sets it.",
		})
		if m.reg != nil {
			m.reg.MustRegister(m.hashMatch)
		}
	})
	if match {
		m.hashMatch.Set(1)
	} else {
		m.hashMatch.Set(0)
	}
}

// HashMatch is the gauge, nil until SetHashMatch has run.
func (m *Metrics) HashMatch() prometheus.Gauge { return m.hashMatch }
