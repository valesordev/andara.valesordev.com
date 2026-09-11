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
// The canonical set was closed on 2026-09-10 — the twelve in docs/glossary.md,
// each with a reverse — but this type is still an unvalidated string, and
// enforcing the set is AW-SRV-021's. Until then a typo like "norht" loads as a
// Direction nobody can traverse rather than failing the boot. The set is
// deliberately enforced here in the loader rather than as a protobuf enum: an
// unknown enum member is dropped silently on the wire, where a rejected string
// names the file and the line (zone.proto, ExitDefinition.direction).
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
	Exits       []Exit // stable, sorted by Direction
}

// Zone is an authored collection of Rooms and the unit of simulation authority.
type Zone struct {
	ID        ZoneID
	Name      string
	Rooms     map[RoomID]*Room
	Partition int32 // hash(ID) % 64
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
