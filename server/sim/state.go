// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package sim

import (
	"crypto/sha256"
	"encoding/binary"
	"sort"
	"strconv"
	"strings"
)

// Tick is the simulation's clock. It advances by exactly one per Step and
// is the only time the core may read (ADR-0002, ADR-0008).
type Tick uint64

// StateVersion is the interpretation version of World state — what the
// State Hash covers and how a snapshot body is read. Present from the first
// commit because adding it later means migrating a live World's history
// (ADR-0002 §4). It moves when the *meaning* of state changes, never for an
// additive field protobuf absorbs.
const StateVersion uint32 = 1

// ZoneState is the mutable state of one Zone: its Entities, and whether a
// panic has quarantined it. Only the tick that owns the Zone's Partition
// mutates it.
type ZoneState struct {
	ID       ZoneID
	Entities map[EntityID]*EntityState
	// Faulted is set when applying a Command panicked inside this Zone
	// (AC-12). A faulted Zone's Partition stops advancing until the process
	// restarts; the state left behind is deterministic — the same records
	// panic at the same point on replay — and is hashed as it stands.
	Faulted     bool
	FaultedTick Tick
}

// WorldState is everything mutable the simulation holds, and everything the
// State Hash covers. Topology (World) is content-versioned and hashed by
// AW-SRV-012's ContentSwap record, not here.
type WorldState struct {
	Version     uint32
	Tick        Tick
	Seed        uint64
	RNG         *RNG
	NextEventID uint64
	// Offsets is the next-to-read offset per Partition this process owns —
	// Kafka's committed-offset convention, so a TickCompleted record can be
	// handed straight to a consumer commit. A tick's applied range is
	// [previous, current) on each Partition.
	Offsets map[int32]int64
	Zones   map[ZoneID]*ZoneState
}

// NewWorldState builds the initial state for a World: every Zone empty and
// unfaulted, every assigned Partition at offset 0, the RNG seeded.
func NewWorldState(w *World, seed uint64, partitions []int32) *WorldState {
	s := &WorldState{
		Version:     StateVersion,
		Seed:        seed,
		RNG:         NewRNG(seed),
		NextEventID: 1,
		Offsets:     make(map[int32]int64, len(partitions)),
		Zones:       make(map[ZoneID]*ZoneState, len(w.Zones)),
	}
	for _, p := range partitions {
		s.Offsets[p] = 0
	}
	for id := range w.Zones {
		s.Zones[id] = &ZoneState{ID: id, Entities: map[EntityID]*EntityState{}}
	}
	return s
}

// DeriveSeed is the default sim.seed: a function of the World's topology,
// so two processes loading the same content start from the same seed
// without anyone choosing one. Overriding it is a debugging affordance.
func DeriveSeed(w *World) uint64 {
	sum := sha256.Sum256(CanonicalBytes(w))
	return binary.BigEndian.Uint64(sum[:8])
}

// Hash is the State Hash: SHA-256 over the canonical serialization of
// everything mutable. Two processes that applied the same records from the
// same starting state hash identically, on every platform — no map order,
// no floats, no wall clock reaches the input (ADR-0007 rule 3).
//
// Covered, in order: state_version, tick, seed, RNG state, next EventID,
// next offset per Partition (sorted), and every Zone (sorted by ID) with its
// faulted flag and its Entities (sorted by ID, via EntityCanonicalBytes).
func (s *WorldState) Hash() [32]byte {
	return sha256.Sum256(s.CanonicalBytes())
}

// CanonicalBytes is the serialization Hash covers, exposed so a test can
// diff two states rather than compare two digests.
func (s *WorldState) CanonicalBytes() []byte {
	var b strings.Builder
	rng := s.RNG.State()
	writeFields(&b, "state",
		strconv.FormatUint(uint64(s.Version), 10),
		strconv.FormatUint(uint64(s.Tick), 10),
		strconv.FormatUint(s.Seed, 10),
		strconv.FormatUint(rng[0], 10), strconv.FormatUint(rng[1], 10),
		strconv.FormatUint(rng[2], 10), strconv.FormatUint(rng[3], 10),
		strconv.FormatUint(s.NextEventID, 10),
	)
	parts := make([]int, 0, len(s.Offsets))
	for p := range s.Offsets {
		parts = append(parts, int(p))
	}
	sort.Ints(parts)
	for _, p := range parts {
		writeFields(&b, "offset", strconv.Itoa(p), strconv.FormatInt(s.Offsets[int32(p)], 10))
	}
	zids := make([]string, 0, len(s.Zones))
	for id := range s.Zones {
		zids = append(zids, string(id))
	}
	sort.Strings(zids)
	for _, zid := range zids {
		z := s.Zones[ZoneID(zid)]
		writeFields(&b, "zone_state", zid, strconv.FormatBool(z.Faulted), strconv.FormatUint(uint64(z.FaultedTick), 10))
		eids := make([]string, 0, len(z.Entities))
		for id := range z.Entities {
			eids = append(eids, string(id))
		}
		sort.Strings(eids)
		for _, eid := range eids {
			b.Write(EntityCanonicalBytes(*z.Entities[EntityID(eid)]))
		}
	}
	return []byte(b.String())
}
