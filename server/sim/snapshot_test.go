// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package sim

import (
	"bytes"
	"testing"
)

func snapshotWorld() *World {
	return &World{Zones: map[ZoneID]*Zone{
		"village": {ID: "village", Name: "Village", Partition: PartitionFor("village"), Rooms: map[RoomID]*Room{
			"square": {ID: "square", Title: "The Square"},
		}},
		"forest": {ID: "forest", Name: "Forest", Partition: PartitionFor("forest"), Rooms: map[RoomID]*Room{
			"glade": {ID: "glade", Title: "A Glade"},
		}},
	}}
}

func snapshotEngine(t *testing.T) *Engine {
	t.Helper()
	w := snapshotWorld()
	e := NewEngine(w, nil, Config{
		Seed:       7,
		Partitions: []int32{PartitionFor("village"), PartitionFor("forest")},
	})
	e.state.Zones["village"].Entities["hero"] = &EntityState{
		ID: "hero", Room: "square", Name: "Hero", Template: "andara.core.Character", ContentVersion: "v1",
		Components: []Component{{Type: "andara.core.Dark", Fields: []ComponentField{{Name: "level", Kind: FieldInt, Int: 3}}}},
	}
	return e
}

// AC-1's premise and the Encode contract: the copy taken at the boundary is
// immutable, so the engine may advance while the round encodes and uploads.
func TestSnapshotBodyIsIsolatedFromTheEngine(t *testing.T) {
	t.Parallel()
	e := snapshotEngine(t)
	snaps := e.SnapshotAll(1234)

	var village *Snapshot
	for i := range snaps {
		if snaps[i].Zone == "village" {
			village = &snaps[i]
		}
	}
	if village == nil {
		t.Fatal("SnapshotAll did not cover village")
	}
	before := append([]byte(nil), ZoneCanonicalBytes(village.Body())...)

	// Mutate every level the engine owns: the Entity, its Component's
	// fields, the Zone's Entity set, and the faulted flag.
	live := e.state.Zones["village"]
	live.Entities["hero"].Room = "elsewhere"
	live.Entities["hero"].Components[0].Fields[0].Int = 99
	live.Entities["intruder"] = &EntityState{ID: "intruder", Room: "square"}
	live.Faulted, live.FaultedTick = true, 5

	if after := ZoneCanonicalBytes(village.Body()); !bytes.Equal(before, after) {
		t.Fatalf("the snapshot body changed when the engine did:\n before %q\n after  %q", before, after)
	}
}

// AC-2, at the level this package owns: the same state produces the same
// canonical bytes and therefore the same hash, every time.
func TestSnapshotOfIdenticalStateHashesIdentically(t *testing.T) {
	t.Parallel()
	a := snapshotEngine(t).SnapshotAll(1)
	b := snapshotEngine(t).SnapshotAll(2) // a different stamp must not change the hash
	if len(a) != len(b) {
		t.Fatalf("round sizes differ: %d and %d", len(a), len(b))
	}
	for i := range a {
		if a[i].Zone != b[i].Zone {
			t.Fatalf("round order is not stable: %q then %q", a[i].Zone, b[i].Zone)
		}
		if a[i].StateHash() != b[i].StateHash() {
			t.Fatalf("zone %q hashed differently across two identical worlds", a[i].Zone)
		}
	}
}

// AC-3: the envelope's hash is the one the sim would compute for that Zone.
func TestSnapshotStateHashIsTheZoneHash(t *testing.T) {
	t.Parallel()
	e := snapshotEngine(t)
	for _, s := range e.SnapshotAll(0) {
		want, ok := e.State().ZoneHash(s.Zone)
		if !ok {
			t.Fatalf("no zone hash for %q", s.Zone)
		}
		if s.StateHash() != want {
			t.Fatalf("zone %q: snapshot hash %x, sim hash %x", s.Zone, s.StateHash(), want)
		}
	}
}

// Two Zones with different contents must not hash alike — the check that the
// per-Zone hash is actually per-Zone and not the World's.
func TestZoneHashDistinguishesZones(t *testing.T) {
	t.Parallel()
	e := snapshotEngine(t)
	village, _ := e.State().ZoneHash("village")
	forest, _ := e.State().ZoneHash("forest")
	if village == forest {
		t.Fatal("an occupied Zone and an empty one hashed the same")
	}
	if _, ok := e.State().ZoneHash("nowhere"); ok {
		t.Fatal("ZoneHash reported a Zone the World does not have")
	}
}

// The World's canonical bytes are the concatenation of the per-Zone sections
// in Zone-ID order. This is what makes the envelope's hash and the boundary
// record's hash two views of one encoding rather than two encoders.
func TestWorldCanonicalBytesContainEachZoneSectionVerbatim(t *testing.T) {
	t.Parallel()
	e := snapshotEngine(t)
	world := e.State().CanonicalBytes()
	var at int
	for _, id := range []ZoneID{"forest", "village"} { // sorted, as CanonicalBytes emits them
		section := ZoneCanonicalBytes(e.State().Zones[id])
		i := bytes.Index(world[at:], section)
		if i < 0 {
			t.Fatalf("zone %q's section is not in the world bytes", id)
		}
		at += i + len(section)
	}
}

// A round is one cut: every Zone at one tick, and every object carrying the
// same process-wide PRNG and EventID.
func TestSnapshotAllIsOneCutAcrossEveryZone(t *testing.T) {
	t.Parallel()
	e := snapshotEngine(t)
	e.state.Tick = 41
	e.state.NextEventID = 99
	snaps := e.SnapshotAll(0)
	if len(snaps) != 2 {
		t.Fatalf("round covered %d zones, want 2", len(snaps))
	}
	for _, s := range snaps {
		if s.Tick != 41 {
			t.Fatalf("zone %q at tick %d, want 41", s.Zone, s.Tick)
		}
		if s.StateVersion != StateVersion {
			t.Fatalf("zone %q at state_version %d, want %d", s.Zone, s.StateVersion, StateVersion)
		}
		if s.NextEventID() != 99 {
			t.Fatalf("zone %q carries next_event_id %d, want 99", s.Zone, s.NextEventID())
		}
		if s.PRNG() != e.State().RNG.State() {
			t.Fatalf("zone %q carries a different PRNG state than the engine", s.Zone)
		}
	}
	if snaps[0].PRNG() != snaps[1].PRNG() {
		t.Fatal("two Zones in one round carry different PRNG state")
	}
}

// Offsets are sorted by Partition and cover every Partition the process owns,
// not just the snapshotted Zone's.
func TestSnapshotOffsetsAreSortedAndComplete(t *testing.T) {
	t.Parallel()
	e := snapshotEngine(t)
	for p := range e.state.Offsets {
		e.state.Offsets[p] = int64(p) + 1
	}
	snaps := e.SnapshotAll(0)
	for _, s := range snaps {
		if len(s.Offsets) != len(e.state.Offsets) {
			t.Fatalf("zone %q carries %d offsets, want %d", s.Zone, len(s.Offsets), len(e.state.Offsets))
		}
		for i := 1; i < len(s.Offsets); i++ {
			if s.Offsets[i-1].Partition >= s.Offsets[i].Partition {
				t.Fatalf("zone %q: offsets are not sorted by partition: %v", s.Zone, s.Offsets)
			}
		}
		po, ok := s.PartitionOf()
		if !ok {
			t.Fatalf("zone %q: its own Partition is not among the offsets", s.Zone)
		}
		if po.Partition != PartitionFor(s.Zone) {
			t.Fatalf("zone %q: PartitionOf returned partition %d", s.Zone, po.Partition)
		}
		if want := SnapshotKey(s.Zone, s.StateVersion, po.Offset); s.Key() != want {
			t.Fatalf("zone %q: Key = %q, want %q", s.Zone, s.Key(), want)
		}
	}
}

// A Zone with no Command since the last round is at the same offset, so it
// writes the same key: an overwrite, not a new object. Pinned because
// AW-SRV-007 must not assume a key identifies a round
// (docs/feedback/AW-SRV-006-zone-snapshots.md §5).
func TestAnIdleZoneRepeatsItsKey(t *testing.T) {
	t.Parallel()
	e := snapshotEngine(t)
	first := e.SnapshotAll(0)
	e.state.Tick += 600 // a minute of ticks, no Commands
	second := e.SnapshotAll(0)
	for i := range first {
		if first[i].Key() != second[i].Key() {
			t.Fatalf("zone %q changed key without changing offset: %q then %q", first[i].Zone, first[i].Key(), second[i].Key())
		}
		if first[i].Tick == second[i].Tick {
			t.Fatalf("zone %q: the test did not advance the tick", first[i].Zone)
		}
	}
}

func TestErrStateVersionNamesBothVersions(t *testing.T) {
	t.Parallel()
	err := &ErrStateVersion{Have: 4, Want: 3}
	msg := err.Error()
	for _, want := range []string{"4", "3"} {
		if !bytes.Contains([]byte(msg), []byte(want)) {
			t.Fatalf("ErrStateVersion message %q does not name %q", msg, want)
		}
	}
}

func TestErrRoundIncompleteNamesTheMissingZones(t *testing.T) {
	t.Parallel()
	err := &ErrRoundIncomplete{Tick: 4200, Missing: []ZoneID{"village", "forest"}}
	msg := err.Error()
	for _, want := range []string{"4200", "village", "forest"} {
		if !bytes.Contains([]byte(msg), []byte(want)) {
			t.Fatalf("ErrRoundIncomplete message %q does not name %q", msg, want)
		}
	}
}
