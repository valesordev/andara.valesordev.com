package sim

import (
	"fmt"
	"strings"
	"testing"

	contentv1 "github.com/valesordev/andara/gen/go/andara/content/v1"
)

// AC-10: two loads of the same content must serialize byte-identically,
// including the order of Exits within a Room, and load must not be
// map-iteration dependent.
//
// Building the same literal twice does not test that. Go's map iteration is
// randomized per range statement, so the only way an ordering bug shows up is
// if the input's own ordering is varied and the output is still required to be
// identical. These tests shuffle the Room order and the Exit order inside the
// definition — the two orderings the loader is allowed to normalize away — and
// hold the serialization fixed across many iterations.

const determinismIterations = 200

// shuffler permutes slices from an explicit seed. depguard denies math/rand
// inside server/sim — including its tests — because global randomness breaks
// replay (ADR-0002), and widening that list for test convenience would be the
// wrong trade. A xorshift written out here is also the better tool: the
// permutations are reproducible from the seed, so a failure is replayable
// rather than a thing that happened once on someone's machine.
type shuffler struct{ state uint64 }

func newShuffler(seed uint64) *shuffler {
	if seed == 0 {
		seed = 0x9e3779b97f4a7c15
	}
	return &shuffler{state: seed}
}

func (s *shuffler) next() uint64 {
	s.state ^= s.state << 13
	s.state ^= s.state >> 7
	s.state ^= s.state << 17
	return s.state
}

// shuffle applies a Fisher-Yates permutation through swap.
func (s *shuffler) shuffle(n int, swap func(i, j int)) {
	for i := n - 1; i > 0; i-- {
		swap(i, int(s.next()%uint64(i+1)))
	}
}

// shuffledWorld builds a fixed Zone graph with Rooms and Exits in a permuted
// order. Its topology is identical every call; only the input ordering moves.
func shuffledWorld(t *testing.T, rnd *shuffler) *World {
	t.Helper()

	const n = 24
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("r%02d", i)
	}

	rooms := make([]*contentv1.RoomDefinition, 0, n)
	for i, id := range ids {
		exits := []*contentv1.ExitDefinition{
			{Direction: "north", ToRoom: ids[(i+1)%n]},
			{Direction: "south", ToRoom: ids[(i+n-1)%n]},
			{Direction: "east", ToRoom: ids[(i+7)%n]},
			{Direction: "up", ToZone: "wilds", ToRoom: "trail"},
		}
		rnd.shuffle(len(exits), func(a, b int) { exits[a], exits[b] = exits[b], exits[a] })
		rooms = append(rooms, &contentv1.RoomDefinition{
			Id:          id,
			Title:       "Room " + id,
			Description: "A room.",
			Exits:       exits,
		})
	}
	rnd.shuffle(len(rooms), func(a, b int) { rooms[a], rooms[b] = rooms[b], rooms[a] })

	inputs := []Input{
		zone("town.json", "town", "Town", rooms...),
		zone("wilds.json", "wilds", "Wilds", room("trail", "Trail", exit("down", "town", "r00"))),
	}
	rnd.shuffle(len(inputs), func(a, b int) { inputs[a], inputs[b] = inputs[b], inputs[a] })

	world, errs := BuildWorld(inputs, Options{})
	if world == nil {
		t.Fatalf("world is nil: %v", errs)
	}
	return world
}

func TestBuildWorld_OrderingOfInputDoesNotReachTheTopology(t *testing.T) {
	rnd := newShuffler(1)
	want := string(CanonicalBytes(shuffledWorld(t, rnd)))
	if want == "" {
		t.Fatal("canonical bytes are empty")
	}
	for i := 1; i < determinismIterations; i++ {
		got := string(CanonicalBytes(shuffledWorld(t, rnd)))
		if got != want {
			t.Fatalf("iteration %d differs from the first load:\nwant:\n%s\ngot:\n%s", i, want, got)
		}
	}
}

// The same property, stated on the Exit slice directly rather than through the
// serialization, so a regression is legible without diffing two blobs.
func TestBuildWorld_ExitOrderIsSortedRegardlessOfInputOrder(t *testing.T) {
	rnd := newShuffler(2)
	for i := 0; i < determinismIterations; i++ {
		world := shuffledWorld(t, rnd)
		for zid, z := range world.Zones {
			for rid, r := range z.Rooms {
				for k := 1; k < len(r.Exits); k++ {
					if r.Exits[k-1].Direction >= r.Exits[k].Direction {
						t.Fatalf("iteration %d: %s/%s exits not strictly sorted: %v",
							i, zid, rid, directions(r.Exits))
					}
				}
			}
		}
	}
}

// A Room map must not leak its iteration order into the Exit slice of another
// Room, which is the failure the serialization comparison would catch late.
// Asserting the exact expected sequence catches it at the point of damage.
func TestBuildWorld_ExitSequenceIsExact(t *testing.T) {
	rnd := newShuffler(3)
	for i := 0; i < determinismIterations; i++ {
		world := shuffledWorld(t, rnd)
		r, ok := world.Resolve(RoomRef{Zone: "town", Room: "r05"})
		if !ok {
			t.Fatal("r05 missing")
		}
		got := strings.Join(directions(r.Exits), ",")
		if want := "east,north,south,up"; got != want {
			t.Fatalf("iteration %d: directions = %s, want %s", i, got, want)
		}
	}
}

// CanonicalBytes must be injective: distinct topologies must not collapse onto
// one serialization. Before escaping, a Title containing a tab moved the field
// boundary and a Description containing a newline could forge a whole record,
// which would have let a determinism comparison pass over a loader that was
// losing data rather than ordering it.
func TestCanonicalBytes_SeparatorsInContentCannotForgeRecords(t *testing.T) {
	build := func(title, desc string) string {
		world, errs := BuildWorld([]Input{
			zone("z.json", "town", "Town", &contentv1.RoomDefinition{
				Id: "plaza", Title: title, Description: desc,
			}),
		}, Options{})
		if world == nil {
			t.Fatalf("world is nil: %v", errs)
		}
		return string(CanonicalBytes(world))
	}

	// A tab moved from the Title into the Description is a different World.
	if a, b := build("A\tB", "d"), build("A", "B\td"); a == b {
		t.Errorf("tab in a field collapses two Worlds onto one serialization: %q", a)
	}
	// A Description cannot introduce a record of its own.
	forged := build("X", "d\nroom\ttown\tghost\tGhost\td")
	if n := strings.Count(forged, "\n"); n != 2 {
		t.Errorf("serialization has %d records, want 2 (zone + room):\n%q", n, forged)
	}
	if strings.Contains(forged, "\nroom\ttown\tghost") {
		t.Errorf("a Description forged a room record:\n%q", forged)
	}
	// A backslash must not let content escape the escaping.
	if a, b := build(`A\`, "tB"), build("A", "\tB"); a == b {
		t.Errorf("backslash in a field is ambiguous with an escape: %q", a)
	}
}

func TestCanonicalBytes_NilWorld(t *testing.T) {
	if b := CanonicalBytes(nil); b != nil {
		t.Errorf("CanonicalBytes(nil) = %q, want nil", b)
	}
}

func directions(exits []Exit) []string {
	out := make([]string, len(exits))
	for i, e := range exits {
		out[i] = string(e.Direction)
	}
	return out
}
