// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package sim_test

import (
	"errors"
	"testing"

	"google.golang.org/protobuf/proto"

	statev1 "github.com/valesordev/andara/gen/go/andara/state/v1"
	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/simtest"
)

// restoreFixture is a World as the server runs it — started with no content,
// on the given seed, its content brought in by a swap — and its round at the
// current tick, read back through the codec a store holds it in, with the
// round tick's recorded State Hash as the boundary log carries it.
type restoreFixture struct {
	c     *fakeContent
	live  *sim.Engine
	topo  sim.Topology
	round sim.RoundState
}

func newRestoreFixture(t *testing.T, seed uint64) *restoreFixture {
	t.Helper()
	c := newContent(t)
	live := sim.NewEngine(sim.EmptyWorld(), nil, sim.Config{Seed: seed, Partitions: simtest.AllPartitions(), Handlers: sim.Handlers(), Content: c})
	mustStep(t, live, c.swap(t, live, nil, "town", 3))
	mustStep(t, live, simtest.Bind("town", "ch-1", "Aldric", "plaza"))
	mustStep(t, live, simtest.Bind("docks", "ch-2", "Bryn", "pier"))
	mustStep(t, live)

	versions, digest := live.Content()
	recorded := live.StateHash()
	round := sim.RoundState{Tick: live.Tick(), StateVersion: sim.StateVersion, Content: versions, ContentDigest: digest[:], RecordedHash: recorded[:]}
	for _, s := range live.SnapshotAll(1) {
		raw, err := s.Encode()
		if err != nil {
			t.Fatal(err)
		}
		var env statev1.SnapshotEnvelope
		var body statev1.ZoneState
		if err := proto.Unmarshal(raw, &env); err != nil {
			t.Fatal(err)
		}
		if err := proto.Unmarshal(env.GetBody(), &body); err != nil {
			t.Fatal(err)
		}
		prng, err := sim.DecodePRNG(body.GetPrngState())
		if err != nil {
			t.Fatal(err)
		}
		round.PRNG, round.NextEventID, round.SimSeed, round.Offsets = prng, body.GetNextEventId(), env.GetSimSeed(), nil
		for _, po := range env.GetOffsets() {
			round.Offsets = append(round.Offsets, sim.PartitionOffset{Partition: po.GetPartition(), Offset: po.GetOffset()})
		}
		round.Zones = append(round.Zones, sim.ZoneStateFromProto(&body))
	}
	topo, err := sim.PrepareContent(c, versions)
	if err != nil {
		t.Fatal(err)
	}
	return &restoreFixture{c: c, live: live, topo: topo, round: round}
}

func (f *restoreFixture) restore(seed uint64, r sim.RoundState) (*sim.Engine, error) {
	return sim.RestoreEngine(f.topo.World, f.topo.Templates, sim.Config{Seed: seed, Handlers: sim.Handlers(), Content: f.c}, r)
}

// AC-1: a round whose restored Zones hash to its tick's recorded State Hash
// restores, and replay proceeds from it.
func TestRestore_VerifiesItsRound(t *testing.T) {
	for _, seed := range []uint64{0, 7} {
		f := newRestoreFixture(t, seed)
		e, err := f.restore(seed, f.round)
		if err != nil {
			t.Fatalf("seed %d: %v", seed, err)
		}
		a, b := mustStep(t, f.live), mustStep(t, e)
		if a.Completed.StateHash != b.Completed.StateHash {
			t.Fatalf("seed %d: tick %d after the restore diverged", seed, a.Tick)
		}
	}
}

// AC-2: a Zone body altered so that it decodes, but hashes differently, is
// refused at the round's tick, naming both hashes; no Engine is returned.
func TestRestore_AlteredBodyIsARestoreMismatch(t *testing.T) {
	f := newRestoreFixture(t, 7)
	r := f.round
	r.Zones = append([]*sim.ZoneState(nil), r.Zones...)
	for i, z := range r.Zones {
		if z.ID != "town" {
			continue
		}
		z = z.Clone()
		z.Entities["ch-1"].Name = "Aldrik"
		r.Zones[i] = z
	}
	e, err := f.restore(7, r)
	var rm *sim.RestoreMismatch
	if !errors.As(err, &rm) || !errors.Is(err, sim.ErrRestoreMismatch) || e != nil {
		t.Fatalf("restore of an altered body: engine %v, err %v", e != nil, err)
	}
	if rm.RoundTick != uint64(f.round.Tick) || string(rm.Recorded) != string(f.round.RecordedHash) || string(rm.Recorded) == string(rm.Restored) || len(rm.Restored) != 32 {
		t.Fatalf("mismatch %+v", rm)
	}
}

// AC-5: a round written with one seed, restored by a process running with
// another — configured or derived — is a seed mismatch naming both, before
// any hash is compared.
func TestRestore_SeedMismatch(t *testing.T) {
	derived := sim.DeriveSeed(sim.EmptyWorld())
	for _, tc := range []struct {
		name                 string
		written, configured  uint64
		wantRecorded, wantIn uint64
	}{
		{"configured against configured", 7, 8, 7, 8},
		{"derived against configured", 0, 8, derived, 8},
		{"configured against derived", 7, 0, 7, derived},
	} {
		f := newRestoreFixture(t, tc.written)
		// Spoil the recorded hash too: the seed must be refused first.
		r := f.round
		r.RecordedHash = make([]byte, 32)
		_, err := f.restore(tc.configured, r)
		var sm *sim.SeedMismatch
		if !errors.As(err, &sm) || !errors.Is(err, sim.ErrSeedMismatch) || errors.Is(err, sim.ErrRestoreMismatch) {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if sm.Recorded != tc.wantRecorded || sm.Configured != tc.wantIn || sm.RoundTick != uint64(f.round.Tick) {
			t.Fatalf("%s: %+v", tc.name, sm)
		}
	}
}

// AC-6: a round written before the field (sim_seed 0) restores on the derived
// seed, as #307 does: it verifies when the World ran on the default, and
// is a hash mismatch, not a seed mismatch, when it didn't.
func TestRestore_PreFieldRound(t *testing.T) {
	f := newRestoreFixture(t, 0)
	r := f.round
	r.SimSeed = 0
	if _, err := f.restore(0, r); err != nil {
		t.Fatalf("a pre-field round written on the default seed: %v", err)
	}
	g := newRestoreFixture(t, 7)
	r = g.round
	r.SimSeed = 0
	if _, err := g.restore(0, r); !errors.Is(err, sim.ErrRestoreMismatch) || errors.Is(err, sim.ErrSeedMismatch) {
		t.Fatalf("a pre-field round written on seed 7, restored on the default: %v", err)
	}
}

// AC-7: a server on the default seed records the derived value, not 0.
func TestRestore_RoundRecordsTheDerivedSeed(t *testing.T) {
	f := newRestoreFixture(t, 0)
	if want := sim.DeriveSeed(sim.EmptyWorld()); f.round.SimSeed != want || want == 0 {
		t.Fatalf("round records sim_seed %d, want the derived %d", f.round.SimSeed, want)
	}
	if g := newRestoreFixture(t, 7); g.round.SimSeed != 7 {
		t.Fatalf("round records sim_seed %d, want 7", g.round.SimSeed)
	}
}

// AC-8: #143's fault — the default seed derived from the round's World
// rather than the empty World the server starts from — reintroduced, is
// named at the round's tick, not as a divergence one tick later.
func TestRestore_Issue143IsNamedAtTheRoundTick(t *testing.T) {
	f := newRestoreFixture(t, 0)
	r := f.round
	r.SimSeed = 0 // a pre-field round, so nothing but the hash can catch it
	pre307 := sim.DeriveSeed(f.topo.World)
	if pre307 == sim.DeriveSeed(sim.EmptyWorld()) {
		t.Fatal("the fixture's World derives the same seed as the empty World; it can't show #143")
	}
	_, err := f.restore(pre307, r)
	var rm *sim.RestoreMismatch
	if !errors.As(err, &rm) || rm.RoundTick != uint64(f.round.Tick) {
		t.Fatalf("#143 reintroduced: %v, want a restore mismatch at tick %d", err, f.round.Tick)
	}
}

// A caller that never read the round tick's boundary is refused rather than
// skipping the check.
func TestRestore_NoRecordedHashIsRefused(t *testing.T) {
	f := newRestoreFixture(t, 7)
	r := f.round
	r.RecordedHash = nil
	e, err := f.restore(7, r)
	if err == nil || e != nil {
		t.Fatalf("restore with no recorded hash: engine %v, err %v", e != nil, err)
	}
	// A caller's bug, not a bad round: no restore outcome, no exit 5.
	if errors.Is(err, sim.ErrRestoreMismatch) || errors.Is(err, sim.ErrSeedMismatch) || sim.RestoreOutcome(err) != "" {
		t.Fatalf("restore with no recorded hash reads as a mismatch: %v", err)
	}
}
