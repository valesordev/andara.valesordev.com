// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package sim

import (
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
}

// RestoreEngine builds an Engine at a round's tick from its decoded state.
//
// cfg.Partitions is ignored in favor of the round's offsets. A Zone the round
// carries that the World does not have is an error: the content that wrote
// the round is not the content loaded, and restoring would drop Entities
// without a trace. A Zone the World has that the round does not is started
// empty — whether a round missing one of the process's Zones is complete is
// the caller's question (store.ListRounds), not this function's.
func RestoreEngine(w *World, templates *TemplateRegistry, cfg Config, r RoundState) (*Engine, error) {
	if r.StateVersion != StateVersion {
		return nil, &ErrStateVersion{Have: r.StateVersion, Want: StateVersion}
	}
	parts := make([]int32, 0, len(r.Offsets))
	for _, po := range r.Offsets {
		parts = append(parts, po.Partition)
	}
	cfg.Partitions = parts
	e := NewEngine(w, templates, cfg)
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
