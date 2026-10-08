// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package sim_test

import (
	"testing"

	logv1 "github.com/valesordev/andara/gen/go/andara/log/v1"
	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/simtest"
)

// AW-SRV-032 AC-3: a dormant body is removed, CharacterPurged goes to the
// Character alone, no Room hears anything, and the dormant count falls.
func TestPurge_RemovesADormantBody(t *testing.T) {
	e := emptyEngine(t)
	simtest.Place(e, "bob", "town", "plaza")
	step(t, e, simtest.Bind("town", "ch-1", "Aldric", "plaza"))
	step(t, e, simtest.Unbind("town", "ch-1"))
	if e.Characters().Dormant != 1 {
		t.Fatalf("counts %+v", e.Characters())
	}

	res := step(t, e, simtest.Purge("town", "ch-1"))
	if len(res.Events) != 1 {
		t.Fatalf("want exactly CharacterPurged, got %v", res.Events)
	}
	ev := ofType(res.Events, sim.EvCharacterPurged)[0]
	if p := ev.Envelope.GetCharacterPurged(); p.GetCharacterName() != "Aldric" || p.GetZoneId() != "town" || p.GetRoomId() != "plaza" {
		t.Fatalf("event %v", p)
	}
	if ev.Scope.Room != (sim.RoomRef{}) || len(ev.Scope.Entities) != 1 || ev.Scope.Entities[0] != "ch-1" {
		t.Fatalf("scope %+v: want the purged Entity alone", ev.Scope)
	}
	if _, ok := e.State().Zones["town"].Entities["ch-1"]; ok {
		t.Fatal("the body is still in Zone state")
	}
	if c := e.Characters(); c.Dormant != 0 || c.Present != 1 {
		t.Fatalf("counts %+v", c)
	}
	if len(res.Purges) != 1 || res.Purges[0].Kind != sim.PurgeApplied || res.Purges[0].WasPresent {
		t.Fatalf("purges %+v", res.Purges)
	}
}

// AC-9: a present body (a crash left it) is despawned to its Room with reason
// purge, then removed.
func TestPurge_DespawnsAPresentBodyFirst(t *testing.T) {
	e := emptyEngine(t)
	simtest.Place(e, "bob", "town", "plaza")
	step(t, e, simtest.Bind("town", "ch-1", "Aldric", "plaza"))

	res := step(t, e, simtest.Purge("town", "ch-1"))
	if len(res.Events) != 2 || res.Events[0].Type != sim.EvCharacterDespawned || res.Events[1].Type != sim.EvCharacterPurged {
		t.Fatalf("want CharacterDespawned then CharacterPurged, got %v", res.Events)
	}
	d := res.Events[0]
	if d.Envelope.GetCharacterDespawned().GetReason() != sim.DespawnPurge || d.Scope.Room != (sim.RoomRef{Zone: "town", Room: "plaza"}) {
		t.Fatalf("despawn %v scope %+v", d.Envelope.GetCharacterDespawned(), d.Scope)
	}
	if _, ok := e.State().Zones["town"].Entities["ch-1"]; ok {
		t.Fatal("the body is still in Zone state")
	}
	if !res.Purges[0].WasPresent {
		t.Fatalf("purges %+v", res.Purges)
	}
}

// AC-10: a linkdead body is purged the same way, and the deadline Tick that
// would have despawned it produces nothing.
func TestPurge_LinkdeadBodyNeverDespawnsTwice(t *testing.T) {
	e := emptyEngine(t)
	step(t, e, simtest.Bind("town", "ch-1", "Aldric", "plaza"))
	step(t, e, simtest.MarkLinkdead("town", "ch-1", 5, 0, 50))

	res := step(t, e, simtest.Purge("town", "ch-1"))
	if got := ofType(res.Events, sim.EvCharacterDespawned); len(got) != 1 || got[0].Envelope.GetCharacterDespawned().GetReason() != sim.DespawnPurge {
		t.Fatalf("events %v", res.Events)
	}
	if len(res.Linkdead) != 1 || res.Linkdead[0].Kind != sim.LinkdeadEnded {
		t.Fatalf("linkdead %+v", res.Linkdead)
	}
	for i := 0; i < 20; i++ {
		if r := stepEmpty(t, e); len(ofType(r.Events, sim.EvCharacterDespawned)) != 0 {
			t.Fatalf("a second despawn at tick %d", e.Tick())
		}
	}
}

// A body in a Zone other than the one the Command names is re-routed to it.
func TestPurge_ReroutesToTheZoneThatHoldsTheBody(t *testing.T) {
	e := emptyEngine(t)
	step(t, e, simtest.Bind("town", "ch-1", "Aldric", "plaza"))
	step(t, e, simtest.Unbind("town", "ch-1"))

	res := step(t, e, simtest.Purge("docks", "ch-1"))
	if len(res.Events) != 0 || len(res.Outbound) != 1 || res.Outbound[0].GetZoneId() != "town" || res.Outbound[0].GetPurgeCharacter() == nil {
		t.Fatalf("want one re-routed PurgeCharacter to town and nothing emitted, got events %v outbound %v", res.Events, res.Outbound)
	}
	if res.Purges[0].Kind != sim.PurgeRerouted {
		t.Fatalf("purges %+v", res.Purges)
	}
	res = step(t, e, res.Outbound[0])
	if len(ofType(res.Events, sim.EvCharacterPurged)) != 1 {
		t.Fatalf("events %v", res.Events)
	}
}

// A Character with no body is a silent no-op: no Event, no rejection.
func TestPurge_NoBodyIsSilent(t *testing.T) {
	e := emptyEngine(t)
	before := zoneBytes(e, "town")
	res := step(t, e, simtest.Purge("town", "never-bound"))
	if len(res.Events) != 0 || len(res.Outbound) != 0 {
		t.Fatalf("events %v outbound %v", res.Events, res.Outbound)
	}
	if len(res.Purges) != 1 || res.Purges[0].Kind != sim.PurgeNoBody {
		t.Fatalf("purges %+v", res.Purges)
	}
	if string(zoneBytes(e, "town")) != string(before) {
		t.Fatal("a no-op changed the Zone")
	}
}

// A Command for a Character that is not a Character body is not a purge.
func TestPurge_LeavesNonCharactersAlone(t *testing.T) {
	e := emptyEngine(t)
	step(t, e, simtest.Bind("town", "ch-1", "Aldric", "plaza"))
	step(t, e, simtest.Unbind("town", "ch-1"))
	e.State().Zones["town"].Entities["ch-1"].Template = "town.Merchant"
	step(t, e, simtest.Purge("town", "ch-1"))
	if _, ok := e.State().Zones["town"].Entities["ch-1"]; !ok {
		t.Fatal("a purge removed an Entity that is not a Character")
	}
}

// AC-4: the log that produced a purge, replayed from the beginning, reaches
// the same State Hash on the same Tick.
func TestPurge_ReplaysOnTheSameTick(t *testing.T) {
	live := emptyEngine(t)
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
	run(simtest.Unbind("town", "ch-1"))
	run()
	run()
	run(simtest.Purge("town", "ch-1"))
	purgedAt := live.Tick()
	run()

	rec := emptyEngine(t)
	if err := rec.Replay(bounds, simtest.MemorySource(log)); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if rec.State().Hash() != live.State().Hash() {
		t.Fatal("the replayed World differs")
	}
	if _, ok := rec.State().Zones["town"].Entities["ch-1"]; ok {
		t.Fatalf("the replayed World still holds the body (purged at tick %d)", purgedAt)
	}
}

func stepEmpty(t *testing.T, e *sim.Engine) sim.StepResult {
	t.Helper()
	res, err := e.Step(sim.TickInput{})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// AC-9: a purge named for a Zone the body is not in is re-routed to the one
// that holds it, despawns a present body to its Room, and a replay of the log
// from the beginning — the re-routed Command included — reaches the same
// State Hash.
func TestPurge_ReroutedPurgeOfAPresentBodyReplays(t *testing.T) {
	live := emptyEngine(t)
	simtest.Place(live, "bob", "town", "plaza")
	log := map[int32][]sim.Record{}
	var bounds []sim.TickCompleted
	var outbound []*logv1.LoggedCommand
	run := func(cmds ...*logv1.LoggedCommand) sim.StepResult {
		var in sim.TickInput
		for _, c := range append(cmds, outbound...) {
			p := sim.PartitionFor(sim.ZoneID(c.GetZoneId()))
			r := sim.Record{Partition: p, Offset: int64(len(log[p])), Command: c}
			log[p] = append(log[p], r)
			in.Records = append(in.Records, r)
		}
		outbound = nil
		res, err := live.Step(in)
		if err != nil {
			t.Fatal(err)
		}
		outbound = res.Outbound
		bounds = append(bounds, res.Completed)
		return res
	}
	run(simtest.Bind("town", "ch-1", "Aldric", "plaza"))
	// The roster named docks; the body, left present by a crash, is in town.
	res := run(simtest.Purge("docks", "ch-1"))
	if len(res.Events) != 0 || len(outbound) != 1 || outbound[0].GetZoneId() != "town" {
		t.Fatalf("events %v outbound %v", res.Events, outbound)
	}
	res = run()
	if ds := ofType(res.Events, sim.EvCharacterDespawned); len(ds) != 1 || ds[0].Envelope.GetCharacterDespawned().GetReason() != sim.DespawnPurge || ds[0].Scope.Room != (sim.RoomRef{Zone: "town", Room: "plaza"}) {
		t.Fatalf("events %v", res.Events)
	}
	if len(ofType(res.Events, sim.EvCharacterPurged)) != 1 {
		t.Fatalf("events %v", res.Events)
	}

	rec := emptyEngine(t)
	simtest.Place(rec, "bob", "town", "plaza")
	if err := rec.Replay(bounds, simtest.MemorySource(log)); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if rec.State().Hash() != live.State().Hash() {
		t.Fatal("the replayed World differs")
	}
}

// A body between Zones is not purged: the Command is rejected in_transit.
func TestPurge_RejectsABodyInTransit(t *testing.T) {
	e, err := simtest.NewVerbEngine(7)
	if err != nil {
		t.Fatal(err)
	}
	step(t, e, simtest.Bind("town", "ch-1", "Aldric", "plaza"))
	step(t, e, simtest.Move("town", "ch-1", "east")) // leaves town now; the Arrive is outbound
	if _, between := e.State().Zones["town"].Transit["ch-1"]; !between {
		t.Skip("fixture engine does not hand off; the in_transit path is covered by the handoff tests")
	}
	if rej := rejection(t, step(t, e, simtest.Purge("town", "ch-1")).Events); rej.GetCode() != sim.CodeInTransit {
		t.Fatalf("rejection %v", rej)
	}
}
