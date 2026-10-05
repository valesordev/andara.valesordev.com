// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package store_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	logv1 "github.com/valesordev/andara/gen/go/andara/log/v1"
	statev1 "github.com/valesordev/andara/gen/go/andara/state/v1"
	"github.com/valesordev/andara/server/canonical"
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

	round, state, ok, err := store.NewestComplete(context.Background(), fs, owned, nil)
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
	// The round tick's Tick Boundary Record would carry this.
	recorded := live.StateHash()
	state.RecordedHash = recorded[:]
	if state.SimSeed != 11 {
		t.Fatalf("the round records sim_seed %d, want 11", state.SimSeed)
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

	rounds, err := store.ListRounds(context.Background(), fs, owned, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rounds) != 2 || rounds[0].Tick <= rounds[1].Tick {
		t.Fatalf("want two rounds newest first, got %+v", rounds)
	}
	if rounds[0].Complete || !strings.Contains(rounds[0].Reason, string(snaps[0].Zone)) {
		t.Fatalf("newer round should be incomplete naming %s: %+v", snaps[0].Zone, rounds[0])
	}
	round, _, ok, err := store.NewestComplete(context.Background(), fs, owned, nil)
	if err != nil || !ok || round.Tick != older {
		t.Fatalf("NewestComplete = tick %d ok=%v err=%v, want %d", round.Tick, ok, err, older)
	}
}

// A body that no longer matches its envelope's hash makes the round
// incomplete, not an error.
func TestHashInvalidObjectMakesTheRoundIncomplete(t *testing.T) {
	t.Parallel()
	_, fs, _, owned := roundFixture(t, 2)
	rounds, err := store.ListRounds(context.Background(), fs, owned, nil)
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
	rounds, err = store.ListRounds(context.Background(), fs, owned, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rounds[0].Complete || rounds[0].Zones[0].Valid || !strings.Contains(rounds[0].Reason, "hashes to") {
		t.Fatalf("tampered round should be incomplete on the hash: %+v", rounds[0])
	}
	if _, _, ok, _ := store.NewestComplete(context.Background(), fs, owned, nil); ok {
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
	rounds, err := store.ListRounds(context.Background(), fs, owned, nil)
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
	rounds, err = store.ListRounds(context.Background(), fs, owned, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rounds[0].Complete {
		t.Fatalf("a round mixing two engines' cuts was complete: %+v", rounds[0])
	}
}

// AW-SRV-043: every Zone's envelope in a round carries the same sim_seed. A
// round whose objects disagree on it is two cuts, not a round, and the older
// complete round is the newest one.
func TestDisagreeingSeedMakesTheRoundIncomplete(t *testing.T) {
	t.Parallel()
	live, fs, remaining, owned := roundFixture(t, 1)
	older := live.Tick()
	if _, err := live.Step(simtest.Batch(remaining, 2)); err != nil {
		t.Fatal(err)
	}
	writeRound(t, fs, live.SnapshotAll(2))
	rounds, err := store.ListRounds(context.Background(), fs, owned, nil)
	if err != nil {
		t.Fatal(err)
	}
	last := rounds[0].Zones[len(rounds[0].Zones)-1]
	raw, err := fs.Get(context.Background(), last.Key)
	if err != nil {
		t.Fatal(err)
	}
	var env statev1.SnapshotEnvelope
	if err := proto.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	env.SimSeed++
	if raw, err = canonical.Marshal(&env); err != nil {
		t.Fatal(err)
	}
	if err := fs.Put(context.Background(), last.Key, raw); err != nil {
		t.Fatal(err)
	}
	rounds, err = store.ListRounds(context.Background(), fs, owned, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rounds[0].Complete || !strings.Contains(rounds[0].Reason, "sim_seed") {
		t.Fatalf("a round disagreeing on sim_seed: complete %t, reason %q", rounds[0].Complete, rounds[0].Reason)
	}
	round, state, ok, err := store.NewestComplete(context.Background(), fs, owned, nil)
	if err != nil || !ok || round.Tick != older || state.SimSeed != 11 {
		t.Fatalf("NewestComplete: tick %d (want %d), seed %d, ok %t, %v", round.Tick, older, state.SimSeed, ok, err)
	}
}

// fixedContent is a ContentSource whose every version is the simtest World.
type fixedContent struct{}

func (fixedContent) Prepare(map[string]uint64, *logv1.ContentSwap) (sim.Topology, error) {
	w, err := simtest.World()
	if err != nil {
		return sim.Topology{}, err
	}
	reg, err := simtest.Templates()
	if err != nil {
		return sim.Topology{}, err
	}
	return sim.Topology{World: w, Templates: reg}, nil
}

// contentRound runs an Engine whose content came in through a swap of
// pack@version and writes one round.
func contentRound(t *testing.T, fs *store.FS, version uint64) *sim.Engine {
	t.Helper()
	topo, _ := fixedContent{}.Prepare(nil, nil)
	d := sim.ContentDigest(topo)
	e := sim.NewEngine(sim.EmptyWorld(), nil, sim.Config{Seed: 11, Partitions: simtest.AllPartitions(), Content: fixedContent{}})
	swap := &logv1.LoggedCommand{Command: &logv1.LoggedCommand_ContentSwap{ContentSwap: &logv1.ContentSwap{PackId: "town", Version: version, WorldDigest: d[:]}}}
	if _, err := e.Step(sim.TickInput{Records: []sim.Record{{Partition: sim.WorldPartition, Offset: 0, Command: swap}}}); err != nil {
		t.Fatal(err)
	}
	writeRound(t, fs, e.SnapshotAll(1))
	return e
}

// AW-SRV-012: a round decodes the content in effect its objects carry, and a
// round whose objects disagree on it is two cuts, not a round.
func TestRoundCarriesTheContentInEffect(t *testing.T) {
	t.Parallel()
	fs := store.NewFS(t.TempDir())
	e := contentRound(t, fs, 3)
	owned := e.State().SortedZoneIDs()
	_, state, ok, err := store.NewestComplete(context.Background(), fs, owned, nil)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	_, digest := e.Content()
	if state.Content["town"] != 3 || len(state.Content) != 1 || string(state.ContentDigest) != string(digest[:]) {
		t.Fatalf("round content %v digest %x", state.Content, state.ContentDigest)
	}

	// One object rewritten by an engine that swapped in town@4 at the same
	// tick: the round no longer agrees with itself.
	other := store.NewFS(t.TempDir())
	e4 := contentRound(t, other, 4)
	snaps := e4.SnapshotAll(1)
	body, err := snaps[0].Encode()
	if err != nil {
		t.Fatal(err)
	}
	if err := fs.Put(context.Background(), snaps[0].Key(), body); err != nil {
		t.Fatal(err)
	}
	rounds, err := store.ListRounds(context.Background(), fs, owned, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rounds[0].Complete || !strings.Contains(rounds[0].Reason, "content in effect disagrees") {
		t.Fatalf("round %+v", rounds[0])
	}
}

// roundTamper rewrites the envelope of one object of the round at tick.
func roundTamper(t *testing.T, fs *store.FS, key string, f func(*statev1.SnapshotEnvelope)) {
	t.Helper()
	raw, err := fs.Get(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	var env statev1.SnapshotEnvelope
	if err := proto.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	f(&env)
	if raw, err = canonical.Marshal(&env); err != nil {
		t.Fatal(err)
	}
	if err := fs.Put(context.Background(), key, raw); err != nil {
		t.Fatal(err)
	}
}

func asIncomplete(t *testing.T, err error) *sim.ErrRoundIncomplete {
	t.Helper()
	var inc *sim.ErrRoundIncomplete
	if !errors.As(err, &inc) {
		t.Fatalf("error is %T (%v), want *sim.ErrRoundIncomplete", err, err)
	}
	return inc
}

// AW-SRV-007 AC-15: a named round resolves to one round, or is refused with
// its cause and Zones, and no other round is substituted for it.
func TestRoundAtNamesTheCauseOfAnIncompleteRound(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	live, fs, _, owned := roundFixture(t, 2)
	tick := live.Tick()

	round, state, err := store.RoundAt(ctx, fs, owned, nil, tick)
	if err != nil || !round.Complete || state.Tick != tick {
		t.Fatalf("a complete round: %+v, %v", round, err)
	}

	// No object at all: missing, with every owned Zone.
	_, _, err = store.RoundAt(ctx, fs, owned, nil, tick+500)
	if inc := asIncomplete(t, err); inc.Cause != sim.RoundMissing || len(inc.Zones) != len(owned) || inc.Tick != tick+500 {
		t.Fatalf("a tick with no objects: %+v", inc)
	}

	rounds, err := store.ListRounds(ctx, fs, owned, nil)
	if err != nil || len(rounds) != 1 {
		t.Fatalf("rounds %v, %v", rounds, err)
	}
	zones := rounds[0].Zones

	// disagree: one object's seed differs.
	roundTamper(t, fs, zones[len(zones)-1].Key, func(e *statev1.SnapshotEnvelope) { e.SimSeed++ })
	_, _, err = store.RoundAt(ctx, fs, owned, nil, tick)
	if inc := asIncomplete(t, err); inc.Cause != sim.RoundDisagree || len(inc.Zones) != 1 || inc.Zones[0] != zones[len(zones)-1].Zone {
		t.Fatalf("a disagreeing seed: %+v", inc)
	}

	// hash: an object's hash is wrong.
	roundTamper(t, fs, zones[len(zones)-1].Key, func(e *statev1.SnapshotEnvelope) { e.SimSeed-- }) // restore the seed
	roundTamper(t, fs, zones[0].Key, func(e *statev1.SnapshotEnvelope) { e.StateHash[0] ^= 0xff })
	_, _, err = store.RoundAt(ctx, fs, owned, nil, tick)
	if inc := asIncomplete(t, err); inc.Cause != sim.RoundHash || len(inc.Zones) != 1 || inc.Zones[0] != zones[0].Zone {
		t.Fatalf("a hash-invalid object: %+v", inc)
	}
	// Named, it is never replaced by an older complete round, even one that exists.
	if _, _, ok, _ := store.NewestComplete(ctx, fs, owned, nil); ok {
		t.Fatal("setup: the only round is hash-invalid, so no round is complete")
	}
}

// A round holding one Zone twice (two offsets at one tick) is malformed:
// incomplete with cause duplicate, and a named one is refused.
func TestRoundAtRefusesADuplicateZone(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	live, fs, _, owned := roundFixture(t, 2)
	tick := live.Tick()
	snaps := live.SnapshotAll(5)
	// A second object for one Zone at the same tick, at another offset.
	snap := snaps[0]
	wire, err := snap.Encode()
	if err != nil {
		t.Fatal(err)
	}
	var env statev1.SnapshotEnvelope
	if err := proto.Unmarshal(wire, &env); err != nil {
		t.Fatal(err)
	}
	dupKey := sim.SnapshotKey(snap.Zone, snap.StateVersion, snap.Tick, 999999)
	if err := fs.Put(ctx, dupKey, wire); err != nil {
		t.Fatal(err)
	}
	_, _, err = store.RoundAt(ctx, fs, owned, nil, tick)
	if inc := asIncomplete(t, err); inc.Cause != sim.RoundDuplicate || len(inc.Zones) != 1 || inc.Zones[0] != snap.Zone {
		t.Fatalf("a duplicate Zone: %+v", inc)
	}
}

// A tick holding a partial group at state_version v+1 and a complete one at v
// names the v round; a tick whose every group is newer than this binary is a
// *sim.ErrStateVersion (exit 4).
func TestRoundAtResolvesToTheHighestReadableVersion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	live, fs, _, owned := roundFixture(t, 2)
	tick := live.Tick()
	rounds, err := store.ListRounds(ctx, fs, owned, nil)
	if err != nil {
		t.Fatal(err)
	}
	// One object of the same round, rewritten as state_version+1: a partial
	// group at the newer version.
	first := rounds[0].Zones[0]
	raw, err := fs.Get(ctx, first.Key)
	if err != nil {
		t.Fatal(err)
	}
	var env statev1.SnapshotEnvelope
	if err := proto.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	env.StateVersion = sim.StateVersion + 1
	newer, err := canonical.Marshal(&env)
	if err != nil {
		t.Fatal(err)
	}
	if err := fs.Put(ctx, sim.SnapshotKey(first.Zone, sim.StateVersion+1, tick, first.Offset), newer); err != nil {
		t.Fatal(err)
	}
	round, state, err := store.RoundAt(ctx, fs, owned, nil, tick)
	if err != nil || round.StateVersion != sim.StateVersion || !round.Complete || state.Tick != tick {
		t.Fatalf("the complete v round should load beside a partial v+1 group: %+v %v", round, err)
	}

	// A tick whose only group is newer than the binary.
	only := store.NewFS(t.TempDir())
	if err := only.Put(ctx, sim.SnapshotKey(first.Zone, sim.StateVersion+1, tick, first.Offset), newer); err != nil {
		t.Fatal(err)
	}
	_, _, err = store.RoundAt(ctx, only, owned, nil, tick)
	var sv *sim.ErrStateVersion
	if !errors.As(err, &sv) || sv.Have != sim.StateVersion+1 || sv.Want != sim.StateVersion {
		t.Fatalf("every group newer than the binary: %v", err)
	}
}

// AW-SRV-007 AC-16: a round is judged against the Zones of the content it
// records, not the content in effect at boot.

// contentTag marks every object of the round at fs's newest tick as written
// under pack@version.
func contentTag(t *testing.T, fs *store.FS, owned []sim.ZoneID, pack string, version uint64) {
	t.Helper()
	rounds, err := store.ListRounds(context.Background(), fs, owned, nil)
	if err != nil || len(rounds) == 0 {
		t.Fatal(err)
	}
	for _, z := range rounds[0].Zones {
		roundTamper(t, fs, z.Key, func(env *statev1.SnapshotEnvelope) {
			env.Content = []*statev1.PackVersion{{PackId: pack, Version: version}}
			env.ContentDigest = []byte("digest")
		})
	}
}

func zonesOf(names ...sim.ZoneID) store.ZonesAt {
	return func(map[string]uint64) ([]sim.ZoneID, error) { return names, nil }
}

func TestRoundIsJudgedAgainstItsOwnContentsZones(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, fs, _, owned := roundFixture(t, 2)
	contentTag(t, fs, owned, "town", 1)
	// The content in effect now has a fourth Zone the round never had: the
	// round is still complete, because V's Zones are the three it holds.
	listed := append(append([]sim.ZoneID(nil), owned...), "newzone")
	rounds, err := store.ListRounds(ctx, fs, listed, zonesOf(owned...))
	if err != nil || len(rounds) != 1 || !rounds[0].Complete {
		t.Fatalf("a round judged on the current content's extra Zone: %+v err %v", rounds, err)
	}
	// Against the current content alone (no ZonesAt) it is missing that Zone:
	// the case AC-16 removes.
	rounds, err = store.ListRounds(ctx, fs, listed, nil)
	if err != nil || rounds[0].Complete || rounds[0].Cause != sim.RoundMissing {
		t.Fatalf("without ZonesAt: %+v", rounds[0])
	}
	// A Zone V lists and the round has no object for is missing.
	rounds, _ = store.ListRounds(ctx, fs, listed, zonesOf(append(append([]sim.ZoneID(nil), owned...), "newzone")...))
	if rounds[0].Complete || rounds[0].Cause != sim.RoundMissing || len(rounds[0].CauseZones) != 1 || rounds[0].CauseZones[0] != "newzone" {
		t.Fatalf("a V Zone with no object: %+v", rounds[0])
	}
}

func TestAnObjectForAZoneItsContentDoesNotListIsContentMismatch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, fs, _, owned := roundFixture(t, 2)
	contentTag(t, fs, owned, "town", 1)
	zonesAt := zonesOf(owned[:2]...) // V lists two of the three Zones the round holds
	rounds, err := store.ListRounds(ctx, fs, owned, zonesAt)
	if err != nil || rounds[0].Complete || rounds[0].Cause != "content" || !strings.Contains(rounds[0].Reason, string(owned[2])) {
		t.Fatalf("round %+v err %v", rounds[0], err)
	}
	var zu *sim.ErrRoundZoneUnknown
	if _, _, ok, err := store.NewestComplete(ctx, fs, owned, zonesAt); ok || !errors.As(err, &zu) || zu.Zone != owned[2] {
		t.Fatalf("NewestComplete: ok %v err %v, want ErrRoundZoneUnknown for %s", ok, err, owned[2])
	}
	var inc *sim.ErrRoundIncomplete
	if _, _, err := store.RoundAt(ctx, fs, owned, zonesAt, rounds[0].Tick); !errors.As(err, &zu) || errors.As(err, &inc) {
		t.Fatalf("RoundAt: %v, want ErrRoundZoneUnknown and never ErrRoundIncomplete", err)
	}
}

func TestAVersionTheSourceLacksIsContentMismatchAndIsNotSkipped(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	live, fs, _, owned := roundFixture(t, 2)
	older := live.Tick()
	// A newer round, written after another tick, whose content the source lacks.
	if _, err := live.Step(sim.TickInput{}); err != nil {
		t.Fatal(err)
	}
	writeRound(t, fs, live.SnapshotAll(2))
	contentTag(t, fs, owned, "town", 9)
	missing := func(map[string]uint64) ([]sim.ZoneID, error) {
		return nil, fmt.Errorf("prepare: %w", &sim.ErrContentVersionUnknown{Pack: "town", Version: 9})
	}
	rounds, err := store.ListRounds(ctx, fs, owned, missing)
	if err != nil || rounds[0].Complete || rounds[0].Cause != "content" || rounds[0].Tick <= older {
		t.Fatalf("newest round %+v err %v", rounds[0], err)
	}
	// The older round carries no content, so it is complete: and NewestComplete
	// must not fall back to it.
	if !rounds[1].Complete || rounds[1].Tick != older {
		t.Fatalf("older round %+v", rounds[1])
	}
	var rc *sim.ErrRoundContent
	if _, _, ok, err := store.NewestComplete(ctx, fs, owned, missing); ok || !errors.As(err, &rc) || rc.Versions["town"] != 9 || rc.Tick != rounds[0].Tick {
		t.Fatalf("NewestComplete: ok %v err %v, want ErrRoundContent at tick %d", ok, err, rounds[0].Tick)
	}
	if _, _, err := store.RoundAt(ctx, fs, owned, missing, rounds[0].Tick); !errors.As(err, &rc) {
		t.Fatalf("RoundAt: %v", err)
	}
}

func TestAnyOtherZonesAtErrorPropagates(t *testing.T) {
	t.Parallel()
	_, fs, _, owned := roundFixture(t, 2)
	contentTag(t, fs, owned, "town", 1)
	boom := errors.New("registry timed out")
	_, err := store.ListRounds(context.Background(), fs, owned, func(map[string]uint64) ([]sim.ZoneID, error) { return nil, boom })
	if !errors.Is(err, boom) {
		t.Fatalf("err %v, want the source's own error, not a round's cause", err)
	}
}

// The ranking: content is below a hash-invalid object and above a missing Zone.
func TestContentRanksBelowHashAndAboveMissing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, fs, _, owned := roundFixture(t, 2)
	contentTag(t, fs, owned, "town", 1)
	// V lists a Zone the round lacks (missing) and omits one it holds (content).
	zonesAt := zonesOf(append([]sim.ZoneID{"absent"}, owned[:2]...)...)
	rounds, err := store.ListRounds(ctx, fs, owned, zonesAt)
	if err != nil || rounds[0].Cause != "content" {
		t.Fatalf("content over missing: %+v err %v", rounds[0], err)
	}
	raw, _ := fs.Get(ctx, rounds[0].Zones[0].Key)
	raw[len(raw)/2] ^= 0xff
	if err := fs.Put(ctx, rounds[0].Zones[0].Key, raw); err != nil {
		t.Fatal(err)
	}
	rounds, err = store.ListRounds(ctx, fs, owned, zonesAt)
	if err != nil || rounds[0].Cause != sim.RoundHash {
		t.Fatalf("hash over content: %+v err %v", rounds[0], err)
	}
}
