// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package projector_test

import (
	"bytes"
	"errors"
	"fmt"
	"slices"
	"sort"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	gamev1 "github.com/valesordev/andara/gen/go/andara/game/v1"
	logv1 "github.com/valesordev/andara/gen/go/andara/log/v1"
	statev1 "github.com/valesordev/andara/gen/go/andara/state/v1"
	"github.com/valesordev/andara/server/canonical"
	"github.com/valesordev/andara/server/projector"
	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/simtest"
)

// world is a miniature live loop over the real verb handlers: Commands are
// appended to their Zone's Partition, each tick applies everything unapplied,
// and a tick's cross-Zone Commands land on their target's Partition for a
// later tick — the shape the live server gives the log.
type world struct {
	t          *testing.T
	live       *sim.Engine
	log        map[int32][]sim.Record
	boundaries []sim.TickCompleted
	events     map[sim.Tick][]sim.Event
}

const seed = 5

// newWorld is the crossing World brought into effect the way a live log
// does it: an Engine with no content, and a genesis ContentSwap as its first
// tick (AW-SRV-012).
func newWorld(t *testing.T) *world {
	t.Helper()
	w := newWorldOn(t, contentEngine(t))
	w.submit(crossing(t).Genesis())
	w.tick()
	return w
}

func crossing(t *testing.T) *simtest.FixedContent {
	t.Helper()
	c, err := simtest.CrossingContent()
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// contentEngine is an Engine with no content yet, preparing swaps from the
// crossing fixture.
func contentEngine(t *testing.T) *sim.Engine {
	t.Helper()
	return sim.NewEngine(sim.EmptyWorld(), nil, sim.Config{Seed: seed, Partitions: simtest.AllPartitions(), Handlers: sim.Handlers(), Content: crossing(t)})
}

func newWorldOn(t *testing.T, e *sim.Engine) *world {
	return &world{t: t, live: e, log: map[int32][]sim.Record{}, events: map[sim.Tick][]sim.Event{}}
}

func (w *world) submit(cmds ...*logv1.LoggedCommand) {
	for _, c := range cmds {
		p := sim.CommandPartition(c)
		w.log[p] = append(w.log[p], sim.Record{Partition: p, Offset: int64(len(w.log[p])), Command: c})
	}
}

func (w *world) tick() sim.Tick {
	w.t.Helper()
	var in sim.TickInput
	parts := make([]int, 0, len(w.log))
	for p := range w.log {
		parts = append(parts, int(p))
	}
	sort.Ints(parts)
	for _, pi := range parts {
		p := int32(pi)
		in.Records = append(in.Records, w.log[p][w.live.State().Offsets[p]:]...)
	}
	res, err := w.live.Step(in)
	if err != nil {
		w.t.Fatal(err)
	}
	w.submit(res.Outbound...)
	w.boundaries = append(w.boundaries, res.Completed)
	w.events[res.Tick] = res.Events
	return res.Tick
}

// script plays a session through every verb the sim has: bind, same-Zone
// moves, a cross-Zone move and its arrival, an unbind to dormant, and a
// rebind — with a second Character standing by.
func script(t *testing.T) *world {
	w := newWorld(t)
	w.submit(simtest.Bind("town", "hero", "Hero", "plaza"), simtest.Bind("town", "ada", "Ada", "plaza"))
	w.tick()
	w.submit(simtest.Move("town", "hero", "north"))
	w.tick()
	w.submit(simtest.Move("town", "hero", "south"), simtest.Look("town", "ada"))
	w.tick()
	w.submit(simtest.Move("town", "hero", "east")) // into wilds: leaves town now, arrives next tick
	w.tick()
	w.tick()
	w.submit(simtest.Move("wilds", "hero", "east"), simtest.Move("town", "ada", "nowhere"))
	w.tick()
	w.submit(simtest.Unbind("town", "ada"))
	w.tick()
	w.submit(simtest.Bind("town", "ada", "Ada", "plaza"))
	w.tick()
	return w
}

func replica(t *testing.T) *projector.Projector {
	t.Helper()
	return projector.New(contentEngine(t), projector.Options{ContentVersion: func(z sim.ZoneID) string { return "fixture@1" }})
}

// view is a compacted topic in miniature: last value per key, tombstones
// delete.
type view map[string][]byte

func (v view) apply(recs []projector.Out) {
	for _, r := range recs {
		if r.Tombstone() {
			delete(v, r.Key)
			continue
		}
		v[r.Key] = r.Value
	}
}

func decode(t *testing.T, b []byte) *statev1.StateRecord {
	t.Helper()
	var r statev1.StateRecord
	if err := proto.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	return &r
}

// sameContent compares two views on everything but when each record was
// written: a rebuild writes every key at its bootstrap tick, the incremental
// projector at the tick that last touched it.
func sameContent(t *testing.T, got, want view) {
	t.Helper()
	keys := func(v view) []string {
		out := make([]string, 0, len(v))
		for k := range v {
			out = append(out, k)
		}
		sort.Strings(out)
		return out
	}
	if !slices.Equal(keys(got), keys(want)) {
		t.Fatalf("key sets differ:\n incremental %v\n dump        %v", keys(got), keys(want))
	}
	for k := range want {
		a, b := decode(t, got[k]), decode(t, want[k])
		if a.GetKind() != b.GetKind() || !bytes.Equal(a.GetDigest(), b.GetDigest()) || !bytes.Equal(a.GetBody(), b.GetBody()) ||
			a.GetContentVersion() != b.GetContentVersion() || a.GetStateVersion() != b.GetStateVersion() {
			t.Fatalf("%s: incremental %v, dump %v", k, a, b)
		}
	}
}

// The property the Touched table has to earn: after every tick of every verb,
// the records the incremental projector has written describe exactly the
// replica's state — no aggregate stale, none missing, none left behind.
// AC-2 rides along: Replay verifies every boundary's hash.
func TestIncrementalRecordsEqualADumpAtEveryTick(t *testing.T) {
	t.Parallel()
	w := script(t)
	p := replica(t)
	v := view{}
	boot, err := p.Dump()
	if err != nil {
		t.Fatal(err)
	}
	v.apply(boot)
	ticks := 0
	err = p.Replay(w.boundaries, simtest.MemorySource(w.log), func(b sim.TickCompleted, recs []projector.Out) error {
		ticks++
		if p.Engine().StateHash() != b.StateHash {
			t.Fatalf("tick %d: replica %x, recorded %x", b.Tick, p.Engine().StateHash(), b.StateHash)
		}
		v.apply(recs)
		fresh := projector.New(p.Engine(), projector.Options{ContentVersion: func(sim.ZoneID) string { return "fixture@1" }})
		dump, err := fresh.Dump()
		if err != nil {
			return err
		}
		want := view{}
		want.apply(dump)
		sameContent(t, v, want)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if ticks != len(w.boundaries) {
		t.Fatalf("sink saw %d ticks, want %d", ticks, len(w.boundaries))
	}
}

// AC-1: the tick a Character arrives on writes its record and its Room's,
// stamped with that tick, carrying the replica's state for each.
func TestArrivalWritesTheCharacterAndTheRoom(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.submit(simtest.Bind("town", "hero", "Hero", "plaza"))
	arrived := w.tick()
	if !slices.ContainsFunc(w.events[arrived], func(e sim.Event) bool { return e.Type == sim.EvCharacterArrived }) {
		t.Fatalf("fixture: tick %d has no CharacterArrived", arrived)
	}
	p := replica(t)
	got := map[string]projector.Out{}
	if err := p.Replay(w.boundaries, simtest.MemorySource(w.log), func(_ sim.TickCompleted, recs []projector.Out) error {
		for _, r := range recs {
			got[r.Key] = r
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	hero := p.Engine().State().Zones["town"].Entities["hero"]
	wantBody, err := canonical.Marshal(hero.StateProto())
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"character:town/hero", "room:town/plaza", "zone:town"} {
		r, ok := got[key]
		if !ok {
			t.Fatalf("no record for %s; got %v", key, keysOf(got))
		}
		rec := decode(t, r.Value)
		if sim.Tick(rec.GetTick()) != arrived || r.Partition != sim.PartitionFor("town") {
			t.Fatalf("%s: tick %d partition %d, want tick %d partition %d", key, rec.GetTick(), r.Partition, arrived, sim.PartitionFor("town"))
		}
	}
	rec := decode(t, got["character:town/hero"].Value)
	if !bytes.Equal(rec.GetBody(), wantBody) || rec.GetKind() != statev1.AggregateKind_CHARACTER {
		t.Fatalf("character body is not the replica's state: %v", rec)
	}
	var room statev1.RoomState
	if err := proto.Unmarshal(decode(t, got["room:town/plaza"].Value).GetBody(), &room); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(room.GetOccupants(), []string{"hero"}) {
		t.Fatalf("plaza occupants %v", room.GetOccupants())
	}
	if rec.GetSourceOffset() != 0 || rec.GetStateVersion() != sim.StateVersion {
		t.Fatalf("source_offset %d state_version %d", rec.GetSourceOffset(), rec.GetStateVersion())
	}
}

// AC-5, through the destroy this story can exercise: leaving a Zone
// tombstones the Entity's key in that Zone on the tick it leaves, and the
// arrival writes a new key on the target's Partition.
func TestZoneExitTombstonesTheOldKey(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.submit(simtest.Bind("town", "hero", "Hero", "plaza"))
	w.tick()
	w.submit(simtest.Move("town", "hero", "east"))
	left := w.tick()
	arrived := w.tick()
	p := replica(t)
	byTick := map[sim.Tick][]projector.Out{}
	if err := p.Replay(w.boundaries, simtest.MemorySource(w.log), func(b sim.TickCompleted, recs []projector.Out) error {
		byTick[b.Tick] = recs
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var tomb *projector.Out
	for i, r := range byTick[left] {
		if r.Key == "character:town/hero" {
			tomb = &byTick[left][i]
		}
	}
	if tomb == nil || !tomb.Tombstone() || tomb.Partition != sim.PartitionFor("town") || tomb.Kind != statev1.AggregateKind_CHARACTER {
		t.Fatalf("tick %d: want a tombstone for character:town/hero on town's partition, got %+v", left, byTick[left])
	}
	var arrival *projector.Out
	for i, r := range byTick[arrived] {
		if r.Key == "character:wilds/hero" {
			arrival = &byTick[arrived][i]
		}
	}
	if arrival == nil || arrival.Tombstone() || arrival.Partition != sim.PartitionFor("wilds") {
		t.Fatalf("tick %d: want a record for character:wilds/hero on wilds' partition, got %+v", arrived, byTick[arrived])
	}
}

// faultEngine is the verb engine with one extra move: "boom" renames every
// Entity in the Zone, deletes one, and panics — partial state applyOne keeps
// and the State Hash covers.
func faultEngine(t *testing.T) *sim.Engine {
	t.Helper()
	w, err := simtest.CrossingWorld()
	if err != nil {
		t.Fatal(err)
	}
	reg, err := simtest.Templates()
	if err != nil {
		t.Fatal(err)
	}
	handlers := sim.Handlers()
	move := handlers[sim.KindMove]
	handlers[sim.KindMove] = func(a *sim.ApplyContext, cmd *logv1.LoggedCommand) error {
		if cmd.GetMove().GetDirection() != "boom" {
			return move(a, cmd)
		}
		for _, ent := range a.Zone.Entities {
			ent.Name = "scorched"
		}
		delete(a.Zone.Entities, "ada")
		panic("boom")
	}
	return sim.NewEngine(w, reg, sim.Config{Seed: seed, Partitions: simtest.AllPartitions(), Handlers: handlers})
}

// Codex review of PR #64: a fault's partial mutations are in the State Hash,
// and ZoneFaulted names none of them, so the Zone is rendered whole — the
// renamed Entity rewritten, the deleted one tombstoned.
//
// Rendered from the live engine's own tick rather than through Replay: a
// replay through a faulted tick does not reproduce it today, because the
// panicking record stays unapplied and the boundary's offset excludes it.
// Making that replay exact is AW-SRV-027 (its AC-3); the projector halts with
// a Divergence there until it lands, exactly as recovery does. AW-SRV-027
// inherits running this case through Replay.
func TestFaultRendersTheZoneWhole(t *testing.T) {
	t.Parallel()
	w := newWorldOn(t, faultEngine(t))
	w.submit(simtest.Bind("town", "hero", "Hero", "plaza"), simtest.Bind("town", "ada", "Ada", "plaza"))
	w.tick()
	p := projector.New(w.live, projector.Options{})
	v := view{}
	boot, err := p.Dump()
	if err != nil {
		t.Fatal(err)
	}
	v.apply(boot)

	w.submit(simtest.Move("town", "hero", "boom"))
	res, err := w.live.Step(sim.TickInput{Records: w.log[sim.PartitionFor("town")][w.live.State().Offsets[sim.PartitionFor("town")]:]})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(res.Events, func(e sim.Event) bool { return e.Type == sim.EvZoneFaulted }) {
		t.Fatalf("fixture: tick %d did not fault", res.Tick)
	}
	recs, err := p.Render(res)
	if err != nil {
		t.Fatal(err)
	}
	v.apply(recs)
	dump, err := projector.New(w.live, projector.Options{}).Dump()
	if err != nil {
		t.Fatal(err)
	}
	want := view{}
	want.apply(dump)
	sameContent(t, v, want)
	if !slices.ContainsFunc(recs, func(o projector.Out) bool { return o.Key == "character:town/ada" && o.Tombstone() }) {
		t.Fatalf("tick %d did not tombstone the Entity the panicking handler deleted: %v", res.Tick, recs)
	}
}

// Codex review of PR #64: a bootstrap from a state newer than the topic. The
// topic was last written at tick 2, the bind (tick 1 is the genesis swap);
// the hero left town while the projector
// was down. Seeding from the newer state alone would never tombstone
// character:town/hero — no later tick names it. Dump then Reconcile against
// the topic's keys leaves the topic holding exactly the replica's state.
func TestBootstrapPastTheTopicReconcilesStaleKeys(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.submit(simtest.Bind("town", "hero", "Hero", "plaza"))
	w.tick()
	w.submit(simtest.Move("town", "hero", "east"))
	w.tick()
	w.tick()

	// The topic as the projector left it, at the bind's tick.
	topic := view{}
	partitions := map[string]int32{}
	first := replica(t)
	if err := first.Replay(w.boundaries[:2], simtest.MemorySource(w.log), func(_ sim.TickCompleted, recs []projector.Out) error {
		topic.apply(recs)
		for _, r := range recs {
			partitions[r.Key] = r.Partition
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok := topic["character:town/hero"]; !ok {
		t.Fatal("fixture: the topic should hold the hero in town")
	}

	// A replica at the last tick — as if restored from a round taken there.
	later := replica(t)
	if err := later.Engine().Replay(w.boundaries, simtest.MemorySource(w.log)); err != nil {
		t.Fatal(err)
	}
	p := projector.New(later.Engine(), projector.Options{ContentVersion: func(sim.ZoneID) string { return "fixture@1" }})
	dump, err := p.Dump()
	if err != nil {
		t.Fatal(err)
	}
	onTopic := map[string]int32{}
	for k := range topic {
		onTopic[k] = partitions[k]
	}
	topic.apply(dump)
	tombs := p.Reconcile(onTopic)
	topic.apply(tombs)

	want := view{}
	want.apply(dump)
	sameContent(t, topic, want)
	if len(tombs) != 1 || tombs[0].Key != "character:town/hero" || !tombs[0].Tombstone() ||
		tombs[0].Partition != sim.PartitionFor("town") || tombs[0].Kind != statev1.AggregateKind_CHARACTER {
		t.Fatalf("want exactly one tombstone, for character:town/hero on town's partition; got %+v", tombs)
	}
}

// AC-3: a divergence at T renders nothing for T, and the error names T, both
// hashes, and the offsets after T-1.
func TestDivergenceHaltsBeforeTheTick(t *testing.T) {
	t.Parallel()
	w := script(t)
	p := replica(t)
	const flipAfter = 3
	var sunk []sim.Tick
	err := p.Replay(w.boundaries, simtest.MemorySource(w.log), func(b sim.TickCompleted, _ []projector.Out) error {
		sunk = append(sunk, b.Tick)
		if b.Tick == flipAfter {
			// Flip a byte in the replica: Ada's name. Ada, because she stays
			// in town; the hero crosses into wilds next tick, and an Entity in
			// transit is carried by the log's Arrive — the live server's copy —
			// which would overwrite the corruption rather than expose it.
			p.Engine().State().Zones["town"].Entities["ada"].Name += "!"
		}
		return nil
	})
	var d *projector.Divergence
	if !errors.As(err, &d) || !errors.Is(err, sim.ErrHashMismatch) {
		t.Fatalf("want a Divergence, got %v", err)
	}
	if d.Tick != flipAfter+1 || d.Recorded != w.boundaries[flipAfter].StateHash || d.Replayed == d.Recorded {
		t.Fatalf("divergence %+v", d)
	}
	if !slices.Equal(sunk, []sim.Tick{1, 2, 3}) {
		t.Fatalf("sink saw %v; nothing may be rendered for the divergent tick", sunk)
	}
	for part, off := range w.boundaries[flipAfter-1].Offsets {
		if d.LastGood[part] != off {
			t.Fatalf("last good offset on %d is %d, the boundary for tick %d says %d", part, d.LastGood[part], flipAfter, off)
		}
	}
}

// AC-4: the same log replayed twice — a redelivery after a crash — produces
// byte-identical records in the same order.
func TestRedeliveryIsByteIdentical(t *testing.T) {
	t.Parallel()
	w := script(t)
	run := func() [][]byte {
		var out [][]byte
		if err := replica(t).Replay(w.boundaries, simtest.MemorySource(w.log), func(_ sim.TickCompleted, recs []projector.Out) error {
			for _, r := range recs {
				out = append(out, append([]byte(r.Key+"="), r.Value...))
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return out
	}
	a, b := run(), run()
	if len(a) == 0 || len(a) != len(b) {
		t.Fatalf("runs produced %d and %d records", len(a), len(b))
	}
	for i := range a {
		if !bytes.Equal(a[i], b[i]) {
			t.Fatalf("record %d differs between deliveries", i)
		}
	}
}

// The Touched table names every EventType the sim emits, and the sim's list
// of EventTypes names every payload in the envelope that TypeOf maps. A new
// Event type fails here until someone decides what it touches.
func TestTouchedTableIsComplete(t *testing.T) {
	t.Parallel()
	for _, et := range sim.EventTypes() {
		if !projector.HasRow(et) {
			t.Errorf("Touched has no row for %q", et)
		}
	}
	env := &gamev1.EventEnvelope{}
	oneof := env.ProtoReflect().Descriptor().Oneofs().ByName("payload")
	if oneof == nil {
		t.Fatal("EventEnvelope has no payload oneof")
	}
	for i := 0; i < oneof.Fields().Len(); i++ {
		fd := oneof.Fields().Get(i)
		m := (&gamev1.EventEnvelope{}).ProtoReflect()
		m.Set(fd, protoreflect.ValueOfMessage(m.NewField(fd).Message()))
		et := sim.TypeOf(m.Interface().(*gamev1.EventEnvelope))
		if et == "" {
			continue // a transport's frame (Heartbeat, Resync), not a sim Event
		}
		if !slices.Contains(sim.EventTypes(), et) {
			t.Errorf("payload %s maps to %q, which sim.EventTypes does not list", fd.Name(), et)
		}
	}
}

func keysOf(m map[string]projector.Out) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// AW-SRV-012: a ContentSwap changes topology without an Event per Room. The
// tick it applies on tombstones every Room the new content removed and
// re-renders every Zone, with the content version now in effect, so the
// incremental records still equal a dump at every tick — including the swap
// that relocates a Character, and the ticks after it.
func TestASwapRendersTheNewTopology(t *testing.T) {
	t.Parallel()
	c, err := simtest.TownVersions()
	if err != nil {
		t.Fatal(err)
	}
	newEngine := func() *sim.Engine {
		return sim.NewEngine(sim.EmptyWorld(), nil, sim.Config{Seed: seed, Partitions: simtest.AllPartitions(), Handlers: sim.Handlers(), Content: c})
	}
	w := newWorldOn(t, newEngine())
	swap := func(v uint64) {
		t.Helper()
		cmd, err := c.Swap(w.live, "town", v)
		if err != nil {
			t.Fatal(err)
		}
		w.submit(cmd)
	}
	swap(1)
	w.tick()
	w.submit(simtest.Bind("town", "hero", "Hero", "plaza"))
	w.tick()
	w.submit(simtest.Move("town", "hero", "north"))
	w.tick()
	swap(2)
	swapTick := w.tick()
	w.submit(simtest.Bind("town", "ada", "Ada", "plaza"))
	w.tick()

	inEffect := map[string]uint64{}
	versionOf := func(sim.ZoneID) string { return fmt.Sprintf("town@%d", inEffect["town"]) }
	p := projector.New(newEngine(), projector.Options{ContentVersion: versionOf, OnSwaps: func(swaps []sim.SwapApplied) {
		for _, s := range swaps {
			inEffect[s.Pack] = s.Version
		}
	}})
	v := view{}
	var tombstonedHall bool
	if err := p.Replay(w.boundaries, simtest.MemorySource(w.log), func(b sim.TickCompleted, recs []projector.Out) error {
		v.apply(recs)
		for _, r := range recs {
			if r.Key == "room:town/hall" && r.Tombstone() {
				tombstonedHall = b.Tick == swapTick
			}
		}
		dump, err := projector.New(p.Engine(), projector.Options{ContentVersion: versionOf}).Dump()
		if err != nil {
			return err
		}
		want := view{}
		want.apply(dump)
		sameContent(t, v, want)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !tombstonedHall {
		t.Fatal("the removed hall was not tombstoned on the swap's tick")
	}
	if rec := decode(t, v["zone:town"]); rec.GetContentVersion() != "town@2" {
		t.Fatalf("zone record names %q, want town@2", rec.GetContentVersion())
	}
	if rec := decode(t, v["character:town/hero"]); rec.GetTick() != uint64(swapTick) {
		t.Fatalf("hero's record is from tick %d, want the swap's %d", rec.GetTick(), swapTick)
	}
}
