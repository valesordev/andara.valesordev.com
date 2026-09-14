// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package sim

// EntityID is an opaque, stable identity for anything the simulation tracks.
type EntityID string

// ZoneID names a Zone. Unique across the World.
type ZoneID string

// RoomID names a Room. Unique within a Zone.
type RoomID string

// Direction is the label on an Exit.
//
// The canonical set is closed — the twelve in docs/glossary.md, each with a
// reverse — and enforced by the loader, not by the type: a Direction value can
// hold anything, and Valid reports whether it is one of the twelve. The set is
// enforced in the loader rather than as a protobuf enum because an unknown enum
// member is dropped silently on the wire, where a rejected string names the
// file and the line (zone.proto, ExitDefinition.direction). See direction.go.
type Direction string

// RoomRef addresses a Room across Zone boundaries. Cross-Zone references are
// values, never pointers — ADR-0001 seam invariant.
type RoomRef struct {
	Zone ZoneID
	Room RoomID
}

// Exit is a directed edge from one Room to another.
type Exit struct {
	Direction Direction
	To        RoomRef
	CrossZone bool // computed at load; To.Zone != containing Zone
}

// Room is the atomic unit of location. Topology only; mutable state is AW-SRV-002.
type Room struct {
	ID          RoomID
	Title       string
	Description string
	Exits       []Exit      // stable, sorted by Direction
	Components  []Component // stable, sorted by Type; at most one of each
}

// Component returns the Room's Component of type t, if it carries one.
//
// A sorted slice rather than a map, even though ADR-0010 decision 3 calls the
// set "keyed by type": uniqueness is enforced at load, and a slice is
// deterministic by construction where a map has to be sorted on the way out of
// every reader that ever touches it. Component sets are single digits long, so
// the scan costs nothing worth a map for.
func (r *Room) Component(t ComponentType) (Component, bool) {
	if r == nil {
		return Component{}, false
	}
	return findComponent(r.Components, t)
}

// Zone is an authored collection of Rooms and the unit of simulation authority.
type Zone struct {
	ID         ZoneID
	Name       string
	Rooms      map[RoomID]*Room
	Partition  int32       // hash(ID) % 64
	Components []Component // stable, sorted by Type; at most one of each
}

// Component returns the Zone's Component of type t, if it carries one.
//
// Zone-level Components are the Zone's own. They do not descend onto its Rooms:
// a Zone carrying Dark does not make its Rooms dark (AW-SRV-021 AC-4). Merging
// across a containment boundary is a different rule from ADR-0010 decision 4's
// inheritance merge, and it wants its own decision before it exists.
func (z *Zone) Component(t ComponentType) (Component, bool) {
	if z == nil {
		return Component{}, false
	}
	return findComponent(z.Components, t)
}

func findComponent(set []Component, t ComponentType) (Component, bool) {
	for _, c := range set {
		if c.Type == t {
			return c, true
		}
	}
	return Component{}, false
}

// World is the immutable topology. Mutable state lives elsewhere (AW-SRV-002).
type World struct {
	Zones map[ZoneID]*Zone
}

// Resolve returns the Room addressed by ref, if it exists.
func (w *World) Resolve(ref RoomRef) (*Room, bool) {
	if w == nil {
		return nil, false
	}
	z, ok := w.Zones[ref.Zone]
	if !ok {
		return nil, false
	}
	r, ok := z.Rooms[ref.Room]
	return r, ok
}

// PartitionOf returns the Partition that owns the Room addressed by ref.
//
// A Room has no Partition of its own: it inherits its Zone's, because a Zone is
// the unit of simulation authority (ADR-0001) and a Zone split across
// Partitions would be unsimulatable. This accessor exists so that invariant is
// something a caller can assert rather than something it has to know
// structurally — AW-SRV-001 AC-11, and the routing AW-SRV-010 needs.
func (w *World) PartitionOf(ref RoomRef) (int32, bool) {
	if w == nil {
		return 0, false
	}
	z, ok := w.Zones[ref.Zone]
	if !ok {
		return 0, false
	}
	if _, ok := z.Rooms[ref.Room]; !ok {
		return 0, false
	}
	return z.Partition, true
}
