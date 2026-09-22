// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package store

import (
	"fmt"
	"sort"

	statev1 "github.com/valesordev/andara/gen/go/andara/state/v1"
	"github.com/valesordev/andara/server/sim"
	"google.golang.org/protobuf/proto"
)

// migrations carries a snapshot body forward one state_version at a time. The
// entry keyed n migrates a body written at version n into version n+1, and a
// read applies every entry from the envelope's version up to the binary's, in
// order.
//
// One per bump, and `make check` fails on a bump without one — see
// TestMigrationsCoverEveryVersion. That check is the point of the map: a
// state_version that moves without a migration is a World that will not load,
// discovered at recovery rather than at build.
//
// Empty at version 1, because there is nothing before version 1 to come from.
// Note that this is *not* where an added protobuf field goes: protobuf absorbs
// those, and state_version moves only when the meaning of state changes
// (ADR-0007 rule 2, sim.StateVersion). See
// docs/feedback/AW-SRV-006-zone-snapshots.md §1 and §6.
var migrations = map[uint32]func(*statev1.ZoneState) error{}

// Decode reads a snapshot envelope, migrates the body forward to the binary's
// state_version, and returns both the envelope and the Zone it carries.
//
// AC-4, in two halves. An older body is migrated through every intermediate
// version, in order, with no step skipped. A newer one is refused with
// sim.ErrStateVersion naming both versions, because a binary that guessed at
// state it does not understand would serve a World it had misread — and on a
// rollback, knowing which snapshot the older binary *can* read is what
// AW-INF-007 needs. Never a partial or silent read: an error here returns no
// Zone at all.
func Decode(envelope []byte) (*statev1.SnapshotEnvelope, *sim.ZoneState, error) {
	var env statev1.SnapshotEnvelope
	if err := proto.Unmarshal(envelope, &env); err != nil {
		return nil, nil, fmt.Errorf("store: decode envelope: %w", err)
	}
	have := env.GetStateVersion()
	if have > sim.StateVersion {
		return nil, nil, &sim.ErrStateVersion{Have: have, Want: sim.StateVersion}
	}
	var body statev1.ZoneState
	if err := proto.Unmarshal(env.GetBody(), &body); err != nil {
		return nil, nil, fmt.Errorf("store: decode body for zone %s: %w", env.GetZoneId(), err)
	}
	if err := Migrate(&body, have, sim.StateVersion); err != nil {
		return nil, nil, err
	}
	return &env, sim.ZoneStateFromProto(&body), nil
}

// Migrate carries body forward from version `from` to version `to`, applying
// each step in order. A missing step is an error rather than a skip: skipping
// one would produce a body that decodes and means something else.
//
// `to` is a parameter rather than sim.StateVersion read directly, because
// sim.StateVersion is a const: a test cannot exercise the chain this function
// exists for without one. Decode passes sim.StateVersion, which is the only
// value production ever uses.
func Migrate(body *statev1.ZoneState, from, to uint32) error {
	if from == 0 {
		return fmt.Errorf("store: state_version 0 is not a version any snapshot was written at")
	}
	for v := from; v < to; v++ {
		step, ok := migrations[v]
		if !ok {
			return fmt.Errorf("store: no migration from state_version %d to %d; the binary cannot read this snapshot", v, v+1)
		}
		if err := step(body); err != nil {
			return fmt.Errorf("store: migrating zone %s from state_version %d to %d: %w", body.GetZoneId(), v, v+1, err)
		}
	}
	return nil
}

// MigrationVersions lists the versions the registry can migrate from, sorted.
// Exported for the completeness test and for an operator-facing diagnostic.
func MigrationVersions() []uint32 {
	out := make([]uint32, 0, len(migrations))
	for v := range migrations {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
