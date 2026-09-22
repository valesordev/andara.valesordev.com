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
}

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
			Name: "andara_character_unbinds_total", Help: "UnbindCharacter produces at Session end by reason and outcome.",
		}, []string{"reason", "outcome"}),
	}
	for _, s := range []string{StatePresent, StateDormant} {
		m.Characters.WithLabelValues(s)
	}
	for _, o := range []string{OutcomeOK, OutcomeAlreadyLive, OutcomeRaceLost, OutcomeNotFound, OutcomeProduceFailed} {
		m.Bindings.WithLabelValues(o)
	}
	for _, o := range []string{UnbindOK, UnbindProduceFailed} {
		m.Unbinds.WithLabelValues(ReasonQuit, o)
	}
	if reg != nil {
		reg.MustRegister(m.SessionsBound, m.Characters, m.Bindings, m.Unbinds)
	}
	return m
}
