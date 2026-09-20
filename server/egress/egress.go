// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package egress

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"
	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	gamev1 "github.com/valesordev/andara/gen/go/andara/game/v1"
	"github.com/valesordev/andara/server/auth"
	"github.com/valesordev/andara/server/events"
	"github.com/valesordev/andara/server/gateway"
	"github.com/valesordev/andara/server/sim"
)

// Observers answers where a Session perceives from: the Entity it drives
// and the Room that Entity was last seen in. The routing table
// (ingress.Bindings) is the production answer; a Session it does not know
// perceives from nowhere and receives only what is addressed to everyone.
type Observers interface {
	Observer(sessionID string) (events.Observer, bool)
}

// ObserverFunc adapts a func to Observers.
type ObserverFunc func(sessionID string) (events.Observer, bool)

// Observer implements Observers.
func (f ObserverFunc) Observer(id string) (events.Observer, bool) { return f(id) }

// Sender is where a stream writes: *connect.ServerStream[EventEnvelope],
// or a test's fake, including one whose Send never returns.
type Sender interface {
	Send(*gamev1.EventEnvelope) error
}

// Options configures an Egress.
type Options struct {
	// Hub is the fan-out every Session subscribes to. Required.
	Hub *events.Hub
	// Observers is where each Session perceives from. Nil means nowhere.
	Observers Observers
	// Buffer is egress.buffer: Events a stream may leave unsent before it
	// is ended. ResumeWindow is egress.resume_window: Events retained per
	// Session for a resume, at least Buffer.
	Buffer       int
	ResumeWindow int
	// HeartbeatInterval is egress.heartbeat_interval.
	HeartbeatInterval time.Duration
	// Abort ends the stream a ctx belongs to from another goroutine, so
	// a Send blocked on a client that stopped consuming returns; if it
	// has not within HeartbeatInterval the client has stopped reading its
	// socket, and Disconnect closes its connection. Default to
	// gateway.AbortStream and gateway.DropConnection; a test's fake
	// Sender needs its own.
	Abort      func(ctx context.Context) bool
	Disconnect func(ctx context.Context) bool
	// LastTick is what a Heartbeat reports; defaults to the Hub's.
	LastTick func() uint64

	Log      *slog.Logger
	Tracer   trace.Tracer
	Registry prometheus.Registerer
}

// Egress implements gateway.Egress over an events.Hub.
type Egress struct {
	opts    Options
	log     *slog.Logger
	tracer  trace.Tracer
	metrics *Metrics
	// draining is set by Drain: a stream that ends after it ended because
	// the server is going away, whatever the ctx says.
	draining atomic.Bool

	mu       sync.Mutex
	sessions map[string]*session
}

// New builds an Egress. Subscribe streams from opts.Hub from now on.
func New(opts Options) *Egress {
	if opts.Hub == nil {
		panic("egress: Hub is required")
	}
	if opts.Buffer <= 0 {
		opts.Buffer = 1024
	}
	if opts.ResumeWindow < opts.Buffer {
		opts.ResumeWindow = opts.Buffer
	}
	if opts.HeartbeatInterval <= 0 {
		opts.HeartbeatInterval = 20 * time.Second
	}
	if opts.Abort == nil {
		opts.Abort = gateway.AbortStream
	}
	if opts.Disconnect == nil {
		opts.Disconnect = gateway.DropConnection
	}
	if opts.LastTick == nil {
		hub := opts.Hub
		opts.LastTick = func() uint64 { return uint64(hub.LastTick()) }
	}
	if opts.Log == nil {
		opts.Log = slog.New(slog.DiscardHandler)
	}
	if opts.Tracer == nil {
		opts.Tracer = noop.NewTracerProvider().Tracer("andara-server")
	}
	return &Egress{
		opts:     opts,
		log:      opts.Log,
		tracer:   opts.Tracer,
		metrics:  NewMetrics(opts.Registry),
		sessions: map[string]*session{},
	}
}

// Metrics exposes the stream metrics, for tests.
func (e *Egress) Metrics() *Metrics { return e.metrics }

// Drain marks the server as going away: every stream that ends from here
// on is counted as draining, not as a client that left. The gateway calls
// it from OnDrain, before it closes any Session.
func (e *Egress) Drain() { e.draining.Store(true) }

// Sessions is how many Sessions hold retained history, for tests.
func (e *Egress) Sessions() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.sessions)
}

// Subscribe implements gateway.Egress: the stream runs until ctx is done,
// the Session's fan-out subscription ends, or the client trails by more
// than egress.buffer.
func (e *Egress) Subscribe(ctx context.Context, s *gateway.Session, req *gamev1.SubscribeRequest, stream *connect.ServerStream[gamev1.EventEnvelope]) error {
	return e.SubscribeWith(ctx, s, req, stream)
}

// SubscribeWith is Subscribe on any Sender: a harness's, or a test's.
func (e *Egress) SubscribeWith(ctx context.Context, s *gateway.Session, req *gamev1.SubscribeRequest, out Sender) error {
	var ended <-chan struct{}
	if life := s.Context(); life != nil {
		ended = life.Done()
	}
	return e.subscribe(ctx, s.ID, s.Principal, ended, req, out)
}

// subscribe is Subscribe on the Session's parts: its ID and Principal,
// and the channel closed when it ends, nil for none.
func (e *Egress) subscribe(ctx context.Context, sessionID string, principal auth.Principal, ended <-chan struct{}, req *gamev1.SubscribeRequest, out Sender) error {
	span := trace.SpanFromContext(ctx)
	span.SetAttributes(attribute.Bool("stream.world", req.GetWorld()), attribute.Int64("stream.last_event_id", int64(req.GetLastEventId())))
	sess, err := e.session(ctx, sessionID, principal, ended, req.GetWorld())
	if err != nil {
		return connectError(err)
	}
	st, resync, err := sess.attach(ctx, req.GetLastEventId())
	if err != nil {
		return connectError(err)
	}
	e.metrics.Streams.Inc()
	defer e.metrics.Streams.Dec()
	if resync != "" {
		span.SetAttributes(attribute.String("stream.resync", resync))
		e.metrics.Resyncs.WithLabelValues(resync).Inc()
		e.log.LogAttrs(ctx, slog.LevelInfo, "stream resync: resume point not retained",
			slog.String("session_id", sessionID), slog.Uint64("last_event_id", req.GetLastEventId()),
			slog.String("reason", resync), slog.String("trace_id", traceID(ctx)))
		env := &gamev1.EventEnvelope{Tick: e.opts.LastTick(), Payload: &gamev1.EventEnvelope_Resync{Resync: &gamev1.Resync{LastEventId: req.GetLastEventId(), Reason: resync}}}
		if err := st.send(out, frame{typ: TypeResync, env: env}); err != nil {
			sess.detach(st)
			return connectError(st.sendErr(err))
		}
	} else if req.GetLastEventId() != 0 {
		span.SetAttributes(attribute.Bool("stream.resumed", true))
	}
	err = st.run(ctx, out)
	sess.detach(st)
	if reason := e.dropReason(ctx, st, err); reason != "" {
		e.metrics.Drops.WithLabelValues(reason).Inc()
	}
	return connectError(err)
}

// dropReason names why a stream ended, for the drops counter: empty when
// the client ended it cleanly.
func (e *Egress) dropReason(ctx context.Context, st *stream, err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrBufferFull):
		return ReasonBufferFull
	case errors.Is(err, ErrDraining), e.draining.Load():
		return ReasonDraining
	}
	return ReasonClientGone
}

// session is the Session's retained state, made on its first Subscribe
// and kept until the Session ends.
func (e *Egress) session(ctx context.Context, id string, principal auth.Principal, ended <-chan struct{}, world bool) (*session, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if s, ok := e.sessions[id]; ok {
		if world != s.world {
			// The privileged view is asked for per stream; changing it is
			// a new perception, with no history to resume against.
			if err := s.resubscribe(ctx, e.observer(id, world)); err != nil {
				return nil, err
			}
		}
		return s, nil
	}
	s := &session{e: e, id: id, principal: principal, hist: newHistory(e.opts.ResumeWindow), notify: make(chan struct{})}
	if err := s.resubscribe(ctx, e.observer(id, world)); err != nil {
		return nil, err
	}
	e.sessions[id] = s
	// The Session ending frees everything: the fan-out subscription, the
	// history, and the stream, which the gateway has already canceled.
	// A Session with no lifetime — a harness's — is kept until the Hub
	// closes.
	if ended != nil {
		go func() {
			<-ended
			e.forget(id)
		}()
	}
	return s, nil
}

func (e *Egress) observer(id string, world bool) events.Observer {
	var obs events.Observer
	if e.opts.Observers != nil {
		obs, _ = e.opts.Observers.Observer(id)
	}
	obs.World = world
	return obs
}

// Rebind re-reads where sessionID perceives from and, if it changed,
// replaces the Session's fan-out subscription. Retained history is
// discarded — it was another perception's — so a stream open at the time
// continues from the new subscription and a later resume from before it
// is a Resync. The routing table calls this when a Character is bound or
// unbound (AW-SRV-014); a Session with no stream state is untouched.
func (e *Egress) Rebind(sessionID string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	s, ok := e.sessions[sessionID]
	if !ok {
		return
	}
	obs := e.observer(sessionID, s.world)
	if obs == s.obs {
		return
	}
	if err := s.resubscribe(context.Background(), obs); err != nil {
		e.log.Warn("stream rebind failed; session keeps its previous perception", "session_id", sessionID, "detail", err.Error())
	}
}

// forget ends a Session's fan-out subscription and drops its history.
func (e *Egress) forget(id string) {
	e.mu.Lock()
	s, ok := e.sessions[id]
	delete(e.sessions, id)
	e.mu.Unlock()
	if ok {
		s.close()
	}
}

// session is one Session's retained history and the pump that fills it
// from the fan-out.
type session struct {
	e         *Egress
	id        string
	principal auth.Principal

	mu    sync.Mutex
	obs   events.Observer
	world bool
	sub   *events.Subscription
	hist  history
	// notify is closed and replaced whenever hist grows or the fan-out
	// subscription ends: what a caught-up stream waits on.
	notify chan struct{}
	// ended is set when the current fan-out subscription closed, reason
	// saying why: from then on the Session is unusable and the stream
	// ends with the reason.
	ended  bool
	reason string
	stream *stream
}

// resubscribe subscribes to the fan-out as obs, replacing any current
// subscription. History is reset and an attached stream continues from
// the new subscription's first delivery.
func (s *session) resubscribe(ctx context.Context, obs events.Observer) error {
	sub, err := s.e.opts.Hub.Subscribe(ctx, events.Subscriber{Observer: obs, Principal: s.principal, SessionID: s.id})
	if err != nil {
		return err
	}
	s.mu.Lock()
	old := s.sub
	s.sub, s.obs, s.world = sub, obs, obs.World
	s.hist.reset()
	s.ended, s.reason = false, ""
	if s.stream != nil {
		s.stream.cursor = s.hist.end()
	}
	s.mu.Unlock()
	if old != nil {
		s.e.opts.Hub.Unsubscribe(old)
	}
	go s.pump(sub)
	return nil
}

// pump reads one fan-out subscription into the history for as long as it
// is the Session's current one. It never waits on the client: a stream
// that trails by more than egress.buffer is ended here, and the pump
// goes on retaining for the resume.
func (s *session) pump(sub *events.Subscription) {
	e := s.e
	for d := range sub.Events() {
		s.mu.Lock()
		if s.sub != sub {
			s.mu.Unlock()
			return
		}
		s.hist.append(frame{id: d.ID, typ: string(d.Type), env: d.Envelope})
		var (
			drop     *stream
			lastSent uint64
			abort    bool
		)
		if st := s.stream; st != nil && !st.ended {
			backlog := s.hist.end() - st.cursor
			e.metrics.BufferDepth.Observe(float64(backlog))
			if backlog > uint64(e.opts.Buffer) {
				st.ended = true
				drop, lastSent, abort = st, st.lastSent, st.sending
			}
		}
		close(s.notify)
		s.notify = make(chan struct{})
		s.mu.Unlock()
		if drop != nil {
			e.log.LogAttrs(drop.ctx, slog.LevelWarn, "stream ended: client not reading, buffer full",
				slog.String("session_id", s.id), slog.Uint64("buffered", uint64(e.opts.Buffer)),
				slog.Uint64("last_sent", lastSent), slog.Uint64("tick", uint64(d.Tick)), slog.String("trace_id", traceID(drop.ctx)))
			if abort {
				// The writer is inside Send, on a client that is not
				// consuming: reset the stream so it returns. Outside the
				// lock — this reaches into the HTTP/2 server. A writer
				// between Sends sees ended on its next pass and ends the
				// stream itself, with the typed reason on the wire.
				e.opts.Abort(drop.ctx)
				// A reset cannot get past a socket the client has stopped
				// reading: every frame for that connection is queued
				// behind the one blocked in the kernel. If the Send is
				// still in progress after a heartbeat's worth of time,
				// the connection is the problem, and it is closed — the
				// Session is disconnected (AC-4), as a client that pulled
				// its network cable would be.
				time.AfterFunc(e.opts.HeartbeatInterval, func() {
					s.mu.Lock()
					stuck := drop.sending
					s.mu.Unlock()
					if stuck {
						e.log.LogAttrs(drop.ctx, slog.LevelWarn, "stream reset did not return: client not reading its socket; closing the connection",
							slog.String("session_id", s.id), slog.String("trace_id", traceID(drop.ctx)))
						e.opts.Disconnect(drop.ctx)
					}
				})
			}
		}
	}
	s.mu.Lock()
	if s.sub == sub {
		s.ended, s.reason = true, sub.Reason()
		close(s.notify)
		s.notify = make(chan struct{})
	}
	s.mu.Unlock()
}

// attach opens a stream on the Session: one at a time. It resolves the
// resume point and reports the Resync reason when there is one.
func (s *session) attach(ctx context.Context, last uint64) (*stream, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stream != nil {
		return nil, "", ErrAlreadySubscribed
	}
	if s.ended {
		return nil, "", hubEnded(s.reason)
	}
	seq, resync := s.hist.resume(last)
	st := &stream{ctx: ctx, s: s, cursor: seq}
	if resync == "" && last != 0 {
		st.lastSent = last
	}
	s.stream = st
	return st, resync, nil
}

// detach closes the stream on the Session.
func (s *session) detach(st *stream) {
	s.mu.Lock()
	if s.stream == st {
		s.stream = nil
	}
	s.mu.Unlock()
}

// close ends the fan-out subscription; the pump marks the Session ended
// and the stream, if any, returns.
func (s *session) close() {
	s.mu.Lock()
	sub := s.sub
	s.mu.Unlock()
	if sub != nil {
		s.e.opts.Hub.Unsubscribe(sub)
	}
}

// hubEnded maps the fan-out's reason for ending a subscription to the
// stream's error.
func hubEnded(reason string) error {
	switch reason {
	case events.ReasonBufferFull:
		return ErrBufferFull
	case events.ReasonShutdown:
		return ErrDraining
	}
	// Unsubscribed: the Session ended, and the gateway has canceled the
	// stream's ctx too.
	return context.Canceled
}

// stream is one Subscribe call: a cursor over the Session's history. Its
// fields are the session's lock's.
type stream struct {
	ctx      context.Context
	s        *session
	cursor   uint64
	lastSent uint64
	// sending is set while the writer is inside Send; ended by the pump
	// when the cursor trails by more than the buffer.
	sending bool
	ended   bool
}

// run sends from the cursor until ctx is done, the Session ends, or the
// pump ends the stream. A heartbeat goes out whenever nothing else has
// for HeartbeatInterval.
func (st *stream) run(ctx context.Context, out Sender) error {
	s := st.s
	e := s.e
	hb := time.NewTimer(e.opts.HeartbeatInterval)
	defer hb.Stop()
	for {
		s.mu.Lock()
		var (
			next   frame
			have   bool
			notify chan struct{}
			err    error
		)
		switch {
		case st.ended:
			err = ErrBufferFull
		case st.cursor < s.hist.end():
			next, have = s.hist.at(st.cursor), true
			st.cursor++
		case s.ended:
			err = hubEnded(s.reason)
		default:
			notify = s.notify
		}
		s.mu.Unlock()
		if err != nil {
			return err
		}
		if have {
			if err := st.send(out, next); err != nil {
				return st.sendErr(err)
			}
			if !hb.Stop() {
				select {
				case <-hb.C:
				default:
				}
			}
			hb.Reset(e.opts.HeartbeatInterval)
			continue
		}
		select {
		case <-notify:
		case <-ctx.Done():
			return ctx.Err()
		case <-hb.C:
			env := &gamev1.EventEnvelope{Tick: e.opts.LastTick(), Payload: &gamev1.EventEnvelope_Heartbeat{Heartbeat: &gamev1.Heartbeat{}}}
			if err := st.send(out, frame{typ: TypeHeartbeat, env: env}); err != nil {
				return st.sendErr(err)
			}
			hb.Reset(e.opts.HeartbeatInterval)
		}
	}
}

// send writes one frame and counts it. The Session's lock marks the
// writer as inside Send, so the pump knows whether ending the stream
// takes an abort or the writer's next pass. An Event's envelope is shared
// with every other recipient and is never edited here.
func (st *stream) send(out Sender, f frame) error {
	s := st.s
	s.mu.Lock()
	if st.ended {
		s.mu.Unlock()
		return ErrBufferFull
	}
	st.sending = true
	s.mu.Unlock()
	err := out.Send(f.env)
	s.mu.Lock()
	st.sending = false
	if err == nil && f.id != 0 {
		st.lastSent = f.id
	}
	s.mu.Unlock()
	if err != nil {
		return err
	}
	s.e.metrics.Sent.WithLabelValues(f.typ).Inc()
	return nil
}

// sendErr is the error a failed Send ends the stream with: the pump's
// reason when it aborted the stream, else the write error — the client
// went away.
func (st *stream) sendErr(err error) error {
	st.s.mu.Lock()
	ended := st.ended
	st.s.mu.Unlock()
	if ended {
		return ErrBufferFull
	}
	if st.ctx.Err() != nil {
		return st.ctx.Err()
	}
	return err
}

func traceID(ctx context.Context) string {
	sc := trace.SpanFromContext(ctx).SpanContext()
	if !sc.HasTraceID() {
		return ""
	}
	return sc.TraceID().String()
}

var _ sim.EventSink = (*events.Hub)(nil)
