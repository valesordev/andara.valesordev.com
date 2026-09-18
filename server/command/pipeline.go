// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package command

import (
	"context"
	"log/slog"
	"strconv"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	logv1 "github.com/valesordev/andara/gen/go/andara/log/v1"
	"github.com/valesordev/andara/server/auth"
	"github.com/valesordev/andara/server/sim"
)

// Binding is what a Session knows about its Character: which Entity acts
// and which Zone it is in, and therefore which Partition its Commands go
// to. It is Session state, not World state — the Gateway's routing view of
// where the Character was last seen — so authorize may read it.
type Binding struct {
	Actor sim.EntityID
	Zone  sim.ZoneID
}

// Binder answers "which Character is this Session bound to?". A Session
// bound to none may submit nothing (AC-7). How a binding is made and kept
// current across a cross-Zone move is AW-SRV-010's and AW-SRV-015's.
type Binder interface {
	Binding(sessionID string) (Binding, bool)
}

// BinderFunc adapts a func to Binder.
type BinderFunc func(sessionID string) (Binding, bool)

// Binding implements Binder.
func (f BinderFunc) Binding(id string) (Binding, bool) { return f(id) }

// Producer is the log. Produce appends one Command to its Zone's Partition
// and returns where it landed, durably (ADR-0002). AW-SRV-010 implements
// it over Kafka; a harness implements it over a slice.
type Producer interface {
	Produce(ctx context.Context, cmd *logv1.LoggedCommand) (Accepted, error)
}

// ProducerFunc adapts a func to Producer.
type ProducerFunc func(ctx context.Context, cmd *logv1.LoggedCommand) (Accepted, error)

// Produce implements Producer.
func (f ProducerFunc) Produce(ctx context.Context, cmd *logv1.LoggedCommand) (Accepted, error) {
	return f(ctx, cmd)
}

// Accepted is a Submit's answer: where the Command is in the log. It means
// accepted and ordered, not succeeded — the outcome arrives as an Event.
type Accepted struct {
	Partition int32
	Offset    int64
	Verb      string
}

// Clock is the pre-log clock, for the duration histogram; tests step it.
type Clock interface{ Now() time.Time }

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

// Pipeline is the pre-log half: parse, authorize, produce. One per Gateway.
type Pipeline struct {
	Table      *VerbTable
	MaxBytes   int
	Authorizer *auth.Authorizer
	Bindings   Binder
	Log        Producer
	Metrics    *Metrics
	Tracer     trace.Tracer
	Logger     *slog.Logger
	Clock      Clock
}

// Submit runs an Intent through parse and authorize and, if both pass,
// produces it. The stages run in order and a failure at one means the
// next did not run — asserted by test, not by convention.
//
// Nothing about a rejected Intent reaches the log: a parse failure is
// counted and returned; an authorize failure is counted, audited, and
// logged at info, since an unauthorized attempt is worth seeing at the
// default level. Raw Intent text is logged at debug only, escaped.
func (p *Pipeline) Submit(ctx context.Context, in Intent, principal auth.Principal) (Accepted, error) {
	tracer, logger, clock := p.Tracer, p.Logger, p.Clock
	if tracer == nil {
		tracer = noop.NewTracerProvider().Tracer("andara-server")
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	if clock == nil {
		clock = realClock{}
	}
	ctx = auth.WithSessionID(ctx, in.SessionID)
	ctx, span := tracer.Start(ctx, "command.execute", trace.WithAttributes(attribute.Bool("pre_log", true)))
	defer span.End()
	began := clock.Now()
	logger.LogAttrs(ctx, slog.LevelDebug, "intent received",
		slog.String("session_id", in.SessionID), slog.String("raw", strconv.Quote(in.Raw)), slog.String("trace_id", traceID(ctx)))

	// parse
	_, pspan := tracer.Start(ctx, "command.parse")
	cmd, verb, err := Parse(in, p.Table, p.MaxBytes)
	pspan.End()
	if err != nil {
		return Accepted{}, p.reject(ctx, span, in, verb, err)
	}
	span.SetAttributes(attribute.String("verb", verb))
	if p.Metrics != nil {
		p.Metrics.Commands.WithLabelValues(verb).Inc()
	}

	// authorize: the verb's role, then the Character binding — the one
	// rule this stage adds to auth.Authorizer (AC-7).
	actx, aspan := tracer.Start(ctx, "command.authorize")
	binding, err := p.authorize(actx, verb, principal, in.SessionID)
	aspan.End()
	if err != nil {
		return Accepted{}, p.reject(ctx, span, in, verb, err)
	}
	cmd.ZoneId = string(binding.Zone)
	cmd.ActorId = string(binding.Actor)
	cmd.AcceptedAtUnixNano = clock.Now().UnixNano()
	cmd.TraceId = traceParent(ctx)
	if p.Metrics != nil {
		p.Metrics.Duration.WithLabelValues(verb, PhasePreLog).Observe(clock.Now().Sub(began).Seconds())
	}

	// produce: the log boundary. From here the outcome is an Event.
	acc, err := p.Log.Produce(ctx, cmd)
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		return Accepted{}, err
	}
	acc.Verb = verb
	span.SetAttributes(attribute.Int64("partition", int64(acc.Partition)), attribute.Int64("offset", acc.Offset))
	logger.LogAttrs(ctx, slog.LevelDebug, "command accepted",
		slog.String("session_id", in.SessionID), slog.String("verb", verb),
		slog.Int64("partition", int64(acc.Partition)), slog.Int64("offset", acc.Offset), slog.String("trace_id", traceID(ctx)))
	return acc, nil
}

// authorize is the stage: role from the table via auth.Authorizer, then
// the binding. Both denials are audited; neither reads World state.
func (p *Pipeline) authorize(ctx context.Context, verb string, principal auth.Principal, sessionID string) (Binding, error) {
	if p.Authorizer != nil {
		if err := p.Authorizer.Authorize(ctx, verb, principal, sessionID); err != nil {
			return Binding{}, &Error{Stage: StageAuthorize, Code: CodeNotAuthorized, Detail: "you may not " + verb, err: err}
		}
	}
	b, ok := Binding{}, false
	if p.Bindings != nil {
		b, ok = p.Bindings.Binding(sessionID)
	}
	if !ok || b.Actor == "" || b.Zone == "" {
		if p.Authorizer != nil && p.Authorizer.Audit != nil {
			p.Authorizer.Audit.Record(ctx, auth.Entry{Actor: principal, Action: auth.ActionAuthorize, Target: verb, Outcome: auth.AuditDenied, Detail: "no character bound"})
		}
		return Binding{}, &Error{Stage: StageAuthorize, Code: CodeNotAuthorized, Detail: "you are not in the world", err: auth.ErrNotAuthorized}
	}
	return b, nil
}

// reject counts, logs, and marks a pre-log rejection and returns it.
func (p *Pipeline) reject(ctx context.Context, span trace.Span, in Intent, verb string, err error) error {
	e, ok := AsError(err)
	if !ok {
		e = &Error{Stage: StageParse, Code: CodeUnknownVerb, Detail: err.Error(), err: err}
	}
	if p.Metrics != nil {
		p.Metrics.Reject(e.Stage, e.Code)
	}
	span.SetAttributes(attribute.String("stage_failed", string(e.Stage)), attribute.String("code", e.Code))
	span.SetStatus(codes.Error, e.Code)
	level := slog.LevelDebug
	if e.Stage == StageAuthorize {
		level = slog.LevelInfo
	}
	if p.Logger != nil {
		p.Logger.LogAttrs(ctx, level, "command rejected",
			slog.String("session_id", in.SessionID), slog.String("verb", verb),
			slog.String("stage", string(e.Stage)), slog.String("code", e.Code), slog.String("detail", e.Detail),
			slog.String("trace_id", traceID(ctx)))
	}
	return e
}

// IsPreLog reports whether err is a pre-log rejection: nothing about the
// Intent reached the log.
func IsPreLog(err error) bool {
	e, ok := AsError(err)
	return ok && e.Stage.PreLog()
}

// traceParent renders the W3C traceparent for ctx's span, the form
// LoggedCommand.trace_id carries so the tick can continue the trace.
func traceParent(ctx context.Context) string {
	carrier := propagation.MapCarrier{}
	propagation.TraceContext{}.Inject(ctx, carrier)
	return carrier.Get("traceparent")
}

// ParentFrom rebuilds the remote span context a LoggedCommand.trace_id
// names, for the post-log span. An empty or malformed value yields ctx
// unchanged.
func ParentFrom(ctx context.Context, traceParentValue string) context.Context {
	if traceParentValue == "" {
		return ctx
	}
	return propagation.TraceContext{}.Extract(ctx, propagation.MapCarrier{"traceparent": traceParentValue})
}

func traceID(ctx context.Context) string {
	sc := trace.SpanFromContext(ctx).SpanContext()
	if !sc.HasTraceID() {
		return ""
	}
	return sc.TraceID().String()
}
