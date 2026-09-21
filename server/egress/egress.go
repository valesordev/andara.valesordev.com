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
	// LastTick is what a Heartbeat reports. Defaults to the later of the
	// Hub's last published Tick and what ObserveTick was told — the Hub
	// sees only ticks that emitted something.
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
	// tick is the last Tick ObserveTick reported.
	tick atomic.Uint64

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
	e := &Egress{sessions: map[string]*session{}}
	if opts.LastTick == nil {
		hub := opts.Hub
		opts.LastTick = func() uint64 { return max(uint64(hub.LastTick()), e.tick.Load()) }
	}
	if opts.Log == nil {
		opts.Log = slog.New(slog.DiscardHandler)
	}
	if opts.Tracer == nil {
		opts.Tracer = noop.NewTracerProvider().Tracer("andara-server")
	}
	e.opts, e.log, e.tracer, e.metrics = opts, opts.Log, opts.Tracer, NewMetrics(opts.Registry)
	return e
}

// LastTick is what the next Heartbeat would report.
func (e *Egress) LastTick() uint64 { return e.opts.LastTick() }

// ObserveTick records that the simulation completed tick t, so a
// Heartbeat on a quiet World still shows it moving. The tick loop calls
// it after every tick; one atomic store.
func (e *Egress) ObserveTick(t uint64) {
	if t > e.tick.Load() {
		e.tick.Store(t)
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
	err = func() error {
		if resync != "" {
			span.SetAttributes(attribute.String("stream.resync", resync))
			e.metrics.Resyncs.WithLabelValues(resync).Inc()
			e.log.LogAttrs(ctx, slog.LevelInfo, "stream resync: resume point not retained",
				slog.String("session_id", sessionID), slog.Uint64("last_event_id", req.GetLastEventId()),
				slog.String("reason", resync), slog.String("trace_id", traceID(ctx)))
			env := &gamev1.EventEnvelope{Tick: e.opts.LastTick(), Payload: &gamev1.EventEnvelope_Resync{Resync: &gamev1.Resync{LastEventId: req.GetLastEventId(), Reason: resync}}}
			if err := st.send(out, frame{typ: TypeResync, env: env}); err != nil {
				return st.sendErr(err)
			}
		} else if req.GetLastEventId() != 0 {
			span.SetAttributes(attribute.Bool("stream.resumed", true))
		}
		return st.run(ctx, out)
	}()
	e.metrics.Streams.Dec()
	reason := e.dropReason(err)
	sess.detach(st, reason)
	if reason != "" {
		e.metrics.Drops.WithLabelValues(reason).Inc()
	}
	return connectError(err)
}

// dropReason names why a stream ended, for the drops counter: empty when
// the client ended it cleanly.
func (e *Egress) dropReason(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrBufferFull):
		return ReasonBufferFull
	case errors.Is(err, ErrRevoked):
		return ReasonRevoked
	case errors.Is(err, ErrDraining), e.draining.Load():
		return ReasonDraining
	}
	return ReasonClientGone
}

// unavailable reports whether a stream ended for reason leaves the Session
// in the drop state the availability SLI counts: the server ended a stream
// the client wanted open, and the client has not reopened one.
func unavailable(reason string) bool {
	return reason == ReasonBufferFull || reason == ReasonDraining
}

// EndSession ends sessionID's stream because the Session is being closed
// for reason — revoked (AW-SRV-008 AC-12) — with a final
// SubscriberDropped{reason} frame before the typed error, and returns
// once the stream has gone or endGrace has passed. The gateway calls it
// before it cancels the Session's context, so the frame gets out ahead
// of the cancellation; a Session with no stream is untouched.
func (e *Egress) EndSession(sessionID, reason string) {
	e.mu.Lock()
	s, ok := e.sessions[sessionID]
	e.mu.Unlock()
	if !ok {
		return
	}
	s.mu.Lock()
	st := s.stream
	if st == nil || st.ended || st.terminal != "" {
		s.mu.Unlock()
		return
	}
	st.terminal = reason
	env := &gamev1.EventEnvelope{Tick: e.opts.LastTick(), Payload: &gamev1.EventEnvelope_SubscriberDropped{SubscriberDropped: &gamev1.SubscriberDropped{Reason: reason}}}
	s.hist.append(frame{typ: string(sim.EvSubscriberDropped), env: env})
	close(s.notify)
	s.notify = make(chan struct{})
	done := st.done
	s.mu.Unlock()
	select {
	case <-done:
	case <-time.After(endGrace):
		// The client is not reading; the frame stays behind its Send and
		// the cancellation that follows this call ends the stream.
	}
}

// endGrace is how long EndSession waits for the final frame to be sent.
const endGrace = time.Second

// session is the Session's retained state, made on its first Subscribe
// and kept until the Session ends. The fan-out subscription is made
// outside e.mu — a World subscription audits, and an audit write waits on
// the log — under the Session's own rebind lock.
func (e *Egress) session(ctx context.Context, id string, principal auth.Principal, ended <-chan struct{}, world bool) (*session, error) {
	e.mu.Lock()
	s, ok := e.sessions[id]
	if !ok {
		s = &session{e: e, id: id, principal: principal, ended: ended, hist: newHistory(e.opts.ResumeWindow), notify: make(chan struct{})}
		e.sessions[id] = s
		// The Session ending frees everything: the fan-out subscription,
		// the history, and the stream, which the gateway has already
		// canceled. A Session with no lifetime — a harness's — is kept
		// until the Hub closes.
		if ended != nil {
			go func() {
				<-ended
				e.forget(id)
			}()
		}
	}
	e.mu.Unlock()

	s.rebind.Lock()
	defer s.rebind.Unlock()
	s.mu.Lock()
	// A subscription the fan-out ended is not one: the next Subscribe
	// starts the Session over, history discarded — a resume from before
	// it is a Resync — rather than reporting the drop forever.
	closing, subscribed, cur, open := s.closing || s.over(), s.sub != nil && !s.hubEnded, s.world, s.stream != nil
	s.mu.Unlock()
	switch {
	case closing:
		return nil, context.Canceled
	case subscribed && cur == world:
		return s, nil
	case open:
		// The privileged view is asked for per stream; changing it is a
		// new perception, and the stream that has the old one is not
		// disturbed by a second Subscribe that will be refused anyway.
		return nil, ErrAlreadySubscribed
	}
	if err := s.resubscribe(ctx, e.observer(id, world)); err != nil {
		if !subscribed {
			e.discard(id, s)
		}
		return nil, err
	}
	return s, nil
}

// discard removes a Session whose first subscription never happened, so
// the next Subscribe starts clean and nothing is retained for nobody.
func (e *Egress) discard(id string, s *session) {
	e.mu.Lock()
	defer e.mu.Unlock()
	s.mu.Lock()
	empty := s.sub == nil
	s.mu.Unlock()
	if empty && e.sessions[id] == s {
		delete(e.sessions, id)
	}
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
// unbound (AW-SRV-014); a Session with no stream state, or one that is
// ending, is untouched.
func (e *Egress) Rebind(sessionID string) {
	e.mu.Lock()
	s, ok := e.sessions[sessionID]
	e.mu.Unlock()
	if !ok {
		return
	}
	s.rebind.Lock()
	defer s.rebind.Unlock()
	s.mu.Lock()
	closing, cur, world := s.closing || s.over(), s.obs, s.world
	s.mu.Unlock()
	if closing {
		// The Session has ended and forget is on its way: the routing
		// table's Unbind woke on the same signal. Nothing is subscribed
		// for a Session that is over — no throwaway subscription, no
		// second subscribe_world record.
		return
	}
	obs := e.observer(sessionID, world)
	if obs == cur {
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
	// ended is closed when the Session ends (nil for a harness's Session
	// with no lifetime): the same signal forget wakes on, read directly
	// where forget's scheduling would otherwise be a race.
	ended <-chan struct{}

	// rebind serializes whoever replaces the fan-out subscription —
	// Subscribe, Rebind, close — so two do not race to be current. Taken
	// before mu, never inside it.
	rebind sync.Mutex

	mu    sync.Mutex
	obs   events.Observer
	world bool
	sub   *events.Subscription
	hist  history
	// notify is closed and replaced whenever hist grows or the fan-out
	// subscription ends: what a caught-up stream waits on.
	notify chan struct{}
	// hubEnded is set when the current fan-out subscription closed,
	// reason saying why: the stream ends with the reason, the history is
	// discarded, and the next Subscribe subscribes again. closing is set
	// by close: the Session is going away and no subscription is made
	// for it again. inDrop is the availability SLI's drop state: the
	// server ended the last stream and the client has not reopened one.
	hubEnded bool
	reason   string
	closing  bool
	inDrop   bool
	stream   *stream
}

// over reports whether the Session's lifetime has ended, whether or not
// forget has run yet.
func (s *session) over() bool {
	select {
	case <-s.ended:
		return true
	default:
		return false
	}
}

// resubscribe subscribes to the fan-out as obs, replacing any current
// subscription. History is reset and an attached stream continues from
// the new subscription's first delivery. Called under rebind.
func (s *session) resubscribe(ctx context.Context, obs events.Observer) error {
	sub, err := s.e.opts.Hub.Subscribe(ctx, events.Subscriber{Observer: obs, Principal: s.principal, SessionID: s.id})
	if err != nil {
		return err
	}
	s.mu.Lock()
	old := s.sub
	s.sub, s.obs, s.world = sub, obs, obs.World
	s.hist.reset()
	s.hubEnded, s.reason = false, ""
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
				if abort {
					// The writer is inside Send, on a client that is not
					// consuming: reset the stream so it returns. Under
					// the lock, deliberately: the writer re-takes it when
					// Send returns, so the handler cannot return — and
					// the HTTP/2 server cannot retire the ResponseWriter
					// the reset acts on — until this call is done. The
					// reset itself only enqueues a frame for the
					// connection's serve loop; it never waits on the
					// socket. A writer between Sends sees ended on its
					// next pass and ends the stream itself, with the
					// typed reason on the wire.
					e.opts.Abort(st.ctx)
				}
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
		s.hubEnded, s.reason = true, sub.Reason()
		close(s.notify)
		s.notify = make(chan struct{})
	}
	s.mu.Unlock()
}

// attach opens a stream on the Session: one at a time. It resolves the
// resume point and reports the Resync reason when there is one. A Session
// in the drop state leaves it here: the client reopened.
func (s *session) attach(ctx context.Context, last uint64) (*stream, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stream != nil {
		return nil, "", ErrAlreadySubscribed
	}
	if s.hubEnded {
		return nil, "", hubEnded(s.reason)
	}
	seq, resync := s.hist.resume(last)
	st := &stream{ctx: ctx, s: s, cursor: seq, done: make(chan struct{})}
	if resync == "" && last != 0 {
		st.lastSent = last
	}
	s.stream = st
	if s.inDrop {
		s.inDrop = false
		s.e.metrics.InDropState.Dec()
	}
	return st, resync, nil
}

// detach closes the stream on the Session, entering the drop state when
// the server ended it for a reason the client did not choose.
func (s *session) detach(st *stream, reason string) {
	s.mu.Lock()
	if s.stream == st {
		s.stream = nil
	}
	if unavailable(reason) && !s.inDrop && !s.closing {
		s.inDrop = true
		s.e.metrics.InDropState.Inc()
	}
	close(st.done)
	s.mu.Unlock()
}

// close ends the fan-out subscription and marks the Session closing, so
// nothing subscribes for it again; the pump marks it ended and the
// stream, if any, returns. A writer blocked in Send on a client that is
// not reading is reset, as a buffer_full drop resets it; the Session's
// connection is the gateway's to close.
func (s *session) close() {
	s.rebind.Lock()
	s.mu.Lock()
	s.closing = true
	sub := s.sub
	if s.inDrop {
		s.inDrop = false
		s.e.metrics.InDropState.Dec()
	}
	if st := s.stream; st != nil && st.sending {
		s.e.opts.Abort(st.ctx)
	}
	s.mu.Unlock()
	s.rebind.Unlock()
	if sub != nil {
		s.e.opts.Hub.Unsubscribe(sub)
	}
}

// terminalErr is the error a stream ends with after its final frame.
func terminalErr(reason string) error {
	if reason == ReasonRevoked {
		return ErrRevoked
	}
	return context.Canceled
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
	// when the cursor trails by more than the buffer; terminal by
	// EndSession, naming the frame the stream ends after sending.
	sending  bool
	ended    bool
	terminal string
	// done is closed when the stream has detached.
	done chan struct{}
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
		case st.terminal != "":
			// The final frame is sent; the Session is closing.
			err = terminalErr(st.terminal)
		case s.hubEnded:
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
