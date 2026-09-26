// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package roster

import "github.com/prometheus/client_golang/prometheus"

// Metrics is AW-SRV-014's observability contract for the binding: what
// the Gateway knows (Sessions with a Character) and what the sim knows
// (bodies), two owners of two facts — present minus bound is the number
// of bodies no Session drives, AW-SRV-015's linkdead count. Character
// name and Account ID are never labels.
type Metrics struct {
	SessionsBound prometheus.Gauge       // andara_sessions_bound
	Characters    *prometheus.GaugeVec   // andara_characters_total{state}, set from the sim by the loop
	Bindings      *prometheus.CounterVec // andara_character_bindings_total{outcome}
	Unbinds       *prometheus.CounterVec // andara_character_unbinds_total{reason, outcome}

	// The linkdead lifecycle (AW-SRV-015), from the sim's
	// StepResult.Linkdead. in_combat is whether a combat interaction
	// extended the body's deadline; it is this process's knowledge, so a
	// body recovered linkdead counts as not in combat.
	Linkdead         *prometheus.GaugeVec     // andara_sessions_linkdead{in_combat}
	LinkdeadOutcomes *prometheus.CounterVec   // andara_linkdead_outcomes_total{outcome}
	LinkdeadDuration *prometheus.HistogramVec // andara_linkdead_duration_seconds{in_combat}
	CombatExtensions prometheus.Counter       // andara_linkdead_combat_extensions_total
	CeilingDespawns  prometheus.Counter       // andara_linkdead_ceiling_despawns_total
}

// Linkdead outcomes: how a body's grace ended. died is declared for the
// combat that will produce it; nothing does yet.
const (
	LinkdeadReconnected = "reconnected"
	LinkdeadDespawned   = "despawned"
	LinkdeadCeiling     = "ceiling"
	LinkdeadDied        = "died"
	LinkdeadQuit        = "quit"
)

// Binding outcomes.
const (
	OutcomeOK            = "ok"
	OutcomeAlreadyLive   = "already_live"
	OutcomeRaceLost      = "race_lost"
	OutcomeNotFound      = "not_found"
	OutcomeProduceFailed = "produce_failed"
)

// Unbind reasons and outcomes.
const (
	ReasonQuit          = "quit"
	ReasonLinkdead      = "linkdead"
	UnbindOK            = "ok"
	UnbindProduceFailed = "produce_failed"
)

// Body states.
const (
	StatePresent = "present"
	StateDormant = "dormant"
)

// NewMetrics registers the roster metrics on reg (nil registers nothing),
// every label pre-seeded.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		SessionsBound: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "andara_sessions_bound", Help: "Sessions driving a Character, as the Gateway's live flags count them.",
		}),
		Characters: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "andara_characters_total", Help: "Character bodies in Zone state by state; a present body with no Session counts as present.",
		}, []string{"state"}),
		Bindings: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "andara_character_bindings_total", Help: "SelectCharacter calls by outcome.",
		}, []string{"outcome"}),
		Unbinds: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "andara_character_unbinds_total", Help: "Session-end teardown produces by reason and outcome: UnbindCharacter for quit, MarkLinkdead for linkdead.",
		}, []string{"reason", "outcome"}),
		Linkdead: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "andara_sessions_linkdead", Help: "Characters waiting out their linkdead grace, by whether combat has extended it.",
		}, []string{"in_combat"}),
		LinkdeadOutcomes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "andara_linkdead_outcomes_total", Help: "How linkdead graces ended: reconnected, despawned at the deadline, at the ceiling, died, or quit.",
		}, []string{"outcome"}),
		LinkdeadDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "andara_linkdead_duration_seconds", Help: "How long a Character was linkdead, from MarkLinkdead to the end of its grace, in Ticks at sim.tick_rate.",
			Buckets: []float64{1, 5, 15, 30, 60, 90, 120, 180, 240, 300},
		}, []string{"in_combat"}),
		CombatExtensions: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "andara_linkdead_combat_extensions_total", Help: "Combat interactions against a linkdead Character.",
		}),
		CeilingDespawns: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "andara_linkdead_ceiling_despawns_total", Help: "Linkdead Characters despawned at session.linkdead_max rather than their deadline.",
		}),
	}
	for _, c := range []string{"true", "false"} {
		m.Linkdead.WithLabelValues(c)
		m.LinkdeadDuration.WithLabelValues(c)
	}
	for _, o := range []string{LinkdeadReconnected, LinkdeadDespawned, LinkdeadCeiling, LinkdeadDied, LinkdeadQuit} {
		m.LinkdeadOutcomes.WithLabelValues(o)
	}
	for _, s := range []string{StatePresent, StateDormant} {
		m.Characters.WithLabelValues(s)
	}
	for _, o := range []string{OutcomeOK, OutcomeAlreadyLive, OutcomeRaceLost, OutcomeNotFound, OutcomeProduceFailed} {
		m.Bindings.WithLabelValues(o)
	}
	for _, o := range []string{UnbindOK, UnbindProduceFailed} {
		m.Unbinds.WithLabelValues(ReasonQuit, o)
		m.Unbinds.WithLabelValues(ReasonLinkdead, o)
	}
	if reg != nil {
		reg.MustRegister(m.SessionsBound, m.Characters, m.Bindings, m.Unbinds,
			m.Linkdead, m.LinkdeadOutcomes, m.LinkdeadDuration, m.CombatExtensions, m.CeilingDespawns)
	}
	return m
}
