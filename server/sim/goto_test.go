// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package sim_test

import (
	"bytes"
	"testing"

	logv1 "github.com/valesordev/andara/gen/go/andara/log/v1"
	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/simtest"
)

// AW-SRV-036: goto, after the log.

func types(evs []sim.Event) []sim.EventType {
	out := make([]sim.EventType, len(evs))
	for i, ev := range evs {
		out[i] = ev.Type
	}
	return out
}

// AC-2: within the Zone, the jump is a relocation: left and arrived with no
// direction, the bystanders on each side, then the new Room described to the
// jumper alone. Nothing is produced.
func TestGoto_InZone(t *testing.T) {
	e := verbEngine(t)
	res := step(t, e, simtest.Goto("town", "alice", "town", "hall"))
	if got := e.State().Zones["town"].Entities["alice"].Room; got != "hall" {
		t.Fatalf("alice is in %s", got)
	}
	if len(res.Outbound) != 0 {
		t.Fatalf("an in-Zone goto produced %v", res.Outbound)
	}
	got := types(res.Events)
	if len(got) != 3 || got[0] != sim.EvCharacterLeft || got[1] != sim.EvCharacterArrived || got[2] != sim.EvRoomDescribed {
		t.Fatalf("events = %v", got)
	}
	l, a := res.Events[0], res.Events[1]
	if l.Envelope.GetCharacterLeft().GetToDirection() != "" || l.Scope.Room != (sim.RoomRef{Zone: "town", Room: "plaza"}) {
		t.Errorf("left = %v", l)
	}
	if a.Envelope.GetCharacterArrived().GetFromDirection() != "" || a.Scope.Room != (sim.RoomRef{Zone: "town", Room: "hall"}) {
		t.Errorf("arrived = %v", a)
	}
	d := res.Events[2]
	if d.Envelope.GetRoomDescribed().GetTitle() != "Town Hall" || len(d.Scope.Entities) != 1 || d.Scope.Entities[0] != "alice" || d.Scope.Room != (sim.RoomRef{}) {
		t.Errorf("description = %v", d)
	}
	if d.Envelope.GetClientRef() != "ref-goto" {
		t.Errorf("client_ref not echoed: %v", d.Envelope)
	}
}

// AC-1: across Zones, the source applies the departure and produces an
// Arrive with no direction, the origin, and the Command's trace; the target
// places the jumper and describes The Pier.
func TestGoto_CrossZone(t *testing.T) {
	e := verbEngine(t)
	res := step(t, e, simtest.Goto("town", "alice", "docks", "pier"))
	if _, still := e.State().Zones["town"].Entities["alice"]; still {
		t.Fatal("alice is still in town")
	}
	if got := types(res.Events); len(got) != 1 || got[0] != sim.EvCharacterLeft {
		t.Fatalf("source events = %v", got)
	}
	if len(res.Outbound) != 1 {
		t.Fatalf("outbound = %v", res.Outbound)
	}
	out := res.Outbound[0]
	arr := out.GetArrive()
	if out.GetZoneId() != "docks" || arr.GetRoomId() != "pier" || arr.GetFromDirection() != "" || arr.GetOriginZoneId() != "town" ||
		arr.GetOriginRoomId() != "plaza" || out.GetTraceId() != "00-trace" || out.GetSessionId() != "s-alice" || out.GetClientRef() != "ref-goto" {
		t.Fatalf("Arrive = %v", out)
	}
	res = step(t, e, out)
	if got := e.State().Zones["docks"].Entities["alice"]; got == nil || got.Room != "pier" {
		t.Fatalf("alice in docks = %+v", got)
	}
	if got := types(res.Events); len(got) != 2 || got[0] != sim.EvCharacterArrived || got[1] != sim.EvRoomDescribed {
		t.Fatalf("target events = %v", got)
	}
	if res.Events[1].Envelope.GetRoomDescribed().GetTitle() != "The Pier" {
		t.Fatalf("described %v", res.Events[1].Envelope.GetRoomDescribed())
	}
}

// AC-6: a goto to the Room one stands in is a fresh look, and nothing moves.
func TestGoto_SameRoomIsALook(t *testing.T) {
	e := verbEngine(t)
	before := zoneBytes(e, "town")
	res := step(t, e, simtest.Goto("town", "alice", "town", "plaza"))
	if got := types(res.Events); len(got) != 1 || got[0] != sim.EvRoomDescribed {
		t.Fatalf("events = %v", got)
	}
	if !bytes.Equal(zoneBytes(e, "town"), before) || len(res.Outbound) != 0 {
		t.Fatal("a goto to where alice stands changed something")
	}
}

// AC-4: an unknown Zone or Room fails at validate, names the reference, and
// moves nothing.
func TestGoto_UnknownTarget(t *testing.T) {
	e := verbEngine(t)
	before := zoneBytes(e, "town")
	for _, tc := range []struct{ zone, room, code string }{
		{"nowhere", "room", sim.CodeUnknownZone},
		{"town", "nowhere", sim.CodeUnknownRoom},
	} {
		res := step(t, e, simtest.Goto("town", "alice", tc.zone, tc.room))
		rej := rejection(t, res.Events)
		if rej.GetCode() != tc.code || rej.GetMessage() != "there is no room "+tc.zone+"/"+tc.room {
			t.Errorf("%s/%s: %v", tc.zone, tc.room, rej)
		}
		if len(res.Outbound) != 0 {
			t.Errorf("%s/%s produced %v", tc.zone, tc.room, res.Outbound)
		}
	}
	if !bytes.Equal(zoneBytes(e, "town"), before) {
		t.Fatal("a refused goto moved alice")
	}
}

// AC-10: a cross-Zone goto whose target Room is gone by the time its Arrive
// applies lands at the target Zone's fallback, relocated, then described.
func TestGoto_IntoAGoneRoomLandsAtTheFallback(t *testing.T) {
	e := verbEngine(t)
	out := step(t, e, simtest.Goto("town", "alice", "wilds", "clearing")).Outbound[0]
	out.GetArrive().RoomId = "vanished" // what a swap before the Arrive leaves
	res := step(t, e, out)
	got := types(res.Events)
	if len(got) != 2 || got[0] != sim.EvEntityRelocated || got[1] != sim.EvRoomDescribed {
		t.Fatalf("events = %v", got)
	}
	fallback := e.World().Zones["wilds"].Fallback
	if r := res.Events[0].Envelope.GetEntityRelocated(); r.GetReason() != sim.ReasonRoomRemoved || r.GetToRoomId() != string(fallback) {
		t.Errorf("relocation = %v", r)
	}
	if d := res.Events[1]; d.Envelope.GetRoomDescribed().GetRoomId() != string(fallback) || d.Scope.Entities[0] != "alice" {
		t.Errorf("description = %v", d)
	}
}

// AC-7: goto is deterministic. Two Engines applying the same log, cross-Zone
// jumps and their Arrives included, agree on the State Hash at every tick.
func TestGoto_ReplayMatchesTheStateHash(t *testing.T) {
	a, b := verbEngine(t), verbEngine(t)
	log := []*logv1.LoggedCommand{
		simtest.Goto("town", "alice", "docks", "warehouse"),
		simtest.Goto("town", "bob", "town", "hall"),
	}
	var arrives []*logv1.LoggedCommand
	for _, cmd := range log {
		ra, rb := step(t, a, cmd), step(t, b, cmd)
		if ra.Completed.StateHash != rb.Completed.StateHash {
			t.Fatalf("hashes diverge after %v", cmd)
		}
		arrives = append(arrives, ra.Outbound...)
	}
	for _, cmd := range arrives {
		ra, rb := step(t, a, cmd), step(t, b, cmd)
		if ra.Completed.StateHash != rb.Completed.StateHash {
			t.Fatalf("hashes diverge after the Arrive %v", cmd)
		}
	}
	if got := a.State().Zones["docks"].Entities["alice"]; got == nil || got.Room != "warehouse" {
		t.Fatalf("alice = %+v", got)
	}
}
