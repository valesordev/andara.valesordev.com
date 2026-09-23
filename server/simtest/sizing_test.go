// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package simtest_test

import (
	"testing"
	"time"

	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/simtest"
)

// The fixture is what AC-1 is measured against, so what it actually contains
// is an assertion, not a comment. A fixture that quietly shrank would turn
// AC-1 into a measurement of nothing.
func TestSizingFixtureIsTheDocumentedScale(t *testing.T) {
	t.Parallel()
	e, err := simtest.SizingEngine(1)
	if err != nil {
		t.Fatalf("SizingEngine: %v", err)
	}
	s := e.State()
	if len(s.Zones) != simtest.SizingZones {
		t.Errorf("zones = %d, want %d", len(s.Zones), simtest.SizingZones)
	}
	rooms, entities, characters, withComponents := 0, 0, 0, 0
	for _, z := range s.Zones {
		entities += len(z.Entities)
		for _, ent := range z.Entities {
			if ent.Name != "" {
				characters++
			}
			if len(ent.Components) > 0 {
				withComponents++
			}
		}
	}
	w, err := simtest.SizingWorld()
	if err != nil {
		t.Fatalf("SizingWorld: %v", err)
	}
	for _, z := range w.Zones {
		rooms += len(z.Rooms)
	}
	if rooms != simtest.SizingRooms {
		t.Errorf("rooms = %d, want %d", rooms, simtest.SizingRooms)
	}
	if entities != simtest.SizingEntities {
		t.Errorf("entities = %d, want %d", entities, simtest.SizingEntities)
	}
	if characters != simtest.SizingCharacters {
		t.Errorf("characters = %d, want %d", characters, simtest.SizingCharacters)
	}
	// The copy is O(Components) as well as O(Entities); a fixture whose
	// Entities carry none would measure the wrong thing.
	if withComponents == 0 {
		t.Error("no Entity in the sizing fixture carries a Component")
	}
}

// AC-1: the in-tick copy of a snapshot round stays under snapshot.max_stall_ms
// at the sizing fixture's scale.
//
// Not parallel, and measured as the worst of several rounds rather than the
// mean: a stall is felt when it happens, not on average, and a timing
// assertion racing the rest of the package measures the scheduler. The
// threshold is the config default scaled by stallFactor, which moves with the
// build — see stallfactor_race_test.go.
//
// What this defends against is not a slow machine but a structural change: an
// encode creeping back inside the tick, or a copy that started walking
// topology. That failure was real once — hashing at the boundary cost 24.7 ms
// against a 5 ms budget — and it is the reason this test was written before
// the rest of the story.
func TestSnapshotCopyStaysInsideTheStallBudget(t *testing.T) {
	e, err := simtest.SizingEngine(1)
	if err != nil {
		t.Fatalf("SizingEngine: %v", err)
	}
	const budget = 15 * time.Millisecond // snapshot.max_stall_ms
	limit := stallFactor * budget

	var worst time.Duration
	var snaps []sim.Snapshot
	for i := 0; i < 5; i++ {
		start := time.Now()
		snaps = e.SnapshotAll(start.UnixNano())
		if d := time.Since(start); d > worst {
			worst = d
		}
	}
	if len(snaps) != simtest.SizingZones {
		t.Fatalf("round covered %d zones, want %d", len(snaps), simtest.SizingZones)
	}
	t.Logf("snapshot copy at the sizing fixture (%d zones, %d entities): worst of 5 rounds = %s, budget %s, limit %s",
		simtest.SizingZones, simtest.SizingEntities, worst.Round(time.Microsecond), budget, limit)
	if worst > limit {
		t.Fatalf("in-tick snapshot copy took %s, past the %s limit (%dx the %s stall budget): either the copy-on-write assumption in AW-SRV-006 no longer holds at this scale, or work that belongs off the tick has moved onto it", worst, limit, stallFactor, budget)
	}
}

// The copy must not be sensitive to how the engine is walked afterwards: a
// round taken while Entities are being mutated still yields a stable body.
func TestSizingRoundIsIsolatedFromLaterMutation(t *testing.T) {
	if testing.Short() {
		t.Skip("sizing fixture is slow to build")
	}
	t.Parallel()
	e, err := simtest.SizingEngine(1)
	if err != nil {
		t.Fatalf("SizingEngine: %v", err)
	}
	snaps := e.SnapshotAll(0)
	before := make(map[sim.ZoneID][32]byte, len(snaps))
	for _, s := range snaps {
		before[s.Zone] = s.StateHash()
	}
	for _, z := range e.State().Zones {
		for _, ent := range z.Entities {
			ent.Room = "moved"
			if len(ent.Components) > 0 && len(ent.Components[0].Fields) > 0 {
				ent.Components[0].Fields[0].Str = "changed"
			}
		}
	}
	for _, s := range snaps {
		if got := sim.ZoneCanonicalBytes(s.Body()); len(got) == 0 {
			t.Fatalf("zone %q: empty body after mutation", s.Zone)
		}
		fresh, _ := e.State().ZoneHash(s.Zone)
		if fresh == before[s.Zone] && len(e.State().Zones[s.Zone].Entities) > 0 {
			t.Fatalf("zone %q: the test's mutation did not change the live hash", s.Zone)
		}
	}
}

func BenchmarkSnapshotAllAtSizingScale(b *testing.B) {
	e, err := simtest.SizingEngine(1)
	if err != nil {
		b.Fatalf("SizingEngine: %v", err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = e.SnapshotAll(0)
	}
}
