// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package ingress

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/trace"

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
	// MaxPending bounds one Session's Submits in flight. Zero means 256.
	MaxPending int

	Metrics *Metrics
	Log     *slog.Logger
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
	warnAt   atomic.Int64
	mu       sync.Mutex
	sessions map[string]*session
}

// session is one Session's ingress state: how many Submits it has in
// flight, and the tail of the queue they wait in. Each Submit waits for
// the one before it, so a Session's Commands reach the log in the order
// its Submits arrived (AC-2), whatever goroutine each ran on.
type session struct {
	mu      sync.Mutex
	pending int
	tail    chan struct{}
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
	if o.Now == nil {
		o.Now = time.Now
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
	t, ok := st.enter(i.opts.MaxPending)
	if !ok {
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
		i.metrics.Submits.WithLabelValues(outcomeOf(ctx.Err())).Inc()
		return nil, connectError(ctx.Err())
	}
	acc, err := i.pipe.Submit(ctx, command.Intent{SessionID: sessionID, Raw: req.GetRaw(), ClientRef: req.GetClientRef()}, principal)
	st.leave(t)
	i.metrics.Submits.WithLabelValues(outcomeOf(err)).Inc()
	if err != nil {
		return nil, connectError(err)
	}
	return &gamev1.SubmitResponse{AcceptedOffset: acc.Offset, Partition: acc.Partition}, nil
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
	delete(i.sessions, id)
	i.mu.Unlock()
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
