// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package sim

import (
	"errors"
	"fmt"
	"sort"
)

// RoundState is one snapshot round as decoded: the process-wide values every
// object in the round carries, and every Zone's body. It is what the load
// phase of AW-SRV-007's sequence turns into an Engine, and what AW-SRV-019's
// state projector bootstraps from.
type RoundState struct {
	Tick         Tick
	StateVersion uint32
	PRNG         [4]uint64
	NextEventID  uint64
	// Offsets is the next-to-read offset on every Partition the writer
	// owned. The restored Engine owns exactly these, because the offsets are
	// part of the State Hash: a replica that owned a different set would
	// never hash equal to the World it replicates.
	Offsets []PartitionOffset
	Zones   []*ZoneState
	// Content is the pack versions in effect at Tick and ContentDigest their
	// world_digest (SnapshotEnvelope fields 8 and 9, AW-SRV-012). A round
	// written before any swap applied carries neither.
	Content       map[string]uint64
	ContentDigest []byte
	// SimSeed is the seed the World ran with (SnapshotEnvelope.sim_seed,
	// AW-SRV-043): 0 for a round written before the field, whose seed is
	// derived as #307 derives it.
	SimSeed uint64
	// RecordedHash is the World State Hash the round's own tick recorded:
	// TickCompleted.state_hash for Tick, read by the caller from the
	// boundary log. A restore must reproduce it before anything replays on
	// top (AW-SRV-043).
	RecordedHash []byte
}

// Errors a restore refuses with (AW-SRV-043). Neither falls back to an older
// round or to replay from zero: an operator decides.
var (
	// ErrRestoreMismatch: the restored Engine does not hash to its round
	// tick's recorded State Hash. Wrapped by *RestoreMismatch.
	ErrRestoreMismatch = errors.New("restore mismatch")
	// ErrSeedMismatch: the round was written with a different seed than the
	// one this process runs with. Wrapped by *SeedMismatch.
	ErrSeedMismatch = errors.New("seed mismatch")
)

// RestoreMismatch names a restore that does not reproduce its round.
type RestoreMismatch struct {
	RoundTick          uint64
	Recorded, Restored []byte // TickCompleted.state_hash vs the restored Engine's StateHash
}

func (e *RestoreMismatch) Error() string {
	return fmt.Sprintf("restore mismatch: the round at tick %d restores to state hash %x, its tick recorded %x", e.RoundTick, e.Restored, e.Recorded)
}

func (e *RestoreMismatch) Unwrap() error { return ErrRestoreMismatch }

// SeedMismatch names a round written with another seed.
type SeedMismatch struct {
	RoundTick            uint64
	Recorded, Configured uint64
}

func (e *SeedMismatch) Error() string {
	return fmt.Sprintf("seed mismatch: the round at tick %d was written with sim seed %d, this process runs with %d", e.RoundTick, e.Recorded, e.Configured)
}

func (e *SeedMismatch) Unwrap() error { return ErrSeedMismatch }

// RestoreOutcome is andara_restore_total's outcome label for what
// RestoreEngine returned: ok, hash_mismatch, or seed_mismatch. Any other
// refusal (a newer state version, a content digest) is not a restore
// outcome, and reports "".
func RestoreOutcome(err error) string {
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, ErrSeedMismatch):
		return "seed_mismatch"
	case errors.Is(err, ErrRestoreMismatch):
		return "hash_mismatch"
	}
	return ""
}

// EffectiveSeed is the seed an Engine started from no content runs with:
// sim.seed when it is set, else the default derived from the empty World
// (AW-SRV-012, #307).
func EffectiveSeed(configured uint64) uint64 {
	if configured != 0 {
		return configured
	}
	return DeriveSeed(EmptyWorld())
}

// RestoreEngine builds an Engine at a round's tick from its decoded state.
//
// cfg.Partitions is ignored in favor of the round's offsets. A Zone the round
// carries that the World does not have is an error: the content that wrote
// the round is not the content loaded, and restoring would drop Entities
// without a trace. A Zone the World has that the round does not is started
// empty — whether a round missing one of the process's Zones is complete is
// the caller's question (store.ListRounds), not this function's.
//
// w and templates must be the topology r.Content builds (PrepareContent):
// its digest is checked against r.ContentDigest before any body is loaded, a
// mismatch is ErrContentDigest, and the restored Engine has r.Content in
// effect (AW-SRV-012).
//
// The restored Engine must hash to r.RecordedHash, the round tick's own
// recorded State Hash, or the restore is a *RestoreMismatch: a bad restore
// is named at the round's tick, not as a divergence one tick later (#143,
// AW-SRV-043). The checks run in the contract's order: state version,
// content digest, seed, build, hash. The seed comes before the build, so a
// seed mismatch is never reported as a hash mismatch. A round with no
// RecordedHash is refused: a caller that skipped reading it would skip the
// check.
func RestoreEngine(w *World, templates *TemplateRegistry, cfg Config, r RoundState) (*Engine, error) {
	if r.StateVersion != StateVersion {
		return nil, &ErrStateVersion{Have: r.StateVersion, Want: StateVersion}
	}
	if len(r.RecordedHash) == 0 {
		return nil, fmt.Errorf("restore: the round at tick %d has no recorded state hash; read its Tick Boundary Record first", r.Tick)
	}
	digest := ContentDigest(Topology{World: w, Templates: templates})
	if len(r.Content) > 0 && string(digest[:]) != string(r.ContentDigest) {
		return nil, &ContentDigestError{Tick: r.Tick, Pack: "(snapshot round)", Recorded: r.ContentDigest, Built: digest}
	}
	// The seed is in the State Hash, so a restore must arrive at the one the
	// World ran with. The default is derived from the topology an Engine
	// starts with, which is the empty World (AW-SRV-012), never w: NewEngine
	// would derive it from w, and the restore would hash wrong (#143). A
	// round that records its seed is held to it; one written before the
	// field (0) is restored on the derived one, as #307 does.
	cfg.Seed = EffectiveSeed(cfg.Seed)
	if r.SimSeed != 0 && r.SimSeed != cfg.Seed {
		return nil, &SeedMismatch{RoundTick: uint64(r.Tick), Recorded: r.SimSeed, Configured: cfg.Seed}
	}
	parts := make([]int32, 0, len(r.Offsets))
	for _, po := range r.Offsets {
		parts = append(parts, po.Partition)
	}
	cfg.Partitions = parts
	e := NewEngine(w, templates, cfg)
	if len(r.Content) > 0 {
		e.versions, e.digest = copyVersions(r.Content), digest
	}
	s := e.state
	s.Tick = r.Tick
	s.RNG.Restore(r.PRNG)
	s.NextEventID = r.NextEventID
	for _, po := range r.Offsets {
		s.Offsets[po.Partition] = po.Offset
	}
	seen := make(map[ZoneID]bool, len(r.Zones))
	for _, z := range r.Zones {
		if _, ok := s.Zones[z.ID]; !ok {
			return nil, fmt.Errorf("restore: round at tick %d carries zone %q, which the loaded content does not have", r.Tick, z.ID)
		}
		if seen[z.ID] {
			return nil, fmt.Errorf("restore: round at tick %d carries zone %q twice", r.Tick, z.ID)
		}
		seen[z.ID] = true
		s.Zones[z.ID] = z.Clone()
	}
	if got := e.StateHash(); string(got[:]) != string(r.RecordedHash) {
		return nil, &RestoreMismatch{RoundTick: uint64(r.Tick), Recorded: append([]byte(nil), r.RecordedHash...), Restored: got[:]}
	}
	return e, nil
}

// SortedZoneIDs is every Zone in the state, sorted.
func (s *WorldState) SortedZoneIDs() []ZoneID {
	out := make([]ZoneID, 0, len(s.Zones))
	for id := range s.Zones {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
