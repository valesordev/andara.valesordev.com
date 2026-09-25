// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package sim

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// SnapshotKeyDigits is the width the offset is zero-padded to in a store key,
// so lexical order is offset order. 20 digits holds every uint64, which is
// wider than an int64 offset can ever be — the extra digit is cheaper than a
// key space that stops sorting correctly at 10^19.
const SnapshotKeyDigits = 20

// PartitionOffset is one Partition's next-to-read offset at a tick boundary —
// Kafka's committed-offset convention, the same one TickCompleted carries.
//
// A slice of these rather than the map WorldState.Offsets holds, because a
// Snapshot is encoded and hashed off the tick goroutine and a map would have
// to be sorted by every reader that touched it.
type PartitionOffset struct {
	Partition int32
	Offset    int64
}

// Snapshot is a copy of one Zone's mutable state, taken at a tick boundary.
//
// The copy is the only work done inside the tick (AC-1): Encode and the upload
// run off-tick, against a body nothing will mutate again. That is what makes a
// snapshot round cost the tick a deep copy rather than a serialization.
type Snapshot struct {
	Zone         ZoneID
	Tick         Tick
	StateVersion uint32
	// Offsets is every Partition this process owns, sorted, not just the
	// Zone's own. The Zone's is what keys the object; the rest are what
	// AW-SRV-007 seeks to when it restores a whole round, and carrying them
	// per object means a round remains restorable when only some of its
	// objects survive.
	Offsets []PartitionOffset
	// TakenAtUnixNano is diagnostic only and is stamped at the boundary, not
	// at Encode, so that encoding the same Snapshot twice produces the same
	// bytes (AC-2).
	TakenAtUnixNano int64

	// body is the copy. Immutable after the boundary: nothing in this package
	// holds a reference to it once SnapshotAll returns, so the off-tick
	// encoder races with nothing.
	body *ZoneState
	// hash caches what StateHash computed. See StateHash.
	hash   [32]byte
	hashed bool
	// prng and nextEventID are process-wide rather than Zone-scoped, so every
	// Snapshot in a round carries the same values. Copied here because the
	// body must carry them and the body is per-Zone.
	prng        [4]uint64
	nextEventID uint64
	// content and contentDigest are the content in effect at Tick
	// (AW-SRV-012), the same for every Snapshot in a round: what a restore
	// rebuilds the topology from, and checks, before loading any body.
	content       map[string]uint64
	contentDigest [32]byte
}

// Content is the content in effect at the Snapshot's tick: the pack versions
// and their world_digest.
func (s *Snapshot) Content() (map[string]uint64, [32]byte) {
	return copyVersions(s.content), s.contentDigest
}

// Body exposes the copied Zone state. It is the encoder's input and a test's
// window onto what the boundary actually captured; callers must not mutate it.
func (s *Snapshot) Body() *ZoneState { return s.body }

// StateHash is the Zone's State Hash at Tick — the per-Zone hash the envelope
// carries, not the World's. See docs/feedback/AW-SRV-006-zone-snapshots.md §2.
//
// Computed here, off the tick, rather than at the boundary. Hashing is
// encoding: it walks every Entity, every Component, and every field and
// escapes each one into a canonical record, and at the sizing fixture's scale
// it costs five times what the copy does. The story's rule — "the only work
// inside the tick is the state copy; encoding and upload run off-tick" —
// covers it, and measuring AC-1 is what showed that it had to.
//
// Safe to compute late because the body is immutable after the boundary: the
// hash of a copy taken at tick N is the hash of the Zone at tick N whenever it
// is taken. The result is cached, and one Snapshot is hashed by one goroutine
// — the round that owns it.
func (s *Snapshot) StateHash() [32]byte {
	if !s.hashed {
		s.hash, s.hashed = HashZone(s.body), true
	}
	return s.hash
}

// PRNG returns the process-wide generator state the snapshot carries.
func (s *Snapshot) PRNG() [4]uint64 { return s.prng }

// NextEventID returns the process-wide next EventID the snapshot carries.
func (s *Snapshot) NextEventID() uint64 { return s.nextEventID }

// PartitionOf returns the Partition that owns the snapshot's Zone, and its
// offset at the boundary. The offset is what keys the object.
func (s *Snapshot) PartitionOf() (PartitionOffset, bool) {
	want := PartitionFor(s.Zone)
	for _, po := range s.Offsets {
		if po.Partition == want {
			return po, true
		}
	}
	return PartitionOffset{Partition: want}, false
}

// Key is where the object goes: {zone_id}/{tick}/{state_version}/{offset},
// tick and offset both zero-padded to SnapshotKeyDigits so a lexical listing
// is a tick-ordered listing.
//
// The tick is what makes a round's objects immutable. Without it the key was
// offset-only, and a Zone that received no Command since the last round keyed
// to the same string and was overwritten — so a round that failed part way
// advanced the idle Zones to a new tick while the failed one stayed behind,
// and no tick was left with a complete set of objects. That is exactly what
// AC-5 promises survives a partial write. An offset-only key, idle Zones that
// repeat offsets, and that promise cannot all hold; the tick is the one of the
// three that was cheapest to change (architecture review of PR #46, feedback
// §5).
//
// The tick sits immediately under the Zone, ahead of state_version, so that
// {zone_id}/{tick}/ is the prefix holding one Zone's part of a round. That is
// what AW-SRV-007's ListRounds groups on, and it groups without knowing which
// state_version wrote it — which a {zone}/{version}/{tick} ordering would have
// required. The offset stays last: it makes the key self-describing for the
// seek and keeps this a pure function of what the Snapshot already holds.
func (s *Snapshot) Key() string {
	po, _ := s.PartitionOf()
	return SnapshotKey(s.Zone, s.StateVersion, s.Tick, po.Offset)
}

// SnapshotKey formats a store key. Exported so the store, the CLI, and
// AW-SRV-007 all produce the same string from the same values rather than each
// formatting it by hand.
func SnapshotKey(zone ZoneID, stateVersion uint32, tick Tick, offset int64) string {
	return fmt.Sprintf("%s/%0*d/%d/%0*d", zone, SnapshotKeyDigits, uint64(tick), stateVersion, SnapshotKeyDigits, offset)
}

// SnapshotRoundPrefix is where one Zone's part of the round at tick lives:
// {zone_id}/{tick}/. Exported because AW-SRV-007 groups rounds by listing this
// rather than by reading a tick out of every candidate's envelope, and because
// a prefix assembled by hand at each call site is a prefix that eventually
// loses its trailing slash and sweeps in a Zone whose ID extends this one's.
func SnapshotRoundPrefix(zone ZoneID, tick Tick) string {
	return fmt.Sprintf("%s/%0*d/", zone, SnapshotKeyDigits, uint64(tick))
}

// ParseSnapshotKey reads a key back into its parts. The stores use it to order
// a listing by tick, and AW-SRV-007 to group one into rounds. Reports false for
// anything that is not a snapshot key, so a bucket holding other objects lists
// cleanly rather than erroring.
func ParseSnapshotKey(key string) (zone ZoneID, stateVersion uint32, tick Tick, offset int64, ok bool) {
	parts := strings.Split(key, "/")
	if len(parts) != 4 || parts[0] == "" {
		return "", 0, 0, 0, false
	}
	tk, err := strconv.ParseUint(parts[1], 10, 64)
	if err != nil {
		return "", 0, 0, 0, false
	}
	v, err := strconv.ParseUint(parts[2], 10, 32)
	if err != nil {
		return "", 0, 0, 0, false
	}
	off, err := strconv.ParseInt(parts[3], 10, 64)
	if err != nil {
		return "", 0, 0, 0, false
	}
	return ZoneID(parts[0]), uint32(v), Tick(tk), off, true
}

// SnapshotAll takes one consistent cut of every Zone at the current tick
// boundary. The loop calls it at a boundary and nowhere else.
//
// One cut across every Zone, rather than a Zone per boundary: a Character
// moving between Zones is two writes at one tick, and two Zones snapshotted at
// two different ticks can catch that half-applied — the body in neither Zone,
// or in both. Staggering is the documented fallback if the copy ever exceeds
// the stall budget, and it reintroduces exactly this problem.
//
// The returned Snapshots share nothing with the engine: every map, slice, and
// Entity is copied.
func (e *Engine) SnapshotAll(takenAtUnixNano int64) []Snapshot {
	s := e.state
	offsets := make([]PartitionOffset, 0, len(s.Offsets))
	for p, o := range s.Offsets {
		offsets = append(offsets, PartitionOffset{Partition: p, Offset: o})
	}
	sort.Slice(offsets, func(i, j int) bool { return offsets[i].Partition < offsets[j].Partition })

	ids := make([]string, 0, len(s.Zones))
	for id := range s.Zones {
		ids = append(ids, string(id))
	}
	sort.Strings(ids)

	content := copyVersions(e.versions)
	out := make([]Snapshot, 0, len(ids))
	for _, id := range ids {
		z := s.Zones[ZoneID(id)]
		out = append(out, Snapshot{
			Zone:         z.ID,
			Tick:         s.Tick,
			StateVersion: s.Version,
			// Shared across the round: every Snapshot gets the same slice
			// because nothing writes to it after this loop.
			Offsets:         offsets,
			TakenAtUnixNano: takenAtUnixNano,
			body:            z.Clone(),
			prng:            s.RNG.State(),
			nextEventID:     s.NextEventID,
			content:         content,
			contentDigest:   e.digest,
		})
	}
	return out
}

// Clone deep-copies a Zone's state. Every Entity, every Component, and every
// field slice is copied, so the result shares no memory with the engine and can
// be encoded from another goroutine while the tick loop runs on.
func (z *ZoneState) Clone() *ZoneState {
	if z == nil {
		return nil
	}
	out := &ZoneState{
		ID:          z.ID,
		Faulted:     z.Faulted,
		FaultedTick: z.FaultedTick,
		Entities:    make(map[EntityID]*EntityState, len(z.Entities)),
	}
	for id, e := range z.Entities {
		out.Entities[id] = e.Clone()
	}
	return out
}

// Clone deep-copies an Entity, including its Components and their fields.
// ComponentField holds only value types, so copying the slices is enough.
func (e *EntityState) Clone() *EntityState {
	if e == nil {
		return nil
	}
	out := *e
	if len(e.Components) > 0 {
		out.Components = make([]Component, len(e.Components))
		for i, c := range e.Components {
			out.Components[i] = Component{Type: c.Type, Fields: append([]ComponentField(nil), c.Fields...)}
		}
	}
	return &out
}

// WorldStore is the persistence seam: it knows keys and bytes, and it knows
// nothing about Kafka or the tick. Declared here because ADR-0002 makes
// persistence an adapter behind an interface the sim layer owns; implemented in
// server/store, because server/sim reaches no filesystem and no network.
type WorldStore interface {
	// Put writes envelope at key. Atomic: the key is visible with complete
	// contents or not at all, so an interrupted write never leaves a partial
	// object for a listing to find (AC-5).
	Put(ctx context.Context, key string, envelope []byte) error
	// Get reads one object. ErrSnapshotNotFound when the key does not exist.
	Get(ctx context.Context, key string) ([]byte, error)
	// List returns a Zone's keys, newest offset first.
	List(ctx context.Context, zone ZoneID) ([]string, error)
}

// Errors the snapshot path returns.
var (
	// ErrSnapshotNotFound: no object at that key.
	ErrSnapshotNotFound = errors.New("snapshot not found")
	// ErrStoreUnavailable: the store refused a Put, Get, or List. The round
	// failed; the next one runs on schedule.
	ErrStoreUnavailable = errors.New("snapshot store unavailable")
)

// ErrStateVersion is a refused read: the envelope was written by a newer binary
// than the one reading it, so the body cannot be interpreted. Naming both
// versions is the whole point — an operator rolling a binary back needs to know
// which snapshot the older one *can* read (AW-INF-007).
//
// Never a partial or silent read (AC-4). A forward migration is the other case
// and is not an error.
type ErrStateVersion struct {
	Have uint32 // what the envelope carries
	Want uint32 // what this binary understands
}

func (e *ErrStateVersion) Error() string {
	return fmt.Sprintf("snapshot state_version %d is newer than this binary's %d: refusing to read it", e.Have, e.Want)
}

// ErrSnapshotStall is a warning, not a refusal: the in-tick copy took longer
// than snapshot.max_stall_ms. The round continues — a slow copy has already
// cost the tick whatever it cost, and abandoning the snapshot would add a
// recovery-time problem to a latency one.
type ErrSnapshotStall struct {
	Tick    Tick
	Elapsed float64 // seconds
	Budget  float64 // seconds
}

func (e *ErrSnapshotStall) Error() string {
	return fmt.Sprintf("snapshot copy at tick %d took %.3fms, over the %.3fms budget", e.Tick, e.Elapsed*1000, e.Budget*1000)
}

// ErrRoundIncomplete: one or more Zone writes failed, so the round is not a
// consistent cut of the World and AW-SRV-007 must not select it. The Zones that
// did succeed are left where they are — they are valid objects, and the
// previous complete round is still the newest selectable one.
type ErrRoundIncomplete struct {
	Tick    Tick
	Missing []ZoneID
}

func (e *ErrRoundIncomplete) Error() string {
	names := make([]string, len(e.Missing))
	for i, z := range e.Missing {
		names[i] = string(z)
	}
	return fmt.Sprintf("snapshot round at tick %d is incomplete: %d zone(s) not written: %v", e.Tick, len(e.Missing), names)
}
