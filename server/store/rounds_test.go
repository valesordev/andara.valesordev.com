// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package store_test

import (
	"context"
	"strings"
	"testing"

	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/simtest"
	"github.com/valesordev/andara/server/store"
	"google.golang.org/protobuf/proto"
)

// roundFixture runs the fixture for ticks, writes one round at the boundary,
// and returns the engine (still live), the store, and the log's remainder.
func roundFixture(t *testing.T, ticks int) (*sim.Engine, *store.FS, map[int32][]sim.Record, []sim.ZoneID) {
	t.Helper()
	e, err := simtest.NewEngine(11)
	if err != nil {
		t.Fatal(err)
	}
	remaining := simtest.Script(12)
	for range ticks {
		if _, err := e.Step(simtest.Batch(remaining, 2)); err != nil {
			t.Fatal(err)
		}
	}
	fs := store.NewFS(t.TempDir())
	writeRound(t, fs, e.SnapshotAll(1))
	return e, fs, remaining, e.State().SortedZoneIDs()
}

func writeRound(t *testing.T, fs *store.FS, snaps []sim.Snapshot) {
	t.Helper()
	for i := range snaps {
		body, err := snaps[i].Encode()
		if err != nil {
			t.Fatal(err)
		}
		if err := fs.Put(context.Background(), snaps[i].Key(), body); err != nil {
			t.Fatal(err)
		}
	}
}

// The load phase: an Engine restored from a round hashes equal to the one
// that wrote it, and stays equal as both apply the same records.
func TestRestoredEngineContinuesTheWorld(t *testing.T) {
	t.Parallel()
	live, fs, remaining, owned := roundFixture(t, 3)

	round, state, ok, err := store.NewestComplete(context.Background(), fs, owned)
	if err != nil || !ok {
		t.Fatalf("NewestComplete: ok=%v err=%v", ok, err)
	}
	if round.Tick != live.Tick() || !round.Complete {
		t.Fatalf("round %+v, want complete at tick %d", round, live.Tick())
	}
	w, err := simtest.World()
	if err != nil {
		t.Fatal(err)
	}
	reg, err := simtest.Templates()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := sim.RestoreEngine(w, reg, sim.Config{Seed: 11, Handlers: simtest.Handlers(reg)}, state)
	if err != nil {
		t.Fatal(err)
	}
	if restored.StateHash() != live.StateHash() {
		t.Fatalf("restored hash %x, live %x", restored.StateHash(), live.StateHash())
	}
	copyRemaining := map[int32][]sim.Record{}
	for p, r := range remaining {
		copyRemaining[p] = append([]sim.Record(nil), r...)
	}
	for range 3 {
		in := simtest.Batch(remaining, 2)
		a, err := live.Step(in)
		if err != nil {
			t.Fatal(err)
		}
		b, err := restored.Step(simtest.Batch(copyRemaining, 2))
		if err != nil {
			t.Fatal(err)
		}
		if a.Completed.StateHash != b.Completed.StateHash {
			t.Fatalf("tick %d: live %x, restored %x", a.Tick, a.Completed.StateHash, b.Completed.StateHash)
		}
	}
}

// Rounds come back newest first, and a newer incomplete round falls through
// to the older complete one.
func TestNewestCompleteSkipsAnIncompleteNewerRound(t *testing.T) {
	t.Parallel()
	live, fs, remaining, owned := roundFixture(t, 2)
	older := live.Tick()
	if _, err := live.Step(simtest.Batch(remaining, 2)); err != nil {
		t.Fatal(err)
	}
	snaps := live.SnapshotAll(2)
	writeRound(t, fs, snaps[1:]) // the first Zone's object never lands

	rounds, err := store.ListRounds(context.Background(), fs, owned)
	if err != nil {
		t.Fatal(err)
	}
	if len(rounds) != 2 || rounds[0].Tick <= rounds[1].Tick {
		t.Fatalf("want two rounds newest first, got %+v", rounds)
	}
	if rounds[0].Complete || !strings.Contains(rounds[0].Reason, string(snaps[0].Zone)) {
		t.Fatalf("newer round should be incomplete naming %s: %+v", snaps[0].Zone, rounds[0])
	}
	round, _, ok, err := store.NewestComplete(context.Background(), fs, owned)
	if err != nil || !ok || round.Tick != older {
		t.Fatalf("NewestComplete = tick %d ok=%v err=%v, want %d", round.Tick, ok, err, older)
	}
}

// A body that no longer matches its envelope's hash makes the round
// incomplete, not an error.
func TestHashInvalidObjectMakesTheRoundIncomplete(t *testing.T) {
	t.Parallel()
	_, fs, _, owned := roundFixture(t, 2)
	rounds, err := store.ListRounds(context.Background(), fs, owned)
	if err != nil || len(rounds) != 1 {
		t.Fatalf("rounds=%v err=%v", rounds, err)
	}
	key := rounds[0].Zones[0].Key
	raw, err := fs.Get(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	env, _, err := store.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	env.StateHash[0] ^= 0xff
	tampered, err := proto.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	if err := fs.Put(context.Background(), key, tampered); err != nil {
		t.Fatal(err)
	}
	rounds, err = store.ListRounds(context.Background(), fs, owned)
	if err != nil {
		t.Fatal(err)
	}
	if rounds[0].Complete || rounds[0].Zones[0].Valid || !strings.Contains(rounds[0].Reason, "hashes to") {
		t.Fatalf("tampered round should be incomplete on the hash: %+v", rounds[0])
	}
	if _, _, ok, _ := store.NewestComplete(context.Background(), fs, owned); ok {
		t.Fatal("NewestComplete selected a hash-invalid round")
	}
}

// AW-SRV-007 AC-11: objects that disagree on the process-wide values are two
// cuts, not a round.
func TestDisagreeingPRNGMakesTheRoundIncomplete(t *testing.T) {
	t.Parallel()
	live, fs, _, owned := roundFixture(t, 1)
	tick := live.Tick()
	// A second engine at the same tick under another seed, so its RNG is
	// elsewhere; one of its Zones is written over the first round's object.
	other, err := simtest.NewEngine(99)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.Step(simtest.Batch(simtest.Script(12), 2)); err != nil {
		t.Fatal(err)
	}
	if other.Tick() != tick {
		t.Fatalf("fixture: ticks %d and %d", other.Tick(), tick)
	}
	snaps := other.SnapshotAll(3)
	rounds, err := store.ListRounds(context.Background(), fs, owned)
	if err != nil {
		t.Fatal(err)
	}
	body, err := snaps[len(snaps)-1].Encode()
	if err != nil {
		t.Fatal(err)
	}
	last := rounds[0].Zones[len(rounds[0].Zones)-1]
	if err := fs.Put(context.Background(), last.Key, body); err != nil {
		t.Fatal(err)
	}
	rounds, err = store.ListRounds(context.Background(), fs, owned)
	if err != nil {
		t.Fatal(err)
	}
	if rounds[0].Complete {
		t.Fatalf("a round mixing two engines' cuts was complete: %+v", rounds[0])
	}
}
