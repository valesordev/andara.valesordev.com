package sim

// EntityID is an opaque, stable identity for anything the simulation tracks.
type EntityID string

// ZoneID names a Zone. Unique across the World.
type ZoneID string

// RoomID names a Room. Unique within a Zone.
type RoomID string

// Direction is the label on an Exit. Opaque until the canonical set is decided.
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
