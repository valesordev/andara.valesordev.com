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

// ZonesAt resolves a round's recorded content (pack ID to version) to the Zones
// that content has (AW-SRV-007 AC-16). The server builds it on the content
// source, so store doesn't import it. An error that wraps
// sim.ErrContentVersionUnknown is cause content; any other error is returned
// as it is. Nil or empty versions resolve to the listed (current) Zones.
type ZonesAt func(versions map[string]uint64) ([]sim.ZoneID, error)

// ReadObserver is told when one object of a round is about to be read, and how
// the read ended: the hook recovery starts its `recovery.load_snapshot` span
// with, around the read itself (AW-SRV-007 §7). It lives on the context so
// that the round functions' signatures don't carry a tracer.
type ReadObserver func(zone sim.ZoneID, key string) (done func(bytes int, err error))

type readObserverKey struct{}

// WithReadObserver returns a context whose rounds' object reads call obs.
func WithReadObserver(ctx context.Context, obs ReadObserver) context.Context {
	return context.WithValue(ctx, readObserverKey{}, obs)
}

func observeRead(ctx context.Context, zone sim.ZoneID, key string) func(int, error) {
	if obs, ok := ctx.Value(readObserverKey{}).(ReadObserver); ok && obs != nil {
		if done := obs(zone, key); done != nil {
			return done
		}
	}
	return func(int, error) {}
}

// causeContent is Round.Cause for a round whose recorded content the source
// can't restore it onto: not on the wire (a round shows complete=false), and
// refused by NewestComplete and RoundAt as a typed error, never skipped.
const causeContent = "content"

// ZoneSnapshotRef is one Zone's object in a round: its key, and whether it
// read back hash-valid.
type ZoneSnapshotRef struct {
	Zone   sim.ZoneID
	Key    string
	Offset int64
	// Bytes is the size of the object as read: the loaded payload.
	Bytes int
	// Valid is set once the object has been read, decoded, and its body's hash
	// found equal to the envelope's. Invalid names why in Reason.
	Valid  bool
	Reason string
	// Cause is the AW-SRV-007 AC-15 cause (sim.RoundMissing, RoundHash,
	// RoundDisagree) this object makes its round incomplete for; empty when
	// Valid.
	Cause string
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
	// Cause is the AW-SRV-007 AC-15 cause of the first problem found (one of
	// sim.RoundMissing, RoundDuplicate, RoundHash, RoundDisagree), and
	// CauseZones the Zones it is about. Empty for a Complete round.
	Cause      string
	CauseZones []sim.ZoneID
	// Err is the typed refusal for cause content: *sim.ErrRoundContent or
	// *sim.ErrRoundZoneUnknown.
	Err error
	// TakenAt is the newest taken_at_unix_nano among the objects that could
	// be read: diagnostic only (Admin.ListSnapshotRounds).
	TakenAt int64
}

// ListRounds groups WorldStore keys by tick and marks completeness against
// the process's owned Zones. Newest first.
//
// Discovery is a key listing — {zone}/{tick}/{state_version}/{offset} groups
// without a Get — but completeness is a per-object hash check, so this reads
// every object of every round it returns. A caller that wants only the newest
// complete round should call NewestComplete, which stops at the first.
func ListRounds(ctx context.Context, ws sim.WorldStore, listed []sim.ZoneID, zonesAt ZonesAt) ([]Round, error) {
	rounds, err := discover(ctx, ws, listed)
	if err != nil {
		return nil, err
	}
	for i := range rounds {
		if _, err := verify(ctx, ws, listed, zonesAt, &rounds[i]); err != nil {
			return nil, err
		}
	}
	return rounds, nil
}

// NewestComplete returns the newest complete round and its decoded state, or
// ok=false when no round is complete. Older rounds are read only while every
// newer one fails to verify.
func NewestComplete(ctx context.Context, ws sim.WorldStore, listed []sim.ZoneID, zonesAt ZonesAt) (Round, sim.RoundState, bool, error) {
	rounds, err := discover(ctx, ws, listed)
	if err != nil {
		return Round{}, sim.RoundState{}, false, err
	}
	for i := range rounds {
		state, err := verify(ctx, ws, listed, zonesAt, &rounds[i])
		if err != nil {
			return Round{}, sim.RoundState{}, false, err
		}
		if rounds[i].Cause == causeContent {
			// A round that doesn't restore onto the content it records is
			// refused where it stands, not skipped: an older round would be a
			// World nobody chose (AW-SRV-007 AC-16).
			return rounds[i], sim.RoundState{}, false, rounds[i].Err
		}
		if rounds[i].Complete {
			return rounds[i], state, true, nil
		}
	}
	return Round{}, sim.RoundState{}, false, nil
}

// RoundAt resolves a named tick to one round and its decoded state
// (AW-SRV-007 AC-15): `recover --verify --round T`, `recovery.pin_round`, and
// `Admin.VerifySnapshotRound`. After a rollback the same tick can hold a group
// at a newer state_version and one at an older one, so the name resolves to the
// highest state_version at T that this binary can read. A group that isn't
// complete is a *sim.ErrRoundIncomplete naming its cause and Zones, and no
// other round is substituted for it. A tick with no object at all is
// RoundMissing with every owned Zone. Every group at T newer than this binary
// is a *sim.ErrStateVersion.
func RoundAt(ctx context.Context, ws sim.WorldStore, listed []sim.ZoneID, zonesAt ZonesAt, tick sim.Tick) (Round, sim.RoundState, error) {
	rounds, err := discover(ctx, ws, listed)
	if err != nil {
		return Round{}, sim.RoundState{}, err
	}
	var group []Round
	for _, r := range rounds {
		if r.Tick == tick {
			group = append(group, r)
		}
	}
	if len(group) == 0 {
		return Round{Tick: tick}, sim.RoundState{}, &sim.ErrRoundIncomplete{Tick: tick, Cause: sim.RoundMissing, Zones: append([]sim.ZoneID(nil), listed...)}
	}
	// discover orders a tick's groups by state_version, highest first.
	for i := range group {
		if group[i].StateVersion > sim.StateVersion {
			continue
		}
		state, err := verify(ctx, ws, listed, zonesAt, &group[i])
		if err != nil {
			return Round{}, sim.RoundState{}, err
		}
		if group[i].Cause == causeContent {
			return group[i], sim.RoundState{}, group[i].Err
		}
		if !group[i].Complete {
			return group[i], sim.RoundState{}, &sim.ErrRoundIncomplete{Tick: tick, Cause: group[i].Cause, Zones: group[i].CauseZones}
		}
		return group[i], state, nil
	}
	return group[0], sim.RoundState{}, &sim.ErrStateVersion{Have: group[0].StateVersion, Want: sim.StateVersion}
}

type roundKey struct {
	tick    sim.Tick
	version uint32
}

// discover lists every owned Zone's keys and groups them into rounds, newest
// tick first; within a tick, the higher state_version first.
func discover(ctx context.Context, ws sim.WorldStore, listed []sim.ZoneID) ([]Round, error) {
	byRound := map[roundKey][]ZoneSnapshotRef{}
	for _, z := range listed {
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
func verify(ctx context.Context, ws sim.WorldStore, listed []sim.ZoneID, zonesAt ZonesAt, r *Round) (sim.RoundState, error) {
	state := sim.RoundState{Tick: r.Tick, StateVersion: sim.StateVersion}
	present := map[sim.ZoneID]int{}
	type problem struct {
		cause string
		text  string
		zones []sim.ZoneID
		err   error // the typed refusal, for cause content
	}
	var (
		first    = true
		problems []problem
	)
	for i := range r.Zones {
		ref := &r.Zones[i]
		present[ref.Zone]++
		end := observeRead(ctx, ref.Zone, ref.Key)
		raw, err := ws.Get(ctx, ref.Key)
		end(len(raw), err)
		ref.Bytes = len(raw)
		if errors.Is(err, sim.ErrSnapshotNotFound) {
			ref.Reason, ref.Cause = "object vanished after listing", sim.RoundMissing
			continue
		}
		if err != nil {
			return sim.RoundState{}, fmt.Errorf("store: read %s: %w", ref.Key, err)
		}
		env, body, err := readVerified(raw, sim.StateVersion, true)
		if err != nil {
			ref.Reason, ref.Cause = err.Error(), sim.RoundHash
			continue
		}
		if t := env.GetTakenAtUnixNano(); t > r.TakenAt {
			r.TakenAt = t
		}
		zone := sim.ZoneStateFromProto(body)
		if zone.ID != ref.Zone || sim.Tick(env.GetTick()) != r.Tick {
			ref.Reason, ref.Cause = fmt.Sprintf("envelope names zone %s tick %d", zone.ID, env.GetTick()), sim.RoundHash
			continue
		}
		prng, err := sim.DecodePRNG(body.GetPrngState())
		if err != nil {
			ref.Reason, ref.Cause = err.Error(), sim.RoundHash
			continue
		}
		next := body.GetNextEventId()
		seed := env.GetSimSeed()
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
			state.PRNG, state.NextEventID, state.Offsets, state.SimSeed, first = prng, next, offsets, seed, false
			if len(content) > 0 {
				state.Content, state.ContentDigest = content, env.GetContentDigest()
			}
		case !sameContent(content, env.GetContentDigest(), state.Content, state.ContentDigest):
			// One cut at one tick has one content in effect (AW-SRV-012).
			ref.Reason, ref.Cause = "content in effect disagrees with the rest of the round", sim.RoundDisagree
			continue
		case prng != state.PRNG || next != state.NextEventID:
			// AW-SRV-007 AC-11: a round is one cut at one tick, so these agree
			// by construction; disagreement means two cuts assembled as one.
			ref.Reason, ref.Cause = fmt.Sprintf("prng %x… / next_event_id %d disagree with the round's %x… / %d", prng[0], next, state.PRNG[0], state.NextEventID), sim.RoundDisagree
			continue
		case seed != state.SimSeed:
			// Every Zone's envelope in a round carries the same seed
			// (AW-SRV-043).
			ref.Reason, ref.Cause = fmt.Sprintf("sim_seed %d disagrees with the round's %d", seed, state.SimSeed), sim.RoundDisagree
			continue
		case !equalOffsets(offsets, state.Offsets):
			ref.Reason, ref.Cause = "partition offsets disagree with the rest of the round", sim.RoundDisagree
			continue
		}
		ref.Valid = true
		state.Zones = append(state.Zones, zone)
	}
	for _, ref := range r.Zones {
		if !ref.Valid {
			problems = append(problems, problem{cause: ref.Cause, text: fmt.Sprintf("%s: %s", ref.Key, ref.Reason), zones: []sim.ZoneID{ref.Zone}})
		}
	}
	for _, z := range sortedZones(present) {
		if present[z] > 1 {
			problems = append(problems, problem{cause: sim.RoundDuplicate, text: fmt.Sprintf("zone %s has %d objects at tick %d", z, present[z], r.Tick), zones: []sim.ZoneID{z}})
		}
	}
	// The Zones this round must hold an object for (AC-16): those of the content
	// it records, V, the content of its first hash-valid envelope in Zone order.
	// A round with no hash-valid envelope, or none recording content, is judged
	// against the listed (current) Zones.
	owned := listed
	if len(state.Content) > 0 && zonesAt != nil {
		zs, err := zonesAt(state.Content)
		var unknown *sim.ErrContentVersionUnknown
		switch {
		case errors.As(err, &unknown):
			rc := &sim.ErrRoundContent{Tick: r.Tick, Versions: copyContent(state.Content), Unknown: unknown}
			problems = append(problems, problem{cause: causeContent, text: rc.Error(), err: rc})
			owned = nil // no Zone list to compute the missing ones against
		case err != nil:
			return sim.RoundState{}, fmt.Errorf("store: round at tick %d: the Zones of its content: %w", r.Tick, err)
		default:
			owned = zs
		}
	}
	if len(state.Content) > 0 && zonesAt != nil && owned != nil {
		have := make(map[sim.ZoneID]bool, len(owned))
		for _, z := range owned {
			have[z] = true
		}
		for _, z := range sortedZones(present) {
			if !have[z] {
				ze := &sim.ErrRoundZoneUnknown{Tick: r.Tick, Zone: z}
				problems = append(problems, problem{cause: causeContent, text: ze.Error(), zones: []sim.ZoneID{z}, err: ze})
			}
		}
	}
	var missing []sim.ZoneID
	for _, z := range owned {
		if present[z] == 0 {
			missing = append(missing, z)
		}
	}
	if len(missing) > 0 {
		problems = append(problems, problem{cause: sim.RoundMissing, text: (&sim.ErrRoundIncomplete{Tick: r.Tick, Cause: sim.RoundMissing, Zones: missing}).Error(), zones: missing})
	}
	// Ranking is the order they were found in: an object problem (a vanished
	// object keeps its own cause, missing), a duplicate, then content, then a
	// missing Zone. problems[0] names the round's cause.
	r.Complete = len(problems) == 0 && len(r.Zones) > 0
	if !r.Complete {
		if len(problems) > 0 {
			r.Reason, r.Cause, r.CauseZones, r.Err = problems[0].text, problems[0].cause, problems[0].zones, problems[0].err
		}
		return sim.RoundState{}, nil
	}
	return state, nil
}

func copyContent(in map[string]uint64) map[string]uint64 {
	out := make(map[string]uint64, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
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
func StateVersionOf(ctx context.Context, ws sim.WorldStore, listed []sim.ZoneID) (uint32, error) {
	rounds, err := discover(ctx, ws, listed)
	if err != nil {
		return 0, err
	}
	var newest uint32
	for _, r := range rounds {
		newest = max(newest, r.StateVersion)
	}
	return newest, nil
}
