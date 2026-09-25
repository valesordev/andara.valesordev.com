// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package simtest

import (
	"fmt"
	"sort"

	contentv1 "github.com/valesordev/andara/gen/go/andara/content/v1"
	logv1 "github.com/valesordev/andara/gen/go/andara/log/v1"
	"github.com/valesordev/andara/server/sim"
)

// VersionedContent is a sim.ContentSource over fixed versions of each pack:
// the World a set of versions builds is every pack's Zones at its version,
// with the fixture Templates (AW-SRV-012).
type VersionedContent struct {
	Zones     map[string]map[uint64][]*contentv1.ZoneDefinition
	Templates *sim.TemplateRegistry
}

// TownVersions is town@1 — the plaza, the hall north of it, fallback the
// plaza — and town@2, the hall removed.
func TownVersions() (*VersionedContent, error) {
	reg, err := Templates()
	if err != nil {
		return nil, err
	}
	room := func(id string, exits ...*contentv1.ExitDefinition) *contentv1.RoomDefinition {
		return &contentv1.RoomDefinition{Id: id, Title: id, Description: "The " + id + ".", Exits: exits}
	}
	exit := func(dir, to string) *contentv1.ExitDefinition {
		return &contentv1.ExitDefinition{Direction: dir, ToRoom: to}
	}
	v1 := &contentv1.ZoneDefinition{FormatVersion: 1, Id: "town", Name: "Town", FallbackRoom: "plaza",
		Rooms: []*contentv1.RoomDefinition{room("plaza", exit("north", "hall")), room("hall", exit("south", "plaza"))}}
	v2 := &contentv1.ZoneDefinition{FormatVersion: 1, Id: "town", Name: "Town", FallbackRoom: "plaza",
		Rooms: []*contentv1.RoomDefinition{room("plaza")}}
	return &VersionedContent{Templates: reg, Zones: map[string]map[uint64][]*contentv1.ZoneDefinition{
		"town": {1: {v1}, 2: {v2}},
	}}, nil
}

// Prepare implements sim.ContentSource.
func (c *VersionedContent) Prepare(inEffect map[string]uint64, swap *logv1.ContentSwap) (sim.Topology, error) {
	after := map[string]uint64{}
	for p, v := range inEffect {
		after[p] = v
	}
	after[swap.GetPackId()] = swap.GetVersion()
	packs := make([]string, 0, len(after))
	for p := range after {
		packs = append(packs, p)
	}
	sort.Strings(packs)
	var inputs []sim.Input
	for _, p := range packs {
		defs, ok := c.Zones[p][after[p]]
		if !ok {
			return sim.Topology{}, fmt.Errorf("simtest: no %s@%d", p, after[p])
		}
		for _, d := range defs {
			inputs = append(inputs, sim.Input{File: d.GetId() + ".json", Def: d})
		}
	}
	w, errs := sim.BuildWorld(inputs, sim.Options{})
	if w == nil {
		return sim.Topology{}, fmt.Errorf("simtest: build: %v", errs)
	}
	return sim.Topology{World: w, Templates: c.Templates}, nil
}

// Swap is the ContentSwap for pack@version on top of what e has in effect,
// carrying the digest that version builds.
func (c *VersionedContent) Swap(e *sim.Engine, pack string, version uint64) (*logv1.LoggedCommand, error) {
	inEffect, base := e.Content()
	cs := &logv1.ContentSwap{PackId: pack, Version: version}
	if len(inEffect) > 0 {
		cs.BaseDigest = base[:]
	}
	topo, err := c.Prepare(inEffect, cs)
	if err != nil {
		return nil, err
	}
	d := sim.ContentDigest(topo)
	cs.WorldDigest = d[:]
	return &logv1.LoggedCommand{Command: &logv1.LoggedCommand_ContentSwap{ContentSwap: cs}}, nil
}

// FixedContent is a sim.ContentSource whose every version is one topology:
// how a fixture World comes into effect through a genesis swap, as a real
// log's does (AW-SRV-012).
type FixedContent struct{ Topology sim.Topology }

// CrossingContent is CrossingWorld and the fixture Templates as one pack.
func CrossingContent() (*FixedContent, error) {
	w, err := CrossingWorld()
	if err != nil {
		return nil, err
	}
	reg, err := Templates()
	if err != nil {
		return nil, err
	}
	return &FixedContent{Topology: sim.Topology{World: w, Templates: reg}}, nil
}

// Prepare implements sim.ContentSource.
func (c *FixedContent) Prepare(map[string]uint64, *logv1.ContentSwap) (sim.Topology, error) {
	return c.Topology, nil
}

// FixturePack is the pack a fixture's genesis swap names.
const FixturePack = "fixture"

// Genesis is the ContentSwap that brings c into effect: fixture@1 with its
// digest, for Partition 0.
func (c *FixedContent) Genesis() *logv1.LoggedCommand {
	d := sim.ContentDigest(c.Topology)
	return &logv1.LoggedCommand{Command: &logv1.LoggedCommand_ContentSwap{ContentSwap: &logv1.ContentSwap{PackId: FixturePack, Version: 1, WorldDigest: d[:]}}}
}
