// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package projector

import (
	"sort"

	gamev1 "github.com/valesordev/andara/gen/go/andara/game/v1"
	"github.com/valesordev/andara/server/sim"
)

// Aggregate names one thing a record describes: a Zone (Room and Entity
// empty), a Room in a Zone, or an Entity in a Zone. It is Touched's output
// rather than a finished record key because an Entity's key prefix is its
// kind, and the kind is read from state, not from the Event.
type Aggregate struct {
	Zone   sim.ZoneID
	Room   sim.RoomID
	Entity sim.EntityID
	// All: the whole Zone — summary, every Room, every Entity, and the
	// tombstone sweep. Room and Entity are empty.
	All bool
}

// touches is what one Event type re-emits. The Zone and Room come from the
// payload; the Entities from the Event's addressed scope, which for an
// arrival or departure is exactly the actor — the sim addresses the mover so
// it perceives its own move, and that address is the one place the Event
// names an EntityID rather than a display name.
type touches struct {
	zone, room, entities bool
	// whole re-renders everything in the Zone, for an Event after which any
	// aggregate in it may have changed without being named.
	whole bool
}

// table is Touched's whole policy: one row per EventType, and a unit test
// fails when the sim gains a type this table does not name. A row of all
// false is a decision — the Event changes no aggregate — not an omission.
var table = map[sim.EventType]touches{
	// look reads state and changes none.
	sim.EvRoomDescribed: {},
	// Arrival: the Entity, the Room it is now in, and the Zone's counts.
	sim.EvCharacterArrived: {zone: true, room: true, entities: true},
	// Departure: the Room it left, the Zone's counts, and the Entity — a
	// record when it is still in the Zone (a same-Zone move, or dormant after
	// an unbind), a tombstone when it left the Zone.
	sim.EvCharacterLeft: {zone: true, room: true, entities: true},
	// A content swap moved the Entity out of a Room the new version removed
	// (AW-SRV-012): the Entity, the fallback Room it is now in, and the
	// Zone's counts. The removed Room is not an aggregate any more.
	sim.EvEntityRelocated: {zone: true, room: true, entities: true},
	// A rejection changes nothing: validate and apply refuse before mutating.
	sim.EvCommandRejected: {},
	// A fault sets the Zone's faulted flag, and the panicking handler may
	// already have mutated any Entity in the Zone: applyOne keeps that partial
	// state, and the State Hash covers it. The Event names none of it, so the
	// Zone is rendered whole. (Codex review of PR #64.)
	sim.EvZoneFaulted: {whole: true},
	// Fan-out and lifecycle notices, not World state.
	sim.EvSubscriberDropped: {},
	sim.EvSimulationStopped: {},
}

// Touched maps the Events of one tick to the aggregates whose bodies must be
// re-emitted, sorted and without duplicates. It is a table over EventType,
// not logic.
//
// It names candidates, not verdicts: an Entity named here that is no longer
// in the Zone becomes a tombstone when the projector renders the tick.
func Touched(events []sim.Event) []Aggregate {
	seen := map[Aggregate]bool{}
	for _, ev := range events {
		row := table[ev.Type]
		zone, room := payloadPlace(ev.Envelope)
		if zone == "" {
			zone = ev.Zone
		}
		if zone == "" {
			continue
		}
		if row.whole {
			seen[Aggregate{Zone: zone, All: true}] = true
		}
		if row.zone {
			seen[Aggregate{Zone: zone}] = true
		}
		if row.room && room != "" {
			seen[Aggregate{Zone: zone, Room: room}] = true
		}
		if row.entities {
			for _, id := range ev.Scope.Entities {
				seen[Aggregate{Zone: zone, Entity: id}] = true
			}
		}
	}
	out := make([]Aggregate, 0, len(seen))
	for a := range seen {
		out = append(out, a)
	}
	sortAggregates(out)
	return out
}

// payloadPlace reads the Zone and Room a payload names, where it names one.
func payloadPlace(env *gamev1.EventEnvelope) (sim.ZoneID, sim.RoomID) {
	switch p := env.GetPayload().(type) {
	case *gamev1.EventEnvelope_CharacterArrived:
		return sim.ZoneID(p.CharacterArrived.GetZoneId()), sim.RoomID(p.CharacterArrived.GetRoomId())
	case *gamev1.EventEnvelope_CharacterLeft:
		return sim.ZoneID(p.CharacterLeft.GetZoneId()), sim.RoomID(p.CharacterLeft.GetRoomId())
	case *gamev1.EventEnvelope_RoomDescribed:
		return sim.ZoneID(p.RoomDescribed.GetZoneId()), sim.RoomID(p.RoomDescribed.GetRoomId())
	case *gamev1.EventEnvelope_ZoneFaulted:
		return sim.ZoneID(p.ZoneFaulted.GetZoneId()), ""
	case *gamev1.EventEnvelope_EntityRelocated:
		return sim.ZoneID(p.EntityRelocated.GetZoneId()), sim.RoomID(p.EntityRelocated.GetToRoomId())
	}
	return "", ""
}

func sortAggregates(a []Aggregate) {
	sort.Slice(a, func(i, j int) bool {
		if a[i].Zone != a[j].Zone {
			return a[i].Zone < a[j].Zone
		}
		if a[i].All != a[j].All {
			return a[i].All
		}
		if a[i].Room != a[j].Room {
			return a[i].Room < a[j].Room
		}
		return a[i].Entity < a[j].Entity
	})
}

// HasRow reports whether the table decides et. Exported for the completeness
// test.
func HasRow(et sim.EventType) bool {
	_, ok := table[et]
	return ok
}
