// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package sim_test

import (
	"testing"

	contentv1 "github.com/valesordev/andara/gen/go/andara/content/v1"
	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/simtest"
)

// wayBackEngine is two Zones whose Exits back are present, absent, or lead
// somewhere else (AW-SRV-041):
//
//	a/r1 --east--> a/r2        a/r2 has no west            (one-way)
//	a/r1 --north-> a/r3        a/r3 south leads to a/r2    (back leads elsewhere)
//	a/r1 --up----> b/s1        b/s1 has no down            (one-way, cross-Zone)
//	a/r1 --west--> b/s2        b/s2 east leads to a/r1     (reciprocal, cross-Zone)
//	a/r1 --down--> b/s3        b/s3 up leads to b/s1       (back leads elsewhere, cross-Zone)
//	a/r1 --south-> a/r4        a/r4 north leads to a/r1    (reciprocal)
func wayBackEngine(t *testing.T) *sim.Engine {
	t.Helper()
	x := func(dir, zone, room string) *contentv1.ExitDefinition {
		return &contentv1.ExitDefinition{Direction: dir, ToZone: zone, ToRoom: room}
	}
	room := func(id string, exits ...*contentv1.ExitDefinition) *contentv1.RoomDefinition {
		return &contentv1.RoomDefinition{Id: id, Title: id, Description: "A " + id + ".", Exits: exits}
	}
	a := &contentv1.ZoneDefinition{FormatVersion: 1, Id: "a", Name: "A", FallbackRoom: "r1", Rooms: []*contentv1.RoomDefinition{
		room("r1", x("east", "", "r2"), x("north", "", "r3"), x("up", "b", "s1"), x("west", "b", "s2"), x("down", "b", "s3"), x("south", "", "r4")),
		room("r2"),
		room("r3", x("south", "", "r2")),
		room("r4", x("north", "", "r1")),
	}}
	b := &contentv1.ZoneDefinition{FormatVersion: 1, Id: "b", Name: "B", FallbackRoom: "s1", Rooms: []*contentv1.RoomDefinition{
		room("s1"),
		room("s2", x("east", "a", "r1")),
		room("s3", x("up", "", "s1")),
	}}
	w, errs := sim.BuildWorld([]sim.Input{{File: "a.json", Def: a}, {File: "b.json", Def: b}}, sim.Options{})
	for _, err := range errs {
		if !sim.IsWarning(err, false) {
			t.Fatal(err)
		}
	}
	reg, err := simtest.Templates()
	if err != nil {
		t.Fatal(err)
	}
	e := sim.NewEngine(w, reg, sim.Config{Seed: 7, Partitions: simtest.AllPartitions(), Handlers: sim.Handlers()})
	simtest.Place(e, "alice", "a", "r1")
	return e
}

// fromDirectionAfter moves alice through dir from a/r1 and returns the from_direction
// of the CharacterArrived a bystander reads, across Zones too.
func fromDirectionAfter(t *testing.T, e *sim.Engine, dir string) string {
	t.Helper()
	res := step(t, e, simtest.Move("a", "alice", dir))
	if len(res.Outbound) == 1 {
		res = step(t, e, res.Outbound[0])
	}
	arrived := ofType(res.Events, sim.EvCharacterArrived)
	if len(arrived) != 1 {
		t.Fatalf("move %s: events = %v", dir, res.Events)
	}
	return arrived[0].Envelope.GetCharacterArrived().GetFromDirection()
}

// ACs 1, 2, 4, 6: from_direction is set iff the destination Room has an Exit
// in that Direction leading back to the Room the mover left.
func TestArrival_NamesADirectionOnlyWhenThereIsAWayBack(t *testing.T) {
	for _, tc := range []struct{ name, dir, want string }{
		{"reciprocal in-Zone", "south", "north"},
		{"one-way in-Zone", "east", ""},
		{"reverse Exit leads to a third Room", "north", ""},
		{"reciprocal cross-Zone", "west", "east"},
		{"one-way cross-Zone", "up", ""},
		{"cross-Zone reverse Exit leads to a third Room", "down", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := fromDirectionAfter(t, wayBackEngine(t), tc.dir); got != tc.want {
				t.Fatalf("from_direction = %q, want %q", got, tc.want)
			}
		})
	}
}

// AC-1/4: the Arrive in the log keeps the reverse Direction regardless; only
// the event is decided by the destination (out of scope: the log is unchanged).
func TestArrival_TheLoggedArriveKeepsTheReverseDirection(t *testing.T) {
	e := wayBackEngine(t)
	res := step(t, e, simtest.Move("a", "alice", "up"))
	if len(res.Outbound) != 1 || res.Outbound[0].GetArrive().GetFromDirection() != "down" {
		t.Fatalf("outbound = %v", res.Outbound)
	}
}
