// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package simtest

import (
	"fmt"

	"github.com/valesordev/andara/server/sim"
)

// The sizing fixture: the World scale AW-SRV-006 AC-1 is measured against.
//
// These numbers are an [ASSUMPTION] the story records and Brian may revise.
// They live here, in one place, so that revising World scale is a visible
// change to a test rather than a silent drift in what "the tick budget holds"
// was ever measured to mean. A story that changes them says so.
const (
	// SizingZones is the Zone count, and therefore the object count in one
	// snapshot round. Zones are bounded by content, not by players.
	SizingZones = 16
	// SizingRooms is the total Room count across every Zone.
	SizingRooms = 2000
	// SizingEntities is the total Entity count: props, NPCs, and the
	// Characters below, all of which the boundary copy walks. Raised from
	// 10,000 on 2026-09-22 (Brian) for headroom as the World grows; it is
	// the only term of the four that moved, because Rooms are topology the
	// copy never touches and the Character count is a concurrency
	// assumption rather than a statement about World size.
	SizingEntities = 25000
	// SizingCharacters is how many of the Entities are Characters — bodies
	// with a Name, and the ones a stall is felt by.
	SizingCharacters = 500
)

// SizingWorld builds the sizing fixture's topology: SizingRooms Rooms spread
// evenly across SizingZones Zones, each Zone a north-south chain so the Rooms
// are connected and the loader's orphan warning stays quiet.
//
// Zone IDs are z00..z15 rather than names: the fixture is about scale, and a
// Zone's ID feeds sim.PartitionFor, so generated IDs keep the Partition spread
// a property of the fixture instead of of somebody's naming.
func SizingWorld() (*sim.World, error) {
	inputs := make([]sim.Input, 0, SizingZones)
	per := SizingRooms / SizingZones
	for z := 0; z < SizingZones; z++ {
		id := fmt.Sprintf("z%02d", z)
		n := per
		if z == SizingZones-1 {
			n = SizingRooms - per*(SizingZones-1) // the remainder lands in the last Zone
		}
		rooms := make([]string, n)
		for r := range rooms {
			rooms[r] = fmt.Sprintf("r%04d", r)
		}
		inputs = append(inputs, zoneDef(id, "Zone "+id, rooms...))
	}
	w, errs := sim.BuildWorld(inputs, sim.Options{})
	for _, e := range errs {
		if !sim.IsWarning(e, false) {
			return nil, e
		}
	}
	return w, nil
}

// SizingEngine is SizingWorld populated to the fixture's Entity counts: the
// Entities spread evenly over the Rooms, the first SizingCharacters of them
// Characters with a Name, the rest Merchants carrying the Components
// town.Merchant flattens onto them.
//
// Components matter to what this measures. The boundary copy is O(state) in
// Entities *and* in the Component fields hanging off them, and an Entity with
// no Components would make the copy look cheaper than the World it is standing
// in for.
func SizingEngine(seed uint64) (*sim.Engine, error) {
	w, err := SizingWorld()
	if err != nil {
		return nil, err
	}
	reg, err := Templates()
	if err != nil {
		return nil, err
	}
	e := sim.NewEngine(w, reg, sim.Config{Seed: seed, Partitions: AllPartitions(), Handlers: sim.Handlers()})

	merchant, ok := reg.Get("town.Merchant")
	if !ok {
		return nil, fmt.Errorf("simtest: the sizing fixture needs town.Merchant")
	}
	character, ok := reg.Get("andara.core.Character")
	if !ok {
		return nil, fmt.Errorf("simtest: the sizing fixture needs andara.core.Character")
	}

	// A stable walk over (zone, room) so the fixture is the same World on
	// every run: the Entity in slot i is always in the same Room.
	type slot struct {
		zone sim.ZoneID
		room sim.RoomID
	}
	slots := make([]slot, 0, SizingRooms)
	for z := 0; z < SizingZones; z++ {
		id := sim.ZoneID(fmt.Sprintf("z%02d", z))
		zone := w.Zones[id]
		rooms := make([]sim.RoomID, 0, len(zone.Rooms))
		for r := range zone.Rooms {
			rooms = append(rooms, r)
		}
		sortRoomIDs(rooms)
		for _, r := range rooms {
			slots = append(slots, slot{zone: id, room: r})
		}
	}
	if len(slots) == 0 {
		return nil, fmt.Errorf("simtest: the sizing fixture built no Rooms")
	}

	for i := 0; i < SizingEntities; i++ {
		s := slots[i%len(slots)]
		var state sim.EntityState
		if i < SizingCharacters {
			state = sim.Instantiate(character, sim.EntityID(fmt.Sprintf("c%05d", i)), "core@1")
			state.Name = fmt.Sprintf("Character %d", i)
		} else {
			state = sim.Instantiate(merchant, sim.EntityID(fmt.Sprintf("e%05d", i)), "town@1")
		}
		state.Room = s.room
		e.State().Zones[s.zone].Entities[state.ID] = &state
	}
	return e, nil
}

func sortRoomIDs(ids []sim.RoomID) {
	for i := 1; i < len(ids); i++ {
		for j := i; j > 0 && ids[j] < ids[j-1]; j-- {
			ids[j], ids[j-1] = ids[j-1], ids[j]
		}
	}
}
