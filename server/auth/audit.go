// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package auth

import (
	"context"
	"log/slog"
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
)

// AllActions lists every audited action, for metric pre-seeding.
var AllActions = []string{
	ActionCreateAccount, ActionResetPassword, ActionSetRoles, ActionSetAccountStatus,
	ActionIssueInvite, ActionRevokeInvite, ActionSetRegistrationMode, ActionCreateAgentAccount,
	ActionRedeemInvite, ActionRefreshRevoked, ActionRevokeRefresh, ActionActAs,
	ActionAuthorize, ActionBind,
}

// Audit outcomes.
const (
	AuditOK       = "ok"
	AuditDenied   = "denied"
	AuditConflict = "conflict"
)

// Auditor writes andara.audit.v1 records, keyed by actor.
type Auditor struct {
	log     recordlog.Log
	slog    *slog.Logger
	metrics *Metrics
	now     func() time.Time
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
	if err == nil {
		err = a.log.Append(ctx, rec.ActorAccountId, body)
	}
	if err != nil {
		a.metrics.AuditWriteFailures.Inc()
		a.slog.LogAttrs(ctx, slog.LevelError, "audit record not written",
			slog.String("action", rec.Action),
			slog.String("actor_account_id", rec.ActorAccountId),
			slog.String("detail", err.Error()),
			slog.String("trace_id", rec.TraceId),
		)
	}
}

func traceID(ctx context.Context) string {
	sc := trace.SpanFromContext(ctx).SpanContext()
	if !sc.HasTraceID() {
		return ""
	}
	return sc.TraceID().String()
}
