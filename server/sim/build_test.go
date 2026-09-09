package sim

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	contentv1 "github.com/valesordev/andara/gen/go/andara/content/v1"
)

func TestBuildWorld_FortyRoomsResolve(t *testing.T) {
	rooms := make([]*contentv1.RoomDefinition, 0, 40)
	for i := 0; i < 40; i++ {
		id := fmt.Sprintf("r%02d", i)
		r := &contentv1.RoomDefinition{
			Id:          id,
			Title:       "Room " + id,
			Description: "d",
		}
		if i < 39 {
			r.Exits = []*contentv1.ExitDefinition{{
				Direction: "east",
				ToRoom:    fmt.Sprintf("r%02d", i+1),
			}}
		}
		rooms = append(rooms, r)
	}

	world, errs := BuildWorld([]Input{zone("town.json", "town", "Town", rooms...)}, Options{})
	if fatal(errs) {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if world == nil {
		t.Fatal("world is nil")
	}
	z, ok := world.Zones[ZoneID("town")]
	if !ok {
		t.Fatal("zone town missing")
	}
	if len(z.Rooms) != 40 {
		t.Fatalf("rooms = %d, want 40", len(z.Rooms))
	}
	for i := 0; i < 40; i++ {
		id := RoomID(fmt.Sprintf("r%02d", i))
		room, ok := world.Resolve(RoomRef{Zone: "town", Room: id})
		if !ok {
			t.Errorf("room %s not resolvable", id)
			continue
		}
		for _, e := range room.Exits {
			if _, ok := world.Resolve(e.To); !ok {
				t.Errorf("exit %s from %s does not resolve to %s/%s", e.Direction, id, e.To.Zone, e.To.Room)
			}
		}
	}
}

func TestBuildWorld_UnknownRoom(t *testing.T) {
	_, errs := BuildWorld([]Input{
		zone("town.json", "town", "Town",
			room("plaza", "Plaza", exit("north", "", "nowhere")),
		),
	}, Options{})
	e := requireCode(t, errs, ErrUnknownRoom)
	if e.File != "town.json" {
		t.Errorf("File = %q, want town.json", e.File)
	}
	if e.Zone != "town" {
		t.Errorf("Zone = %q, want town", e.Zone)
	}
	if e.Room != "plaza" {
		t.Errorf("Room = %q, want plaza", e.Room)
	}
	if !strings.Contains(e.Detail, "nowhere") {
		t.Errorf("Detail %q does not name unresolved target", e.Detail)
	}
	if !strings.Contains(strings.ToLower(e.Detail), "north") {
		t.Errorf("Detail %q does not name direction", e.Detail)
	}
}

func TestBuildWorld_UnknownZone(t *testing.T) {
	_, errs := BuildWorld([]Input{
		zone("town.json", "town", "Town",
			room("plaza", "Plaza", exit("east", "wilds", "trail")),
		),
	}, Options{})
	e := requireCode(t, errs, ErrUnknownZone)
	if !strings.Contains(e.Detail, "wilds") {
		t.Errorf("Detail %q does not name missing ZoneID", e.Detail)
	}
}

func TestBuildWorld_DuplicateRoomNamesBothFiles(t *testing.T) {
	_, errs := BuildWorld([]Input{
		zone("a.json", "town", "Town", room("market-square", "Market")),
		zone("b.json", "town", "Town", room("market-square", "Market")),
	}, Options{})
	e := requireCode(t, errs, ErrDuplicateRoom)
	if !strings.Contains(e.Detail, "a.json") || !strings.Contains(e.Detail, "b.json") {
		t.Errorf("Detail %q does not name both file paths", e.Detail)
	}
	if e.Room != "market-square" {
		t.Errorf("Room = %q, want market-square", e.Room)
	}
}

func TestBuildWorld_DuplicateRoomSameFile(t *testing.T) {
	_, errs := BuildWorld([]Input{
		zone("town.json", "town", "Town",
			room("plaza", "A"),
			room("plaza", "B"),
		),
	}, Options{})
	e := requireCode(t, errs, ErrDuplicateRoom)
	if e.Room != "plaza" {
		t.Errorf("Room = %q, want plaza", e.Room)
	}
}

func TestBuildWorld_DuplicateZone(t *testing.T) {
	_, errs := BuildWorld([]Input{
		zone("a.json", "town", "Town", room("plaza", "Plaza")),
		zone("b.json", "town", "Town Copy", room("dock", "Dock")),
	}, Options{})
	e := requireCode(t, errs, ErrDuplicateZone)
	if e.Zone != "town" {
		t.Errorf("Zone = %q, want town", e.Zone)
	}
	if !strings.Contains(e.Detail, "a.json") || !strings.Contains(e.Detail, "b.json") {
		t.Errorf("Detail %q does not name both files", e.Detail)
	}
}

func TestBuildWorld_CrossZoneExit(t *testing.T) {
	world, errs := BuildWorld([]Input{
		zone("town.json", "town", "Town",
			room("plaza", "Plaza", exit("east", "wilds", "trail")),
		),
		zone("wilds.json", "wilds", "Wilds",
			room("trail", "Trail", exit("west", "town", "plaza")),
		),
	}, Options{})
	if fatal(errs) {
		t.Fatalf("unexpected errors: %v", errs)
	}
	plaza, ok := world.Resolve(RoomRef{Zone: "town", Room: "plaza"})
	if !ok {
		t.Fatal("plaza missing")
	}
	if len(plaza.Exits) != 1 {
		t.Fatalf("exits = %d, want 1", len(plaza.Exits))
	}
	e := plaza.Exits[0]
	if !e.CrossZone {
		t.Error("CrossZone = false, want true")
	}
	if e.To != (RoomRef{Zone: "wilds", Room: "trail"}) {
		t.Errorf("To = %+v", e.To)
	}
}

func TestBuildWorld_SameZoneExitNotCrossZone(t *testing.T) {
	world, errs := BuildWorld([]Input{
		zone("town.json", "town", "Town",
			room("plaza", "Plaza", exit("north", "", "hall")),
			room("hall", "Hall", exit("south", "town", "plaza")),
		),
	}, Options{})
	if fatal(errs) {
		t.Fatalf("unexpected errors: %v", errs)
	}
	plaza, _ := world.Resolve(RoomRef{Zone: "town", Room: "plaza"})
	if plaza.Exits[0].CrossZone {
		t.Error("empty to_zone should not be CrossZone")
	}
	hall, _ := world.Resolve(RoomRef{Zone: "town", Room: "hall"})
	if hall.Exits[0].CrossZone {
		t.Error("explicit same-zone to_zone should not be CrossZone")
	}
}

func TestBuildWorld_OrphanWarning(t *testing.T) {
	world, errs := BuildWorld([]Input{
		zone("town.json", "town", "Town",
			room("plaza", "Plaza", exit("north", "", "hall")),
			room("hall", "Hall"),
			room("attic", "Attic"),
		),
	}, Options{})
	if world == nil {
		t.Fatalf("world is nil; errs=%v", errs)
	}
	if fatal(errs) {
		t.Fatalf("orphans must not be fatal by default: %v", errs)
	}
	orphans := codes(errs, ErrOrphanRoom)
	ids := make([]string, 0, len(orphans))
	for _, e := range orphans {
		ids = append(ids, string(e.Room))
	}
	if !slices.Contains(ids, "plaza") {
		t.Errorf("expected orphan plaza, got %v", ids)
	}
	if !slices.Contains(ids, "attic") {
		t.Errorf("expected orphan attic, got %v", ids)
	}
	if slices.Contains(ids, "hall") {
		t.Errorf("hall is reachable, not an orphan: %v", ids)
	}
}

func TestBuildWorld_StrictOrphansFatal(t *testing.T) {
	world, errs := BuildWorld([]Input{
		zone("town.json", "town", "Town",
			room("plaza", "Plaza"),
		),
	}, Options{StrictOrphans: true})
	if world != nil {
		t.Error("world should be nil when strict orphans fail")
	}
	requireCode(t, errs, ErrOrphanRoom)
}

func TestBuildWorld_UnsupportedVersion(t *testing.T) {
	in := zone("future.json", "town", "Town", room("plaza", "Plaza"))
	in.Def.FormatVersion = 99
	_, errs := BuildWorld([]Input{in}, Options{})
	e := requireCode(t, errs, ErrUnsupportedVersion)
	if !strings.Contains(e.Detail, "99") {
		t.Errorf("Detail %q does not name file version", e.Detail)
	}
	if !strings.Contains(e.Detail, "1") {
		t.Errorf("Detail %q does not name supported range", e.Detail)
	}
}

func TestBuildWorld_MissingFormatVersion(t *testing.T) {
	in := zone("old.json", "town", "Town", room("plaza", "Plaza"))
	in.Def.FormatVersion = 0
	_, errs := BuildWorld([]Input{in}, Options{})
	requireCode(t, errs, ErrUnsupportedVersion)
}

func TestBuildWorld_EmptyContent(t *testing.T) {
	world, errs := BuildWorld(nil, Options{})
	if world != nil {
		t.Error("world should be nil")
	}
	e := requireCode(t, errs, ErrEmptyContent)
	if e.Detail == "" {
		t.Error("Detail should name the content source")
	}
}

func TestBuildWorld_EmptyZoneIDMalformed(t *testing.T) {
	_, errs := BuildWorld([]Input{
		zone("bad.json", "", "Town", room("plaza", "Plaza")),
	}, Options{})
	requireCode(t, errs, ErrMalformed)
}

func TestBuildWorld_ReportsEveryError(t *testing.T) {
	_, errs := BuildWorld([]Input{
		zone("a.json", "town", "Town",
			room("plaza", "Plaza", exit("north", "", "missing"), exit("east", "nope", "x")),
		),
		zone("b.json", "town", "Other", room("dock", "Dock")),
	}, Options{})
	if !hasCode(errs, ErrUnknownRoom) || !hasCode(errs, ErrUnknownZone) || !hasCode(errs, ErrDuplicateZone) {
		t.Fatalf("want unknown_room, unknown_zone, duplicate_zone; got %v", errs)
	}
}

func TestBuildWorld_DeterministicCanonicalBytes(t *testing.T) {
	mk := func() []Input {
		return []Input{
			zone("wilds.json", "wilds", "Wilds",
				room("trail", "Trail",
					exit("west", "town", "plaza"),
					exit("north", "", "grove"),
				),
				room("grove", "Grove"),
			),
			zone("town.json", "town", "Town",
				room("plaza", "Plaza",
					exit("south", "", "dock"),
					exit("east", "wilds", "trail"),
					exit("north", "", "hall"),
				),
				room("hall", "Hall"),
				room("dock", "Dock"),
			),
		}
	}
	w1, e1 := BuildWorld(mk(), Options{})
	w2, e2 := BuildWorld(mk(), Options{})
	if fatal(e1) || fatal(e2) {
		t.Fatalf("errors: %v %v", e1, e2)
	}
	b1 := CanonicalBytes(w1)
	b2 := CanonicalBytes(w2)
	if string(b1) != string(b2) {
		t.Fatalf("canonical bytes differ:\n%s\n---\n%s", b1, b2)
	}

	reversed := mk()
	reversed[0], reversed[1] = reversed[1], reversed[0]
	w3, e3 := BuildWorld(reversed, Options{})
	if fatal(e3) {
		t.Fatalf("errors: %v", e3)
	}
	if string(CanonicalBytes(w3)) != string(b1) {
		t.Fatal("input order leaked into topology")
	}
}

func TestBuildWorld_ExitsSortedByDirection(t *testing.T) {
	world, errs := BuildWorld([]Input{
		zone("town.json", "town", "Town",
			room("plaza", "Plaza",
				exit("south", "", "dock"),
				exit("east", "", "gate"),
				exit("north", "", "hall"),
			),
			room("dock", "Dock"),
			room("gate", "Gate"),
			room("hall", "Hall"),
		),
	}, Options{})
	if fatal(errs) {
		t.Fatalf("errors: %v", errs)
	}
	plaza, _ := world.Resolve(RoomRef{Zone: "town", Room: "plaza"})
	got := make([]string, len(plaza.Exits))
	for i, e := range plaza.Exits {
		got[i] = string(e.Direction)
	}
	want := []string{"east", "north", "south"}
	if !slices.Equal(got, want) {
		t.Errorf("directions = %v, want %v", got, want)
	}
}

func TestBuildWorld_PartitionIdenticalWithinZone(t *testing.T) {
	world, errs := BuildWorld([]Input{
		zone("town.json", "town", "Town",
			room("a", "A"),
			room("b", "B"),
		),
		zone("wilds.json", "wilds", "Wilds",
			room("c", "C"),
		),
	}, Options{})
	if fatal(errs) {
		t.Fatalf("errors: %v", errs)
	}
	town := world.Zones["town"]
	if town.Partition != PartitionFor("town") {
		t.Errorf("town partition = %d, want %d", town.Partition, PartitionFor("town"))
	}
	if town.Rooms["a"] == nil || town.Rooms["b"] == nil {
		t.Fatal("rooms missing")
	}
	// AC-11: two rooms in the same Zone share the Zone's partition.
	if town.Partition != PartitionFor("town") {
		t.Fatal("zone partition drifted from PartitionFor")
	}
	wilds := world.Zones["wilds"]
	if wilds.Partition != PartitionFor("wilds") {
		t.Errorf("wilds partition = %d, want %d", wilds.Partition, PartitionFor("wilds"))
	}
}

func TestBuildWorld_NilDefMalformed(t *testing.T) {
	_, errs := BuildWorld([]Input{{File: "nil.json", Def: nil}}, Options{})
	requireCode(t, errs, ErrMalformed)
}

func TestPartitionFor_StableAndInRange(t *testing.T) {
	a := PartitionFor("town")
	b := PartitionFor("town")
	if a != b {
		t.Fatalf("PartitionFor not stable: %d vs %d", a, b)
	}
	if a < 0 || a > 63 {
		t.Fatalf("partition %d out of range 0-63", a)
	}
	if PartitionFor("town") == PartitionFor("wilds") {
		// Unlikely with FNV-1a; if it happens the test still documents the range.
		t.Log("town and wilds hashed to the same partition (allowed)")
	}
}

func zone(file, id, name string, rooms ...*contentv1.RoomDefinition) Input {
	return Input{
		File: file,
		Def: &contentv1.ZoneDefinition{
			FormatVersion: 1,
			Id:            id,
			Name:          name,
			Rooms:         rooms,
		},
	}
}

func room(id, title string, exits ...*contentv1.ExitDefinition) *contentv1.RoomDefinition {
	return &contentv1.RoomDefinition{
		Id:          id,
		Title:       title,
		Description: "d",
		Exits:       exits,
	}
}

func exit(dir, toZone, toRoom string) *contentv1.ExitDefinition {
	return &contentv1.ExitDefinition{Direction: dir, ToZone: toZone, ToRoom: toRoom}
}

func hasCode(errs []ValidationError, code ErrCode) bool {
	for _, e := range errs {
		if e.Code == code {
			return true
		}
	}
	return false
}

func codes(errs []ValidationError, code ErrCode) []ValidationError {
	var out []ValidationError
	for _, e := range errs {
		if e.Code == code {
			out = append(out, e)
		}
	}
	return out
}

func requireCode(t *testing.T, errs []ValidationError, code ErrCode) ValidationError {
	t.Helper()
	for _, e := range errs {
		if e.Code == code {
			return e
		}
	}
	t.Fatalf("missing %s in %v", code, errs)
	return ValidationError{}
}

func fatal(errs []ValidationError) bool {
	for _, e := range errs {
		if e.Code != ErrOrphanRoom {
			return true
		}
	}
	return false
}
