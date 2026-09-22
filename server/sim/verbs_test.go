// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package sim_test

import (
	"bytes"
	"errors"
	"slices"
	"strings"
	"testing"

	gamev1 "github.com/valesordev/andara/gen/go/andara/game/v1"
	logv1 "github.com/valesordev/andara/gen/go/andara/log/v1"
	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/simtest"
)

// verbEngine is CrossingWorld with alice and bob in the plaza.
func verbEngine(t *testing.T) *sim.Engine {
	t.Helper()
	e, err := simtest.NewVerbEngine(7)
	if err != nil {
		t.Fatal(err)
	}
	simtest.Place(e, "alice", "town", "plaza")
	simtest.Place(e, "bob", "town", "plaza")
	return e
}

// step applies one Command at the next offset on its Zone's Partition.
func step(t *testing.T, e *sim.Engine, cmd *logv1.LoggedCommand) sim.StepResult {
	t.Helper()
	p := sim.PartitionFor(sim.ZoneID(cmd.GetZoneId()))
	res, err := e.Step(sim.TickInput{Records: []sim.Record{{Partition: p, Offset: e.State().Offsets[p], Command: cmd}}})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func ofType(events []sim.Event, et sim.EventType) []sim.Event {
	var out []sim.Event
	for _, ev := range events {
		if ev.Type == et {
			out = append(out, ev)
		}
	}
	return out
}

func rejection(t *testing.T, events []sim.Event) *gamev1.CommandRejected {
	t.Helper()
	rs := ofType(events, sim.EvCommandRejected)
	if len(rs) != 1 {
		t.Fatalf("want one CommandRejected, got %d in %v", len(rs), events)
	}
	return rs[0].Envelope.GetCommandRejected()
}

// zoneBytes is the canonical form of one Zone's Entities — what a
// rejection must leave unchanged, where the State Hash necessarily moves
// (tick, offsets, next event ID).
func zoneBytes(e *sim.Engine, zone string) []byte {
	var b bytes.Buffer
	z := e.State().Zones[sim.ZoneID(zone)]
	ids := make([]string, 0, len(z.Entities))
	for id := range z.Entities {
		ids = append(ids, string(id))
	}
	slices.Sort(ids)
	for _, id := range ids {
		b.Write(sim.EntityCanonicalBytes(*z.Entities[sim.EntityID(id)]))
	}
	return b.Bytes()
}

// AC-1: look emits exactly one RoomDescribed with title, description,
// every Exit, and every other Entity present.
func TestLook_DescribesRoom(t *testing.T) {
	e := verbEngine(t)
	before := zoneBytes(e, "town")
	res := step(t, e, simtest.Look("town", "alice"))
	if len(res.Events) != 1 || res.Events[0].Type != sim.EvRoomDescribed {
		t.Fatalf("want exactly one RoomDescribed, got %v", res.Events)
	}
	rd := res.Events[0].Envelope.GetRoomDescribed()
	if rd.GetTitle() != "Market Plaza" || rd.GetDescription() != "A dusty square of packed earth." {
		t.Fatalf("wrong room: %v", rd)
	}
	if !slices.Equal(rd.GetExits(), []string{"east", "north", "south"}) {
		t.Fatalf("exits = %v", rd.GetExits())
	}
	if !slices.Equal(rd.GetOccupants(), []string{"bob"}) {
		t.Fatalf("occupants = %v (the viewer is not an occupant of its own description)", rd.GetOccupants())
	}
	if res.Events[0].Envelope.GetClientRef() != "ref-alice" {
		t.Fatalf("client_ref not echoed: %v", res.Events[0].Envelope)
	}
	if !bytes.Equal(zoneBytes(e, "town"), before) {
		t.Fatal("look moved something")
	}
}

// AC-2: move north on tick T puts the Character in the target Room at the
// end of T and emits CharacterLeft and CharacterArrived.
func TestMove_InZone(t *testing.T) {
	e := verbEngine(t)
	res := step(t, e, simtest.Move("town", "alice", "north"))
	if got := e.State().Zones["town"].Entities["alice"].Room; got != "hall" {
		t.Fatalf("alice is in %q at the end of tick %d, want hall", got, res.Tick)
	}
	if e.State().Zones["town"].Entities["bob"].Room != "plaza" {
		t.Fatal("bob moved")
	}
	left, arrived := ofType(res.Events, sim.EvCharacterLeft), ofType(res.Events, sim.EvCharacterArrived)
	if len(left) != 1 || len(arrived) != 1 || len(res.Events) != 2 {
		t.Fatalf("events = %v", res.Events)
	}
	l, a := left[0].Envelope.GetCharacterLeft(), arrived[0].Envelope.GetCharacterArrived()
	if l.GetRoomId() != "plaza" || l.GetToDirection() != "north" || l.GetCharacterName() != "alice" || l.GetZoneId() != "town" {
		t.Fatalf("CharacterLeft = %v", l)
	}
	if a.GetRoomId() != "hall" || a.GetFromDirection() != "south" || a.GetCharacterName() != "alice" || a.GetZoneId() != "town" {
		t.Fatalf("CharacterArrived = %v", a)
	}
	if left[0].ID >= arrived[0].ID {
		t.Fatal("left must precede arrived")
	}
	// bob now sees an empty plaza; alice sees the hall.
	rd := step(t, e, simtest.Look("town", "bob")).Events[0].Envelope.GetRoomDescribed()
	if len(rd.GetOccupants()) != 0 {
		t.Fatalf("bob sees %v", rd.GetOccupants())
	}
	if rd = step(t, e, simtest.Look("town", "alice")).Events[0].Envelope.GetRoomDescribed(); rd.GetRoomId() != "hall" || !slices.Equal(rd.GetExits(), []string{"south"}) {
		t.Fatalf("alice sees %v", rd)
	}
}

// AC-3, AC-8: a move through no Exit changes nothing, consumes its
// offset, and rejects with no_such_exit naming the Direction.
func TestMove_NoSuchExit(t *testing.T) {
	e := verbEngine(t)
	before := zoneBytes(e, "town")
	p := sim.PartitionFor("town")
	res := step(t, e, simtest.Move("town", "alice", "west"))
	if !bytes.Equal(zoneBytes(e, "town"), before) {
		t.Fatal("a rejected move changed state")
	}
	if e.State().Offsets[p] != 1 || res.Completed.CommandsApplied != 1 {
		t.Fatalf("a post-log rejection consumes its offset: offsets=%v applied=%d", e.State().Offsets, res.Completed.CommandsApplied)
	}
	rej := rejection(t, res.Events)
	if rej.GetCode() != sim.CodeNoSuchExit || !strings.Contains(rej.GetMessage(), "west") {
		t.Fatalf("rejection = %v", rej)
	}
	if len(res.Events) != 1 {
		t.Fatalf("rejection is never partial: %v", res.Events)
	}
}

// Information leak: a rejection names what the actor attempted and
// nothing the actor cannot perceive — no Room ID, no Zone ID, no other
// Entity.
func TestRejection_MessagesLeakNothing(t *testing.T) {
	e := verbEngine(t)
	simtest.Place(e, "carol", "town", "hall")
	cases := []*logv1.LoggedCommand{
		simtest.Move("town", "alice", "west"),
		simtest.Move("town", "nobody", "north"),
		simtest.Look("wilds", "alice"),
	}
	for _, cmd := range cases {
		msg := rejection(t, step(t, e, cmd).Events).GetMessage()
		for _, secret := range []string{"hall", "plaza", "trail", "pier", "carol", "bob", "town", "wilds", "docks", "#"} {
			if strings.Contains(msg, secret) {
				t.Errorf("%v: message %q reveals %q", cmd.GetCommand(), msg, secret)
			}
		}
	}
}

// actor_not_found: the actor is not in the Zone the Command named.
func TestMove_ActorNotFound(t *testing.T) {
	e := verbEngine(t)
	rej := rejection(t, step(t, e, simtest.Look("wilds", "alice")).Events)
	if rej.GetCode() != sim.CodeActorNotFound {
		t.Fatalf("code = %s", rej.GetCode())
	}
	if e.State().Zones["wilds"].Entities["alice"] != nil {
		t.Fatal("a rejection created an Entity")
	}
}

// AC-9: a cross-Zone move removes the Character from the source Zone and
// produces an Arrive to the target Partition; the arrival resolves on a
// later tick, never in the same Step, even though this process owns both.
func TestMove_CrossZone(t *testing.T) {
	e := verbEngine(t)
	res := step(t, e, simtest.Move("town", "alice", "east"))
	if e.State().Zones["town"].Entities["alice"] != nil {
		t.Fatal("alice is still in town after leaving")
	}
	if e.State().Zones["wilds"].Entities["alice"] != nil {
		t.Fatal("alice arrived in the same tick — that is a synchronous cross-Zone call (ADR-0001 rule 4)")
	}
	if len(ofType(res.Events, sim.EvCharacterArrived)) != 0 || len(ofType(res.Events, sim.EvCharacterLeft)) != 1 {
		t.Fatalf("tick %d events = %v", res.Tick, res.Events)
	}
	if len(res.Outbound) != 1 {
		t.Fatalf("outbound = %v", res.Outbound)
	}
	out := res.Outbound[0]
	arr := out.GetArrive()
	if out.GetZoneId() != "wilds" || out.GetActorId() != "alice" || arr == nil || arr.GetRoomId() != "trail" || arr.GetFromDirection() != "west" {
		t.Fatalf("outbound = %v", out)
	}
	if arr.GetEntity().GetId() != "alice" || arr.GetEntity().GetTemplate() != "andara.core.Character" {
		t.Fatalf("the Entity did not travel: %v", arr.GetEntity())
	}
	if out.GetClientRef() != "" || out.GetSessionId() != "" {
		// simtest.Move sets neither; a real one carries both. Checked below.
		t.Fatalf("correlation invented: %v", out)
	}

	// Between departure and arrival the actor is nowhere: a Command on
	// either Zone is actor_not_found.
	if rej := rejection(t, step(t, e, simtest.Look("town", "alice")).Events); rej.GetCode() != sim.CodeActorNotFound {
		t.Fatalf("in transit, town: %v", rej)
	}

	// The next tick on the target Partition places it.
	res2 := step(t, e, out)
	got := e.State().Zones["wilds"].Entities["alice"]
	if got == nil || got.Room != "trail" || got.Template != "andara.core.Character" || got.ContentVersion != "core@1" {
		t.Fatalf("alice in wilds = %+v", got)
	}
	arrived := ofType(res2.Events, sim.EvCharacterArrived)
	if len(arrived) != 1 || len(res2.Events) != 1 {
		t.Fatalf("tick %d events = %v", res2.Tick, res2.Events)
	}
	a := arrived[0].Envelope.GetCharacterArrived()
	if a.GetZoneId() != "wilds" || a.GetRoomId() != "trail" || a.GetFromDirection() != "west" || a.GetCharacterName() != "alice" {
		t.Fatalf("CharacterArrived = %v", a)
	}
	// And she can act there.
	rd := step(t, e, simtest.Look("wilds", "alice")).Events[0].Envelope.GetRoomDescribed()
	if rd.GetRoomId() != "trail" {
		t.Fatalf("alice sees %v", rd)
	}
}

// The Arrive carries the correlation of the Move that caused it, so the
// arrival Event reaches the Session that moved.
func TestMove_CrossZoneCarriesCorrelation(t *testing.T) {
	e := verbEngine(t)
	cmd := simtest.Move("town", "alice", "south")
	cmd.SessionId, cmd.ClientRef, cmd.TraceId = "sess-1", "ref-9", "00-aa-bb-01"
	out := step(t, e, cmd).Outbound[0]
	if out.GetSessionId() != "sess-1" || out.GetClientRef() != "ref-9" || out.GetTraceId() != "00-aa-bb-01" {
		t.Fatalf("outbound = %v", out)
	}
	res := step(t, e, out)
	if res.Events[0].Envelope.GetClientRef() != "ref-9" {
		t.Fatalf("arrival not correlated: %v", res.Events[0].Envelope)
	}
}

// The Entity that crosses is the Entity that left: Components and all,
// hashed identically to one that had been there all along.
func TestArrive_RebuildsEntityExactly(t *testing.T) {
	e := verbEngine(t)
	reg, _ := simtest.Templates()
	tmpl, _ := reg.Get("town.Merchant")
	ent := sim.Instantiate(tmpl, "merchant", "town@3")
	ent.Room = "plaza"
	e.State().Zones["town"].Entities["merchant"] = &ent
	want := ent
	want.Room = "pier"

	out := step(t, e, simtest.Move("town", "merchant", "south")).Outbound[0]
	step(t, e, out)
	got := e.State().Zones["docks"].Entities["merchant"]
	if got == nil || !bytes.Equal(sim.EntityCanonicalBytes(*got), sim.EntityCanonicalBytes(want)) {
		t.Fatalf("after crossing:\n%s\nwant:\n%s", sim.EntityCanonicalBytes(*got), sim.EntityCanonicalBytes(want))
	}
}

// An Arrive whose Room is gone sends the Entity back where it came from,
// once; the bounce carries no origin, so a second failure rejects.
func TestArrive_BouncesOnceThenRejects(t *testing.T) {
	e := verbEngine(t)
	out := step(t, e, simtest.Move("town", "alice", "east")).Outbound[0]
	out.GetArrive().RoomId = "vanished"
	res := step(t, e, out)
	if rej := rejection(t, res.Events); rej.GetCode() != sim.CodeUnknownRoom {
		t.Fatalf("rejection = %v", rej)
	}
	if len(res.Outbound) != 1 || res.Outbound[0].GetZoneId() != "town" || res.Outbound[0].GetArrive().GetRoomId() != "plaza" {
		t.Fatalf("no bounce: %v", res.Outbound)
	}
	back := res.Outbound[0]
	if back.GetArrive().GetOriginZoneId() != "" {
		t.Fatal("a bounce must not carry an origin, or two gone Rooms would ping-pong forever")
	}
	if e.State().Zones["wilds"].Entities["alice"] != nil {
		t.Fatal("placed in a Room that does not exist")
	}
	res = step(t, e, back)
	if got := e.State().Zones["town"].Entities["alice"]; got == nil || got.Room != "plaza" {
		t.Fatalf("not returned: %+v", got)
	}
	if a := ofType(res.Events, sim.EvCharacterArrived); len(a) != 1 || a[0].Envelope.GetCharacterArrived().GetFromDirection() != "east" {
		t.Fatalf("return arrival = %v", res.Events)
	}

	// A bounce whose own Room is gone is the end of the line: rejected,
	// reported, no further outbound.
	back.GetArrive().RoomId = "also-vanished"
	e2 := verbEngine(t)
	res = step(t, e2, back)
	if rej := rejection(t, res.Events); rej.GetCode() != sim.CodeUnknownRoom || len(res.Outbound) != 0 {
		t.Fatalf("second failure: %v outbound=%v", rej, res.Outbound)
	}
}

// AC-11: validate and apply are unreachable without a consumed Record.
// The handlers refuse an ApplyContext Step did not build, before reading
// or touching anything.
func TestHandlers_RefuseUnconsumedContext(t *testing.T) {
	e := verbEngine(t)
	for kind, h := range sim.Handlers() {
		var cmd *logv1.LoggedCommand
		switch kind {
		case sim.KindLook:
			cmd = simtest.Look("town", "alice")
		case sim.KindMove:
			cmd = simtest.Move("town", "alice", "north")
		default:
			cmd = &logv1.LoggedCommand{ZoneId: "town", ActorId: "alice", Command: &logv1.LoggedCommand_Arrive{Arrive: &logv1.Arrive{RoomId: "plaza"}}}
		}
		// Everything a handler could want, except that the context did not
		// come from Step.
		forged := &sim.ApplyContext{
			Tick: 1, World: nil, Zone: e.State().Zones["town"], State: e.State(), RNG: e.State().RNG,
			Record: sim.Record{Partition: sim.PartitionFor("town"), Offset: 0, Command: cmd},
		}
		before := zoneBytes(e, "town")
		err := h(forged, cmd)
		if !errors.Is(err, sim.ErrNotConsumed) {
			t.Errorf("%s: forged context: err = %v, want ErrNotConsumed", kind, err)
		}
		if !bytes.Equal(zoneBytes(e, "town"), before) {
			t.Errorf("%s: forged context changed state", kind)
		}
		if forged.Consumed() {
			t.Errorf("%s: a forged context reports consumed", kind)
		}
	}
}

// The verb table's kinds are exactly the handlers, and KindOf names the
// arrive arm.
func TestHandlers_CoverEveryArm(t *testing.T) {
	h := sim.Handlers()
	for _, cmd := range []*logv1.LoggedCommand{
		{Command: &logv1.LoggedCommand_Look{Look: &logv1.Look{}}},
		{Command: &logv1.LoggedCommand_Move{Move: &logv1.Move{}}},
		{Command: &logv1.LoggedCommand_Arrive{Arrive: &logv1.Arrive{}}},
		{Command: &logv1.LoggedCommand_BindCharacter{BindCharacter: &logv1.BindCharacter{}}},
		{Command: &logv1.LoggedCommand_UnbindCharacter{UnbindCharacter: &logv1.UnbindCharacter{}}},
	} {
		if _, ok := h[sim.KindOf(cmd)]; !ok {
			t.Errorf("no handler for %T", cmd.Command)
		}
	}
	if sim.KindOf(&logv1.LoggedCommand{}) != "" {
		t.Fatal("empty oneof has a kind")
	}
}

// Sentinels match by code.
func TestRejectError_Is(t *testing.T) {
	err := error(&sim.RejectError{Code: sim.CodeNoSuchExit, Stage: sim.StageValidate, Message: "there is no exit west"})
	if !errors.Is(err, sim.ErrNoSuchExit) || errors.Is(err, sim.ErrActorNotFound) {
		t.Fatal("RejectError.Is")
	}
	if !slices.IsSorted(sim.RejectCodes()) {
		t.Fatal("RejectCodes must be sorted")
	}
}

// Position is hashed, and an Entity without one hashes as it did before
// position was state.
func TestEntityCanonicalBytes_Position(t *testing.T) {
	a := sim.EntityState{ID: "x", Template: "t", ContentVersion: "v"}
	b := a
	b.Room = "plaza"
	if bytes.Equal(sim.EntityCanonicalBytes(a), sim.EntityCanonicalBytes(b)) {
		t.Fatal("position is not hashed")
	}
	if got, want := string(sim.EntityCanonicalBytes(a)), "entity\tx\tt\tv\n"; got != want {
		t.Fatalf("an Entity nowhere encodes %q, want %q", got, want)
	}
	if !strings.Contains(string(sim.EntityCanonicalBytes(b)), "entity_room\tx\tplaza\n") {
		t.Fatalf("%q", sim.EntityCanonicalBytes(b))
	}
}

// Two Engines applying the same verbs hash identically — the verbs read
// no clock and iterate no map in an order-dependent way.
func TestVerbs_Deterministic(t *testing.T) {
	run := func() [32]byte {
		e := verbEngine(t)
		simtest.Place(e, "carol", "town", "hall")
		simtest.Place(e, "dave", "town", "plaza")
		for _, cmd := range []*logv1.LoggedCommand{
			simtest.Look("town", "alice"), simtest.Move("town", "alice", "north"), simtest.Move("town", "bob", "west"),
			simtest.Move("town", "dave", "east"), simtest.Look("town", "carol"),
		} {
			res := step(t, e, cmd)
			for _, out := range res.Outbound {
				step(t, e, out)
			}
		}
		return e.StateHash()
	}
	first, second := run(), run()
	if first != second {
		t.Fatal("hash differs between runs")
	}
}
