// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package content

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/valesordev/andara/server/sim"
)

// Publish outcomes, the values of andara_content_publishes_total{outcome}.
const (
	PublishOK          = "ok"
	PublishRejected    = "rejected"
	PublishDenied      = "denied"
	PublishTooLarge    = "too_large"
	PublishStaleParent = "stale_parent"
)

// Approval outcomes, the values of andara_content_approvals_total{outcome}.
// self is a refused self-approval; self_operator is an Operator approving
// their own build (AC-13), never folded into ok.
const (
	ApprovalOK           = "ok"
	ApprovalSelf         = "self"
	ApprovalDenied       = "denied"
	ApprovalSelfOperator = "self_operator"
)

// Pointer move directions.
const (
	DirectionForward  = "forward"
	DirectionRollback = "rollback"
)

// RefusedUnapproved is the activation refusal that isn't the Loader's.
const RefusedUnapproved = "unapproved"

// PublishMetrics is the publish path's domain outcomes (AW-SRV-013), which the
// Gateway's RED metrics can't tell apart. Pack ID is not a label on any of
// them: publishes are rare, and the audit topic answers "which pack". Every
// label set is closed and pre-seeded at 0.
type PublishMetrics struct {
	Publishes          *prometheus.CounterVec
	Approvals          *prometheus.CounterVec
	PointerMoves       *prometheus.CounterVec
	ActivationsRefused *prometheus.CounterVec
	BlobBytes          prometheus.Counter
	ValidationFailures *prometheus.CounterVec
}

// NewPublishMetrics registers the publish path's metrics on reg (nil
// registers nothing).
func NewPublishMetrics(reg prometheus.Registerer) *PublishMetrics {
	m := &PublishMetrics{
		Publishes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "andara_content_publishes_total", Help: "PublishVersion calls, by outcome.",
		}, []string{"outcome"}),
		Approvals: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "andara_content_approvals_total", Help: "ApproveVersion calls, by outcome; self_operator is an Operator approving their own publish.",
		}, []string{"outcome"}),
		PointerMoves: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "andara_content_pointer_moves_total", Help: "Active Pointer moves, by direction and whether approval was overridden.",
		}, []string{"direction", "override"}),
		ActivationsRefused: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "andara_content_activations_refused_total", Help: "Activations refused before the pointer moved, by reason.",
		}, []string{"reason"}),
		BlobBytes: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "andara_content_blob_bytes_total", Help: "Blob bytes accepted into the content store, after deduplication.",
		}),
		ValidationFailures: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "andara_content_validation_failures_total", Help: "Refusing findings at publish, by code.",
		}, []string{"code"}),
	}
	for _, o := range []string{PublishOK, PublishRejected, PublishDenied, PublishTooLarge, PublishStaleParent} {
		m.Publishes.WithLabelValues(o)
	}
	for _, o := range []string{ApprovalOK, ApprovalSelf, ApprovalDenied, ApprovalSelfOperator} {
		m.Approvals.WithLabelValues(o)
	}
	for _, d := range []string{DirectionForward, DirectionRollback} {
		for _, o := range []string{"true", "false"} {
			m.PointerMoves.WithLabelValues(d, o)
		}
	}
	for _, r := range []string{RefusedUnapproved, RefusalZoneRemoved, RefusalSpawnRoomRemoved, RefusalCoreVersion} {
		m.ActivationsRefused.WithLabelValues(r)
	}
	for _, c := range sim.AllErrCodes {
		m.ValidationFailures.WithLabelValues(string(c))
	}
	if reg != nil {
		reg.MustRegister(m.Publishes, m.Approvals, m.PointerMoves, m.ActivationsRefused, m.BlobBytes, m.ValidationFailures)
	}
	return m
}
