// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

// Package events is the fan-out behind the simulation's EventSink
// (AW-SRV-004): the one sink the tick publishes to, and the subscriptions
// everything else — Session streams (AW-SRV-011), the CLI's event tap, a
// projector — reads from.
//
// The tick calls Publish synchronously for every Event and must never wait
// on an observer, so Publish is one non-blocking enqueue; delivery happens
// on the Hub's own goroutine, subscriber by subscriber, each behind a
// bounded buffer. A subscriber that stops reading is dropped, with a
// SubscriberDropped Event as its last, rather than slowing the tick.
//
// Who sees what is the sim's decision (sim.Scope, computed at emit) and the
// Hub only applies it: an Event reaches an Observer whose Room, Entity, or
// World privilege the Scope names, in the form the sim prepared for that
// privilege. The Hub never edits an envelope.
package events

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
	"google.golang.org/protobuf/proto"

	gamev1 "github.com/valesordev/andara/gen/go/andara/game/v1"
	"github.com/valesordev/andara/server/auth"
	"github.com/valesordev/andara/server/sim"
)

// Defaults for events.subscriber_buffer and events.max_subscribers.
const (
	DefaultSubscriberBuffer = 1024
	DefaultMaxSubscribers   = 10000
)

// Drop reasons — the `reason` label on andara_subscriber_drops_total.
const (
	ReasonBufferFull   = "buffer_full"
	ReasonUnsubscribed = "unsubscribed"
	ReasonShutdown     = "shutdown"
)

// Errors Subscribe returns.
var (
	// ErrTooManySubscribers: events.max_subscribers reached.
	ErrTooManySubscribers = errors.New("events: too many subscribers")
	// ErrNotPrivileged: World visibility asked for without the role.
	ErrNotPrivileged = errors.New("events: world visibility requires game_master or operator")
	// ErrClosed: the Hub has shut down.
	ErrClosed = errors.New("events: hub closed")
)

// Observer is where a subscriber perceives from: the Room it stands in,
// the Entity it is, and whether it holds World visibility. Any of the
// three may be unset; an Observer with none set receives nothing.
//
// An Observer bound to an Entity follows it: the Hub sees every
// CharacterLeft and CharacterArrived addressed to the Entity, in order,
// before anything is delivered after them, and moves the Observer's Room
// there and then — cleared on leaving, set on arriving. So a Session's
// perception is never a consumer's read latency behind the sim (AC-1),
// and between a cross-Zone departure and the arrival the Observer is in
// no Room, which is where the Character is.
type Observer struct {
	Entity sim.EntityID
	Room   sim.RoomRef
	World  bool
}

// Subscriber is who is subscribing: the Observer, and the Principal and
// Session the privilege check and the audit record name.
type Subscriber struct {
	Observer  Observer
	Principal auth.Principal
	SessionID string
}

// Delivery is one Event as a subscriber receives it: the envelope in the
// form its privilege allows, plus what the Hub knew about it.
type Delivery struct {
	ID       uint64
	Tick     sim.Tick
	Type     sim.EventType
	Envelope *gamev1.EventEnvelope
}

// Subscription is one subscriber's stream. Events arrives in the order the
// sim produced them and is closed when the subscription ends; Reason then
// says why.
type Subscription struct {
	ID    uint64
	hub   *Hub
	start sim.Tick
	// mu serializes the sender (the fan-out goroutine) against whoever
	// ends the subscription (Unsubscribe from any goroutine): a send and a
	// close on ch never race.
	mu      sync.Mutex
	obs     Observer
	session string
	ch      chan Delivery
	dropped bool
	reason  string
}

// Events is the stream. Closed when the subscription ends.
func (s *Subscription) Events() <-chan Delivery { return s.ch }

// Reason is why the stream closed: buffer_full, unsubscribed, or shutdown.
// Empty while it is open.
func (s *Subscription) Reason() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reason
}

func (s *Subscription) observer() Observer {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.obs
}

// follow moves the Observer with its Entity: a CharacterLeft addressed to
// it clears the Room, a CharacterArrived sets it. Position tracking is
// independent of delivery — it applies to Events before the start tick
// too, so a subscription registered mid-move is not left in the Room its
// Character had already left.
func (s *Subscription) follow(ev sim.Event) {
	if ev.Type != sim.EvCharacterLeft && ev.Type != sim.EvCharacterArrived {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.obs.Entity == "" || !addressed(ev.Scope, s.obs.Entity) {
		return
	}
	switch p := ev.Envelope.GetPayload().(type) {
	case *gamev1.EventEnvelope_CharacterLeft:
		s.obs.Room = sim.RoomRef{}
	case *gamev1.EventEnvelope_CharacterArrived:
		s.obs.Room = sim.RoomRef{Zone: sim.ZoneID(p.CharacterArrived.GetZoneId()), Room: sim.RoomID(p.CharacterArrived.GetRoomId())}
	}
}

// push delivers without blocking. full reports a buffer that could not
// take it; a subscription already ended takes nothing and reports neither.
func (s *Subscription) push(d Delivery) (ok, full bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dropped {
		return false, false
	}
	// One slot past the buffer is reserved for SubscriberDropped.
	if len(s.ch) >= cap(s.ch)-1 {
		return false, true
	}
	select {
	case s.ch <- d:
		return true, false
	default:
		return false, true
	}
}

// finish ends the subscription once: records the reason, delivers the
// SubscriberDropped notice into the reserved slot for a buffer_full drop,
// and closes the stream. Reports whether this call was the one that did.
func (s *Subscription) finish(reason string, tick sim.Tick) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dropped {
		return false
	}
	s.dropped = true
	s.reason = reason
	if reason == ReasonBufferFull {
		// The one drop the subscriber did not ask for and was not told of
		// otherwise: shutdown was announced by SimulationStopped.
		env := &gamev1.EventEnvelope{EventId: 0, Tick: uint64(tick), Payload: &gamev1.EventEnvelope_SubscriberDropped{SubscriberDropped: &gamev1.SubscriberDropped{Reason: reason}}}
		select {
		case s.ch <- Delivery{Tick: tick, Type: sim.EvSubscriberDropped, Envelope: env}:
		default:
		}
	}
	close(s.ch)
	return true
}

// Options configures a Hub.
type Options struct {
	// Buffer is events.subscriber_buffer: Events a subscriber may have
	// unread before it is dropped.
	Buffer int
	// MaxSubscribers is events.max_subscribers.
	MaxSubscribers int
	// Audit records a World-visibility subscription as a privileged read
	// (AC-8). Optional.
	Audit    *auth.Auditor
	Log      *slog.Logger
	Tracer   trace.Tracer
	Registry prometheus.Registerer
}

// Hub is the fan-out. One per process; the Engine's only sink.
type Hub struct {
	opts    Options
	log     *slog.Logger
	tracer  trace.Tracer
	metrics *Metrics

	in       chan item
	stop     chan struct{}
	stopOnce sync.Once
	done     chan struct{}
	lastTick atomic.Uint64
	// closed means the Hub accepts no more subscriptions or Events: set
	// by Close, and by SimulationStopped.
	closed atomic.Bool

	mu     sync.RWMutex
	subs   map[uint64]*Subscription
	nextID uint64
	// lastWarn rate-limits the warning for an Event the fan-out could not
	// even queue — the Hub's own goroutine is not keeping up, which is a
	// process problem, not a subscriber's. Counted, never blocked on.
	lastWarn time.Time
}

// item is what crosses from the tick to the fan-out goroutine: an Event,
// or a flush marker whose done channel is closed once everything queued
// before it has been delivered.
type item struct {
	ev    sim.Event
	flush chan struct{}
}

// New builds a Hub and starts its fan-out goroutine.
func New(o Options) *Hub {
	if o.Buffer <= 0 {
		o.Buffer = DefaultSubscriberBuffer
	}
	if o.MaxSubscribers <= 0 {
		o.MaxSubscribers = DefaultMaxSubscribers
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Tracer == nil {
		o.Tracer = noop.NewTracerProvider().Tracer("andara-server")
	}
	h := &Hub{
		opts: o, log: o.Log, tracer: o.Tracer, metrics: NewMetrics(o.Registry),
		// Inbound holds many ticks' worth: the fan-out is O(subscribers)
		// per Event and never blocks, so this only fills if the process
		// is starved, and then dropping is the honest outcome.
		in:   make(chan item, 16*o.Buffer),
		stop: make(chan struct{}),
		done: make(chan struct{}),
		subs: map[uint64]*Subscription{},
	}
	go h.run()
	return h
}

// Publish implements sim.EventSink. Called from the tick for every Event,
// in order; returns without waiting on anyone.
func (h *Hub) Publish(ev sim.Event) {
	h.metrics.Emitted.WithLabelValues(string(ev.Type)).Inc()
	if ev.ID != 0 {
		// Lifecycle notifications (SimulationStopped, event_id 0) do not
		// move the tick a new subscriber starts from.
		h.lastTick.Store(uint64(ev.Tick))
	}
	if h.closed.Load() {
		return
	}
	select {
	case h.in <- item{ev: ev}:
	default:
		h.metrics.InboundDropped.Inc()
		if time.Since(h.lastWarn) > time.Minute {
			h.lastWarn = time.Now()
			h.log.Warn("event fan-out is not keeping up; events dropped before delivery", "tick", uint64(ev.Tick), "queued", len(h.in))
		}
	}
}

// LastTick is the Tick of the last Event published: what a stream's
// heartbeat reports so a quiet World is seen to be ticking (AW-SRV-011).
func (h *Hub) LastTick() sim.Tick { return sim.Tick(h.lastTick.Load()) }

// Flush blocks until every Event published before the call has been
// delivered to its subscribers' buffers. For harnesses that need the
// stream in step with the tick; a transport never calls it.
func (h *Hub) Flush() {
	if h.closed.Load() {
		return
	}
	done := make(chan struct{})
	select {
	case h.in <- item{flush: done}:
		select {
		case <-done:
		case <-h.done:
		}
	case <-h.done:
	}
}

// Subscribe registers an Observer. The subscription receives Events from
// the tick after the one currently being published onward, never a partial
// tick (AC-10). World visibility requires the game_master or operator role
// and is audited as a privileged read (AC-8).
func (h *Hub) Subscribe(ctx context.Context, s Subscriber) (*Subscription, error) {
	if s.Observer.World {
		if !s.Principal.Has(auth.RoleGameMaster) && !s.Principal.Has(auth.RoleOperator) {
			return nil, ErrNotPrivileged
		}
	}
	h.mu.Lock()
	// closed is decided under the same lock dropAll sets it under, so a
	// subscription is never inserted after the last one was ended.
	if h.closed.Load() {
		h.mu.Unlock()
		return nil, ErrClosed
	}
	if len(h.subs) >= h.opts.MaxSubscribers {
		h.mu.Unlock()
		return nil, ErrTooManySubscribers
	}
	h.nextID++
	sub := &Subscription{
		ID: h.nextID, hub: h, obs: s.Observer, session: s.SessionID,
		// One slot past the buffer is reserved for the SubscriberDropped
		// Event, so it always fits when the buffer is what filled.
		ch:    make(chan Delivery, h.opts.Buffer+1),
		start: sim.Tick(h.lastTick.Load()) + 1,
	}
	h.subs[sub.ID] = sub
	n := len(h.subs)
	h.mu.Unlock()
	h.metrics.Subscribers.Set(float64(n))
	if s.Observer.World {
		ctx = auth.WithSessionID(ctx, s.SessionID)
		h.log.LogAttrs(ctx, slog.LevelInfo, "world-scope subscription: privileged read",
			slog.Uint64("subscription_id", sub.ID), slog.String("session_id", s.SessionID),
			slog.String("actor_account_id", s.Principal.AccountID), slog.String("trace_id", traceID(ctx)))
		if h.opts.Audit != nil {
			h.opts.Audit.Record(ctx, auth.Entry{Actor: s.Principal, Action: auth.ActionSubscribeWorld, Target: "world", Outcome: auth.AuditOK})
		}
	}
	return sub, nil
}

// Unsubscribe ends a subscription; its stream closes with reason
// unsubscribed.
func (h *Hub) Unsubscribe(sub *Subscription) {
	h.end(sub, ReasonUnsubscribed, 0)
}

// end closes a subscription once and accounts for it.
func (h *Hub) end(sub *Subscription, reason string, tick sim.Tick) {
	if !sub.finish(reason, tick) {
		return
	}
	h.mu.Lock()
	delete(h.subs, sub.ID)
	n := len(h.subs)
	h.mu.Unlock()
	h.metrics.Subscribers.Set(float64(n))
	h.metrics.Drops.WithLabelValues(reason).Inc()
	if reason == ReasonBufferFull {
		h.log.Warn("subscriber dropped: buffer full", "subscription_id", sub.ID, "reason", reason, "buffered", cap(sub.ch)-1, "tick", uint64(tick))
	}
}

// Close delivers whatever is still queued — the drain's SimulationStopped
// among it — ends every subscription with reason shutdown, and stops the
// fan-out. Idempotent.
func (h *Hub) Close() {
	h.closed.Store(true)
	h.stopOnce.Do(func() { close(h.stop) })
	<-h.done
}

// run is the fan-out goroutine: Events grouped into batches — everything
// queued at once, which under load is one tick's worth — delivered to
// every subscriber whose Observer the Scope reaches. One span and one
// histogram observation per batch, never per Event.
func (h *Hub) run() {
	defer close(h.done)
	defer h.dropAll()
	for {
		var batch []item
		select {
		case it := <-h.in:
			batch = append(batch, it)
		case <-h.stop:
			// Stopping delivers what was queued before it: a subscriber
			// promised SimulationStopped gets it before the stream ends.
			h.deliver(h.drainQueued(nil))
			return
		}
		h.deliver(h.drainQueued(batch))
	}
}

// drainQueued appends what is immediately available on the inbound
// channel, up to a batch bound.
func (h *Hub) drainQueued(batch []item) []item {
	for len(batch) < 4096 {
		select {
		case next := <-h.in:
			batch = append(batch, next)
		default:
			return batch
		}
	}
	return batch
}

func (h *Hub) deliver(batch []item) {
	start := time.Now()
	h.mu.RLock()
	subs := make([]*Subscription, 0, len(h.subs))
	for _, s := range h.subs {
		subs = append(subs, s)
	}
	h.mu.RUnlock()
	events := 0
	var tick sim.Tick
	for _, it := range batch {
		if it.flush != nil {
			close(it.flush)
			continue
		}
		events++
		tick = it.ev.Tick
		f := forms{ev: it.ev}
		if it.ev.Type == sim.EvSimulationStopped && it.ev.ID == 0 {
			// Lifecycle: everyone hears it, in the form their privilege
			// allows, then everyone is dropped. Not scoped, not gated on
			// the start tick — a subscriber that just arrived should not
			// wait for a tick that will never come.
			for _, s := range subs {
				h.send(s, &f, tick)
			}
			h.dropAll()
			continue
		}
		for _, s := range subs {
			obs := s.observer()
			if it.ev.Tick >= s.start && visible(it.ev.Scope, obs) {
				h.send(s, &f, tick)
			}
			// After delivery, in Event order: the next Event is filtered
			// against where the Character is now.
			s.follow(it.ev)
		}
	}
	if events > 0 {
		d := time.Since(start)
		h.metrics.FanoutDuration.Observe(d.Seconds())
		_, span := h.tracer.Start(context.Background(), "event.fanout", trace.WithAttributes(
			attribute.Int("event_count", events), attribute.Int("subscriber_count", len(subs)),
			attribute.Int64("tick", int64(tick)), attribute.Float64("duration_ms", float64(d.Microseconds())/1000)))
		span.End()
	}
}

// forms is one Event's deliverable envelopes, built on first use: whole
// or redacted by privilege, and with client_ref only for the Session
// whose Command caused it — every other recipient gets it blank, as
// event.proto promises, here rather than in every transport.
type forms struct {
	ev       sim.Event
	stripped [2]*gamev1.EventEnvelope // [privileged]
}

func (f *forms) envelope(privileged, ownSession bool) *gamev1.EventEnvelope {
	env := f.ev.Deliverable(privileged)
	if ownSession || env.GetClientRef() == "" {
		return env
	}
	i := 0
	if privileged {
		i = 1
	}
	if f.stripped[i] == nil {
		c := proto.Clone(env).(*gamev1.EventEnvelope)
		c.ClientRef = ""
		f.stripped[i] = c
	}
	return f.stripped[i]
}

// send delivers one Event to one subscriber without blocking; a full
// buffer drops the subscriber (AC-5).
func (h *Hub) send(s *Subscription, f *forms, tick sim.Tick) {
	s.mu.Lock()
	privileged, own := s.obs.World, f.ev.Session != "" && s.session == f.ev.Session
	s.mu.Unlock()
	env := f.envelope(privileged, own)
	ok, full := s.push(Delivery{ID: f.ev.ID, Tick: f.ev.Tick, Type: f.ev.Type, Envelope: env})
	if ok && !privileged && f.ev.Redacted != nil {
		h.metrics.Redactions.WithLabelValues(string(f.ev.Type)).Inc()
	}
	if full {
		h.end(s, ReasonBufferFull, tick)
	}
}

// dropAll ends every subscription with reason shutdown and closes the
// Hub to new ones, under the lock Subscribe checks, so nothing is
// inserted after the last one is ended.
func (h *Hub) dropAll() {
	h.mu.Lock()
	h.closed.Store(true)
	subs := make([]*Subscription, 0, len(h.subs))
	for _, s := range h.subs {
		subs = append(subs, s)
	}
	h.mu.Unlock()
	for _, s := range subs {
		h.end(s, ReasonShutdown, sim.Tick(h.lastTick.Load()))
	}
}

// addressed reports whether the Scope names the Entity explicitly.
func addressed(scope sim.Scope, id sim.EntityID) bool {
	for _, e := range scope.Entities {
		if e == id {
			return true
		}
	}
	return false
}

// visible applies a Scope to an Observer. Shapes are additive: any one
// that names the Observer is enough. World visibility is the privileged
// view of everything (AC-8) — it is granted per subscription, audited, and
// never to a player — and a Scope with only World set reaches nobody else.
func visible(scope sim.Scope, obs Observer) bool {
	if obs.World {
		return true
	}
	if scope.Zoned() && obs.Room.Zone == scope.Room.Zone && (scope.Room.Room == "" || scope.Room.Room == obs.Room.Room) {
		return true
	}
	return obs.Entity != "" && addressed(scope, obs.Entity)
}

// Visible is visible, exported for tests and for the CLI's event tap.
func Visible(scope sim.Scope, obs Observer) bool { return visible(scope, obs) }

func traceID(ctx context.Context) string {
	sc := trace.SpanFromContext(ctx).SpanContext()
	if !sc.HasTraceID() {
		return ""
	}
	return sc.TraceID().String()
}
