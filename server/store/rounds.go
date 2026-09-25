// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package store

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/valesordev/andara/server/sim"
)

// ZoneSnapshotRef is one Zone's object in a round: its key, and whether it
// read back hash-valid.
type ZoneSnapshotRef struct {
	Zone   sim.ZoneID
	Key    string
	Offset int64
	// Valid is set once the object has been read, decoded, and its body's hash
	// found equal to the envelope's. Invalid names why in Reason.
	Valid  bool
	Reason string
}

// Round is one snapshot round: every object written at one tick under one
// state_version. AW-SRV-007's contract, implemented by AW-SRV-019 (the state
// projector bootstraps from a round and could not wait for AW-SRV-007's
// dependencies); AW-SRV-007 reuses it unchanged.
type Round struct {
	Tick         sim.Tick
	StateVersion uint32
	Zones        []ZoneSnapshotRef // sorted by Zone
	// Complete: every owned Zone present and hash-valid, and every object
	// agreeing on the process-wide PRNG and next EventID (AW-SRV-007 AC-11).
	Complete bool
	// Reason says why a round is not Complete, for `snapshot list` and logs.
	Reason string
}

// ListRounds groups WorldStore keys by tick and marks completeness against
// the process's owned Zones. Newest first.
//
// Discovery is a key listing — {zone}/{tick}/{state_version}/{offset} groups
// without a Get — but completeness is a per-object hash check, so this reads
// every object of every round it returns. A caller that wants only the newest
// complete round should call NewestComplete, which stops at the first.
func ListRounds(ctx context.Context, ws sim.WorldStore, owned []sim.ZoneID) ([]Round, error) {
	rounds, err := discover(ctx, ws, owned)
	if err != nil {
		return nil, err
	}
	for i := range rounds {
		if _, err := verify(ctx, ws, owned, &rounds[i]); err != nil {
			return nil, err
		}
	}
	return rounds, nil
}

// NewestComplete returns the newest complete round and its decoded state, or
// ok=false when no round is complete. Older rounds are read only while every
// newer one fails to verify.
func NewestComplete(ctx context.Context, ws sim.WorldStore, owned []sim.ZoneID) (Round, sim.RoundState, bool, error) {
	rounds, err := discover(ctx, ws, owned)
	if err != nil {
		return Round{}, sim.RoundState{}, false, err
	}
	for i := range rounds {
		state, err := verify(ctx, ws, owned, &rounds[i])
		if err != nil {
			return Round{}, sim.RoundState{}, false, err
		}
		if rounds[i].Complete {
			return rounds[i], state, true, nil
		}
	}
	return Round{}, sim.RoundState{}, false, nil
}

type roundKey struct {
	tick    sim.Tick
	version uint32
}

// discover lists every owned Zone's keys and groups them into rounds, newest
// tick first; within a tick, the higher state_version first.
func discover(ctx context.Context, ws sim.WorldStore, owned []sim.ZoneID) ([]Round, error) {
	byRound := map[roundKey][]ZoneSnapshotRef{}
	for _, z := range owned {
		keys, err := ws.List(ctx, z)
		if err != nil {
			return nil, fmt.Errorf("store: list rounds: zone %s: %w", z, err)
		}
		for _, k := range keys {
			zone, v, tick, off, ok := sim.ParseSnapshotKey(k)
			if !ok || zone != z {
				continue
			}
			rk := roundKey{tick, v}
			byRound[rk] = append(byRound[rk], ZoneSnapshotRef{Zone: zone, Key: k, Offset: off})
		}
	}
	out := make([]Round, 0, len(byRound))
	for rk, refs := range byRound {
		sort.Slice(refs, func(i, j int) bool {
			if refs[i].Zone != refs[j].Zone {
				return refs[i].Zone < refs[j].Zone
			}
			return refs[i].Offset < refs[j].Offset
		})
		out = append(out, Round{Tick: rk.tick, StateVersion: rk.version, Zones: refs})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Tick != out[j].Tick {
			return out[i].Tick > out[j].Tick
		}
		return out[i].StateVersion > out[j].StateVersion
	})
	return out, nil
}

// verify reads every object of r, sets Valid on each and Complete on r, and
// returns the decoded state when r is complete. A store error is returned; an
// object that is missing, undecodable, hash-invalid, or disagreeing with the
// rest of the round marks the round incomplete and is not an error.
//
// A body newer than this binary is recorded as the round's reason like any
// other invalid object. The caller that must refuse on it (AW-SRV-019 AC-10,
// AW-SRV-007's exit 4) reads it with StateVersionOf.
func verify(ctx context.Context, ws sim.WorldStore, owned []sim.ZoneID, r *Round) (sim.RoundState, error) {
	state := sim.RoundState{Tick: r.Tick, StateVersion: sim.StateVersion}
	present := map[sim.ZoneID]int{}
	var (
		first   = true
		reasons []string
	)
	for i := range r.Zones {
		ref := &r.Zones[i]
		present[ref.Zone]++
		raw, err := ws.Get(ctx, ref.Key)
		if errors.Is(err, sim.ErrSnapshotNotFound) {
			ref.Reason = "object vanished after listing"
			continue
		}
		if err != nil {
			return sim.RoundState{}, fmt.Errorf("store: read %s: %w", ref.Key, err)
		}
		env, body, err := readVerified(raw, sim.StateVersion, true)
		if err != nil {
			ref.Reason = err.Error()
			continue
		}
		zone := sim.ZoneStateFromProto(body)
		if zone.ID != ref.Zone || sim.Tick(env.GetTick()) != r.Tick {
			ref.Reason = fmt.Sprintf("envelope names zone %s tick %d", zone.ID, env.GetTick())
			continue
		}
		prng, err := sim.DecodePRNG(body.GetPrngState())
		if err != nil {
			ref.Reason = err.Error()
			continue
		}
		next := body.GetNextEventId()
		content := map[string]uint64{}
		for _, pv := range env.GetContent() {
			content[pv.GetPackId()] = pv.GetVersion()
		}
		offsets := make([]sim.PartitionOffset, 0, len(env.GetOffsets()))
		for _, po := range env.GetOffsets() {
			offsets = append(offsets, sim.PartitionOffset{Partition: po.GetPartition(), Offset: po.GetOffset()})
		}
		sort.Slice(offsets, func(i, j int) bool { return offsets[i].Partition < offsets[j].Partition })
		switch {
		case first:
			state.PRNG, state.NextEventID, state.Offsets, first = prng, next, offsets, false
			if len(content) > 0 {
				state.Content, state.ContentDigest = content, env.GetContentDigest()
			}
		case !sameContent(content, env.GetContentDigest(), state.Content, state.ContentDigest):
			// One cut at one tick has one content in effect (AW-SRV-012).
			ref.Reason = "content in effect disagrees with the rest of the round"
			continue
		case prng != state.PRNG || next != state.NextEventID:
			// AW-SRV-007 AC-11: a round is one cut at one tick, so these agree
			// by construction; disagreement means two cuts assembled as one.
			ref.Reason = fmt.Sprintf("prng %x… / next_event_id %d disagree with the round's %x… / %d", prng[0], next, state.PRNG[0], state.NextEventID)
			continue
		case !equalOffsets(offsets, state.Offsets):
			ref.Reason = "partition offsets disagree with the rest of the round"
			continue
		}
		ref.Valid = true
		state.Zones = append(state.Zones, zone)
	}
	for _, ref := range r.Zones {
		if !ref.Valid {
			reasons = append(reasons, fmt.Sprintf("%s: %s", ref.Key, ref.Reason))
		}
	}
	for _, z := range sortedZones(present) {
		if present[z] > 1 {
			reasons = append(reasons, fmt.Sprintf("zone %s has %d objects at tick %d", z, present[z], r.Tick))
		}
	}
	var missing []sim.ZoneID
	for _, z := range owned {
		if present[z] == 0 {
			missing = append(missing, z)
		}
	}
	if len(missing) > 0 {
		reasons = append(reasons, (&sim.ErrRoundIncomplete{Tick: r.Tick, Missing: missing}).Error())
	}
	r.Complete = len(reasons) == 0 && len(r.Zones) > 0
	if !r.Complete {
		if len(reasons) > 0 {
			r.Reason = reasons[0]
		}
		return sim.RoundState{}, nil
	}
	return state, nil
}

func sameContent(a map[string]uint64, ad []byte, b map[string]uint64, bd []byte) bool {
	if len(a) != len(b) || string(ad) != string(bd) {
		return false
	}
	for p, v := range a {
		if b[p] != v {
			return false
		}
	}
	return true
}

func equalOffsets(a, b []sim.PartitionOffset) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func sortedZones(m map[sim.ZoneID]int) []sim.ZoneID {
	out := make([]sim.ZoneID, 0, len(m))
	for z := range m {
		out = append(out, z)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// StateVersionOf returns the newest state_version any of a round's objects
// was written at, read from the envelopes alone. A bootstrap that found no
// complete round uses it to tell "nothing to load" from "written by a newer
// binary" (AW-SRV-019 AC-10, AW-SRV-007 exit 4).
func StateVersionOf(ctx context.Context, ws sim.WorldStore, owned []sim.ZoneID) (uint32, error) {
	rounds, err := discover(ctx, ws, owned)
	if err != nil {
		return 0, err
	}
	var newest uint32
	for _, r := range rounds {
		newest = max(newest, r.StateVersion)
	}
	return newest, nil
}
