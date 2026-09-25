// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package sim_test

import (
	"errors"
	"fmt"
	"sort"
	"testing"

	contentv1 "github.com/valesordev/andara/gen/go/andara/content/v1"
	logv1 "github.com/valesordev/andara/gen/go/andara/log/v1"
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
	inEffect, _ := e.Content()
	for p, v := range extra {
		inEffect[p] = v
	}
	cs := &logv1.ContentSwap{PackId: pack, Version: version}
	topo, err := f.Prepare(inEffect, cs)
	if err != nil {
		t.Fatal(err)
	}
	d := sim.ContentDigest(topo)
	cs.WorldDigest = d[:]
	return &logv1.LoggedCommand{Command: &logv1.LoggedCommand_ContentSwap{ContentSwap: cs}}
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

	// The same two, the other way round, are not the same digests: each is a
	// claim about the whole World at its point in the order.
	e2 := contentEngine(t, c)
	mustStep(t, e2, c.swap(t, e2, nil, "town", 1))
	if _, err := stepAll(t, e2, second, first); !errors.Is(err, sim.ErrContentDigest) {
		t.Fatalf("swaps out of order: err = %v", err)
	}
}

// A version that drops a whole Zone while Entities stand in it strands them:
// their state is kept and their Commands refused unknown_zone, until content
// brings the Zone back.
func TestContentSwap_ARemovedZoneStrandsItsState(t *testing.T) {
	c := newContent(t)
	e := contentEngine(t, c)
	mustStep(t, e, c.swap(t, e, nil, "town", 3))
	mustStep(t, e, simtest.Bind("docks", "ch-1", "Aldric", "pier"))
	res := mustStep(t, e, c.swap(t, e, nil, "town", 4))
	if s := res.Swaps[0].Stranded; len(s) != 1 || s[0] != "docks" {
		t.Fatalf("stranded %v", s)
	}
	if ent := e.State().Zones["docks"].Entities["ch-1"]; ent == nil || ent.Room != "pier" {
		t.Fatalf("stranded body %+v", ent)
	}
	if r := rejection(t, mustStep(t, e, simtest.Look("docks", "ch-1")).Events); r.GetCode() != sim.CodeUnknownZone {
		t.Fatalf("code %q", r.GetCode())
	}
	mustStep(t, e, c.swap(t, e, nil, "town", 3))
	if d := mustStep(t, e, simtest.Look("docks", "ch-1")).Events[0].Envelope.GetRoomDescribed(); d.GetRoomId() != "pier" {
		t.Fatalf("back in docks: %v", d)
	}
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
