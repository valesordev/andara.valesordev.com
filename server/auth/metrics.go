// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package auth

import (
	"github.com/prometheus/client_golang/prometheus"
)

// Metrics is AW-SRV-008's observability contract. Account ID, username,
// token, and hash are never labels; every label set here is enumerable.
type Metrics struct {
	Attempts           *prometheus.CounterVec // andara_auth_attempts_total{outcome}
	VerifyDuration     prometheus.Histogram   // andara_auth_verify_duration_seconds
	Registrations      *prometheus.CounterVec // andara_registrations_total{mode}
	InviteRedemptions  *prometheus.CounterVec // andara_invite_redemptions_total{outcome}
	PrivilegedActions  *prometheus.CounterVec // andara_privileged_actions_total{action}
	Accounts           *prometheus.GaugeVec   // andara_accounts_total{role}
	AuditWriteFailures prometheus.Counter     // andara_audit_write_failures_total
	CharacterCreations *prometheus.CounterVec // andara_character_creations_total{outcome} (AW-SRV-014)
}

// Attempt outcomes.
const (
	OutcomeOK            = "ok"
	OutcomeBadCredential = "bad_credential"
	OutcomeRateLimited   = "rate_limited"
	OutcomeDisabled      = "disabled"
)

// Invite redemption outcomes.
const (
	RedemptionOK       = "ok"
	RedemptionInvalid  = "invalid"
	RedemptionRaceLost = "race_lost"
)

// NewMetrics registers the auth metrics on reg (nil registers nothing) and
// pre-seeds every enumerable label so a dashboard sees zero rather than
// absence.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		Attempts: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "andara_auth_attempts_total",
			Help: "Authentication attempts by outcome.",
		}, []string{"outcome"}),
		VerifyDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "andara_auth_verify_duration_seconds",
			Help:    "Argon2id credential verification time — the cost as seen in production.",
			Buckets: []float64{0.01, 0.025, 0.05, 0.1, 0.2, 0.4, 0.8, 1.6},
		}),
		Registrations: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "andara_registrations_total",
			Help: "Accounts created through Register, by the registration mode in effect.",
		}, []string{"mode"}),
		InviteRedemptions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "andara_invite_redemptions_total",
			Help: "Invite code presentations by outcome.",
		}, []string{"outcome"}),
		PrivilegedActions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "andara_privileged_actions_total",
			Help: "Audited privileged actions by action. Bounded by the Admin RPC list.",
		}, []string{"action"}),
		Accounts: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "andara_accounts_total",
			Help: "Active accounts holding each role. An account holding two roles counts under both.",
		}, []string{"role"}),
		AuditWriteFailures: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "andara_audit_write_failures_total",
			Help: "Audit records that could not be written to andara.audit.v1. Any value above zero is an operator page.",
		}),
		CharacterCreations: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "andara_character_creations_total",
			Help: "CreateCharacter calls by outcome.",
		}, []string{"outcome"}),
	}
	for _, o := range []string{CreationOK, CreationRosterFull, CreationNameTaken, CreationNameInvalid} {
		m.CharacterCreations.WithLabelValues(o)
	}
	for _, o := range []string{OutcomeOK, OutcomeBadCredential, OutcomeRateLimited, OutcomeDisabled} {
		m.Attempts.WithLabelValues(o)
	}
	for _, o := range []string{RedemptionOK, RedemptionInvalid, RedemptionRaceLost} {
		m.InviteRedemptions.WithLabelValues(o)
	}
	for _, mode := range []string{"closed", "invite", "open"} {
		m.Registrations.WithLabelValues(mode)
	}
	for _, a := range AllActions {
		m.PrivilegedActions.WithLabelValues(a)
	}
	for _, r := range AllRoles {
		m.Accounts.WithLabelValues(string(r))
	}
	if reg != nil {
		reg.MustRegister(m.Attempts, m.VerifyDuration, m.Registrations, m.InviteRedemptions,
			m.PrivilegedActions, m.Accounts, m.AuditWriteFailures, m.CharacterCreations)
	}
	return m
}
