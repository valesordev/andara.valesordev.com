// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package sim_test

import (
	"strings"
	"testing"

	logv1 "github.com/valesordev/andara/gen/go/andara/log/v1"
	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/simtest"
	"github.com/valesordev/andara/server/store"
)

// The durations every test here marks with, in Ticks: 180 s, 60 s and 300 s
// at 10 Hz are too long to step through, so the ratios are kept and the
// numbers shrunk.
const (
	grace     = 18
	extension = 6
	ceiling   = 30
)

// linkdeadWorld is the verb engine with bob standing in the plaza and Aldric
// bound there, then marked linkdead. It returns the Tick the mark applied.
func linkdeadWorld(t *testing.T, e *sim.Engine) sim.Tick {
	t.Helper()
	simtest.Place(e, "bob", "town", "plaza")
	step(t, e, simtest.Bind("town", "ch-1", "Aldric", "plaza"))
	step(t, e, simtest.MarkLinkdead("town", "ch-1", grace, extension, ceiling))
	return e.Tick()
}

func idle(t *testing.T, e *sim.Engine) sim.StepResult {
	t.Helper()
	res, err := e.Step(sim.TickInput{})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// idleUntil steps with no input until the engine is at tick, returning every
// tick's result.
func idleUntil(t *testing.T, e *sim.Engine, tick sim.Tick) []sim.StepResult {
	t.Helper()
	var out []sim.StepResult
	for e.Tick() < tick {
		out = append(out, idle(t, e))
	}
	return out
}

// AC-1, the sim's half: a MarkLinkdead leaves the body where it stands with
// its four fields set from the applying Tick and the record's durations,
// tells the Room, and look lists the name in both occupants and linkdead.
func TestMarkLinkdead_KeepsTheBodyAndTellsTheRoom(t *testing.T) {
	e := emptyEngine(t)
	simtest.Place(e, "bob", "town", "plaza")
	step(t, e, simtest.Bind("town", "ch-1", "Aldric", "plaza"))
	res := step(t, e, simtest.MarkLinkdead("town", "ch-1", grace, extension, ceiling))
	at := e.Tick()

	evs := ofType(res.Events, sim.EvCharacterLinkdead)
	if len(evs) != 1 || len(res.Events) != 1 {
		t.Fatalf("want exactly one CharacterLinkdead, got %v", res.Events)
	}
	p := evs[0].Envelope.GetCharacterLinkdead()
	if p.GetCharacterName() != "Aldric" || p.GetZoneId() != "town" || p.GetRoomId() != "plaza" {
		t.Fatalf("CharacterLinkdead %v", p)
	}
	if evs[0].Scope.Room != (sim.RoomRef{Zone: "town", Room: "plaza"}) {
		t.Fatalf("scope %+v: want the Room", evs[0].Scope)
	}
	ent := e.State().Zones["town"].Entities["ch-1"]
	want := sim.EntityState{LinkdeadSince: at, LinkdeadDeadline: at + grace, LinkdeadCeiling: at + ceiling, LinkdeadExtension: extension}
	if ent.LinkdeadSince != want.LinkdeadSince || ent.LinkdeadDeadline != want.LinkdeadDeadline ||
		ent.LinkdeadCeiling != want.LinkdeadCeiling || ent.LinkdeadExtension != want.LinkdeadExtension {
		t.Fatalf("body %+v: want since %d, deadline %d, ceiling %d, extension %d", ent, at, at+grace, at+ceiling, extension)
	}
	if !ent.Present() || ent.Room != "plaza" {
		t.Fatalf("body %+v: want present in the plaza", ent)
	}
	if len(res.Linkdead) != 1 || res.Linkdead[0].Kind != sim.LinkdeadEntered || res.Linkdead[0].Character != "ch-1" || res.Linkdead[0].Session != "s-ch-1" {
		t.Fatalf("lifecycle %+v", res.Linkdead)
	}

	desc := step(t, e, simtest.Look("town", "bob")).Events[0].Envelope.GetRoomDescribed()
	if strings.Join(desc.GetOccupants(), ",") != "Aldric" || strings.Join(desc.GetLinkdead(), ",") != "Aldric" {
		t.Fatalf("look: occupants %v, linkdead %v; want Aldric in both", desc.GetOccupants(), desc.GetLinkdead())
	}
}

// AC-17: a MarkLinkdead for a body already linkdead, dormant, or absent
// changes nothing and emits nothing — a drain racing a drop produces two.
func TestMarkLinkdead_IsANoOpOnABodyItCannotMark(t *testing.T) {
	for name, setup := range map[string]func(t *testing.T, e *sim.Engine){
		"already linkdead": func(t *testing.T, e *sim.Engine) { linkdeadWorld(t, e) },
		"dormant": func(t *testing.T, e *sim.Engine) {
			step(t, e, simtest.Bind("town", "ch-1", "Aldric", "plaza"))
			step(t, e, simtest.Unbind("town", "ch-1"))
		},
		"absent": func(*testing.T, *sim.Engine) {},
	} {
		t.Run(name, func(t *testing.T) {
			e := emptyEngine(t)
			setup(t, e)
			before := e.State().Zones["town"].Entities["ch-1"]
			var was sim.EntityState
			if before != nil {
				was = *before
			}
			res := step(t, e, simtest.MarkLinkdead("town", "ch-1", 99, 99, 99))
			if len(res.Events) != 0 || len(res.Linkdead) != 0 {
				t.Fatalf("emitted %v, reported %v", res.Events, res.Linkdead)
			}
			after := e.State().Zones["town"].Entities["ch-1"]
			if (before == nil) != (after == nil) || (after != nil && string(sim.EntityCanonicalBytes(*after)) != string(sim.EntityCanonicalBytes(was))) {
				t.Fatalf("body changed: %+v → %+v", was, after)
			}
		})
	}
}

// AC-2, the sim's half: a BindCharacter for a linkdead body clears the four
// fields and emits CharacterReconnected to the Room in place of
// CharacterArrived. The body never left.
func TestBind_ReconnectsALinkdeadBody(t *testing.T) {
	e := emptyEngine(t)
	linkdeadWorld(t, e)
	res := step(t, e, simtest.Bind("town", "ch-1", "Aldric", "plaza"))
	if len(ofType(res.Events, sim.EvCharacterArrived)) != 0 {
		t.Fatalf("a reconnect emitted CharacterArrived: %v", res.Events)
	}
	evs := ofType(res.Events, sim.EvCharacterReconnected)
	if len(evs) != 1 || len(res.Events) != 1 {
		t.Fatalf("want exactly one CharacterReconnected, got %v", res.Events)
	}
	p := evs[0].Envelope.GetCharacterReconnected()
	if p.GetCharacterName() != "Aldric" || p.GetRoomId() != "plaza" {
		t.Fatalf("CharacterReconnected %v", p)
	}
	// The Room hears it, and so does the Character, whose new Session's
	// routing settles on it.
	if evs[0].Scope.Room != (sim.RoomRef{Zone: "town", Room: "plaza"}) || len(evs[0].Scope.Entities) != 1 || evs[0].Scope.Entities[0] != "ch-1" {
		t.Fatalf("scope %+v", evs[0].Scope)
	}
	ent := e.State().Zones["town"].Entities["ch-1"]
	if ent.Linkdead() || ent.LinkdeadDeadline|ent.LinkdeadCeiling|ent.LinkdeadExtension != 0 || !ent.Present() {
		t.Fatalf("body %+v: want present with the linkdead fields zero", ent)
	}
	if len(res.Linkdead) != 1 || res.Linkdead[0].Kind != sim.LinkdeadReconnected {
		t.Fatalf("lifecycle %+v", res.Linkdead)
	}
	// And the deadline it had no longer applies.
	for _, r := range idleUntil(t, e, e.Tick()+grace+1) {
		if len(r.Events) != 0 {
			t.Fatalf("tick %d emitted %v after the reconnect", r.Tick, r.Events)
		}
	}
}

// AC-3 and AC-4: at its deadline Tick the sim despawns the body in that
// tick — dormant with its position, CharacterDespawned{linkdead} to the
// Room, no longer listed — and the next BindCharacter wakes it there.
func TestLinkdead_DespawnsAtTheDeadline(t *testing.T) {
	e := emptyEngine(t)
	at := linkdeadWorld(t, e)
	for _, r := range idleUntil(t, e, at+grace-1) {
		if len(r.Events) != 0 {
			t.Fatalf("tick %d, before the deadline, emitted %v", r.Tick, r.Events)
		}
	}
	res := idle(t, e)
	if res.Tick != at+grace {
		t.Fatalf("stepped to %d", res.Tick)
	}
	evs := ofType(res.Events, sim.EvCharacterDespawned)
	if len(evs) != 1 || len(res.Events) != 1 {
		t.Fatalf("want exactly one CharacterDespawned at the deadline, got %v", res.Events)
	}
	if p := evs[0].Envelope.GetCharacterDespawned(); p.GetReason() != sim.DespawnLinkdead || p.GetCharacterName() != "Aldric" || p.GetRoomId() != "plaza" {
		t.Fatalf("CharacterDespawned %v", p)
	}
	ent := e.State().Zones["town"].Entities["ch-1"]
	if !ent.Dormant || ent.DormantSince != at+grace || ent.Room != "plaza" || ent.Linkdead() {
		t.Fatalf("body %+v: want dormant in the plaza since %d, linkdead cleared", ent, at+grace)
	}
	if len(res.Linkdead) != 1 || res.Linkdead[0].Kind != sim.LinkdeadEnded || res.Linkdead[0].Reason != sim.DespawnLinkdead || res.Linkdead[0].Since != at {
		t.Fatalf("lifecycle %+v", res.Linkdead)
	}
	desc := step(t, e, simtest.Look("town", "bob")).Events[0].Envelope.GetRoomDescribed()
	if len(desc.GetOccupants()) != 0 {
		t.Fatalf("a despawned body is listed: %v", desc.GetOccupants())
	}

	// AC-4: back where it was.
	arr := ofType(step(t, e, simtest.Bind("town", "ch-1", "Aldric", "hall")).Events, sim.EvCharacterArrived)
	if len(arr) != 1 || arr[0].Envelope.GetCharacterArrived().GetRoomId() != "plaza" {
		t.Fatalf("the rebind arrived %v, want the plaza", arr)
	}
}

// An UnbindCharacter for a linkdead body — the Account's credential revoked,
// say — despawns it with its reason, and ends the grace.
func TestUnbind_EndsALinkdeadBodysGrace(t *testing.T) {
	e := emptyEngine(t)
	linkdeadWorld(t, e)
	res := step(t, e, simtest.Unbind("town", "ch-1"))
	evs := ofType(res.Events, sim.EvCharacterDespawned)
	if len(evs) != 1 || evs[0].Envelope.GetCharacterDespawned().GetReason() != sim.DespawnQuit {
		t.Fatalf("events %v", res.Events)
	}
	if ent := e.State().Zones["town"].Entities["ch-1"]; !ent.Dormant || ent.Linkdead() {
		t.Fatalf("body %+v", ent)
	}
	if len(res.Linkdead) != 1 || res.Linkdead[0].Kind != sim.LinkdeadEnded || res.Linkdead[0].Reason != sim.DespawnQuit {
		t.Fatalf("lifecycle %+v", res.Linkdead)
	}
	for _, r := range idleUntil(t, e, e.Tick()+grace+1) {
		if len(r.Events) != 0 {
			t.Fatalf("tick %d emitted %v after the unbind", r.Tick, r.Events)
		}
	}
}

func combatEngine(t *testing.T) *sim.Engine {
	t.Helper()
	e, err := simtest.NewCombatEngine(7)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// AC-10: each combat interaction makes the deadline
// max(deadline, now + extension), capped at the ceiling — a refresh, not an
// accumulation. A table over the Tick of the blow, from the mark at T.
func TestOnCombatInteraction_RefreshesTheDeadline(t *testing.T) {
	for _, c := range []struct {
		name    string
		blows   []sim.Tick // offsets from T
		want    sim.Tick   // deadline offset from T
		extends int
	}{
		{"early blow leaves the grace", []sim.Tick{2}, grace, 1},
		{"late blow extends", []sim.Tick{15}, 15 + extension, 1},
		{"two blows in one tick do not accumulate", []sim.Tick{15, 15}, 15 + extension, 2},
		{"a later blow refreshes", []sim.Tick{15, 17}, 17 + extension, 2},
		{"capped at the ceiling", []sim.Tick{15, 20, 26}, ceiling, 3},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := combatEngine(t)
			at := linkdeadWorld(t, e)
			extends := 0
			for i := 0; i < len(c.blows); {
				idleUntil(t, e, at+c.blows[i]-1)
				// Every blow landing on this Tick goes in one Step.
				var in sim.TickInput
				p := sim.PartitionFor("town")
				for ; i < len(c.blows) && c.blows[i] == e.Tick()+1-at; i++ {
					in.Records = append(in.Records, sim.Record{Partition: p, Offset: e.State().Offsets[p] + int64(len(in.Records)), Command: simtest.Strike("town", "bob", "ch-1")})
				}
				res, err := e.Step(in)
				if err != nil {
					t.Fatal(err)
				}
				extends += len(res.Linkdead)
			}
			if got := e.State().Zones["town"].Entities["ch-1"].LinkdeadDeadline; got != at+c.want {
				t.Fatalf("deadline T+%d, want T+%d", got-at, c.want)
			}
			if extends != c.extends {
				t.Fatalf("%d extensions reported, want %d", extends, c.extends)
			}
		})
	}
}

// AC-11: under sustained attack the body despawns at its ceiling, with
// linkdead_ceiling.
func TestLinkdead_SustainedAttackDespawnsAtTheCeiling(t *testing.T) {
	e := combatEngine(t)
	at := linkdeadWorld(t, e)
	var last sim.StepResult
	for e.Tick() < at+ceiling {
		last = step(t, e, simtest.Strike("town", "bob", "ch-1"))
		if e.Tick() < at+ceiling && len(ofType(last.Events, sim.EvCharacterDespawned)) != 0 {
			t.Fatalf("despawned at T+%d, before the ceiling", e.Tick()-at)
		}
	}
	evs := ofType(last.Events, sim.EvCharacterDespawned)
	if len(evs) != 1 || evs[0].Envelope.GetCharacterDespawned().GetReason() != sim.DespawnLinkdeadCeiling {
		t.Fatalf("at the ceiling: %v", last.Events)
	}
}

// AC-12: one blow, then silence: the body despawns extension Ticks after the
// blow, not at the deadline the mark set.
func TestLinkdead_OneBlowThenSilenceDespawnsAnExtensionLater(t *testing.T) {
	e := combatEngine(t)
	at := linkdeadWorld(t, e)
	idleUntil(t, e, at+15-1)
	step(t, e, simtest.Strike("town", "bob", "ch-1"))
	for e.Tick() < at+15+extension {
		res := idle(t, e)
		despawned := len(ofType(res.Events, sim.EvCharacterDespawned)) == 1
		if despawned != (res.Tick == at+15+extension) {
			t.Fatalf("tick T+%d: despawned %v", res.Tick-at, despawned)
		}
		if res.Tick == at+grace && despawned {
			t.Fatal("despawned at the original deadline")
		}
	}
	if r := e.State().Zones["town"].Entities["ch-1"]; !r.Dormant {
		t.Fatalf("body %+v", r)
	}
}

// AC-6 and AC-14: a World replayed from its log — killed mid-grace and
// recovered by full-log replay — holds the same four fields at the kill,
// and despawns on the same Tick, with the same State Hash every tick. The
// durations are the record's, so nothing a replaying binary is configured
// with can move them; the sim reads no configuration to apply a
// MarkLinkdead at all.
func TestLinkdead_ReplayDespawnsOnTheSameTick(t *testing.T) {
	live := combatEngine(t)
	simtest.Place(live, "bob", "town", "plaza")
	log := map[int32][]sim.Record{}
	var bounds []sim.TickCompleted
	run := func(cmds ...*logv1.LoggedCommand) sim.StepResult {
		var in sim.TickInput
		for _, c := range cmds {
			p := sim.PartitionFor(sim.ZoneID(c.GetZoneId()))
			r := sim.Record{Partition: p, Offset: int64(len(log[p])), Command: c}
			log[p] = append(log[p], r)
			in.Records = append(in.Records, r)
		}
		res, err := live.Step(in)
		if err != nil {
			t.Fatal(err)
		}
		bounds = append(bounds, res.Completed)
		return res
	}
	run(simtest.Bind("town", "ch-1", "Aldric", "plaza"))
	run(simtest.MarkLinkdead("town", "ch-1", grace, extension, ceiling))
	at := live.Tick()
	for live.Tick() < at+15 {
		run()
	}
	run(simtest.Strike("town", "bob", "ch-1"))
	killed := len(bounds)
	var despawnedAt sim.Tick
	for despawnedAt == 0 {
		if len(ofType(run().Events, sim.EvCharacterDespawned)) == 1 {
			despawnedAt = live.Tick()
		}
	}

	// Recovered: a fresh engine replays the log up to the kill.
	rec := combatEngine(t)
	simtest.Place(rec, "bob", "town", "plaza")
	if err := rec.Replay(bounds[:killed], simtest.MemorySource(log)); err != nil {
		t.Fatalf("replay to the kill: %v", err)
	}
	got := rec.State().Zones["town"].Entities["ch-1"]
	if !got.Linkdead() || got.LinkdeadSince != at || got.LinkdeadDeadline != at+16+extension || got.LinkdeadCeiling != at+ceiling || got.LinkdeadExtension != extension {
		t.Fatalf("recovered body %+v: want since %d, deadline %d, ceiling %d", got, at, at+16+extension, at+ceiling)
	}
	// And onward: every hash agrees, so the despawn lands on the same Tick.
	if err := rec.Replay(bounds[killed:], simtest.MemorySource(log)); err != nil {
		t.Fatalf("replay past the kill: %v", err)
	}
	if ent := rec.State().Zones["town"].Entities["ch-1"]; !ent.Dormant || ent.DormantSince != despawnedAt {
		t.Fatalf("recovered body %+v: want dormant since %d", ent, despawnedAt)
	}
}

// AW-SRV-007: a body the crash left present is listed; one that is linkdead or
// dormant, or not a Character, is not.
func TestPresentCharacters_ListsBodiesWithNoSessionMark(t *testing.T) {
	e := emptyEngine(t)
	simtest.Place(e, "bob", "town", "plaza") // not a Character body: an Entity of another Template
	e.State().Zones["town"].Entities["bob"].Template = "andara.core.Npc"
	step(t, e, simtest.Bind("town", "ch-1", "Aldric", "plaza"))
	step(t, e, simtest.Bind("town", "ch-2", "Brin", "plaza"))
	step(t, e, simtest.MarkLinkdead("town", "ch-2", grace, extension, ceiling))
	got := e.PresentCharacters()
	if len(got) != 1 || got[0].ID != "ch-1" || got[0].Zone != "town" {
		t.Fatalf("PresentCharacters = %v, want only ch-1 in town", got)
	}
	e.State().Zones["town"].Entities["ch-1"].Dormant = true
	if got := e.PresentCharacters(); len(got) != 0 {
		t.Fatalf("a dormant body is listed: %v", got)
	}
}

// AW-SRV-007: a body linkdead at the kill comes back from a snapshot with its
// four linkdead fields as they were.
func TestLinkdead_SurvivesASnapshotRound(t *testing.T) {
	e := emptyEngine(t)
	linkdeadWorld(t, e)
	before := *e.State().Zones["town"].Entities["ch-1"]
	var zs *sim.ZoneState
	for _, s := range e.SnapshotAll(1) {
		raw, err := s.Encode()
		if err != nil {
			t.Fatal(err)
		}
		if s.Zone != "town" {
			continue
		}
		_, body, err := store.Decode(raw)
		if err != nil {
			t.Fatal(err)
		}
		zs = body
	}
	if zs == nil {
		t.Fatal("no town snapshot")
	}
	after := zs.Entities["ch-1"]
	if after == nil || after.LinkdeadSince != before.LinkdeadSince || after.LinkdeadDeadline != before.LinkdeadDeadline ||
		after.LinkdeadCeiling != before.LinkdeadCeiling || after.LinkdeadExtension != before.LinkdeadExtension || !after.Linkdead() {
		t.Fatalf("restored %+v, want the fields of %+v", after, before)
	}
}
