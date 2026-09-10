package sim

import (
	"fmt"
	"testing"

	contentv1 "github.com/valesordev/andara/gen/go/andara/content/v1"
)

// AC-11: every Room in a Zone lands on one Partition, because a Zone is the
// unit of simulation authority (ADR-0001) and a Zone split across Partitions
// would be unsimulatable.
//
// The structural reason this holds is that a Room carries no Partition of its
// own. That makes it easy to assert nothing, so these tests go through
// World.PartitionOf — the accessor a router would use — and check the property
// the invariant is actually about: the answer depends on the ZoneID and on
// nothing else.
func TestPartitionOf_EveryRoomInAZoneAgrees(t *testing.T) {
	rooms := make([]*contentv1.RoomDefinition, 0, 40)
	for i := 0; i < 40; i++ {
		id := fmt.Sprintf("r%02d", i)
		rooms = append(rooms, room(id, "Room "+id))
	}
	world, errs := BuildWorld([]Input{
		zone("town.json", "town", "Town", rooms...),
		zone("wilds.json", "wilds", "Wilds", room("trail", "Trail")),
	}, Options{})
	if fatal(errs) {
		t.Fatalf("errors: %v", errs)
	}

	for zid, z := range world.Zones {
		want := z.Partition
		if want != PartitionFor(zid) {
			t.Errorf("zone %s Partition = %d, PartitionFor = %d", zid, want, PartitionFor(zid))
		}
		if len(z.Rooms) == 0 {
			t.Fatalf("zone %s has no rooms to compare", zid)
		}
		for rid := range z.Rooms {
			got, ok := world.PartitionOf(RoomRef{Zone: zid, Room: rid})
			if !ok {
				t.Fatalf("PartitionOf(%s/%s) not found", zid, rid)
			}
			if got != want {
				t.Errorf("room %s/%s on partition %d, zone is on %d", zid, rid, got, want)
			}
		}
	}

	// The two Zones must be distinguishable for the test above to mean anything:
	// if every Zone shared a Partition, "all Rooms agree" would be trivially true.
	if world.Zones["town"].Partition == world.Zones["wilds"].Partition {
		t.Fatal("fixture Zones collide on one Partition; pick ZoneIDs that do not")
	}
}

// A Partition is a function of the ZoneID alone. Same ID, different Rooms,
// different file, different Zone name — same Partition. This is the property
// that makes the mapping safe to recompute at every boot from content that has
// changed in the meantime.
func TestPartitionFor_DependsOnZoneIDAlone(t *testing.T) {
	a, errs := BuildWorld([]Input{
		zone("first.json", "town", "Town", room("plaza", "Plaza")),
	}, Options{})
	if fatal(errs) {
		t.Fatalf("errors: %v", errs)
	}
	b, errs := BuildWorld([]Input{
		zone("second.json", "town", "Town Renamed",
			room("docks", "Docks"),
			room("attic", "Attic"),
			room("cellar", "Cellar"),
		),
	}, Options{})
	if fatal(errs) {
		t.Fatalf("errors: %v", errs)
	}
	if a.Zones["town"].Partition != b.Zones["town"].Partition {
		t.Errorf("partition moved with content: %d then %d",
			a.Zones["town"].Partition, b.Zones["town"].Partition)
	}
}

// A cross-Zone Exit must not drag a Room onto its target's Partition. The Exit
// is a value pair, not a pointer, precisely so the two Zones stay independently
// placeable (ADR-0001).
func TestPartitionOf_CrossZoneExitDoesNotMoveARoom(t *testing.T) {
	world, errs := BuildWorld([]Input{
		zone("town.json", "town", "Town", room("plaza", "Plaza", exit("east", "wilds", "trail"))),
		zone("wilds.json", "wilds", "Wilds", room("trail", "Trail", exit("west", "town", "plaza"))),
	}, Options{})
	if fatal(errs) {
		t.Fatalf("errors: %v", errs)
	}
	plaza, ok := world.PartitionOf(RoomRef{Zone: "town", Room: "plaza"})
	if !ok {
		t.Fatal("plaza missing")
	}
	if plaza != PartitionFor("town") {
		t.Errorf("plaza on partition %d, want town's %d", plaza, PartitionFor("town"))
	}
	trail, ok := world.PartitionOf(RoomRef{Zone: "wilds", Room: "trail"})
	if !ok {
		t.Fatal("trail missing")
	}
	if trail != PartitionFor("wilds") {
		t.Errorf("trail on partition %d, want wilds' %d", trail, PartitionFor("wilds"))
	}
}

func TestPartitionOf_UnknownRefs(t *testing.T) {
	world, errs := BuildWorld([]Input{
		zone("town.json", "town", "Town", room("plaza", "Plaza")),
	}, Options{})
	if fatal(errs) {
		t.Fatalf("errors: %v", errs)
	}
	if _, ok := world.PartitionOf(RoomRef{Zone: "nowhere", Room: "plaza"}); ok {
		t.Error("unknown Zone resolved")
	}
	if _, ok := world.PartitionOf(RoomRef{Zone: "town", Room: "nowhere"}); ok {
		t.Error("unknown Room resolved")
	}
	var nilWorld *World
	if _, ok := nilWorld.PartitionOf(RoomRef{Zone: "town", Room: "plaza"}); ok {
		t.Error("nil World resolved")
	}
}

// Golden vectors. Under ADR-0002 the Zone→Partition mapping is permanent once a
// log has been written against it: repartitioning a keyed topic reorders
// history. So a change to PartitionFor — a different hash, a different modulus,
// a library swap in AW-SRV-010 — is a migration, not a refactor, and must fail
// here loudly rather than being discovered from a reordered replay.
//
// Updating these numbers is only correct alongside a decision about existing
// logs. If this test fails, that decision is the work, not the test.
func TestPartitionFor_GoldenVectors(t *testing.T) {
	golden := map[ZoneID]int32{
		"town":          5,
		"wilds":         50,
		"docks":         11,
		"":              5,
		"a":             44,
		"market-square": 19,
		"zone-63":       17,
		"Ashenvale":     30,
	}
	for id, want := range golden {
		if got := PartitionFor(id); got != want {
			t.Errorf("PartitionFor(%q) = %d, want %d — see this test's comment before changing it",
				id, got, want)
		}
	}
}

// Every Partition must be a legal index into andara.commands.v1, and the
// mapping must cover the range rather than collapsing onto a few values.
func TestPartitionFor_RangeAndSpread(t *testing.T) {
	seen := make(map[int32]struct{}, PartitionCount)
	for i := 0; i < 4096; i++ {
		p := PartitionFor(ZoneID(fmt.Sprintf("zone-%d", i)))
		if p < 0 || p >= PartitionCount {
			t.Fatalf("PartitionFor(zone-%d) = %d, outside [0,%d)", i, p, PartitionCount)
		}
		seen[p] = struct{}{}
	}
	if len(seen) != int(PartitionCount) {
		t.Errorf("4096 ZoneIDs reached %d of %d partitions; the mapping is not spreading",
			len(seen), PartitionCount)
	}
}
