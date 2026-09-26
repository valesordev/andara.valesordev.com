// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package sim_test

import (
	"bytes"
	"strings"
	"testing"

	logv1 "github.com/valesordev/andara/gen/go/andara/log/v1"
	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/simtest"
)

// emptyEngine is CrossingWorld with nobody in it.
func emptyEngine(t *testing.T) *sim.Engine {
	t.Helper()
	e, err := simtest.NewVerbEngine(7)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// AC-5: a never-bound Character is instantiated from andara.core.Character
// at the spawn Room, its arrival is emitted with Room scope and an empty
// from_direction, and the next look answers with the Room, Here: naming
// nobody else — a bystander's look names it.
func TestBind_SpawnsANewBody(t *testing.T) {
	e := emptyEngine(t)
	simtest.Place(e, "bob", "town", "plaza")
	res := step(t, e, simtest.Bind("town", "ch-1", "Aldric", "plaza"))

	arr := ofType(res.Events, sim.EvCharacterArrived)
	if len(arr) != 1 {
		t.Fatalf("want one CharacterArrived, got %v", res.Events)
	}
	p := arr[0].Envelope.GetCharacterArrived()
	if p.GetCharacterName() != "Aldric" || p.GetFromDirection() != "" || p.GetRoomId() != "plaza" || p.GetZoneId() != "town" {
		t.Fatalf("arrival %v", p)
	}
	if arr[0].Scope.Room != (sim.RoomRef{Zone: "town", Room: "plaza"}) || !containsEntity(arr[0].Scope, "ch-1") {
		t.Fatalf("scope %+v: want the Room and the Character", arr[0].Scope)
	}
	if arr[0].Session != "s-ch-1" {
		t.Fatalf("session %q", arr[0].Session)
	}
	ent := e.State().Zones["town"].Entities["ch-1"]
	if ent == nil || ent.Template != sim.CharacterTemplate || ent.Room != "plaza" || ent.Name != "Aldric" || ent.Dormant {
		t.Fatalf("body %+v", ent)
	}

	// Its own look: the Room, Here: bob; bob's look: Here: Aldric.
	desc := step(t, e, simtest.Look("town", "ch-1")).Events[0].Envelope.GetRoomDescribed()
	if desc.GetTitle() != "Market Plaza" || strings.Join(desc.GetOccupants(), ",") != "bob" {
		t.Fatalf("look %v", desc)
	}
	desc = step(t, e, simtest.Look("town", "bob")).Events[0].Envelope.GetRoomDescribed()
	if strings.Join(desc.GetOccupants(), ",") != "Aldric" {
		t.Fatalf("bob's look names %v, want Aldric", desc.GetOccupants())
	}
}

// AC-8: an UnbindCharacter makes the body dormant where it stands, emits
// CharacterLeft with an empty to_direction to the Room, and the Room's
// occupants no longer name it. A dormant body is invisible to look and
// acts for nobody.
func TestUnbind_MakesTheBodyDormant(t *testing.T) {
	e := emptyEngine(t)
	simtest.Place(e, "bob", "town", "plaza")
	step(t, e, simtest.Bind("town", "ch-1", "Aldric", "plaza"))
	step(t, e, simtest.Move("town", "ch-1", "north"))

	res := step(t, e, simtest.Unbind("town", "ch-1"))
	left := ofType(res.Events, sim.EvCharacterLeft)
	if len(left) != 1 || len(res.Events) != 1 {
		t.Fatalf("want exactly one CharacterLeft, got %v", res.Events)
	}
	p := left[0].Envelope.GetCharacterLeft()
	if p.GetCharacterName() != "Aldric" || p.GetToDirection() != "" || p.GetRoomId() != "hall" {
		t.Fatalf("departure %v", p)
	}
	if left[0].Scope.Room != (sim.RoomRef{Zone: "town", Room: "hall"}) {
		t.Fatalf("scope %+v", left[0].Scope)
	}
	ent := e.State().Zones["town"].Entities["ch-1"]
	if !ent.Dormant || ent.DormantSince != e.Tick() || ent.Room != "hall" {
		t.Fatalf("body %+v: want dormant in the hall since tick %d", ent, e.Tick())
	}
	if e.Characters() != (sim.CharacterCounts{Present: 1, Dormant: 1}) {
		t.Fatalf("counts %+v", e.Characters())
	}

	// Invisible: bob walks into the hall and sees nobody.
	step(t, e, simtest.Move("town", "bob", "north"))
	desc := step(t, e, simtest.Look("town", "bob")).Events[0].Envelope.GetRoomDescribed()
	if len(desc.GetOccupants()) != 0 {
		t.Fatalf("a dormant body is listed: %v", desc.GetOccupants())
	}
	// Acts for nobody: a stale Command for it is actor_not_found.
	if rej := rejection(t, step(t, e, simtest.Look("town", "ch-1")).Events); rej.GetCode() != sim.CodeActorNotFound {
		t.Fatalf("a dormant actor's look: %v", rej)
	}
	// A second teardown applies as none: no Event, no change.
	before := zoneBytes(e, "town")
	if res := step(t, e, simtest.Unbind("town", "ch-1")); len(res.Events) != 0 || !bytes.Equal(zoneBytes(e, "town"), before) {
		t.Fatalf("a repeated unbind did something: %v", res.Events)
	}
}

// AC-6: a dormant Character is placed where it went dormant — the hall,
// not the spawn Room — and its arrival is emitted there.
func TestBind_WakesADormantBodyWhereItWas(t *testing.T) {
	e := emptyEngine(t)
	step(t, e, simtest.Bind("town", "ch-1", "Aldric", "plaza"))
	step(t, e, simtest.Move("town", "ch-1", "north"))
	step(t, e, simtest.Unbind("town", "ch-1"))

	res := step(t, e, simtest.Bind("town", "ch-1", "Aldric", "plaza"))
	arr := ofType(res.Events, sim.EvCharacterArrived)
	if len(arr) != 1 || arr[0].Envelope.GetCharacterArrived().GetRoomId() != "hall" || arr[0].Envelope.GetCharacterArrived().GetFromDirection() != "" {
		t.Fatalf("want an arrival in the hall, got %v", res.Events)
	}
	ent := e.State().Zones["town"].Entities["ch-1"]
	if ent.Dormant || ent.Room != "hall" {
		t.Fatalf("body %+v", ent)
	}
	if e.Characters() != (sim.CharacterCounts{Present: 1}) {
		t.Fatalf("counts %+v", e.Characters())
	}
}

// AC-11: a BindCharacter for a body that is already present takes it where
// it stands — the Zone's Entities byte-identical, the Room told nothing.
// The Character alone is told where it stands, which is what moves the
// Session's routing and perception: the body may have walked on after the
// roster's last write (review of PR #43).
func TestBind_PresentBodyIsIdempotent(t *testing.T) {
	e := emptyEngine(t)
	step(t, e, simtest.Bind("town", "ch-1", "Aldric", "plaza"))
	step(t, e, simtest.Move("town", "ch-1", "north"))
	before := zoneBytes(e, "town")
	// The roster still says the plaza; the body is in the hall.
	res := step(t, e, simtest.Bind("town", "ch-1", "Aldric", "plaza"))
	if len(res.Events) != 1 {
		t.Fatalf("a present body's bind emitted %v", res.Events)
	}
	ev := res.Events[0]
	if a := ev.Envelope.GetCharacterArrived(); a.GetRoomId() != "hall" || a.GetCharacterName() != "Aldric" || a.GetFromDirection() != "" {
		t.Fatalf("arrival %v, want the hall where it stands", a)
	}
	if ev.Scope.Zoned() || !containsEntity(ev.Scope, "ch-1") {
		t.Fatalf("scope %+v: the Character alone, never the Room", ev.Scope)
	}
	if !bytes.Equal(zoneBytes(e, "town"), before) {
		t.Fatal("a present body's bind changed the Zone")
	}
	// A bystander in the hall hears nothing: the Room never saw it leave.
	simtest.Place(e, "bob", "town", "hall")
	res = step(t, e, simtest.Bind("town", "ch-1", "Aldric", "plaza"))
	if len(res.Events) != 1 || !containsEntity(res.Events[0].Scope, "ch-1") || res.Events[0].Scope.Zoned() {
		t.Fatalf("with a bystander present: %v", res.Events)
	}
}

// The roster's last knowledge can be stale after a crash: a BindCharacter
// routed to the wrong Zone is re-produced to the Zone that holds the
// dormant body, and applies there, rather than spawning a second one.
func TestBind_ReroutesToTheZoneThatHoldsTheBody(t *testing.T) {
	e := emptyEngine(t)
	step(t, e, simtest.Bind("town", "ch-1", "Aldric", "plaza"))
	res := step(t, e, simtest.Move("town", "ch-1", "east")) // cross-Zone: wilds/trail
	for _, out := range res.Outbound {
		step(t, e, out)
	}
	step(t, e, simtest.Unbind("wilds", "ch-1"))

	res = step(t, e, simtest.Bind("town", "ch-1", "Aldric", "plaza"))
	if len(res.Events) != 0 || len(res.Outbound) != 1 || res.Outbound[0].GetZoneId() != "wilds" || res.Outbound[0].GetBindCharacter() == nil {
		t.Fatalf("want one re-routed BindCharacter to wilds and nothing emitted, got events %v outbound %v", res.Events, res.Outbound)
	}
	if _, dup := e.State().Zones["town"].Entities["ch-1"]; dup {
		t.Fatal("a second body was spawned in town")
	}
	res = step(t, e, res.Outbound[0])
	arr := ofType(res.Events, sim.EvCharacterArrived)
	if len(arr) != 1 || arr[0].Envelope.GetCharacterArrived().GetZoneId() != "wilds" || arr[0].Envelope.GetCharacterArrived().GetRoomId() != "trail" {
		t.Fatalf("want the arrival on the trail, got %v", res.Events)
	}
	if e.Characters() != (sim.CharacterCounts{Present: 1}) {
		t.Fatalf("counts %+v", e.Characters())
	}
}

// A never-bound Character whose spawn Room the Zone does not have, or
// whose World has no Character Template, is rejected and nothing is made.
func TestBind_RejectsWhatItCannotSpawn(t *testing.T) {
	e := emptyEngine(t)
	if rej := rejection(t, step(t, e, simtest.Bind("town", "ch-1", "Aldric", "nowhere")).Events); rej.GetCode() != sim.CodeUnknownRoom {
		t.Fatalf("unknown spawn Room: %v", rej)
	}
	w, err := simtest.CrossingWorld()
	if err != nil {
		t.Fatal(err)
	}
	reg, errs := sim.BuildTemplates(nil, sim.TemplateOptions{})
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	bare := sim.NewEngine(w, reg, sim.Config{Seed: 1, Partitions: simtest.AllPartitions(), Handlers: sim.Handlers()})
	if rej := rejection(t, step(t, bare, simtest.Bind("town", "ch-1", "Aldric", "plaza")).Events); rej.GetCode() != sim.CodeTemplateMissing {
		t.Fatalf("no Template: %v", rej)
	}
	if len(e.State().Zones["town"].Entities)+len(bare.State().Zones["town"].Entities) != 0 {
		t.Fatal("a rejected bind made a body")
	}
}

// Name and dormancy are hashed, and an Entity without either encodes as it
// did before they were state.
func TestEntityCanonicalBytes_NameAndDormancy(t *testing.T) {
	a := sim.EntityState{ID: "x", Template: "t", ContentVersion: "v", Room: "plaza"}
	if got, want := string(sim.EntityCanonicalBytes(a)), "entity\tx\tt\tv\nentity_room\tx\tplaza\n"; got != want {
		t.Fatalf("%q", got)
	}
	named := a
	named.Name = "Aldric"
	dormant := a
	dormant.Dormant, dormant.DormantSince = true, 9
	for _, b := range []sim.EntityState{named, dormant} {
		if bytes.Equal(sim.EntityCanonicalBytes(a), sim.EntityCanonicalBytes(b)) {
			t.Fatalf("%+v hashes like %+v", b, a)
		}
	}
	if !strings.Contains(string(sim.EntityCanonicalBytes(named)), "entity_name\tx\tAldric\n") {
		t.Fatalf("%q", sim.EntityCanonicalBytes(named))
	}
	if !strings.Contains(string(sim.EntityCanonicalBytes(dormant)), "entity_dormant\tx\t9\n") {
		t.Fatalf("%q", sim.EntityCanonicalBytes(dormant))
	}
	if named.DisplayName() != "Aldric" || a.DisplayName() != "x" {
		t.Fatal("display name")
	}
}

// A name crosses a Zone boundary with the body.
func TestArrive_CarriesTheName(t *testing.T) {
	e := emptyEngine(t)
	step(t, e, simtest.Bind("town", "ch-1", "Aldric", "plaza"))
	res := step(t, e, simtest.Move("town", "ch-1", "east"))
	if len(res.Outbound) != 1 || res.Outbound[0].GetArrive().GetEntity().GetName() != "Aldric" {
		t.Fatalf("outbound %v", res.Outbound)
	}
	res = step(t, e, res.Outbound[0])
	if arr := ofType(res.Events, sim.EvCharacterArrived); len(arr) != 1 || arr[0].Envelope.GetCharacterArrived().GetCharacterName() != "Aldric" {
		t.Fatalf("%v", res.Events)
	}
	if e.State().Zones["wilds"].Entities["ch-1"].Name != "Aldric" {
		t.Fatal("the name did not cross")
	}
}

// AC-9: a crash with one Character present and one dormant, replayed from
// the beginning, leaves the present one present where it was and the
// dormant one dormant where it was — the log carries no UnbindCharacter
// for the present one, and nothing at boot invents one. The next
// BindCharacter takes the present body where it stands (AC-11).
func TestReplay_PresentAndDormantSurvive(t *testing.T) {
	script := []*logv1.LoggedCommand{
		simtest.Bind("town", "ch-1", "Aldric", "plaza"),
		simtest.Bind("town", "ch-2", "Brin", "plaza"),
		simtest.Move("town", "ch-1", "north"),
		simtest.Unbind("town", "ch-2"),
	}
	log := simtest.MemorySource{}
	var boundaries []sim.TickCompleted
	live := emptyEngine(t)
	for _, cmd := range script {
		p := sim.PartitionFor(sim.ZoneID(cmd.GetZoneId()))
		rec := sim.Record{Partition: p, Offset: int64(len(log[p])), Command: cmd}
		log[p] = append(log[p], rec)
		res, err := live.Step(sim.TickInput{Records: []sim.Record{rec}})
		if err != nil {
			t.Fatal(err)
		}
		boundaries = append(boundaries, res.Completed)
	}
	// Crash. A fresh process replays.
	r := emptyEngine(t)
	if err := r.Replay(boundaries, log); err != nil {
		t.Fatal(err)
	}
	if r.StateHash() != live.StateHash() {
		t.Fatal("replay diverged")
	}
	town := r.State().Zones["town"].Entities
	if a := town["ch-1"]; a == nil || a.Dormant || a.Room != "hall" {
		t.Fatalf("present body after replay: %+v", town["ch-1"])
	}
	if b := town["ch-2"]; b == nil || !b.Dormant || b.Room != "plaza" {
		t.Fatalf("dormant body after replay: %+v", town["ch-2"])
	}
	if r.Characters() != (sim.CharacterCounts{Present: 1, Dormant: 1}) {
		t.Fatalf("counts %+v", r.Characters())
	}
	// The Account selects the present one again: taken where it stands,
	// and told so — the roster's record says the plaza, the body is in
	// the hall.
	res := step(t, r, simtest.Bind("town", "ch-1", "Aldric", "plaza"))
	if len(res.Events) != 1 || res.Events[0].Envelope.GetCharacterArrived().GetRoomId() != "hall" {
		t.Fatalf("re-binding the present body emitted %v", res.Events)
	}
	desc := step(t, r, simtest.Look("town", "ch-1")).Events[0].Envelope.GetRoomDescribed()
	if desc.GetRoomId() != "hall" {
		t.Fatalf("look after replay: %v", desc)
	}
}

func containsEntity(s sim.Scope, id sim.EntityID) bool {
	for _, e := range s.Entities {
		if e == id {
			return true
		}
	}
	return false
}

// outcomes records every Outcome the engine reports.
type outcomes struct{ got []sim.Outcome }

func (o *outcomes) Begin(sim.ZoneID, sim.Record) func(sim.Outcome) {
	return func(out sim.Outcome) { o.got = append(o.got, out) }
}

func (o *outcomes) last(t *testing.T) sim.Outcome {
	t.Helper()
	if len(o.got) == 0 {
		t.Fatal("no outcome reported")
	}
	return o.got[len(o.got)-1]
}

// The Outcome of an applied BindCharacter says where the body is and what
// the bind did to it — the loop's bind-applied line (AW-SRV-014) — in each
// of the handler's cases; a rejected one, and every other verb, carries
// none.
func TestBind_OutcomeReportsWhatTheBindDid(t *testing.T) {
	e := emptyEngine(t)
	obs := &outcomes{}
	e.SetObserver(obs)
	want := func(zone sim.ZoneID, room sim.RoomID, body sim.BindBody) {
		t.Helper()
		got := obs.last(t).Bind
		if got == nil || *got != (sim.BindResult{Account: "acct-ch-1", Zone: zone, Room: room, Body: body}) {
			t.Fatalf("bind result %+v, want %s/%s %s", got, zone, room, body)
		}
	}

	step(t, e, simtest.Bind("town", "ch-1", "Aldric", "plaza"))
	want("town", "plaza", sim.BindSpawned)
	step(t, e, simtest.Bind("town", "ch-1", "Aldric", "plaza"))
	want("town", "plaza", sim.BindPresent)
	res := step(t, e, simtest.Move("town", "ch-1", "east")) // cross-Zone: wilds/trail
	if obs.last(t).Bind != nil {
		t.Fatalf("a move reported a bind: %+v", obs.last(t).Bind)
	}
	for _, out := range res.Outbound {
		step(t, e, out)
	}
	// Present in another Zone than the roster names.
	step(t, e, simtest.Bind("town", "ch-1", "Aldric", "plaza"))
	want("wilds", "trail", sim.BindPresent)
	step(t, e, simtest.Unbind("wilds", "ch-1"))
	res = step(t, e, simtest.Bind("town", "ch-1", "Aldric", "plaza"))
	want("wilds", "trail", sim.BindRerouted)
	step(t, e, res.Outbound[0])
	want("wilds", "trail", sim.BindWoken)

	step(t, e, simtest.Bind("town", "ch-2", "Brenna", "nowhere"))
	if out := obs.last(t); out.Code != sim.CodeUnknownRoom || out.Bind != nil {
		t.Fatalf("a rejected bind: %+v", out)
	}
}
