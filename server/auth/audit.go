// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package auth

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/trace"
	"google.golang.org/protobuf/proto"

	auditv1 "github.com/valesordev/andara/gen/go/andara/audit/v1"
	"github.com/valesordev/andara/server/recordlog"
)

// Audited actions. The list bounds andara_privileged_actions_total{action}.
const (
	ActionCreateAccount       = "create_account"
	ActionResetPassword       = "reset_password"
	ActionSetRoles            = "set_roles"
	ActionSetAccountStatus    = "set_account_status"
	ActionIssueInvite         = "issue_invite"
	ActionRevokeInvite        = "revoke_invite"
	ActionSetRegistrationMode = "set_registration_mode"
	ActionCreateAgentAccount  = "create_agent_account"
	ActionRedeemInvite        = "redeem_invite"
	ActionRefreshRevoked      = "refresh_revoked"
	ActionRevokeRefresh       = "revoke_refresh"
	ActionActAs               = "act_as"
	ActionAuthorize           = "authorize"
	ActionBind                = "bind"
	// ActionSubscribeWorld: a World-visibility Event subscription — a
	// privileged read of everything that happens (AW-SRV-004 AC-8).
	ActionSubscribeWorld = "subscribe_world"
)

// AllActions lists every audited action, for metric pre-seeding.
var AllActions = []string{
	ActionCreateAccount, ActionResetPassword, ActionSetRoles, ActionSetAccountStatus,
	ActionIssueInvite, ActionRevokeInvite, ActionSetRegistrationMode, ActionCreateAgentAccount,
	ActionRedeemInvite, ActionRefreshRevoked, ActionRevokeRefresh, ActionActAs,
	ActionAuthorize, ActionBind, ActionSubscribeWorld,
}

// Audit outcomes.
const (
	AuditOK       = "ok"
	AuditDenied   = "denied"
	AuditConflict = "conflict"
)

// AuditWriteTimeout is how long Record waits for the write before
// returning with it still in flight. A produce normally takes
// milliseconds.
const AuditWriteTimeout = 2 * time.Second

// Auditor writes andara.audit.v1 records, keyed by actor.
type Auditor struct {
	log     recordlog.Log
	slog    *slog.Logger
	metrics *Metrics
	now     func() time.Time
	// WriteTimeout overrides AuditWriteTimeout; zero means the default.
	WriteTimeout  time.Duration
	pendingWarnAt atomic.Int64
}

// NewAuditor builds an Auditor over an audit log outside a Store, for the
// command pipeline's tests and harnesses (AW-SRV-003): the Authorizer it
// calls audits every denial, so an Authorizer needs one even where no
// Account store exists. metrics may be nil.
func NewAuditor(log recordlog.Log, logger *slog.Logger, metrics *Metrics, now func() time.Time) *Auditor {
	if metrics == nil {
		metrics = NewMetrics(nil)
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	if now == nil {
		now = time.Now
	}
	return &Auditor{log: log, slog: logger, metrics: metrics, now: now}
}

// Entry is one privileged action to record. Session and trace IDs are read
// from ctx.
type Entry struct {
	Actor   Principal
	Action  string
	Target  string
	Outcome string
	Detail  string
}

// Record writes one audit record and one info log line. A write failure is
// logged at error and counted; the action it describes has already happened
// and hiding that would not help anyone.
func (a *Auditor) Record(ctx context.Context, e Entry) {
	rec := &auditv1.AuditRecord{
		ActorAccountId:    e.Actor.AccountID,
		ActingAsAccountId: e.Actor.ActingAs,
		Action:            e.Action,
		Target:            e.Target,
		Outcome:           e.Outcome,
		SessionId:         SessionIDFrom(ctx),
		TraceId:           traceID(ctx),
		TsUnixNano:        a.now().UnixNano(),
		Detail:            e.Detail,
	}
	a.metrics.PrivilegedActions.WithLabelValues(e.Action).Inc()
	a.slog.LogAttrs(ctx, slog.LevelInfo, "privileged action",
		slog.String("actor_account_id", rec.ActorAccountId),
		slog.String("acting_as_account_id", rec.ActingAsAccountId),
		slog.String("action", rec.Action),
		slog.String("target", rec.Target),
		slog.String("outcome", rec.Outcome),
		slog.String("session_id", rec.SessionId),
		slog.String("trace_id", rec.TraceId),
	)
	body, err := proto.Marshal(rec)
	if err != nil {
		a.failed(ctx, rec, err)
		return
	}
	// The write is waited on for AuditWriteTimeout and no longer. With the
	// broker gone the idempotent producer keeps the record until a broker
	// returns — minutes, perhaps — and the player whose Command was
	// refused must not wait with it (AW-SRV-010 AC-5). The record is not
	// dropped: the write finishes in the background and a failure is
	// counted and logged then.
	done := make(chan error, 1)
	bg := context.WithoutCancel(ctx)
	go func() { done <- a.log.Append(bg, rec.ActorAccountId, body) }()
	timeout := a.WriteTimeout
	if timeout <= 0 {
		timeout = AuditWriteTimeout
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-done:
		if err != nil {
			a.failed(ctx, rec, err)
		}
	case <-timer.C:
		// One line a second: during a broker outage every denial is late.
		now := a.now().UnixNano()
		if last := a.pendingWarnAt.Load(); now-last >= int64(time.Second) && a.pendingWarnAt.CompareAndSwap(last, now) {
			a.slog.LogAttrs(ctx, slog.LevelWarn, "audit record write pending past its timeout; it completes in the background",
				slog.String("action", rec.Action),
				slog.String("actor_account_id", rec.ActorAccountId),
				slog.String("trace_id", rec.TraceId),
			)
		}
		go func() {
			if err := <-done; err != nil {
				a.failed(bg, rec, err)
			}
		}()
	}
}

// failed counts and logs an audit record that was not written.
func (a *Auditor) failed(ctx context.Context, rec *auditv1.AuditRecord, err error) {
	a.metrics.AuditWriteFailures.Inc()
	a.slog.LogAttrs(ctx, slog.LevelError, "audit record not written",
		slog.String("action", rec.Action),
		slog.String("actor_account_id", rec.ActorAccountId),
		slog.String("detail", err.Error()),
		slog.String("trace_id", rec.TraceId),
	)
}

func traceID(ctx context.Context) string {
	sc := trace.SpanFromContext(ctx).SpanContext()
	if !sc.HasTraceID() {
		return ""
	}
	return sc.TraceID().String()
}
