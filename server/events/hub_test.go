// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package events_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"google.golang.org/protobuf/proto"

	auditv1 "github.com/valesordev/andara/gen/go/andara/audit/v1"
	gamev1 "github.com/valesordev/andara/gen/go/andara/game/v1"
	logv1 "github.com/valesordev/andara/gen/go/andara/log/v1"
	"github.com/valesordev/andara/server/auth"
	"github.com/valesordev/andara/server/events"
	"github.com/valesordev/andara/server/recordlog"
	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/simtest"
)

var (
	player = auth.Principal{AccountID: "p", Roles: []auth.Role{auth.RolePlayer}}
	gm     = auth.Principal{AccountID: "g", Roles: []auth.Role{auth.RolePlayer, auth.RoleGameMaster}}
)

type fixture struct {
	t     *testing.T
	hub   *events.Hub
	e     *sim.Engine
	reg   *prometheus.Registry
	audit *recordlog.Memory
}

// newFixture is CrossingWorld with the real handlers, alice and bob in the
// plaza, carol in the hall, and the Hub as the Engine's sink.
func newFixture(t *testing.T, buffer int) *fixture {
	t.Helper()
	e, err := simtest.NewVerbEngine(5)
	if err != nil {
		t.Fatal(err)
	}
	simtest.Place(e, "alice", "town", "plaza")
	simtest.Place(e, "bob", "town", "plaza")
	simtest.Place(e, "carol", "town", "hall")
	f := &fixture{t: t, e: e, reg: prometheus.NewRegistry(), audit: recordlog.NewMemory()}
	f.hub = events.New(events.Options{Buffer: buffer, Registry: f.reg, Audit: auth.NewAuditor(f.audit, nil, nil, nil)})
	t.Cleanup(f.hub.Close)
	e.Subscribe(f.hub)
	return f
}

func (f *fixture) sub(obs events.Observer, p auth.Principal) *events.Subscription {
	f.t.Helper()
	s, err := f.hub.Subscribe(context.Background(), events.Subscriber{Observer: obs, Principal: p, SessionID: "s-" + string(obs.Entity)})
	if err != nil {
		f.t.Fatal(err)
	}
	return s
}

func room(zone, room string) sim.RoomRef {
	return sim.RoomRef{Zone: sim.ZoneID(zone), Room: sim.RoomID(room)}
}

// step applies one Command and flushes the fan-out.
func (f *fixture) step(cmd *logv1.LoggedCommand) sim.StepResult {
	f.t.Helper()
	p := sim.PartitionFor(sim.ZoneID(cmd.GetZoneId()))
	res, err := f.e.Step(sim.TickInput{Records: []sim.Record{{Partition: p, Offset: f.e.State().Offsets[p], Command: cmd}}})
	if err != nil {
		f.t.Fatal(err)
	}
	f.hub.Flush()
	return res
}

// drain reads everything currently buffered.
func drain(s *events.Subscription) []events.Delivery {
	var out []events.Delivery
	for {
		select {
		case d, ok := <-s.Events():
			if !ok {
				return out
			}
			out = append(out, d)
		default:
			return out
		}
	}
}

func types(ds []events.Delivery) []sim.EventType {
	out := make([]sim.EventType, len(ds))
	for i, d := range ds {
		out[i] = d.Type
	}
	return out
}

// AC-1: a move from A to B reaches A's observer as CharacterLeft, B's as
// CharacterArrived, and C's not at all. The mover, addressed on both, sees
// both wherever the Hub currently places it.
func TestScope_RoomMove(t *testing.T) {
	f := newFixture(t, 16)
	inA := f.sub(events.Observer{Entity: "bob", Room: room("town", "plaza")}, player)
	inB := f.sub(events.Observer{Entity: "carol", Room: room("town", "hall")}, player)
	inC := f.sub(events.Observer{Entity: "dave", Room: room("docks", "pier")}, player)
	mover := f.sub(events.Observer{Entity: "alice", Room: room("town", "plaza")}, player)
	world := f.sub(events.Observer{World: true}, gm)

	f.step(simtest.Move("town", "alice", "north"))
	if got := types(drain(world)); len(got) != 2 {
		t.Fatalf("GM saw %v, want every Event (AC-8)", got)
	}
	if got := types(drain(inA)); len(got) != 1 || got[0] != sim.EvCharacterLeft {
		t.Fatalf("A saw %v", got)
	}
	if got := types(drain(inB)); len(got) != 1 || got[0] != sim.EvCharacterArrived {
		t.Fatalf("B saw %v", got)
	}
	if got := drain(inC); len(got) != 0 {
		t.Fatalf("C saw %v", types(got))
	}
	if got := types(drain(mover)); len(got) != 2 || got[0] != sim.EvCharacterLeft || got[1] != sim.EvCharacterArrived {
		t.Fatalf("the mover saw %v", got)
	}
}

// The DoD's leak case: a description is addressed to the one who looked.
// Scoped in the transport, a bystander in the same Room would receive it
// whole; scoped in the sim, the bystander receives nothing.
func TestScope_LookIsAddressedToTheLooker(t *testing.T) {
	f := newFixture(t, 16)
	looker := f.sub(events.Observer{Entity: "alice", Room: room("town", "plaza")}, player)
	bystander := f.sub(events.Observer{Entity: "bob", Room: room("town", "plaza")}, player)
	f.step(simtest.Look("town", "alice"))
	if got := types(drain(looker)); len(got) != 1 || got[0] != sim.EvRoomDescribed {
		t.Fatalf("looker saw %v", got)
	}
	if got := drain(bystander); len(got) != 0 {
		t.Fatalf("bystander in the same Room saw %v", types(got))
	}
	// A rejection likewise reaches the actor alone.
	f.step(simtest.Move("town", "bob", "west"))
	if got := drain(looker); len(got) != 0 {
		t.Fatalf("alice saw bob's rejection: %v", types(got))
	}
	if got := types(drain(bystander)); len(got) != 1 || got[0] != sim.EvCommandRejected {
		t.Fatalf("bob saw %v", got)
	}
}

// AC-7, AC-8: privileged detail is withheld from a player and delivered
// whole to a Game Master; the World subscription is audited.
func TestScope_RedactionAndWorld(t *testing.T) {
	f := newFixture(t, 16)
	f.e.State().Zones["town"].Entities["alice"].Room = "plaza"
	inZone := f.sub(events.Observer{Entity: "bob", Room: room("town", "plaza")}, player)
	elsewhere := f.sub(events.Observer{Entity: "dave", Room: room("docks", "pier")}, player)
	world := f.sub(events.Observer{World: true}, gm)

	// A handler panic: ZoneFaulted, Zone-wide and World.
	handlers := sim.Handlers()
	handlers[sim.KindLook] = func(*sim.ApplyContext, *logv1.LoggedCommand) error { panic("boom") }
	w, _ := simtest.CrossingWorld()
	reg, _ := simtest.Templates()
	e2 := sim.NewEngine(w, reg, sim.Config{Seed: 1, Partitions: simtest.AllPartitions(), Handlers: handlers})
	simtest.Place(e2, "alice", "town", "plaza")
	e2.Subscribe(f.hub)
	p := sim.PartitionFor("town")
	if _, err := e2.Step(sim.TickInput{Records: []sim.Record{{Partition: p, Offset: 0, Command: simtest.Look("town", "alice")}}}); err != nil {
		t.Fatal(err)
	}
	f.hub.Flush()

	got := drain(inZone)
	if len(got) != 1 || got[0].Type != sim.EvZoneFaulted {
		t.Fatalf("player in the Zone saw %v", types(got))
	}
	if got[0].Envelope.GetZoneFaulted().GetZoneId() != "" {
		t.Fatalf("player received privileged detail: %v", got[0].Envelope)
	}
	if got := drain(elsewhere); len(got) != 0 {
		t.Fatalf("player in another Zone saw %v", types(got))
	}
	wg := drain(world)
	if len(wg) != 1 || wg[0].Envelope.GetZoneFaulted().GetZoneId() != "town" {
		t.Fatalf("GM saw %v", wg)
	}
	if got := testutil.ToFloat64(f.hub.Metrics().Redactions.WithLabelValues("zone_faulted")); got != 1 {
		t.Fatalf("redactions = %v", got)
	}
	// The GM's subscription was audited; the players' were not.
	recs := f.audit.Records()
	if len(recs) != 1 {
		t.Fatalf("audit records = %d", len(recs))
	}
	var rec auditv1.AuditRecord
	if err := proto.Unmarshal(recs[0].Value, &rec); err != nil {
		t.Fatal(err)
	}
	if rec.GetAction() != auth.ActionSubscribeWorld || rec.GetActorAccountId() != "g" || rec.GetOutcome() != auth.AuditOK {
		t.Fatalf("audit = %v", &rec)
	}
	// And a player cannot ask for World at all.
	if _, err := f.hub.Subscribe(context.Background(), events.Subscriber{Observer: events.Observer{World: true}, Principal: player}); !errors.Is(err, events.ErrNotPrivileged) {
		t.Fatalf("player world subscription: %v", err)
	}
}

// AC-2: within a tick, Events arrive in the order the sim produced them,
// IDs ascending; across ticks, ascending too.
func TestOrdering(t *testing.T) {
	f := newFixture(t, 64)
	mover := f.sub(events.Observer{Entity: "alice", Room: room("town", "plaza")}, player)
	p := sim.PartitionFor("town")
	in := sim.TickInput{Records: []sim.Record{
		{Partition: p, Offset: 0, Command: simtest.Look("town", "alice")},
		{Partition: p, Offset: 1, Command: simtest.Move("town", "alice", "north")},
		{Partition: p, Offset: 2, Command: simtest.Look("town", "alice")},
	}}
	if _, err := f.e.Step(in); err != nil {
		t.Fatal(err)
	}
	f.step(simtest.Move("town", "alice", "south"))
	got := drain(mover)
	if len(got) != 6 {
		t.Fatalf("deliveries = %v", types(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i].ID <= got[i-1].ID || got[i].Tick < got[i-1].Tick {
			t.Fatalf("out of order at %d: %+v", i, got)
		}
	}
	if got[0].Tick != 1 || got[5].Tick != 2 {
		t.Fatalf("ticks = %d..%d", got[0].Tick, got[5].Tick)
	}
}

// AC-10: a subscriber registered while tick T is being published — after
// the Hub has seen T's first Event — receives tick T+1 onward and never a
// partial T. (Registered before T publishes, it receives all of T, which
// is whole.)
func TestSubscribeMidTick(t *testing.T) {
	f := newFixture(t, 64)
	var late *events.Subscription
	// A sink registered after the Hub runs after it for every Event: on
	// the first Event of tick 2 the Hub has already recorded tick 2.
	f.e.Subscribe(sinkFunc(func(ev sim.Event) {
		if ev.Tick == 2 && late == nil {
			late = f.sub(events.Observer{Entity: "alice", Room: room("town", "plaza")}, player)
		}
	}))
	p := sim.PartitionFor("town")
	f.step(simtest.Look("town", "alice"))
	if _, err := f.e.Step(sim.TickInput{Records: []sim.Record{
		{Partition: p, Offset: 1, Command: simtest.Look("town", "alice")},
		{Partition: p, Offset: 2, Command: simtest.Look("town", "alice")},
	}}); err != nil {
		t.Fatal(err)
	}
	f.hub.Flush()
	if got := drain(late); len(got) != 0 {
		t.Fatalf("subscribed during tick 2, received tick-2 Events: %v", types(got))
	}
	f.step(simtest.Look("town", "alice"))
	got := drain(late)
	if len(got) != 1 || got[0].Tick != 3 {
		t.Fatalf("late subscriber saw %+v, want tick 3 only", got)
	}
	// Registered between ticks: everything from the next tick.
	early := f.sub(events.Observer{Entity: "alice", Room: room("town", "plaza")}, player)
	f.step(simtest.Look("town", "alice"))
	if got := drain(early); len(got) != 1 || got[0].Tick != 4 {
		t.Fatalf("early subscriber saw %+v", got)
	}
}

type sinkFunc func(sim.Event)

func (f sinkFunc) Publish(ev sim.Event) { f(ev) }

// AC-5: a subscriber that stops reading is dropped with SubscriberDropped
// as its last delivery, the counter moves, and the tick never waited.
func TestDropNotBlock(t *testing.T) {
	f := newFixture(t, 4)
	stalled := f.sub(events.Observer{Entity: "alice", Room: room("town", "plaza")}, player)
	healthy := f.sub(events.Observer{Entity: "bob", Room: room("town", "plaza")}, player)
	for i := 0; i < 8; i++ {
		began := time.Now()
		f.step(simtest.Look("town", "alice"))
		if time.Since(began) > time.Second {
			t.Fatalf("step %d took %s with a stalled subscriber", i, time.Since(began))
		}
		drain(healthy)
	}
	got := drain(stalled)
	if len(got) != 5 || got[4].Type != sim.EvSubscriberDropped || got[4].Envelope.GetSubscriberDropped().GetReason() != events.ReasonBufferFull {
		t.Fatalf("stalled subscriber got %v", types(got))
	}
	if _, ok := <-stalled.Events(); ok {
		t.Fatal("stream not closed after drop")
	}
	if stalled.Reason() != events.ReasonBufferFull {
		t.Fatalf("reason = %q", stalled.Reason())
	}
	if got := testutil.ToFloat64(f.hub.Metrics().Drops.WithLabelValues(events.ReasonBufferFull)); got != 1 {
		t.Fatalf("drops{buffer_full} = %v", got)
	}
	if got := testutil.ToFloat64(f.hub.Metrics().Subscribers); got != 1 {
		t.Fatalf("subscribers = %v", got)
	}
	// The healthy one is untouched by its neighbour's fate.
	f.step(simtest.Look("town", "bob"))
	if got := types(drain(healthy)); len(got) != 1 || got[0] != sim.EvRoomDescribed {
		t.Fatalf("healthy saw %v", got)
	}
	f.hub.Unsubscribe(healthy)
	if _, ok := <-healthy.Events(); ok || healthy.Reason() != events.ReasonUnsubscribed {
		t.Fatalf("unsubscribe: open=%v reason=%q", ok, healthy.Reason())
	}
}

// AC-6: 500 subscribers on one Room, a fifth of them stalled; Publish is
// one enqueue and the tick's wall time does not carry the fan-out.
func TestFanoutOutsideTick(t *testing.T) {
	f := newFixture(t, 2)
	subs := make([]*events.Subscription, 500)
	for i := range subs {
		subs[i] = f.sub(events.Observer{Room: room("town", "plaza")}, player)
	}
	for i := 0; i < 10; i++ {
		began := time.Now()
		p := sim.PartitionFor("town")
		if _, err := f.e.Step(sim.TickInput{Records: []sim.Record{{Partition: p, Offset: f.e.State().Offsets[p], Command: simtest.Move("town", "alice", map[bool]string{true: "north", false: "south"}[i%2 == 0])}}}); err != nil {
			t.Fatal(err)
		}
		if d := time.Since(began); d > 250*time.Millisecond {
			t.Fatalf("Step %d took %s with 500 subscribers: fan-out is inside the tick", i, d)
		}
		f.hub.Flush()
		for j, s := range subs {
			if j%5 != 0 {
				drain(s)
			}
		}
	}
	f.hub.Flush()
	if got := testutil.ToFloat64(f.hub.Metrics().Drops.WithLabelValues(events.ReasonBufferFull)); got != 100 {
		t.Fatalf("drops{buffer_full} = %v, want the 100 stalled", got)
	}
	if got := testutil.ToFloat64(f.hub.Metrics().Subscribers); got != 400 {
		t.Fatalf("subscribers = %v", got)
	}
	if n := testutil.CollectAndCount(f.hub.Metrics().FanoutDuration); n == 0 {
		t.Fatal("fan-out duration not observed")
	}
}

// events.max_subscribers is enforced.
func TestMaxSubscribers(t *testing.T) {
	hub := events.New(events.Options{Buffer: 1, MaxSubscribers: 2})
	defer hub.Close()
	for i := 0; i < 2; i++ {
		if _, err := hub.Subscribe(context.Background(), events.Subscriber{Observer: events.Observer{Entity: "x"}, Principal: player}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := hub.Subscribe(context.Background(), events.Subscriber{Observer: events.Observer{Entity: "x"}, Principal: player}); !errors.Is(err, events.ErrTooManySubscribers) {
		t.Fatalf("err = %v", err)
	}
}

// SimulationStopped reaches everyone — redacted for players, whole for the
// GM — and then every subscription ends with reason shutdown.
func TestShutdown(t *testing.T) {
	f := newFixture(t, 4)
	p := f.sub(events.Observer{Entity: "alice", Room: room("town", "plaza")}, player)
	g := f.sub(events.Observer{World: true}, gm)
	f.e.Stop("drain: SIGTERM")
	f.hub.Flush()
	pg, gg := drain(p), drain(g)
	if len(pg) != 1 || pg[0].Type != sim.EvSimulationStopped || pg[0].Envelope.GetSimulationStopped().GetReason() != "" {
		t.Fatalf("player saw %v", pg)
	}
	if len(gg) != 1 || gg[0].Envelope.GetSimulationStopped().GetReason() != "drain: SIGTERM" {
		t.Fatalf("GM saw %v", gg)
	}
	if _, ok := <-p.Events(); ok || p.Reason() != events.ReasonShutdown {
		t.Fatalf("player stream: open=%v reason=%q", ok, p.Reason())
	}
	if _, err := f.hub.Subscribe(context.Background(), events.Subscriber{Observer: events.Observer{Entity: "x"}, Principal: player}); !errors.Is(err, events.ErrClosed) {
		t.Fatalf("subscribe after stop: %v", err)
	}
	f.hub.Close()
	f.hub.Close() // idempotent
}

// An Observer bound to an Entity follows it inside the Hub, in Event
// order: after its Character moves A→B, an Event in A later in the same
// tick is not delivered and an Event in B is — never a consumer's read
// latency behind the sim (AC-1).
func TestObserverFollowsEntity(t *testing.T) {
	f := newFixture(t, 32)
	alice := f.sub(events.Observer{Entity: "alice", Room: room("town", "plaza")}, player)
	p := sim.PartitionFor("town")
	// One tick: alice plaza→hall, then carol hall→plaza. Carol leaving the
	// hall is in alice's new Room; carol arriving in the plaza is in her
	// old one.
	if _, err := f.e.Step(sim.TickInput{Records: []sim.Record{
		{Partition: p, Offset: 0, Command: simtest.Move("town", "alice", "north")},
		{Partition: p, Offset: 1, Command: simtest.Move("town", "carol", "south")},
	}}); err != nil {
		t.Fatal(err)
	}
	f.hub.Flush()
	got := drain(alice)
	names := make([]string, 0, len(got))
	for _, d := range got {
		switch pl := d.Envelope.GetPayload().(type) {
		case *gamev1.EventEnvelope_CharacterLeft:
			names = append(names, "left:"+pl.CharacterLeft.GetCharacterName()+"@"+pl.CharacterLeft.GetRoomId())
		case *gamev1.EventEnvelope_CharacterArrived:
			names = append(names, "arrived:"+pl.CharacterArrived.GetCharacterName()+"@"+pl.CharacterArrived.GetRoomId())
		}
	}
	want := []string{"left:alice@plaza", "arrived:alice@hall", "left:carol@hall"}
	if !slices.Equal(names, want) {
		t.Fatalf("alice perceived %v, want %v", names, want)
	}

	// Cross-Zone: leaving clears the Room, so nothing in the old Room
	// reaches her in transit; arriving sets it, so the new Room does.
	f.step(simtest.Move("town", "alice", "south")) // hall → plaza
	drain(alice)
	res := f.step(simtest.Move("town", "alice", "south")) // plaza → docks/pier, Arrive outbound
	if got := types(drain(alice)); len(got) != 1 || got[0] != sim.EvCharacterLeft {
		t.Fatalf("in transit saw %v", got)
	}
	f.step(simtest.Move("town", "bob", "north")) // plaza: alice must not hear it
	if got := drain(alice); len(got) != 0 {
		t.Fatalf("in transit, alice heard her old Room: %v", types(got))
	}
	f.step(res.Outbound[0]) // arrives at the pier
	if got := types(drain(alice)); len(got) != 1 || got[0] != sim.EvCharacterArrived {
		t.Fatalf("arrival saw %v", got)
	}
	simtest.Place(f.e, "dave", "docks", "pier")
	f.step(simtest.Move("docks", "dave", "south")) // pier → warehouse: alice hears it
	if got := types(drain(alice)); len(got) != 1 || got[0] != sim.EvCharacterLeft {
		t.Fatalf("at the pier saw %v", got)
	}
}

// client_ref reaches the Session whose Command caused the Event and no
// one else — blanked in the Hub, not in every transport. The sim's own
// envelope is untouched.
func TestClientRefOnlyToOwnSession(t *testing.T) {
	f := newFixture(t, 16)
	bob := f.sub(events.Observer{Entity: "bob", Room: room("town", "plaza")}, player)     // session s-bob
	alice := f.sub(events.Observer{Entity: "alice", Room: room("town", "plaza")}, player) // session s-alice
	world := f.sub(events.Observer{World: true}, gm)
	cmd := simtest.Move("town", "bob", "north")
	cmd.SessionId, cmd.ClientRef = "s-bob", "ref-42"
	res := f.step(cmd)
	if res.Events[0].Envelope.GetClientRef() != "ref-42" {
		t.Fatal("the sim's envelope lost its client_ref")
	}
	if got := drain(bob); len(got) != 2 || got[0].Envelope.GetClientRef() != "ref-42" {
		t.Fatalf("bob got %v", got)
	}
	if got := drain(alice); len(got) != 1 || got[0].Envelope.GetClientRef() != "" {
		t.Fatalf("alice got another Session's client_ref: %v", got)
	}
	if got := drain(world); len(got) != 2 || got[0].Envelope.GetClientRef() != "" {
		t.Fatalf("GM got another Session's client_ref: %v", got)
	}
}

// Close delivers what is queued before ending the streams: a subscriber
// promised SimulationStopped gets it even when Close follows Stop with no
// Flush between.
func TestCloseDeliversQueued(t *testing.T) {
	for i := 0; i < 20; i++ {
		f := newFixture(t, 64)
		s := f.sub(events.Observer{Entity: "alice", Room: room("town", "plaza")}, player)
		for j := 0; j < 5; j++ {
			f.step(simtest.Look("town", "alice"))
		}
		f.e.Stop("drain")
		f.hub.Close()
		var got []events.Delivery
		for d := range s.Events() {
			got = append(got, d)
		}
		if len(got) != 6 || got[5].Type != sim.EvSimulationStopped {
			t.Fatalf("run %d: got %v", i, types(got))
		}
		if s.Reason() != events.ReasonShutdown {
			t.Fatalf("reason = %q", s.Reason())
		}
	}
}

// Subscribe racing Close: every subscription that was handed out ends,
// and none is handed out after the fan-out is gone.
func TestSubscribeRacesClose(t *testing.T) {
	hub := events.New(events.Options{Buffer: 1})
	var mu sync.Mutex
	var subs []*events.Subscription
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				s, err := hub.Subscribe(context.Background(), events.Subscriber{Observer: events.Observer{Entity: "x"}, Principal: player})
				if err != nil {
					return
				}
				mu.Lock()
				subs = append(subs, s)
				mu.Unlock()
			}
		}()
	}
	time.Sleep(5 * time.Millisecond)
	hub.Close()
	wg.Wait()
	for _, s := range subs {
		select {
		case _, ok := <-s.Events():
			if ok {
				t.Fatal("delivery after Close")
			}
		case <-time.After(time.Second):
			t.Fatal("a subscription handed out around Close never ended")
		}
	}
}

func TestVisible(t *testing.T) {
	cases := []struct {
		scope sim.Scope
		obs   events.Observer
		want  bool
	}{
		{sim.ScopeRoom("z", "a"), events.Observer{Room: room("z", "a")}, true},
		{sim.ScopeRoom("z", "a"), events.Observer{Room: room("z", "b")}, false},
		{sim.ScopeZone("z"), events.Observer{Room: room("z", "b")}, true},
		{sim.ScopeZone("z"), events.Observer{Room: room("y", "b")}, false},
		{sim.ScopeEntities("e"), events.Observer{Entity: "e"}, true},
		{sim.ScopeEntities("e"), events.Observer{Entity: "f", Room: room("z", "a")}, false},
		{sim.ScopeWorld(), events.Observer{World: true}, true},
		{sim.ScopeWorld(), events.Observer{Room: room("z", "a"), Entity: "e"}, false},
		{sim.ScopeRoom("z", "a"), events.Observer{World: true}, true},
		{sim.ScopeEntities("e"), events.Observer{World: true}, true},
		{sim.Scope{}, events.Observer{Room: room("z", "a"), Entity: "e"}, false},
		{sim.Scope{}, events.Observer{World: true}, true},
		{sim.ScopeRoom("z", "a").With("e"), events.Observer{Entity: "e"}, true},
	}
	for i, c := range cases {
		if got := events.Visible(c.scope, c.obs); got != c.want {
			t.Errorf("case %d: %+v / %+v = %v", i, c.scope, c.obs, got)
		}
	}
	if s := sim.ScopeEntities("b", "a", "b", ""); len(s.Entities) != 2 || s.Entities[0] != "a" {
		t.Fatalf("With: %v", s.Entities)
	}
}

// Unsubscribe from many goroutines while the fan-out is delivering: a send
// and a close on one stream never race (run under -race).
func TestUnsubscribeDuringDelivery(t *testing.T) {
	f := newFixture(t, 2)
	subs := make([]*events.Subscription, 200)
	for i := range subs {
		subs[i] = f.sub(events.Observer{Room: room("town", "plaza")}, player)
	}
	// The wait is for the Unsubscribes themselves, not for the loop that
	// spawns them: closing on the spawn left the gauge assertion below
	// racing calls still in flight, which is what made this test flake.
	var unsubscribed sync.WaitGroup
	unsubscribed.Add(len(subs))
	done := make(chan struct{})
	go func() {
		defer close(done)
		for _, s := range subs {
			go func() {
				defer unsubscribed.Done()
				f.hub.Unsubscribe(s)
			}()
		}
	}()
	p := sim.PartitionFor("town")
	for i := 0; i < 20; i++ {
		dir := map[bool]string{true: "north", false: "south"}[i%2 == 0]
		if _, err := f.e.Step(sim.TickInput{Records: []sim.Record{{Partition: p, Offset: f.e.State().Offsets[p], Command: simtest.Move("town", "alice", dir)}}}); err != nil {
			t.Fatal(err)
		}
	}
	<-done
	unsubscribed.Wait()
	f.hub.Flush()
	for _, s := range subs {
		for range s.Events() {
		}
		if r := s.Reason(); r != events.ReasonUnsubscribed && r != events.ReasonBufferFull {
			t.Fatalf("reason = %q", r)
		}
	}
	if got := testutil.ToFloat64(f.hub.Metrics().Subscribers); got != 0 {
		t.Fatalf("subscribers = %v", got)
	}
}
