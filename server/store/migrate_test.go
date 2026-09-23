// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package store

import (
	"errors"
	"testing"

	statev1 "github.com/valesordev/andara/gen/go/andara/state/v1"
	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/simtest"
	"google.golang.org/protobuf/proto"
)

// The check the story asks `make check` to enforce: a state_version bump
// without a migration fails the build.
//
// Expressed as "every version from 1 to StateVersion-1 has an entry" rather
// than as a count, so that bumping sim.StateVersion is what fails — not
// editing this file. At StateVersion 1 the required set is empty and this
// passes vacuously, which is correct: there is nothing before the first
// version to migrate from.
func TestMigrationsCoverEveryVersion(t *testing.T) {
	t.Parallel()
	for v := uint32(1); v < sim.StateVersion; v++ {
		if _, ok := migrations[v]; !ok {
			t.Errorf("no migration from state_version %d to %d.\n"+
				"sim.StateVersion is %d, so a snapshot written at %d must be readable by this binary. "+
				"Add an entry to migrations in server/store/migrate.go.", v, v+1, sim.StateVersion, v)
		}
	}
	for _, v := range MigrationVersions() {
		if v >= sim.StateVersion {
			t.Errorf("migrations has an entry from state_version %d, but sim.StateVersion is %d: "+
				"a migration to a version that does not exist will never run", v, sim.StateVersion)
		}
	}
}

// The test plan's chain: 1→2→3 applied in order, with 4 refused. Run against a
// substituted registry, because the real one is empty at StateVersion 1 and
// the machinery has to be proven before there is a real bump to prove it with
// (docs/feedback/AW-SRV-006-zone-snapshots.md §6).
func TestMigrationChainAppliesEveryStepInOrder(t *testing.T) {
	defer swapMigrations(map[uint32]func(*statev1.ZoneState) error{
		1: func(z *statev1.ZoneState) error { z.ZoneId += "+1"; return nil },
		2: func(z *statev1.ZoneState) error { z.ZoneId += "+2"; return nil },
		3: func(z *statev1.ZoneState) error { z.ZoneId += "+3"; return nil },
	})()

	for _, tc := range []struct {
		from uint32
		want string
	}{
		{1, "village+1+2+3"},
		{2, "village+2+3"},
		{3, "village+3"},
		{4, "village"}, // already current: no step runs
	} {
		body := &statev1.ZoneState{ZoneId: "village"}
		if err := Migrate(body, tc.from, 4); err != nil {
			t.Fatalf("Migrate from %d: %v", tc.from, err)
		}
		if body.GetZoneId() != tc.want {
			t.Errorf("Migrate from %d gave %q, want %q — the steps ran out of order or one was skipped",
				tc.from, body.GetZoneId(), tc.want)
		}
	}
}

// A gap in the chain is refused, not skipped: skipping a step yields a body
// that decodes and means something else.
func TestMigrateRefusesAGapInTheChain(t *testing.T) {
	defer swapMigrations(map[uint32]func(*statev1.ZoneState) error{
		1: func(z *statev1.ZoneState) error { z.ZoneId += "+1"; return nil },
		// 2 is missing.
		3: func(z *statev1.ZoneState) error { z.ZoneId += "+3"; return nil },
	})()

	err := Migrate(&statev1.ZoneState{ZoneId: "village"}, 1, 4)
	if err == nil {
		t.Fatal("Migrate across a missing step succeeded, want a refusal")
	}
	if !contains(err.Error(), "2") || !contains(err.Error(), "3") {
		t.Errorf("error %q does not name the missing step", err)
	}
}

// AC-4's refusal half: an envelope newer than the binary is refused with both
// versions named, and nothing is returned.
func TestDecodeRefusesANewerStateVersion(t *testing.T) {
	t.Parallel()
	env := &statev1.SnapshotEnvelope{
		StateVersion: sim.StateVersion + 1,
		ZoneId:       "village",
		Tick:         10,
	}
	b, err := proto.Marshal(env)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	gotEnv, zone, err := Decode(b)
	if err == nil {
		t.Fatal("Decode of a newer snapshot succeeded, want ErrStateVersion")
	}
	var verr *sim.ErrStateVersion
	if !errors.As(err, &verr) {
		t.Fatalf("Decode error is %T (%v), want *sim.ErrStateVersion", err, err)
	}
	if verr.Have != sim.StateVersion+1 || verr.Want != sim.StateVersion {
		t.Errorf("ErrStateVersion{Have:%d, Want:%d}, want {%d, %d}", verr.Have, verr.Want, sim.StateVersion+1, sim.StateVersion)
	}
	// Never a partial read.
	if gotEnv != nil || zone != nil {
		t.Error("Decode returned an envelope or a Zone alongside the refusal")
	}
}

// The whole path a writer and a reader share: encode a Snapshot, decode it
// back, and land on a Zone that hashes the same.
func TestDecodeRoundTripsAnEncodedSnapshot(t *testing.T) {
	t.Parallel()
	e, err := newSnapshotFixture()
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	for _, s := range e.SnapshotAll(1758500000000000000) {
		wire, err := s.Encode()
		if err != nil {
			t.Fatalf("Encode %s: %v", s.Zone, err)
		}
		env, zone, err := Decode(wire)
		if err != nil {
			t.Fatalf("Decode %s: %v", s.Zone, err)
		}
		if env.GetZoneId() != string(s.Zone) {
			t.Errorf("zone_id = %q, want %q", env.GetZoneId(), s.Zone)
		}
		if env.GetTick() != uint64(s.Tick) {
			t.Errorf("tick = %d, want %d", env.GetTick(), s.Tick)
		}
		// AC-3, end to end: the envelope's hash is the hash of the Zone the
		// envelope carries.
		want := s.StateHash()
		if got := sim.HashZone(zone); got != want {
			t.Errorf("zone %s: decoded Zone hashes %x, envelope says %x", s.Zone, got, want)
		}
	}
}

func TestDecodeRefusesGarbage(t *testing.T) {
	t.Parallel()
	if _, _, err := Decode([]byte{0xff, 0xff, 0xff, 0xff}); err == nil {
		t.Fatal("Decode of garbage succeeded")
	}
}

func TestMigrateRefusesVersionZero(t *testing.T) {
	t.Parallel()
	if err := Migrate(&statev1.ZoneState{}, 0, sim.StateVersion); err == nil {
		t.Fatal("Migrate from state_version 0 succeeded, want a refusal")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// swapMigrations substitutes the registry for a test and returns the undo.
// Not parallel-safe, so the tests that use it do not call t.Parallel.
func swapMigrations(m map[uint32]func(*statev1.ZoneState) error) func() {
	saved := migrations
	migrations = m
	return func() { migrations = saved }
}

// newSnapshotFixture is a small two-Zone World with one populated Entity, for
// the end-to-end encode/decode path.
func newSnapshotFixture() (*sim.Engine, error) {
	w, err := simtest.World()
	if err != nil {
		return nil, err
	}
	reg, err := simtest.Templates()
	if err != nil {
		return nil, err
	}
	e := sim.NewEngine(w, reg, sim.Config{Seed: 7, Partitions: simtest.AllPartitions(), Handlers: sim.Handlers()})
	t, ok := reg.Get("town.Merchant")
	if !ok {
		return nil, errors.New("fixture: no town.Merchant")
	}
	ent := sim.Instantiate(t, "merchant-1", "town@1")
	ent.Room = "plaza"
	ent.Name = "Wandering Merchant"
	e.State().Zones["town"].Entities[ent.ID] = &ent
	return e, nil
}
