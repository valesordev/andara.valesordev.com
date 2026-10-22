// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package kafkaclient

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// The reasons an authentication or authorization failure is counted and
// logged under (the counter's reason label).
const (
	// ReasonSASLFailed: the broker refused the SCRAM handshake.
	ReasonSASLFailed = "sasl_failed"
	// ReasonTopicUnauthorized: the principal lacks the ACL for the topic.
	ReasonTopicUnauthorized = "topic_unauthorized"
	// ReasonPasswordFile: a new connection found the password file missing,
	// unreadable or empty.
	ReasonPasswordFile = "password_file"
)

var reasons = []string{ReasonSASLFailed, ReasonTopicUnauthorized, ReasonPasswordFile}

// AuthError is every authentication or authorization failure the package
// surfaces (AC-8). It carries the principal's name and never the password.
type AuthError struct {
	Reason    string // sasl_failed, topic_unauthorized or password_file
	ClientID  string
	Principal string // the username, or "none"
	Topic     string // topic_unauthorized only
	Err       error  // the broker's or the file's error
}

func (e *AuthError) Error() string {
	on := ""
	if e.Topic != "" {
		on = " on " + e.Topic
	}
	return fmt.Sprintf("kafka %s: client %s as %s%s: %v", e.Reason, e.ClientID, e.Principal, on, e.Err)
}

func (e *AuthError) Unwrap() error { return e.Err }

// ErrorName is the span's error_name for err: the reason of an *AuthError,
// or "" for any other error.
func ErrorName(err error) string {
	var ae *AuthError
	if errors.As(err, &ae) {
		return ae.Reason
	}
	return ""
}

// Refused reports whether err is the broker refusing the principal a topic
// (topic_unauthorized): a refusal that holds until an ACL changes, which a
// reader stops on or backs off from. A password_file or sasl_failed is a
// connection's failure, which the client retries on its own (AC-6a), and is
// not a refusal.
func Refused(err error) bool { return ErrorName(err) == ReasonTopicUnauthorized }

// SpanError marks span as failed with err and, when err is an authentication
// or authorization failure, adds error_name = its reason (the Observability
// requirements' trace rule). The principal is never an attribute.
func SpanError(span trace.Span, err error) {
	span.SetStatus(codes.Error, err.Error())
	if name := ErrorName(err); name != "" {
		span.SetAttributes(attribute.String("error_name", name))
	}
}

// lineEvery is the most often one client ID's failures of one reason are
// logged; the rest are counted into the next line's suppressed.
const lineEvery = 60 * time.Second

// Metrics is the authentication-failure counter and the process's state for
// the lines about it: which client IDs have logged their config and when each
// client ID and reason last logged a failure.
type Metrics struct {
	Failures *prometheus.CounterVec

	now func() time.Time

	mu     sync.Mutex
	config map[string]bool
	lines  map[[2]string]*line
}

type line struct {
	at         time.Time
	suppressed int
}

// NewMetrics registers andara_kafka_client_auth_failures_total once per
// process; a nil reg registers nothing.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		Failures: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "andara_kafka_client_auth_failures_total",
			Help: "Kafka authentication and authorization failures, by client and reason (sasl_failed, topic_unauthorized, password_file).",
		}, []string{"client_id", "reason"}),
		now:    time.Now,
		config: map[string]bool{},
		lines:  map[[2]string]*line{},
	}
	if reg != nil {
		reg.MustRegister(m.Failures)
	}
	return m
}

// zero makes the client ID's three series exist at 0, so increase() sees the
// first failure.
func (m *Metrics) zero(clientID string) {
	if m == nil {
		return
	}
	for _, r := range reasons {
		m.Failures.WithLabelValues(clientID, r)
	}
}

func (m *Metrics) count(clientID, reason string) {
	if m == nil {
		return
	}
	m.Failures.WithLabelValues(clientID, reason).Inc()
}

// firstConfig is true the first time a client ID asks: its config line is
// written once per process. Without Metrics there is no process state, and
// every client logs it.
func (m *Metrics) firstConfig(clientID string) bool {
	if m == nil {
		return true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.config[clientID] {
		return false
	}
	m.config[clientID] = true
	return true
}

// allowLine is whether a failure of reason on clientID gets an error line now,
// and how many were suppressed since the last one. Without Metrics every
// failure is logged.
func (m *Metrics) allowLine(clientID, reason string) (suppressed int, ok bool) {
	if m == nil {
		return 0, true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	k := [2]string{clientID, reason}
	l := m.lines[k]
	if l == nil {
		m.lines[k] = &line{at: now}
		return 0, true
	}
	if now.Sub(l.at) < lineEvery {
		l.suppressed++
		return 0, false
	}
	suppressed = l.suppressed
	l.at, l.suppressed = now, 0
	return suppressed, true
}
