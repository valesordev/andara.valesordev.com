// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package command

import (
	"strconv"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/valesordev/andara/server/sim"
)

// Phase is the `phase` label on andara_command_duration_seconds.
const (
	PhasePreLog  = "pre_log"
	PhasePostLog = "post_log"
)

// Metrics is the command pipeline's instrumentation, both halves: the
// Gateway observes the pre-log side, the tick loop the post-log side, and
// both write the same series so a dashboard reads one pipeline.
//
// No label carries a Session ID, an Entity ID, a Room ID, or raw Intent
// text. `verb` is bounded by the verb table — an unknown verb is counted
// under its rejection code, never under what was typed — and `code` and
// `stage` are closed enums.
type Metrics struct {
	// Commands counts Commands that parsed, by verb.
	Commands *prometheus.CounterVec
	// Rejected counts rejections by stage and code; pre_log says which side
	// of the log the stage is on.
	Rejected *prometheus.CounterVec
	// Duration is stage time by verb and phase: parse+authorize pre-log,
	// validate+apply post-log. The gap between them is the log.
	Duration *prometheus.HistogramVec
}

// NewMetrics registers the pipeline metrics on reg (nil registers nothing)
// and pre-seeds every enumerable label so a dashboard sees zero rather
// than absence. verbs is the table's verb names.
func NewMetrics(reg prometheus.Registerer, verbs []string) *Metrics {
	m := &Metrics{
		Commands: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "andara_commands_total",
			Help: "Commands that parsed, by verb. Bounded by the verb table.",
		}, []string{"verb"}),
		Rejected: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "andara_command_rejected_total",
			Help: "Command rejections by pipeline stage and code; pre_log distinguishes the Gateway side from the tick side.",
		}, []string{"stage", "code", "pre_log"}),
		Duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "andara_command_duration_seconds",
			Help:    "Stage time per Command by verb: parse+authorize (pre_log) and validate+apply (post_log).",
			Buckets: []float64{0.00005, 0.0001, 0.00025, 0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05},
		}, []string{"verb", "phase"}),
	}
	for _, v := range verbs {
		m.Commands.WithLabelValues(v)
		m.Duration.WithLabelValues(v, PhasePreLog)
		m.Duration.WithLabelValues(v, PhasePostLog)
	}
	for stage, codes := range PreLogCodes {
		for _, c := range codes {
			m.Rejected.WithLabelValues(string(stage), c, "true")
		}
	}
	for _, c := range sim.RejectCodes() {
		m.Rejected.WithLabelValues(sim.StageValidate, c, "false")
		m.Rejected.WithLabelValues(sim.StageApply, c, "false")
	}
	m.Rejected.WithLabelValues(sim.StageFault, sim.CodeZoneFaulted, "false")
	if reg != nil {
		reg.MustRegister(m.Commands, m.Rejected, m.Duration)
	}
	return m
}

// Reject counts one rejection.
func (m *Metrics) Reject(stage Stage, code string) {
	if m == nil {
		return
	}
	m.Rejected.WithLabelValues(string(stage), code, strconv.FormatBool(stage.PreLog())).Inc()
}
