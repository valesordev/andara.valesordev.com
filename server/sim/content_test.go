// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package sim_test

import (
	"errors"
	"fmt"
	"sort"
	"testing"

	"google.golang.org/protobuf/proto"

	contentv1 "github.com/valesordev/andara/gen/go/andara/content/v1"
	logv1 "github.com/valesordev/andara/gen/go/andara/log/v1"
	statev1 "github.com/valesordev/andara/gen/go/andara/state/v1"
	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/simtest"
)

// fakeContent is a ContentSource over fixed versions of each pack: the World a
// set of versions builds is every pack's Zones at its version, together.
type fakeContent struct {
	zones     map[string]map[uint64][]*contentv1.ZoneDefinition
	templates *sim.TemplateRegistry
}

func (f *fakeContent) Prepare(inEffect map[string]uint64, swap *logv1.ContentSwap) (sim.Topology, error) {
	inEffect[swap.GetPackId()] = swap.GetVersion()
	packs := make([]string, 0, len(inEffect))
	for p := range inEffect {
		packs = append(packs, p)
	}
	sort.Strings(packs)
	var inputs []sim.Input
	for _, p := range packs {
		defs, ok := f.zones[p][inEffect[p]]
		if !ok {
			return sim.Topology{}, fmt.Errorf("no %s@%d", p, inEffect[p])
		}
		for _, d := range defs {
			inputs = append(inputs, sim.Input{File: d.GetId() + ".json", Def: d})
		}
	}
	if len(inputs) == 0 {
		return sim.Topology{World: sim.EmptyWorld(), Templates: f.templates}, nil
	}
	w, errs := sim.BuildWorld(inputs, sim.Options{})
	if w == nil {
		return sim.Topology{}, fmt.Errorf("build: %v", errs)
	}
	return sim.Topology{World: w, Templates: f.templates}, nil
}

// swap is the ContentSwap a Loader would produce for pack@version on top of
// what e has in effect: the digest is the one that version builds.
func (f *fakeContent) swap(t *testing.T, e *sim.Engine, extra map[string]uint64, pack string, version uint64) *logv1.LoggedCommand {
	t.Helper()
	inEffect, base := e.Content()
	for p, v := range extra {
		inEffect[p] = v
	}
	if len(extra) > 0 {
		// Built on top of a swap not yet applied: its base is that swap's
		// World, as the Loader would record it.
		prev, err := sim.PrepareContent(f, copyMap(inEffect))
		if err != nil {
			t.Fatal(err)
		}
		base = sim.ContentDigest(prev)
	}
	cs := &logv1.ContentSwap{PackId: pack, Version: version}
	if len(inEffect) > 0 {
		cs.BaseDigest = base[:]
	}
	topo, err := f.Prepare(inEffect, cs)
	if err != nil {
		t.Fatal(err)
	}
	d := sim.ContentDigest(topo)
	cs.WorldDigest = d[:]
	return &logv1.LoggedCommand{Command: &logv1.LoggedCommand_ContentSwap{ContentSwap: cs}}
}

func copyMap(m map[string]uint64) map[string]uint64 {
	out := make(map[string]uint64, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func zdef(id, fallback string, rooms ...*contentv1.RoomDefinition) *contentv1.ZoneDefinition {
	return &contentv1.ZoneDefinition{FormatVersion: 1, Id: id, Name: id, FallbackRoom: fallback, Rooms: rooms}
}

func rdef(id string, exits ...*contentv1.ExitDefinition) *contentv1.RoomDefinition {
	return &contentv1.RoomDefinition{Id: id, Title: id, Description: "The " + id + ".", Exits: exits}
}

func xdef(dir, room string) *contentv1.ExitDefinition {
	return &contentv1.ExitDefinition{Direction: dir, ToRoom: room}
}

// newContent is town@1 (plaza, hall north of it), town@2 (the hall removed),
// town@3 (town and docks), town@4 (town alone again), and docks@1.
func newContent(t *testing.T) *fakeContent {
	t.Helper()
	reg, err := simtest.Templates()
	if err != nil {
		t.Fatal(err)
	}
	v1 := zdef("town", "plaza", rdef("plaza", xdef("north", "hall")), rdef("hall", xdef("south", "plaza")))
	v2 := zdef("town", "plaza", rdef("plaza"))
	docks := zdef("docks", "pier", rdef("pier"))
	return &fakeContent{templates: reg, zones: map[string]map[uint64][]*contentv1.ZoneDefinition{
		"town":  {1: {v1}, 2: {v2}, 3: {v1, docks}, 4: {v1}},
		"docks": {1: {docks}},
	}}
}

func contentEngine(t *testing.T, c sim.ContentSource) *sim.Engine {
	t.Helper()
	return sim.NewEngine(sim.EmptyWorld(), nil, sim.Config{Seed: 7, Partitions: simtest.AllPartitions(), Handlers: sim.Handlers(), Content: c})
}

// stepAll applies cmds as one tick, each on its Zone's Partition (a swap on
// Partition 0), at the next offsets.
func stepAll(t *testing.T, e *sim.Engine, cmds ...*logv1.LoggedCommand) (sim.StepResult, error) {
	t.Helper()
	next := map[int32]int64{}
	var in sim.TickInput
	for _, c := range cmds {
		p := int32(0)
		if c.GetZoneId() != "" {
			p = sim.PartitionFor(sim.ZoneID(c.GetZoneId()))
		}
		if _, ok := next[p]; !ok {
			next[p] = e.State().Offsets[p]
		}
		in.Records = append(in.Records, sim.Record{Partition: p, Offset: next[p], Command: c})
		next[p]++
	}
	return e.Step(in)
}

func mustStep(t *testing.T, e *sim.Engine, cmds ...*logv1.LoggedCommand) sim.StepResult {
	t.Helper()
	res, err := stepAll(t, e, cmds...)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// Genesis: an Engine starts with no content, and the first swap is what gives
// it Zones to stand in and Templates to spawn from.
func TestContentSwap_GenesisGivesTheWorldItsContent(t *testing.T) {
	c := newContent(t)
	e := contentEngine(t, c)
	if v, d := e.Content(); len(v) != 0 || d != ([32]byte{}) || len(e.World().Zones) != 0 {
		t.Fatalf("before any swap: versions %v, %d Zones", v, len(e.World().Zones))
	}
	res := mustStep(t, e, c.swap(t, e, nil, "town", 1))
	if len(res.Swaps) != 1 || res.Swaps[0].Pack != "town" || res.Swaps[0].Version != 1 {
		t.Fatalf("swaps %+v", res.Swaps)
	}
	v, d := e.Content()
	if v["town"] != 1 || d != res.Swaps[0].Digest || e.State().Zones["town"] == nil || e.World().Zones["town"].Fallback != "plaza" {
		t.Fatalf("after genesis: versions %v, zones %v", v, e.State().Zones)
	}
	arr := ofType(mustStep(t, e, simtest.Bind("town", "ch-1", "Aldric", "plaza")).Events, sim.EvCharacterArrived)
	if len(arr) != 1 {
		t.Fatal("a Character could not be bound on genesis content")
	}
}

// AC-2 and AC-3: the swap applies after every other record of its tick, so a
// Command of that tick sees the old version and the next tick the new one; a
// Character in a Room the new version removed moves to the fallback in that
// same tick, with EntityRelocated scoped to the fallback Room and to it. A
// dormant body moves too, silently.
func TestContentSwap_RelocatesInTheSwapTick(t *testing.T) {
	c := newContent(t)
	e := contentEngine(t, c)
	mustStep(t, e, c.swap(t, e, nil, "town", 1))
	mustStep(t, e, simtest.Bind("town", "ch-1", "Aldric", "plaza"), simtest.Bind("town", "ch-2", "Brin", "plaza"))
	mustStep(t, e, simtest.Move("town", "ch-1", "north"), simtest.Move("town", "ch-2", "north"))
	mustStep(t, e, simtest.Unbind("town", "ch-2"))

	res := mustStep(t, e, simtest.Look("town", "ch-1"), c.swap(t, e, nil, "town", 2))
	if len(res.Events) < 2 {
		t.Fatalf("events %v", res.Events)
	}
	if d := res.Events[0].Envelope.GetRoomDescribed(); d.GetRoomId() != "hall" {
		t.Fatalf("the look in the swap's tick saw %v, want the hall of town@1", d)
	}
	rel := ofType(res.Events, sim.EvEntityRelocated)
	if len(rel) != 1 {
		t.Fatalf("want one EntityRelocated (the dormant body is silent), got %v", res.Events)
	}
	p := rel[0].Envelope.GetEntityRelocated()
	if p.GetZoneId() != "town" || p.GetEntityName() != "Aldric" || p.GetFromRoomId() != "hall" || p.GetToRoomId() != "plaza" || p.GetReason() != sim.ReasonRoomRemoved {
		t.Fatalf("relocated %v", p)
	}
	if rel[0].Scope.Room != (sim.RoomRef{Zone: "town", Room: "plaza"}) || !containsEntity(rel[0].Scope, "ch-1") || rel[0].Tick != res.Tick {
		t.Fatalf("scope %+v tick %d", rel[0].Scope, rel[0].Tick)
	}
	ents := e.State().Zones["town"].Entities
	if ents["ch-1"].Room != "plaza" || ents["ch-2"].Room != "plaza" || !ents["ch-2"].Dormant {
		t.Fatalf("bodies %+v %+v", ents["ch-1"], ents["ch-2"])
	}
	if got := res.Swaps[0].Relocations; len(got) != 2 || got[0].Entity != "ch-1" || got[1].Entity != "ch-2" || !got[1].Dormant {
		t.Fatalf("relocations %+v", got)
	}
	if d := mustStep(t, e, simtest.Look("town", "ch-1")).Events[0].Envelope.GetRoomDescribed(); d.GetRoomId() != "plaza" || len(d.GetExits()) != 0 {
		t.Fatalf("the next tick saw %v, want town@2's plaza", d)
	}
}

// A swap whose recorded digest is not what its version builds now refuses the
// Step before anything is applied: the tick, the state and the content in
// effect are untouched.
func TestContentSwap_DigestMismatchRefusesTheStep(t *testing.T) {
	c := newContent(t)
	e := contentEngine(t, c)
	mustStep(t, e, c.swap(t, e, nil, "town", 1))
	mustStep(t, e, simtest.Bind("town", "ch-1", "Aldric", "plaza"))
	tick, hash := e.Tick(), e.StateHash()
	versions, digest := e.Content()

	bad := c.swap(t, e, nil, "town", 2)
	bad.GetContentSwap().WorldDigest[0] ^= 0xff
	_, err := stepAll(t, e, simtest.Move("town", "ch-1", "north"), bad)
	var de *sim.ContentDigestError
	if !errors.Is(err, sim.ErrContentDigest) || !errors.As(err, &de) || de.Pack != "town" || de.Version != 2 {
		t.Fatalf("err = %v", err)
	}
	v, d := e.Content()
	if e.Tick() != tick || e.StateHash() != hash || v["town"] != versions["town"] || d != digest {
		t.Fatal("a refused swap changed the World")
	}
	if e.State().Zones["town"].Entities["ch-1"].Room != "plaza" {
		t.Fatal("the move in the refused tick was applied")
	}
}

// Two swaps in one tick apply in offset order, the second digesting a World
// that includes the first.
func TestContentSwap_TwoInOneTickApplyInOffsetOrder(t *testing.T) {
	c := newContent(t)
	e := contentEngine(t, c)
	mustStep(t, e, c.swap(t, e, nil, "town", 1))
	first := c.swap(t, e, nil, "town", 2)
	second := c.swap(t, e, map[string]uint64{"town": 2}, "docks", 1)
	res := mustStep(t, e, first, second)
	if len(res.Swaps) != 2 || res.Swaps[0].Pack != "town" || res.Swaps[1].Pack != "docks" {
		t.Fatalf("swaps %+v", res.Swaps)
	}
	if v, _ := e.Content(); v["town"] != 2 || v["docks"] != 1 || e.State().Zones["docks"] == nil {
		t.Fatalf("versions %v", v)
	}

	// The same two the other way round: the second was built on the first's
	// World, which is not in effect when it comes up, so it is stale — a
	// no-op, refused — and the first applies after it.
	e2 := contentEngine(t, c)
	mustStep(t, e2, c.swap(t, e2, nil, "town", 1))
	res, err := stepAll(t, e2, second, first)
	if err != nil || len(res.SwapsRefused) != 1 || res.SwapsRefused[0].Pack != "docks" || res.SwapsRefused[0].Reason != sim.SwapStaleBase ||
		len(res.Swaps) != 1 || res.Swaps[0].Pack != "town" {
		t.Fatalf("swaps out of order: err %v applied %v refused %v", err, res.Swaps, res.SwapsRefused)
	}
}

// A swap whose World removes a Zone the content in effect has is refused, a
// deterministic no-op, whether or not anyone stands in it (review of #87): a
// body in the Zone, or walking into it in the swap's tick, stays where the
// content still has it.
func TestContentSwap_ARemovedZoneIsRefused(t *testing.T) {
	for _, occupied := range []bool{true, false} {
		c := newContent(t)
		e := contentEngine(t, c)
		mustStep(t, e, c.swap(t, e, nil, "town", 3))
		if occupied {
			mustStep(t, e, simtest.Bind("docks", "ch-1", "Aldric", "pier"))
		}
		versions, digest := e.Content()
		hash := e.StateHash()
		res := mustStep(t, e, c.swap(t, e, nil, "town", 4))
		if len(res.Swaps) != 0 || len(res.SwapsRefused) != 1 || res.SwapsRefused[0].Reason != sim.SwapZoneRemoved {
			t.Fatalf("occupied=%v: applied %v refused %v", occupied, res.Swaps, res.SwapsRefused)
		}
		if v, d := e.Content(); v["town"] != versions["town"] || d != digest || e.World().Zones["docks"] == nil {
			t.Fatalf("occupied=%v: a refused swap changed the content in effect: %v", occupied, v)
		}
		if e.StateHash() == hash {
			t.Fatal("the refused swap's record was not consumed: the offset did not move")
		}
		if occupied {
			if d := mustStep(t, e, simtest.Look("docks", "ch-1")).Events[0].Envelope.GetRoomDescribed(); d.GetRoomId() != "pier" {
				t.Fatalf("after the refusal: %v", d)
			}
		}
	}
}

// A swap built on a World the log has moved past is stale: consumed and
// refused, a no-op, never a halt — the same on replay.
func TestContentSwap_AStaleSwapIsANoOp(t *testing.T) {
	c := newContent(t)
	e := contentEngine(t, c)
	mustStep(t, e, c.swap(t, e, nil, "town", 1))
	stale := c.swap(t, e, nil, "town", 2) // built on town@1
	mustStep(t, e, c.swap(t, e, nil, "town", 3))
	_, digest := e.Content()
	res := mustStep(t, e, stale)
	if len(res.Swaps) != 0 || len(res.SwapsRefused) != 1 || res.SwapsRefused[0].Reason != sim.SwapStaleBase {
		t.Fatalf("applied %v refused %v", res.Swaps, res.SwapsRefused)
	}
	if v, d := e.Content(); v["town"] != 3 || d != digest {
		t.Fatalf("in effect %v after a stale swap", v)
	}

	// A genesis swap on a World that already has content is stale too.
	genesis := c.swap(t, contentEngine(t, c), nil, "town", 1)
	if res := mustStep(t, e, genesis); len(res.SwapsRefused) != 1 || res.SwapsRefused[0].Reason != sim.SwapStaleBase {
		t.Fatalf("a second genesis: %v", res.SwapsRefused)
	}
}

// A ContentSwap not on the World Partition, or carrying a zone_id, is refused.
func TestContentSwap_AMisroutedSwapIsRefused(t *testing.T) {
	c := newContent(t)
	e := contentEngine(t, c)
	swap := c.swap(t, e, nil, "town", 1)
	swap.ZoneId = "town"
	p := sim.PartitionFor("town")
	res, err := e.Step(sim.TickInput{Records: []sim.Record{{Partition: p, Offset: 0, Command: swap}}})
	if err != nil || len(res.SwapsRefused) != 1 || res.SwapsRefused[0].Reason != sim.SwapMisrouted {
		t.Fatalf("err %v refused %v", err, res.SwapsRefused)
	}
	if v, _ := e.Content(); len(v) != 0 {
		t.Fatalf("a misrouted swap applied: %v", v)
	}
}

// A source that hands back a Zone with no fallback of its own is not trusted:
// the swap is refused rather than relocating anyone to nowhere.
func TestContentSwap_AFallbacklessZoneIsRefused(t *testing.T) {
	c := newContent(t)
	e := contentEngine(t, c)
	mustStep(t, e, c.swap(t, e, nil, "town", 1))
	bad := &badFallback{c}
	e2 := sim.NewEngine(sim.EmptyWorld(), nil, sim.Config{Seed: 7, Partitions: simtest.AllPartitions(), Handlers: sim.Handlers(), Content: bad})
	swap := c.swap(t, e2, nil, "town", 1)
	topo, _ := bad.Prepare(map[string]uint64{}, swap.GetContentSwap())
	d := sim.ContentDigest(topo)
	swap.GetContentSwap().WorldDigest = d[:]
	res := mustStep(t, e2, swap)
	if len(res.SwapsRefused) != 1 || res.SwapsRefused[0].Reason != sim.SwapFallbackMissing {
		t.Fatalf("refused %v", res.SwapsRefused)
	}
}

// badFallback builds c's World and then empties every Zone's fallback.
type badFallback struct{ c *fakeContent }

func (b *badFallback) Prepare(in map[string]uint64, s *logv1.ContentSwap) (sim.Topology, error) {
	topo, err := b.c.Prepare(in, s)
	if err != nil {
		return topo, err
	}
	for _, z := range topo.World.Zones {
		z.Fallback = ""
	}
	return topo, nil
}

// A Character in transit when a swap removes the Room it is walking into lands
// in that Zone's fallback with EntityRelocated, and is never bounced or lost.
func TestContentSwap_AnArrivalIntoARemovedRoomLandsAtTheFallback(t *testing.T) {
	reg, err := simtest.Templates()
	if err != nil {
		t.Fatal(err)
	}
	crossing := func(pierToo bool) []*contentv1.ZoneDefinition {
		town := zdef("town", "plaza", rdef("plaza", xdef("north", "hall")),
			rdef("hall", xdef("south", "plaza"), &contentv1.ExitDefinition{Direction: "east", ToZone: "docks", ToRoom: "pier"}))
		docks := zdef("docks", "quay", rdef("quay"), rdef("pier", &contentv1.ExitDefinition{Direction: "west", ToZone: "town", ToRoom: "hall"}))
		if !pierToo {
			town = zdef("town", "plaza", rdef("plaza", xdef("north", "hall")), rdef("hall", xdef("south", "plaza")))
			docks = zdef("docks", "quay", rdef("quay"))
		}
		return []*contentv1.ZoneDefinition{town, docks}
	}
	c := &fakeContent{templates: reg, zones: map[string]map[uint64][]*contentv1.ZoneDefinition{"world": {1: crossing(true), 2: crossing(false)}}}
	e := contentEngine(t, c)
	mustStep(t, e, c.swap(t, e, nil, "world", 1))
	mustStep(t, e, simtest.Bind("town", "ch-1", "Aldric", "plaza"))
	mustStep(t, e, simtest.Move("town", "ch-1", "north"))
	// east from the hall leaves town this tick; the swap removes the pier.
	res := mustStep(t, e, simtest.Move("town", "ch-1", "east"), c.swap(t, e, nil, "world", 2))
	if len(res.Outbound) != 1 || len(res.Swaps) != 1 {
		t.Fatalf("outbound %v swaps %v", res.Outbound, res.Swaps)
	}
	arrived := mustStep(t, e, res.Outbound[0])
	rel := ofType(arrived.Events, sim.EvEntityRelocated)
	if len(rel) != 1 || rel[0].Envelope.GetEntityRelocated().GetToRoomId() != "quay" || rel[0].Envelope.GetEntityRelocated().GetFromRoomId() != "pier" {
		t.Fatalf("arrival events %v", arrived.Events)
	}
	if ent := e.State().Zones["docks"].Entities["ch-1"]; ent == nil || ent.Room != "quay" {
		t.Fatalf("body %+v", ent)
	}
}

// Relocation across two swaps: an Entity moved to a fallback by one swap is
// moved on by a later one that removes that Room too.
func TestContentSwap_RelocationAcrossTwoSwaps(t *testing.T) {
	reg, _ := simtest.Templates()
	c := &fakeContent{templates: reg, zones: map[string]map[uint64][]*contentv1.ZoneDefinition{"town": {
		1: {zdef("town", "hall", rdef("plaza", xdef("north", "hall")), rdef("hall", xdef("south", "plaza"), xdef("north", "attic")), rdef("attic", xdef("south", "hall")))},
		2: {zdef("town", "hall", rdef("plaza", xdef("north", "hall")), rdef("hall", xdef("south", "plaza")))},
		3: {zdef("town", "plaza", rdef("plaza"))},
	}}}
	e := contentEngine(t, c)
	mustStep(t, e, c.swap(t, e, nil, "town", 1))
	mustStep(t, e, simtest.Bind("town", "ch-1", "Aldric", "plaza"))
	mustStep(t, e, simtest.Move("town", "ch-1", "north"), simtest.Move("town", "ch-1", "north"))
	if r := e.State().Zones["town"].Entities["ch-1"].Room; r != "attic" {
		t.Fatalf("fixture: in %s", r)
	}
	mustStep(t, e, c.swap(t, e, nil, "town", 2))
	if r := e.State().Zones["town"].Entities["ch-1"].Room; r != "hall" {
		t.Fatalf("after town@2: in %s", r)
	}
	res := mustStep(t, e, c.swap(t, e, nil, "town", 3))
	if r := e.State().Zones["town"].Entities["ch-1"].Room; r != "plaza" || len(res.Swaps[0].Relocations) != 1 {
		t.Fatalf("after town@3: in %s, %v", r, res.Swaps)
	}
}

// A swap behind a record whose Zone faulted on its Partition is requeued with
// it, not applied: the tick applies nothing past the fault there.
func TestContentSwap_BehindAFaultIsRequeued(t *testing.T) {
	// A fault precedes a swap on its Partition only when a Zone lives on the
	// World Partition, so the fixture stands one up there, and a Look into it
	// panics.
	c := newContent(t)
	zone := zoneOnWorldPartition(t)
	panicky := sim.NewEngine(sim.EmptyWorld(), nil, sim.Config{Seed: 7, Partitions: simtest.AllPartitions(), Content: c,
		Handlers: map[sim.CommandKind]sim.Apply{sim.KindLook: func(*sim.ApplyContext, *logv1.LoggedCommand) error { panic("boom") }}})
	mustStep(t, panicky, c.swap(t, panicky, nil, "town", 1))
	panicky.State().Zones[zone] = &sim.ZoneState{ID: zone, Entities: map[sim.EntityID]*sim.EntityState{}}
	panicky.World().Zones[zone] = &sim.Zone{ID: zone, Rooms: map[sim.RoomID]*sim.Room{"r": {ID: "r"}}, Fallback: "r", Partition: sim.WorldPartition}
	next := c.swap(t, panicky, nil, "town", 2)
	off := panicky.State().Offsets[sim.WorldPartition]
	res, err := panicky.Step(sim.TickInput{Records: []sim.Record{
		{Partition: sim.WorldPartition, Offset: off, Command: simtest.Look(string(zone), "x")},
		{Partition: sim.WorldPartition, Offset: off + 1, Command: next},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Faults) != 1 || len(res.Swaps) != 0 || len(res.Unapplied) != 2 {
		t.Fatalf("faults %v swaps %v unapplied %d", res.Faults, res.Swaps, len(res.Unapplied))
	}
	if v, _ := panicky.Content(); v["town"] != 1 {
		t.Fatalf("a swap behind a fault applied: %v", v)
	}
}

// zoneOnWorldPartition is a Zone ID that sim.PartitionFor maps to Partition 0.
func zoneOnWorldPartition(t *testing.T) sim.ZoneID {
	t.Helper()
	for i := 0; i < 10000; i++ {
		z := sim.ZoneID(fmt.Sprintf("z%d", i))
		if sim.PartitionFor(z) == sim.WorldPartition {
			return z
		}
	}
	t.Fatal("no zone on partition 0")
	return ""
}

// An Engine with no ContentSource cannot apply a swap, and says so.
func TestContentSwap_NeedsASource(t *testing.T) {
	c := newContent(t)
	e := sim.NewEngine(sim.EmptyWorld(), nil, sim.Config{Seed: 7, Partitions: simtest.AllPartitions(), Handlers: sim.Handlers()})
	if _, err := stepAll(t, e, c.swap(t, e, nil, "town", 1)); !errors.Is(err, sim.ErrNoContentSource) {
		t.Fatalf("err = %v", err)
	}
}

// The DoD's replay across a swap: an Engine replaying the recorded boundaries
// from an empty topology reaches the same State Hash and the same content in
// effect, building content only from the swaps it replays. Replayed against
// content that changed since, it halts on the digest.
func TestContentSwap_ReplayAcrossASwapIsExact(t *testing.T) {
	c := newContent(t)
	live := contentEngine(t, c)
	log := simtest.MemorySource{}
	var boundaries []sim.TickCompleted
	tick := func(cmds ...*logv1.LoggedCommand) {
		t.Helper()
		res, err := stepAll(t, live, cmds...)
		if err != nil {
			t.Fatal(err)
		}
		boundaries = append(boundaries, res.Completed)
	}
	record := func(cmds ...*logv1.LoggedCommand) []*logv1.LoggedCommand {
		for _, cmd := range cmds {
			p := int32(0)
			if cmd.GetZoneId() != "" {
				p = sim.PartitionFor(sim.ZoneID(cmd.GetZoneId()))
			}
			log[p] = append(log[p], sim.Record{Partition: p, Offset: int64(len(log[p])), Command: cmd})
		}
		return cmds
	}
	tick(record(c.swap(t, live, nil, "town", 1))...)
	tick(record(simtest.Bind("town", "ch-1", "Aldric", "plaza"))...)
	tick(record(simtest.Move("town", "ch-1", "north"))...)
	tick()
	tick(record(simtest.Look("town", "ch-1"), c.swap(t, live, nil, "town", 2))...)
	tick(record(simtest.Look("town", "ch-1"))...)

	replay := contentEngine(t, c)
	if err := replay.Replay(boundaries, log); err != nil {
		t.Fatal(err)
	}
	lv, ld := live.Content()
	rv, rd := replay.Content()
	if replay.StateHash() != live.StateHash() || rv["town"] != lv["town"] || rd != ld {
		t.Fatalf("replay reached %v/%x, live %v/%x", rv, rd[:4], lv, ld[:4])
	}

	// town@2 is not what it was when the log was written.
	changed := newContent(t)
	changed.zones["town"][2] = []*contentv1.ZoneDefinition{zdef("town", "plaza", rdef("plaza"), rdef("cellar"))}
	if err := contentEngine(t, changed).Replay(boundaries, log); !errors.Is(err, sim.ErrContentDigest) {
		t.Fatalf("replay over changed content: err = %v", err)
	}
}

// world_digest covers every Zone and Template and nothing about where a
// Template blob was read from.
func TestContentDigest_CoversTopologyNotProvenance(t *testing.T) {
	c := newContent(t)
	build := func(defs ...*contentv1.ZoneDefinition) *sim.World {
		var in []sim.Input
		for _, d := range defs {
			in = append(in, sim.Input{File: d.GetId() + ".json", Def: d})
		}
		w, errs := sim.BuildWorld(in, sim.Options{})
		if w == nil {
			t.Fatal(errs)
		}
		return w
	}
	base := sim.ContentDigest(sim.Topology{World: build(c.zones["town"][1]...), Templates: c.templates})
	if base == sim.ContentDigest(sim.Topology{World: build(c.zones["town"][1]...)}) {
		t.Error("the digest does not cover Templates")
	}
	moved := zdef("town", "hall", rdef("plaza", xdef("north", "hall")), rdef("hall", xdef("south", "plaza")))
	if base == sim.ContentDigest(sim.Topology{World: build(moved), Templates: c.templates}) {
		t.Error("the digest does not cover the fallback Room")
	}
	elsewhere, errs := sim.BuildTemplates([]sim.TemplateInput{
		{File: "/mnt/elsewhere/andara.core.Entity.json", Def: &contentv1.TemplateDefinition{FormatVersion: sim.TemplateFormatVersion, Name: "andara.core.Entity", Kind: contentv1.TemplateKind_ENTITY, Chain: []string{"andara.core.Entity"}, Resolved: true}},
	}, sim.TemplateOptions{})
	here, _ := sim.BuildTemplates([]sim.TemplateInput{
		{File: "andara.core.Entity.json", Def: &contentv1.TemplateDefinition{FormatVersion: sim.TemplateFormatVersion, Name: "andara.core.Entity", Kind: contentv1.TemplateKind_ENTITY, Chain: []string{"andara.core.Entity"}, Resolved: true, Source: &contentv1.SourceRef{File: "core/entity.aw", Line: 3}}},
	}, sim.TemplateOptions{})
	if len(errs) > 0 || sim.TemplatesCanonicalBytes(elsewhere) == nil || string(sim.TemplatesCanonicalBytes(elsewhere)) != string(sim.TemplatesCanonicalBytes(here)) {
		t.Errorf("the digest reads a Template's file or source: %v", errs)
	}
}

// A snapshot round carries the content in effect at its tick (envelope fields
// 8 and 9), and a restore rebuilds that topology through the content source
// and checks its digest before loading any body (AW-SRV-012, for AW-SRV-007).
func TestSnapshot_CarriesAndRestoresTheContentInEffect(t *testing.T) {
	c := newContent(t)
	e := contentEngine(t, c)
	mustStep(t, e, c.swap(t, e, nil, "town", 3))
	mustStep(t, e, simtest.Bind("town", "ch-1", "Aldric", "plaza"))
	versions, digest := e.Content()

	snaps := e.SnapshotAll(1)
	if len(snaps) == 0 {
		t.Fatal("no snapshots")
	}
	for _, s := range snaps {
		v, d := s.Content()
		if v["town"] != 3 || d != digest {
			t.Fatalf("snapshot of %s carries %v", s.Zone, v)
		}
	}
	raw, err := snaps[0].Encode()
	if err != nil {
		t.Fatal(err)
	}
	var env statev1.SnapshotEnvelope
	if err := proto.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	if len(env.GetContent()) != 1 || env.GetContent()[0].GetPackId() != "town" || env.GetContent()[0].GetVersion() != 3 || string(env.GetContentDigest()) != string(digest[:]) {
		t.Fatalf("envelope content %v digest %x", env.GetContent(), env.GetContentDigest())
	}

	round := sim.RoundState{Tick: e.Tick(), StateVersion: sim.StateVersion, PRNG: e.State().RNG.State(), NextEventID: e.State().NextEventID,
		Content: versions, ContentDigest: digest[:]}
	for p, o := range e.State().Offsets {
		round.Offsets = append(round.Offsets, sim.PartitionOffset{Partition: p, Offset: o})
	}
	for _, s := range snaps {
		round.Zones = append(round.Zones, s.Body())
	}
	topo, err := sim.PrepareContent(c, versions)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := sim.RestoreEngine(topo.World, topo.Templates, sim.Config{Seed: 7, Handlers: sim.Handlers(), Content: c}, round)
	if err != nil {
		t.Fatal(err)
	}
	if rv, rd := restored.Content(); rv["town"] != 3 || rd != digest || restored.StateHash() != e.StateHash() {
		t.Fatalf("restored content %v, hash equal %v", rv, restored.StateHash() == e.StateHash())
	}

	other, _ := sim.PrepareContent(c, map[string]uint64{"town": 1})
	if _, err := sim.RestoreEngine(other.World, other.Templates, sim.Config{Seed: 7}, round); !errors.Is(err, sim.ErrContentDigest) {
		t.Fatalf("restore onto other content: err = %v", err)
	}
}

// A bound Character records the pack@version its Template came from, as the
// log has it in effect: andara.core's when that pack is in effect, and the
// one pack in effect when a single pack supplies everything (review of #91;
// closes #70's gap).
func TestBind_RecordsTheContentVersionInEffect(t *testing.T) {
	c := newContent(t)
	c.zones["andara.core"] = map[uint64][]*contentv1.ZoneDefinition{4: {}}
	e := contentEngine(t, c)
	mustStep(t, e, c.swap(t, e, nil, "andara.core", 4))
	mustStep(t, e, c.swap(t, e, nil, "town", 1))
	mustStep(t, e, simtest.Bind("town", "ch-1", "Aldric", "plaza"))
	if got := e.State().Zones["town"].Entities["ch-1"].ContentVersion; got != "andara.core@4" {
		t.Fatalf("content_version = %q, want andara.core@4", got)
	}

	single := contentEngine(t, c)
	mustStep(t, single, c.swap(t, single, nil, "town", 1))
	mustStep(t, single, simtest.Bind("town", "ch-1", "Aldric", "plaza"))
	if got := single.State().Zones["town"].Entities["ch-1"].ContentVersion; got != "town@1" {
		t.Fatalf("single pack: content_version = %q, want town@1", got)
	}
}

// A swap changes future spawns, not existing bodies: a Template re-parented
// under andara.core.Character does not make a body already made from it a
// Character (review of #91).
func TestContentSwap_ReparentingATemplateDoesNotReclassifyBodies(t *testing.T) {
	entity := contentv1.TemplateKind_ENTITY
	def := func(name string, chain ...string) sim.TemplateInput {
		return sim.TemplateInput{File: name + ".json", Def: &contentv1.TemplateDefinition{
			FormatVersion: sim.TemplateFormatVersion, Name: name, Kind: entity, Chain: chain, Resolved: true}}
	}
	registry := func(guardChain ...string) *sim.TemplateRegistry {
		reg, errs := sim.BuildTemplates([]sim.TemplateInput{
			def("andara.core.Entity", "andara.core.Entity"),
			def("andara.core.Character", "andara.core.Entity", "andara.core.Character"),
			def("andara.core.Npc", "andara.core.Entity", "andara.core.Npc"),
			def("town.Guard", guardChain...),
		}, sim.TemplateOptions{})
		if len(errs) > 0 {
			t.Fatal(errs)
		}
		return reg
	}
	v1 := registry("andara.core.Entity", "andara.core.Npc", "town.Guard")
	v2 := registry("andara.core.Entity", "andara.core.Character", "town.Guard")
	c := newContent(t)
	src := &perVersionTemplates{c: c, templates: map[uint64]*sim.TemplateRegistry{1: v1, 4: v2}}
	e := sim.NewEngine(sim.EmptyWorld(), nil, sim.Config{Seed: 7, Partitions: simtest.AllPartitions(), Handlers: sim.Handlers(), Content: src})
	mustStep(t, e, swapFor(t, src, e, "town", 1))
	guard, _ := v1.Get("town.Guard")
	body := sim.Instantiate(guard, "g-1", "town@1")
	body.Room = "plaza"
	e.State().Zones["town"].Entities["g-1"] = &body
	before := e.Characters()

	mustStep(t, e, swapFor(t, src, e, "town", 4))
	if after := e.Characters(); after != before || e.IsCharacter(&body) {
		t.Fatalf("characters %+v -> %+v; the guard became a Character", before, after)
	}
}

// perVersionTemplates is fakeContent with a Template registry per town version.
type perVersionTemplates struct {
	c         *fakeContent
	templates map[uint64]*sim.TemplateRegistry
}

func (p *perVersionTemplates) Prepare(in map[string]uint64, s *logv1.ContentSwap) (sim.Topology, error) {
	topo, err := p.c.Prepare(in, s)
	if err != nil {
		return topo, err
	}
	topo.Templates = p.templates[in["town"]]
	return topo, nil
}

func swapFor(t *testing.T, src sim.ContentSource, e *sim.Engine, pack string, version uint64) *logv1.LoggedCommand {
	t.Helper()
	inEffect, base := e.Content()
	cs := &logv1.ContentSwap{PackId: pack, Version: version}
	if len(inEffect) > 0 {
		cs.BaseDigest = base[:]
	}
	topo, err := src.Prepare(copyMap(inEffect), cs)
	if err != nil {
		t.Fatal(err)
	}
	d := sim.ContentDigest(topo)
	cs.WorldDigest = d[:]
	return &logv1.LoggedCommand{Command: &logv1.LoggedCommand_ContentSwap{ContentSwap: cs}}
}
