// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package ingress

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	gamev1 "github.com/valesordev/andara/gen/go/andara/game/v1"
	"github.com/valesordev/andara/server/auth"
	"github.com/valesordev/andara/server/command"
	"github.com/valesordev/andara/server/gateway"
)

// Options configures an Ingress.
type Options struct {
	// Pipeline is the pre-log half (AW-SRV-003) with its Log set to the
	// producer. Its Bindings should be the Bindings below.
	Pipeline *command.Pipeline
	// Bindings is the routing table; nil disables Session cleanup of it.
	Bindings *Bindings
	// RateLimit and Burst are the per-Session token bucket; AgentRateLimit
	// replaces the rate for agent Principals (ADR-0005). A zero rate
	// disables limiting.
	RateLimit      auth.RateLimit
	AgentRateLimit auth.RateLimit
	Burst          int
	// MaxPending bounds one Session's Submits in flight, and the
	// idempotency keys it may hold. Zero means 256.
	MaxPending int
	// IdempotencyWindow is how long a Submit's outcome is remembered
	// against its (Session, client_ref) (AW-SRV-031). Zero means 30s.
	IdempotencyWindow time.Duration

	Metrics *Metrics
	Log     *slog.Logger
	Tracer  trace.Tracer
	Now     func() time.Time
}

// Ingress implements gateway.Ingress.
type Ingress struct {
	opts     Options
	pipe     *command.Pipeline
	metrics  *Metrics
	limiter  *auth.Limiter
	agents   *auth.Limiter
	log      *slog.Logger
	tracer   trace.Tracer
	warnAt   atomic.Int64
	mu       sync.Mutex
	sessions map[string]*session
}

// session is one Session's ingress state: how many Submits it has in
// flight, the tail of the queue they wait in, and the idempotency keys
// it holds. Each Submit waits for the one before it, so a Session's
// Commands reach the log in the order its Submits arrived (AC-2),
// whatever goroutine each ran on.
type session struct {
	mu      sync.Mutex
	pending int
	tail    chan struct{}
	keys    table
}

type turn struct {
	prev <-chan struct{}
	done chan struct{}
}

// New builds an Ingress.
func New(o Options) *Ingress {
	if o.MaxPending <= 0 {
		o.MaxPending = 256
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.IdempotencyWindow <= 0 {
		o.IdempotencyWindow = 30 * time.Second
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Tracer == nil {
		o.Tracer = noop.NewTracerProvider().Tracer("andara-server")
	}
	if o.Metrics == nil {
		o.Metrics = NewMetrics(nil)
	}
	return &Ingress{
		opts:     o,
		pipe:     o.Pipeline,
		metrics:  o.Metrics,
		limiter:  auth.NewLimiter(o.RateLimit, o.Burst, o.Now),
		agents:   auth.NewLimiter(o.AgentRateLimit, o.Burst, o.Now),
		log:      o.Log,
		tracer:   o.Tracer,
		sessions: map[string]*session{},
	}
}

// Metrics returns the ingress metrics.
func (i *Ingress) Metrics() *Metrics { return i.metrics }

// Submit implements gateway.Ingress: the gateway has resolved the Session
// and bounded the call.
func (i *Ingress) Submit(ctx context.Context, s *gateway.Session, req *gamev1.SubmitRequest) (*gamev1.SubmitResponse, error) {
	var ended <-chan struct{}
	if life := s.Context(); life != nil {
		ended = life.Done()
	}
	return i.submit(ctx, s.ID, s.Principal, ended, req)
}

// submit is Submit on the Session's parts: its ID and Principal, and the
// channel closed when it ends, nil for none.
func (i *Ingress) submit(ctx context.Context, sessionID string, principal auth.Principal, ended <-chan struct{}, req *gamev1.SubmitRequest) (*gamev1.SubmitResponse, error) {
	// Rate limit before parse: a flood is refused before it costs anything.
	lim := i.limiter
	if principal.Has(auth.RoleAgent) {
		lim = i.agents
	}
	if !lim.Allow(sessionID) {
		i.metrics.Submits.WithLabelValues(OutcomeRateLimited).Inc()
		i.warn(ctx, "session rate limited", sessionID)
		return nil, connectError(ErrRateLimited)
	}
	st := i.session(sessionID, ended)

	// The idempotency key (AW-SRV-031): a known client_ref is answered
	// with its outcome — waiting for it if the original is still in
	// flight — without queueing or running anything. An empty ref is not
	// deduplicated.
	var e *entry
	for ref := req.GetClientRef(); ref != ""; {
		var hit bool
		var err error
		e, hit, err = i.lookup(st, ref, req.GetRaw())
		if err != nil {
			i.metrics.Submits.WithLabelValues(outcomeOf(err)).Inc()
			if errors.Is(err, ErrPendingFull) {
				i.warn(ctx, "session pending queue full", sessionID)
			} else {
				i.warn(ctx, "client_ref reused for a different command", sessionID)
			}
			return nil, connectError(err)
		}
		if !hit {
			break
		}
		resp, again, err := i.await(ctx, sessionID, ref, e)
		if again {
			continue
		}
		return resp, err
	}

	t, ok := st.enter(i.opts.MaxPending)
	if !ok {
		i.resolve(st, e, nil, ErrPendingFull, false)
		i.metrics.Submits.WithLabelValues(OutcomePendingFull).Inc()
		i.warn(ctx, "session pending queue full", sessionID)
		return nil, connectError(ErrPendingFull)
	}
	i.metrics.Pending.Inc()
	defer i.metrics.Pending.Dec()

	// Our turn: behind every earlier Submit of this Session.
	select {
	case <-t.prev:
	case <-ctx.Done():
		st.leaveAfter(t)
		i.resolve(st, e, nil, ctx.Err(), false)
		i.metrics.Submits.WithLabelValues(outcomeOf(ctx.Err())).Inc()
		return nil, connectError(ctx.Err())
	}
	acc, err := i.pipe.Submit(ctx, command.Intent{SessionID: sessionID, Raw: req.GetRaw(), ClientRef: req.GetClientRef()}, principal)
	st.leave(t)
	i.metrics.Submits.WithLabelValues(outcomeOf(err)).Inc()
	if err != nil {
		i.settle(st, e, err)
		return nil, connectError(err)
	}
	resp := &gamev1.SubmitResponse{AcceptedOffset: acc.Offset, Partition: acc.Partition}
	i.resolve(st, e, resp, nil, true)
	return resp, nil
}

// lookup finds or makes the key's entry, keeping the gauge current.
func (i *Ingress) lookup(st *session, ref, raw string) (*entry, bool, error) {
	st.mu.Lock()
	e, hit, n, err := st.keys.lookup(ref, raw, i.opts.Now(), i.opts.IdempotencyWindow, i.opts.MaxPending)
	st.mu.Unlock()
	i.metrics.IdempotencyKeys.Add(float64(n))
	return e, hit, err
}

// await is a dedup hit: wait for the entry's outcome, bounded by the
// caller, and return it. again reports that the outcome was transient
// and not kept, so the caller should run the Command itself.
func (i *Ingress) await(ctx context.Context, sessionID, ref string, e *entry) (resp *gamev1.SubmitResponse, again bool, err error) {
	ctx, span := i.tracer.Start(ctx, "command.execute", trace.WithAttributes(attribute.Bool("pre_log", true), attribute.Bool("deduplicated", true)))
	defer span.End()
	select {
	case <-e.done:
	case <-ctx.Done():
		span.SetStatus(codes.Error, ctx.Err().Error())
		i.metrics.Submits.WithLabelValues(outcomeOf(ctx.Err())).Inc()
		return nil, false, connectError(ctx.Err())
	}
	if !e.kept {
		span.SetAttributes(attribute.Bool("deduplicated", false))
		return nil, true, nil
	}
	i.metrics.Submits.WithLabelValues(OutcomeDeduplicated).Inc()
	i.log.LogAttrs(ctx, slog.LevelDebug, "submit deduplicated",
		slog.String("session_id", sessionID), slog.String("client_ref", ref), slog.String("trace_id", traceID(ctx)))
	if e.err != nil {
		span.SetStatus(codes.Error, e.err.Error())
		return nil, false, connectError(e.err)
	}
	return e.resp, false, nil
}

// settle resolves the entry from a pipeline error. A rejection of the
// Intent is the Command's fate and is kept; a transient refusal is not.
// An Unsettled produce — the deadline passed with the record live — keeps
// the entry open until the producer knows the record's fate: landed, and
// a retry gets the offset; not written, and a retry is a new Command;
// still unknown, and a retry inside the window is told so again.
func (i *Ingress) settle(st *session, e *entry, err error) {
	if e == nil {
		return
	}
	var u *Unsettled
	if errors.As(err, &u) {
		go func() {
			<-u.Settled()
			acc, ferr := u.Outcome()
			switch {
			case ferr == nil:
				i.resolve(st, e, &gamev1.SubmitResponse{AcceptedOffset: acc.Offset, Partition: acc.Partition}, nil, true)
			case errors.Is(ferr, ErrNotWritten):
				i.resolve(st, e, nil, ferr, false)
			default:
				i.resolve(st, e, nil, ferr, true)
			}
		}()
		return
	}
	if ce, ok := command.AsError(err); ok && ce.Code != command.CodeInTransit {
		i.resolve(st, e, nil, err, true)
		return
	}
	i.resolve(st, e, nil, err, false)
}

// resolve records an entry's outcome; nil e is a Submit without a key.
func (i *Ingress) resolve(st *session, e *entry, resp *gamev1.SubmitResponse, err error, kept bool) {
	if e == nil {
		return
	}
	st.mu.Lock()
	n := st.keys.resolve(e, resp, err, kept, i.opts.Now())
	st.mu.Unlock()
	i.metrics.IdempotencyKeys.Add(float64(n))
}

// session finds or creates the Session's state. The first sight of a
// Session registers its cleanup: when it ends, its queue, its bucket,
// and its binding go with it.
func (i *Ingress) session(id string, ended <-chan struct{}) *session {
	i.mu.Lock()
	defer i.mu.Unlock()
	if st, ok := i.sessions[id]; ok {
		return st
	}
	closed := make(chan struct{})
	close(closed)
	st := &session{tail: closed}
	i.sessions[id] = st
	if ended != nil {
		go func() {
			<-ended
			i.forget(id)
		}()
	}
	return st
}

// forget drops everything the ingress holds for a Session.
func (i *Ingress) forget(id string) {
	i.mu.Lock()
	st := i.sessions[id]
	delete(i.sessions, id)
	i.mu.Unlock()
	if st != nil {
		st.mu.Lock()
		n := st.keys.forget()
		st.mu.Unlock()
		i.metrics.IdempotencyKeys.Add(float64(n))
	}
	i.limiter.Forget(id)
	i.agents.Forget(id)
	if i.opts.Bindings != nil {
		i.opts.Bindings.Unbind(id)
	}
}

// enter takes a place in the queue, or reports the queue full.
func (s *session) enter(max int) (*turn, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending >= max {
		return nil, false
	}
	s.pending++
	t := &turn{prev: s.tail, done: make(chan struct{})}
	s.tail = t.done
	return t, true
}

// leave gives up the place; the next Submit may proceed.
func (s *session) leave(t *turn) {
	close(t.done)
	s.mu.Lock()
	s.pending--
	s.mu.Unlock()
}

// leaveAfter is leave for a Submit that gave up before its turn: its
// place closes only once the one before it has, so the order behind it
// holds.
func (s *session) leaveAfter(t *turn) {
	s.mu.Lock()
	s.pending--
	s.mu.Unlock()
	go func() {
		<-t.prev
		close(t.done)
	}()
}

// warn logs at warn, at most once a second across the process, with the
// Session — the volume is the point of the limit.
func (i *Ingress) warn(ctx context.Context, msg, sessionID string) {
	now := i.opts.Now().UnixNano()
	if last := i.warnAt.Load(); now-last < int64(time.Second) || !i.warnAt.CompareAndSwap(last, now) {
		return
	}
	i.log.LogAttrs(ctx, slog.LevelWarn, msg, slog.String("session_id", sessionID), slog.String("trace_id", traceID(ctx)))
}

func traceID(ctx context.Context) string {
	sc := trace.SpanFromContext(ctx).SpanContext()
	if !sc.HasTraceID() {
		return ""
	}
	return sc.TraceID().String()
}

var _ gateway.Ingress = (*Ingress)(nil)
