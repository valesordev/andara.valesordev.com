// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package egress

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"google.golang.org/genproto/googleapis/rpc/errdetails"

	gamev1 "github.com/valesordev/andara/gen/go/andara/game/v1"
	"github.com/valesordev/andara/server/auth"
	"github.com/valesordev/andara/server/events"
	"github.com/valesordev/andara/server/sim"
)

var (
	player = auth.Principal{AccountID: "p", Roles: []auth.Role{auth.RolePlayer}}
	gm     = auth.Principal{AccountID: "g", Roles: []auth.Role{auth.RolePlayer, auth.RoleGameMaster}}
)

// fixture is a Hub fed by hand and an Egress over it, with observers the
// test assigns per Session.
type fixture struct {
	t       *testing.T
	hub     *events.Hub
	e       *Egress
	reg     *prometheus.Registry
	logs    *syncBuffer
	aborted chan context.Context

	mu   sync.Mutex
	obs  map[string]events.Observer
	tick uint64
	id   uint64
}

type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func newFixture(t *testing.T, mutate func(*Options)) *fixture {
	t.Helper()
	f := &fixture{t: t, reg: prometheus.NewRegistry(), logs: &syncBuffer{}, aborted: make(chan context.Context, 16), obs: map[string]events.Observer{}}
	f.hub = events.New(events.Options{Buffer: 64, Registry: f.reg})
	t.Cleanup(f.hub.Close)
	opts := Options{
		Hub:               f.hub,
		Observers:         ObserverFunc(f.observer),
		Buffer:            8,
		ResumeWindow:      16,
		HeartbeatInterval: time.Hour,
		Abort:             func(ctx context.Context) bool { f.aborted <- ctx; return true },
		Log:               slog.New(slog.NewJSONHandler(f.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Registry:          f.reg,
	}
	if mutate != nil {
		mutate(&opts)
	}
	f.e = New(opts)
	return f
}

func (f *fixture) observer(id string) (events.Observer, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	o, ok := f.obs[id]
	return o, ok
}

func (f *fixture) place(session string, entity string, zone, room string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.obs[session] = events.Observer{Entity: sim.EntityID(entity), Room: sim.RoomRef{Zone: sim.ZoneID(zone), Room: sim.RoomID(room)}}
}

// emit publishes one RoomDescribed with the given Scope on a new tick and
// flushes the fan-out. Returns the Event ID.
func (f *fixture) emit(scope sim.Scope) uint64 {
	f.mu.Lock()
	f.tick++
	f.id++
	id, tick := f.id, f.tick
	f.mu.Unlock()
	env := &gamev1.EventEnvelope{EventId: id, Tick: tick, Payload: &gamev1.EventEnvelope_RoomDescribed{RoomDescribed: &gamev1.RoomDescribed{RoomId: fmt.Sprint(id)}}}
	f.hub.Publish(sim.Event{ID: id, Tick: sim.Tick(tick), Zone: "town", Type: sim.EvRoomDescribed, Scope: scope, Envelope: env})
	f.hub.Flush()
	return id
}

// settled waits until the Session's pump has retained Event id: Flush
// only waits for the fan-out's delivery into the subscription buffer.
func (f *fixture) settled(session string, id uint64) {
	f.t.Helper()
	waitFor(f.t, func() bool {
		f.e.mu.Lock()
		s, ok := f.e.sessions[session]
		f.e.mu.Unlock()
		if !ok {
			return false
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.hist.newest >= id
	}, fmt.Sprintf("event %d retained for %s", id, session))
}

func plaza() sim.Scope { return sim.ScopeRoom("town", "plaza") }
func hall() sim.Scope  { return sim.ScopeRoom("town", "hall") }

// client is one Subscribe call driven against a fake Sender.
type client struct {
	f      *fixture
	id     string
	ctx    context.Context
	cancel context.CancelFunc
	ended  chan struct{}
	recv   chan *gamev1.EventEnvelope
	block  chan struct{} // when non-nil, Send blocks on it
	done   chan error
	sendMu sync.Mutex
	inSend bool
}

func (c *client) Send(env *gamev1.EventEnvelope) error {
	if c.block != nil {
		c.sendMu.Lock()
		c.inSend = true
		c.sendMu.Unlock()
		select {
		case <-c.block:
		case <-c.ctx.Done():
			return c.ctx.Err()
		}
		c.sendMu.Lock()
		c.inSend = false
		c.sendMu.Unlock()
	}
	select {
	case c.recv <- env:
		return nil
	case <-c.ctx.Done():
		return c.ctx.Err()
	}
}

// subscribe opens a stream for session with last, on a fresh Session
// lifetime unless one is given.
func (f *fixture) subscribe(session string, p auth.Principal, last uint64, world bool, mutate func(*client)) *client {
	c := &client{f: f, id: session, ended: make(chan struct{}), recv: make(chan *gamev1.EventEnvelope, 1024), done: make(chan error, 1)}
	c.ctx, c.cancel = context.WithCancel(context.Background())
	if mutate != nil {
		mutate(c)
	}
	go func() {
		c.done <- f.e.subscribe(c.ctx, session, p, c.ended, &gamev1.SubscribeRequest{SessionId: session, LastEventId: last, World: world}, c)
	}()
	return c
}

// next waits for one frame.
func (c *client) next() *gamev1.EventEnvelope {
	c.f.t.Helper()
	// A frame already delivered comes before the stream's end.
	select {
	case env := <-c.recv:
		return env
	default:
	}
	select {
	case env := <-c.recv:
		return env
	case err := <-c.done:
		c.f.t.Fatalf("stream ended: %v", err)
	case <-time.After(5 * time.Second):
		c.f.t.Fatal("no frame within 5s")
	}
	return nil
}

// quiet asserts nothing arrives for a moment.
func (c *client) quiet() {
	c.f.t.Helper()
	select {
	case env := <-c.recv:
		c.f.t.Fatalf("unexpected frame: %v", env)
	case <-time.After(50 * time.Millisecond):
	}
}

// wait returns the stream's error once it ends.
func (c *client) wait() error {
	c.f.t.Helper()
	select {
	case err := <-c.done:
		return err
	case <-time.After(5 * time.Second):
		c.f.t.Fatal("stream did not end within 5s")
	}
	return nil
}

func (c *client) end() {
	close(c.ended)
	c.cancel()
}

func ids(envs ...*gamev1.EventEnvelope) []uint64 {
	out := make([]uint64, len(envs))
	for i, e := range envs {
		out[i] = e.GetEventId()
	}
	return out
}

func reasonOf(t *testing.T, err error) (connect.Code, string) {
	t.Helper()
	var ce *connect.Error
	if !errors.As(err, &ce) {
		t.Fatalf("not a connect error: %v", err)
	}
	for _, d := range ce.Details() {
		if v, err := d.Value(); err == nil {
			if info, ok := v.(*errdetails.ErrorInfo); ok {
				return ce.Code(), info.GetReason()
			}
		}
	}
	return ce.Code(), ""
}

func counter(t *testing.T, c prometheus.Collector) float64 {
	t.Helper()
	return testutil.ToFloat64(c)
}

// AC-1: a Session perceives from its Character's Room; an Event in another
// Room is not delivered. Perception is the Hub's; this asserts the seam
// carries the Observer through.
func TestScope_RoomDelivery(t *testing.T) {
	f := newFixture(t, nil)
	f.place("a", "alice", "town", "plaza")
	f.place("c", "carol", "town", "hall")
	a := f.subscribe("a", player, 0, false, nil)
	c := f.subscribe("c", player, 0, false, nil)
	defer a.end()
	defer c.end()
	waitFor(t, func() bool { return counter(t, f.hub.Metrics().Subscribers) == 2 }, "two subscriptions")

	id := f.emit(plaza())
	if got := a.next(); got.GetEventId() != id {
		t.Fatalf("a got %v, want %d", got, id)
	}
	c.quiet()
	id2 := f.emit(hall())
	if got := c.next(); got.GetEventId() != id2 {
		t.Fatalf("c got %v, want %d", got, id2)
	}
	a.quiet()
	if got := counter(t, f.e.Metrics().Sent.WithLabelValues(string(sim.EvRoomDescribed))); got != 2 {
		t.Errorf("sent{room_described} = %v", got)
	}
	if got := counter(t, f.e.Metrics().Streams); got != 2 {
		t.Errorf("streams = %v", got)
	}
}

// AC-2: Events arrive in publication order with strictly increasing IDs,
// across ticks.
func TestOrdering(t *testing.T) {
	// Room for the burst: the writer may lag the pump by a few Events
	// under the race detector, and that is not what this test is about.
	f := newFixture(t, func(o *Options) { o.Buffer, o.ResumeWindow = 64, 64 })
	f.place("a", "alice", "town", "plaza")
	a := f.subscribe("a", player, 0, false, nil)
	defer a.end()
	waitFor(t, func() bool { return counter(t, f.hub.Metrics().Subscribers) == 1 }, "subscribed")
	for i := 0; i < 20; i++ {
		f.emit(plaza())
	}
	var last uint64
	for i := 0; i < 20; i++ {
		got := a.next().GetEventId()
		if got <= last {
			t.Fatalf("frame %d: id %d after %d", i, got, last)
		}
		last = got
	}
}

// AC-6: a stream reopened with last_event_id resumes from the next Event
// with no gap and no duplicate — including Events that arrived while no
// stream was open — and a resume point the window no longer reaches, or
// one the Session was never sent, opens with a Resync.
func TestResume(t *testing.T) {
	f := newFixture(t, nil)
	f.place("a", "alice", "town", "plaza")
	a := f.subscribe("a", player, 0, false, nil)
	waitFor(t, func() bool { return counter(t, f.hub.Metrics().Subscribers) == 1 }, "subscribed")
	first := f.emit(plaza())
	second := f.emit(plaza())
	if got := ids(a.next(), a.next()); got[0] != first || got[1] != second {
		t.Fatalf("got %v", got)
	}
	// The client closes its stream; the Session lives on and Events keep
	// being retained for it.
	a.cancel()
	if err := a.wait(); !errors.Is(err, context.Canceled) && connect.CodeOf(err) != connect.CodeCanceled {
		t.Fatalf("closed stream ended with %v", err)
	}
	third := f.emit(plaza())
	fourth := f.emit(plaza())
	f.settled("a", fourth)

	// Resume from the last one seen: third and fourth, nothing else.
	b := f.subscribe("a", player, second, false, func(c *client) { c.ended = a.ended })
	if got := ids(b.next(), b.next()); got[0] != third || got[1] != fourth {
		t.Fatalf("resumed got %v, want [%d %d]", got, third, fourth)
	}
	b.quiet()
	if got := counter(t, f.e.Metrics().Resyncs.WithLabelValues(ResyncWindowExceeded)) + counter(t, f.e.Metrics().Resyncs.WithLabelValues(ResyncNoHistory)); got != 0 {
		t.Fatalf("resyncs = %v on a clean resume", got)
	}
	b.cancel()
	b.wait()

	// Push the window (16) past `first`.
	var pushed uint64
	for i := 0; i < 20; i++ {
		pushed = f.emit(plaza())
	}
	f.settled("a", pushed)
	c := f.subscribe("a", player, first, false, func(c *client) { c.ended = a.ended })
	got := c.next()
	if got.GetResync() == nil || got.GetResync().GetReason() != ResyncWindowExceeded || got.GetResync().GetLastEventId() != first {
		t.Fatalf("expected resync frame, got %v", got)
	}
	// Then live: nothing retained is replayed.
	c.quiet()
	live := f.emit(plaza())
	if got := c.next().GetEventId(); got != live {
		t.Fatalf("after resync got %d, want %d", got, live)
	}
	if got := counter(t, f.e.Metrics().Resyncs.WithLabelValues(ResyncWindowExceeded)); got != 1 {
		t.Errorf("resyncs{window} = %v", got)
	}
	c.cancel()
	c.wait()

	// An ID this Session was never sent: no history for it.
	d := f.subscribe("a", player, live+1000, false, func(c *client) { c.ended = a.ended })
	if got := d.next(); got.GetResync().GetReason() != ResyncNoHistory {
		t.Fatalf("expected no_history resync, got %v", got)
	}
	d.cancel()
	d.wait()
	if !strings.Contains(f.logs.String(), `"msg":"stream resync`) {
		t.Error("no resync log line")
	}
	a.end()
	waitFor(t, func() bool { return f.e.Sessions() == 0 }, "session state freed")
	if got := counter(t, f.hub.Metrics().Subscribers); got != 0 {
		t.Errorf("hub subscribers after session end = %v", got)
	}
}

// AC-4: a client that stops reading trails the ring; past egress.buffer
// its stream is ended with buffer_full, the writer blocked in Send is
// aborted, the warn line names the Session, and a neighbor is untouched.
// The Session's history keeps going, so the reopened stream resumes.
func TestBufferFull_EndsStreamNotSession(t *testing.T) {
	f := newFixture(t, nil)
	f.place("a", "alice", "town", "plaza")
	f.place("b", "bob", "town", "plaza")
	stalled := f.subscribe("a", player, 0, false, func(c *client) { c.block = make(chan struct{}) })
	healthy := f.subscribe("b", player, 0, false, nil)
	defer healthy.end()
	waitFor(t, func() bool { return counter(t, f.hub.Metrics().Subscribers) == 2 }, "subscribed")

	// The first Event parks the stalled writer inside Send; eight more
	// fill its buffer; the ninth exceeds it.
	var sent []uint64
	for i := 0; i < 10; i++ {
		sent = append(sent, f.emit(plaza()))
		healthy.next()
	}
	select {
	case ctx := <-f.aborted:
		if ctx != stalled.ctx {
			t.Fatal("aborted the wrong stream")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stalled stream was not aborted")
	}
	// The abort in production fails the Send; here the fake's Send is
	// released and, finding the stream ended, the writer returns the
	// typed error.
	close(stalled.block)
	err := stalled.wait()
	if code, reason := reasonOf(t, err); code != connect.CodeResourceExhausted || reason != ReasonBufferFull {
		t.Fatalf("stalled stream ended with %v (%s/%s)", err, code, reason)
	}
	if got := counter(t, f.e.Metrics().Drops.WithLabelValues(ReasonBufferFull)); got != 1 {
		t.Errorf("drops{buffer_full} = %v", got)
	}
	// The Session is in the drop state until the client reopens.
	if got := counter(t, f.e.Metrics().InDropState); got != 1 {
		t.Errorf("sessions_in_drop_state after the drop = %v", got)
	}
	logs := f.logs.String()
	if !strings.Contains(logs, `"msg":"stream ended: client not reading, buffer full"`) || !strings.Contains(logs, `"session_id":"a"`) || !strings.Contains(logs, `"buffered":8`) {
		t.Errorf("warn line missing or incomplete:\n%s", logs)
	}
	// Neither the Session nor its fan-out subscription ended.
	if got := counter(t, f.hub.Metrics().Subscribers); got != 2 {
		t.Errorf("hub subscribers = %v; the Session's subscription should survive its stream", got)
	}
	if got := counter(t, f.hub.Metrics().Drops.WithLabelValues(events.ReasonBufferFull)); got != 0 {
		t.Errorf("hub drops{buffer_full} = %v; the pump never stalls", got)
	}
	// The healthy neighbor saw everything and is still open.
	if got := f.emit(plaza()); healthy.next().GetEventId() != got {
		t.Fatal("healthy stream disturbed")
	}
	// last_event_id 0 is a fresh start from now: nothing retained is
	// replayed.
	f.settled("a", sent[len(sent)-1]+1)
	again := f.subscribe("a", player, 0, false, func(c *client) { c.ended = stalled.ended })
	again.quiet()
	if got := counter(t, f.e.Metrics().InDropState); got != 0 {
		t.Errorf("sessions_in_drop_state after reopening = %v", got)
	}
	live := f.emit(plaza())
	healthy.next()
	if got := again.next().GetEventId(); got != live {
		t.Fatalf("reopened stream got %d, want %d", got, live)
	}
	again.cancel()
	again.wait()
	// And a resume from inside the window replays what it missed.
	back := f.subscribe("a", player, sent[len(sent)-1], false, func(c *client) { c.ended = stalled.ended })
	if got := back.next().GetEventId(); got != sent[len(sent)-1]+1 {
		t.Fatalf("resume from %d got %d", sent[len(sent)-1], got)
	}
	back.end()
	waitFor(t, func() bool { return f.e.Sessions() == 1 }, "first session forgotten")
	// A Session that ends while in the drop state leaves it.
	f.place("c", "carol", "town", "plaza")
	gone := f.subscribe("c", player, 0, false, func(c *client) { c.block = make(chan struct{}) })
	waitFor(t, func() bool { return counter(t, f.hub.Metrics().Subscribers) == 2 }, "third subscribed")
	for i := 0; i < 10; i++ {
		f.emit(plaza())
		healthy.next()
	}
	<-f.aborted
	close(gone.block)
	gone.wait()
	waitFor(t, func() bool { return counter(t, f.e.Metrics().InDropState) == 1 }, "third in drop state")
	gone.end()
	waitFor(t, func() bool { return counter(t, f.e.Metrics().InDropState) == 0 }, "drop state released on session end")
}

// A Session closed as revoked: its stream's last frame is
// SubscriberDropped{reason=revoked}, then PERMISSION_DENIED revoked, and
// the drop is counted as revoked — not as the client leaving, and not as
// unavailability.
func TestEndSession_Revoked(t *testing.T) {
	f := newFixture(t, nil)
	f.place("a", "alice", "town", "plaza")
	a := f.subscribe("a", player, 0, false, nil)
	waitFor(t, func() bool { return counter(t, f.e.Metrics().Streams) == 1 }, "stream open")
	id := f.emit(plaza())
	a.next()
	f.e.EndSession("a", ReasonRevoked)
	got := a.next()
	if got.GetSubscriberDropped().GetReason() != ReasonRevoked || got.GetEventId() != 0 {
		t.Fatalf("final frame = %v", got)
	}
	if code, reason := reasonOf(t, a.wait()); code != connect.CodePermissionDenied || reason != ReasonRevoked {
		t.Fatalf("revoked stream ended %s/%s", code, reason)
	}
	if got := counter(t, f.e.Metrics().Drops.WithLabelValues(ReasonRevoked)); got != 1 {
		t.Errorf("drops{revoked} = %v", got)
	}
	if got := counter(t, f.e.Metrics().Drops.WithLabelValues(ReasonClientGone)); got != 0 {
		t.Errorf("drops{client_gone} = %v", got)
	}
	if got := counter(t, f.e.Metrics().InDropState); got != 0 {
		t.Errorf("a revoked Session is not in the drop state: %v", got)
	}
	_ = id
	a.end()
	// No stream: nothing to end.
	f.e.EndSession("a", ReasonRevoked)
	f.e.EndSession("nobody", ReasonRevoked)
}

// A reset that does not return the writer within a heartbeat interval —
// the client has stopped reading its socket and the reset is stuck behind
// the blocked write — escalates to closing the connection.
func TestEscalation_DisconnectsWhenResetDoesNotReturn(t *testing.T) {
	disconnected := make(chan context.Context, 1)
	f := newFixture(t, func(o *Options) {
		o.HeartbeatInterval = 50 * time.Millisecond
		o.Disconnect = func(ctx context.Context) bool { disconnected <- ctx; return true }
	})
	f.place("a", "alice", "town", "plaza")
	stalled := f.subscribe("a", player, 0, false, func(c *client) { c.block = make(chan struct{}) })
	defer stalled.end()
	waitFor(t, func() bool { return counter(t, f.hub.Metrics().Subscribers) == 1 }, "subscribed")
	for i := 0; i < 10; i++ {
		f.emit(plaza())
	}
	<-f.aborted // the reset, which the fake ignores: Send stays blocked
	select {
	case ctx := <-disconnected:
		if ctx != stalled.ctx {
			t.Fatal("disconnected the wrong stream")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no escalation after the heartbeat interval")
	}
	if !strings.Contains(f.logs.String(), "closing the connection") {
		t.Error("no escalation log line")
	}
	// A reset that did return in time escalates nothing.
	f.place("b", "bob", "town", "plaza")
	quick := f.subscribe("b", player, 0, false, func(c *client) { c.block = make(chan struct{}) })
	defer quick.end()
	waitFor(t, func() bool { return counter(t, f.hub.Metrics().Subscribers) == 2 }, "second subscribed")
	for i := 0; i < 10; i++ {
		f.emit(plaza())
	}
	<-f.aborted
	close(quick.block) // the reset "returned" the writer
	quick.wait()
	select {
	case <-disconnected:
		t.Fatal("escalated although the writer returned")
	case <-time.After(150 * time.Millisecond):
	}
}

// One stream per Session: a second Subscribe while one is open is refused.
func TestOneStreamPerSession(t *testing.T) {
	f := newFixture(t, nil)
	a := f.subscribe("a", player, 0, false, nil)
	defer a.end()
	waitFor(t, func() bool { return counter(t, f.e.Metrics().Streams) == 1 }, "first stream")
	b := f.subscribe("a", player, 0, false, func(c *client) { c.ended = a.ended })
	if code, reason := reasonOf(t, b.wait()); code != connect.CodeFailedPrecondition || reason != ReasonAlreadySubscribed {
		t.Fatalf("second stream: %s/%s", code, reason)
	}
}

// AC-7: heartbeats when nothing else has been sent for the interval, and
// they carry the last Tick seen.
func TestHeartbeat(t *testing.T) {
	f := newFixture(t, func(o *Options) { o.HeartbeatInterval = 30 * time.Millisecond })
	f.emit(sim.ScopeWorld()) // tick 1, seen by nobody
	a := f.subscribe("a", player, 0, false, nil)
	defer a.end()
	got := a.next()
	if got.GetHeartbeat() == nil || got.GetEventId() != 0 || got.GetTick() != 1 {
		t.Fatalf("expected heartbeat at tick 1, got %v", got)
	}
	a.next()
	if got := counter(t, f.e.Metrics().Sent.WithLabelValues(TypeHeartbeat)); got < 2 {
		t.Errorf("sent{heartbeat} = %v", got)
	}
}

// AC-9: World visibility is asked for per stream, refused without the
// role, and granted with it — every Event, wherever it is.
func TestWorld(t *testing.T) {
	f := newFixture(t, nil)
	denied := f.subscribe("p", player, 0, true, nil)
	if code, reason := reasonOf(t, denied.wait()); code != connect.CodePermissionDenied || reason != ReasonNotPrivileged {
		t.Fatalf("player world subscribe: %s/%s", code, reason)
	}
	if f.e.Sessions() != 0 {
		t.Fatal("refused subscribe left session state behind")
	}
	g := f.subscribe("g", gm, 0, true, nil)
	defer g.end()
	waitFor(t, func() bool { return counter(t, f.hub.Metrics().Subscribers) == 1 }, "subscribed")
	one, two := f.emit(plaza()), f.emit(hall())
	if got := ids(g.next(), g.next()); got[0] != one || got[1] != two {
		t.Fatalf("gm got %v", got)
	}
}

// A second Subscribe that toggles World visibility while a stream is open
// is refused without disturbing the open stream's perception or history.
func TestWorldToggle_RefusedWhileStreamOpen(t *testing.T) {
	f := newFixture(t, nil)
	f.place("g", "alice", "town", "plaza")
	g := f.subscribe("g", gm, 0, false, nil)
	defer g.end()
	waitFor(t, func() bool { return counter(t, f.hub.Metrics().Subscribers) == 1 }, "subscribed")
	first := f.emit(plaza())
	g.next()
	second := f.subscribe("g", gm, 0, true, func(c *client) { c.ended = g.ended })
	if code, reason := reasonOf(t, second.wait()); code != connect.CodeFailedPrecondition || reason != ReasonAlreadySubscribed {
		t.Fatalf("second stream: %s/%s", code, reason)
	}
	if got := counter(t, f.hub.Metrics().Drops.WithLabelValues(events.ReasonUnsubscribed)); got != 0 {
		t.Fatalf("the open stream's subscription was replaced: drops = %v", got)
	}
	// Still the Character's perception, history intact.
	f.emit(hall())
	g.quiet()
	next := f.emit(plaza())
	if got := g.next().GetEventId(); got != next {
		t.Fatalf("got %d, want %d", got, next)
	}
	g.cancel()
	g.wait()
	f.settled("g", next)
	back := f.subscribe("g", gm, first, false, func(c *client) { c.ended = g.ended })
	if got := back.next().GetEventId(); got != next {
		t.Fatalf("resume after the refused toggle got %d, want %d", got, next)
	}
	back.cancel()
	back.wait()
}

// AC-8 at the seam: the fan-out shutting down ends the stream with
// draining, and so does the Session ending after Drain.
func TestDraining(t *testing.T) {
	f := newFixture(t, nil)
	f.place("a", "alice", "town", "plaza")
	a := f.subscribe("a", player, 0, false, nil)
	waitFor(t, func() bool { return counter(t, f.hub.Metrics().Subscribers) == 1 }, "subscribed")
	f.e.Drain()
	f.hub.Close()
	if code, reason := reasonOf(t, a.wait()); code != connect.CodeUnavailable || reason != ReasonDraining {
		t.Fatalf("stream after hub close: %s/%s", code, reason)
	}
	if got := counter(t, f.e.Metrics().Drops.WithLabelValues(ReasonDraining)); got != 1 {
		t.Errorf("drops{draining} = %v", got)
	}
	a.end()
	// After the fan-out is gone, subscribing is UNAVAILABLE too.
	b := f.subscribe("b", player, 0, false, nil)
	if code, reason := reasonOf(t, b.wait()); code != connect.CodeUnavailable || reason != ReasonDraining {
		t.Fatalf("subscribe after close: %s/%s", code, reason)
	}
}

// Rebind: the routing table moving a Session's Character replaces its
// perception; retained history is discarded and an open stream continues
// from the new one.
func TestRebind(t *testing.T) {
	f := newFixture(t, nil)
	a := f.subscribe("a", player, 0, false, nil)
	defer a.end()
	waitFor(t, func() bool { return counter(t, f.hub.Metrics().Subscribers) == 1 }, "subscribed")
	f.emit(plaza())
	a.quiet() // nowhere yet
	f.place("a", "alice", "town", "plaza")
	f.e.Rebind("a")
	waitFor(t, func() bool { return counter(t, f.hub.Metrics().Drops.WithLabelValues(events.ReasonUnsubscribed)) == 1 }, "old subscription ended")
	id := f.emit(plaza())
	if got := a.next().GetEventId(); got != id {
		t.Fatalf("after rebind got %d, want %d", got, id)
	}
	f.e.Rebind("a") // unchanged: no-op
	if got := counter(t, f.hub.Metrics().Drops.WithLabelValues(events.ReasonUnsubscribed)); got != 1 {
		t.Errorf("no-op rebind resubscribed: drops = %v", got)
	}
	f.e.Rebind("nobody") // no state: no-op
}

// The fan-out dropping the Session's subscription — the pump starved,
// a process problem — ends the stream buffer_full and the next Subscribe
// starts the Session over with a fresh subscription; a resume against the
// discarded history is a Resync, not the drop reported forever (review of
// PR #37).
func TestHubDrop(t *testing.T) {
	f := newFixture(t, nil)
	f.place("a", "alice", "town", "plaza")
	a := f.subscribe("a", player, 0, false, nil)
	waitFor(t, func() bool { return counter(t, f.hub.Metrics().Subscribers) == 1 }, "subscribed")
	first := f.emit(plaza())
	a.next()
	// Starve the pump: the fan-out's buffer (64) is filled while the
	// Session's lock is held.
	sess := func() *session {
		f.e.mu.Lock()
		defer f.e.mu.Unlock()
		return f.e.sessions["a"]
	}()
	sess.mu.Lock()
	for i := 0; i < 70; i++ {
		f.emit(plaza())
	}
	sess.mu.Unlock()
	if code, reason := reasonOf(t, a.wait()); code != connect.CodeResourceExhausted || reason != ReasonBufferFull {
		t.Fatalf("stream after hub drop: %s/%s", code, reason)
	}
	if got := counter(t, f.hub.Metrics().Drops.WithLabelValues(events.ReasonBufferFull)); got != 1 {
		t.Errorf("hub drops{buffer_full} = %v", got)
	}
	// The stream may have ended on its own backlog before the pump saw the
	// fan-out close its channel; wait for the pump to have seen it.
	waitFor(t, func() bool {
		sess.mu.Lock()
		defer sess.mu.Unlock()
		return sess.ended
	}, "pump to observe the fan-out drop")
	b := f.subscribe("a", player, first, false, func(c *client) { c.ended = a.ended })
	if got := b.next(); got.GetResync().GetReason() != ResyncNoHistory {
		t.Fatalf("resume across a fan-out drop: got %v, want no_history resync", got)
	}
	waitFor(t, func() bool { return counter(t, f.hub.Metrics().Subscribers) == 1 }, "resubscribed")
	live := f.emit(plaza())
	if got := b.next().GetEventId(); got != live {
		t.Fatalf("after the fresh subscription got %d, want %d", got, live)
	}
	b.cancel()
	b.wait()
	a.end()
}

func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
