// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

// Package simtest is the shared fixture for tests that drive the simulation
// core: a three-Zone World, the core Templates plus a town Merchant, handlers
// that exercise state through the RNG, a deterministic scripted log, and an
// in-memory RecordSource. It lives outside server/sim because the core's
// own tests may not touch the filesystem (depguard) and the loop's tests
// need the same World.
package simtest

import (
	"fmt"
	"sort"

	contentv1 "github.com/valesordev/andara/gen/go/andara/content/v1"
	gamev1 "github.com/valesordev/andara/gen/go/andara/game/v1"
	logv1 "github.com/valesordev/andara/gen/go/andara/log/v1"
	"github.com/valesordev/andara/server/sim"
)

// Zones is the fixture's Zone IDs.
var Zones = []string{"town", "docks", "wilds"}

func zoneDef(id, name string, rooms ...string) sim.Input {
	def := &contentv1.ZoneDefinition{FormatVersion: 1, Id: id, Name: name}
	for i, r := range rooms {
		rd := &contentv1.RoomDefinition{Id: r, Title: r, Description: "A " + r + "."}
		if i > 0 {
			rd.Exits = []*contentv1.ExitDefinition{{Direction: "north", ToRoom: rooms[i-1]}}
			def.Rooms[i-1].Exits = append(def.Rooms[i-1].Exits, &contentv1.ExitDefinition{Direction: "south", ToRoom: r})
		}
		def.Rooms = append(def.Rooms, rd)
	}
	return sim.Input{File: id + ".json", Def: def}
}

// World is three Zones on three Partitions.
func World() (*sim.World, error) {
	w, errs := sim.BuildWorld([]sim.Input{
		zoneDef("town", "Town", "plaza", "lane"),
		zoneDef("docks", "Docks", "pier"),
		zoneDef("wilds", "Wilds", "glade"),
	}, sim.Options{})
	for _, e := range errs {
		if !sim.IsWarning(e, false) {
			return nil, e
		}
	}
	return w, nil
}

// CrossingWorld is the World in testdata/content/valid, built in code so
// the core's tests may use it: town (plaza, hall), docks (pier, warehouse),
// wilds (trail, clearing), with plaza's east Exit into wilds and south Exit
// into docks — the cross-Zone edges AW-SRV-003 AC-9 needs.
func CrossingWorld() (*sim.World, error) {
	exit := func(dir, zone, room string) *contentv1.ExitDefinition {
		return &contentv1.ExitDefinition{Direction: dir, ToZone: zone, ToRoom: room}
	}
	town := &contentv1.ZoneDefinition{FormatVersion: 1, Id: "town", Name: "Town", Rooms: []*contentv1.RoomDefinition{
		{Id: "plaza", Title: "Market Plaza", Description: "A dusty square of packed earth.",
			Exits: []*contentv1.ExitDefinition{exit("north", "", "hall"), exit("east", "wilds", "trail"), exit("south", "docks", "pier")}},
		{Id: "hall", Title: "Town Hall", Description: "Stone walls and faded banners.",
			Exits: []*contentv1.ExitDefinition{exit("south", "", "plaza")}},
	}}
	docks := &contentv1.ZoneDefinition{FormatVersion: 1, Id: "docks", Name: "Docks", Rooms: []*contentv1.RoomDefinition{
		{Id: "pier", Title: "The Pier", Description: "Salt air and creaking boards.",
			Exits: []*contentv1.ExitDefinition{exit("north", "town", "plaza"), exit("south", "", "warehouse")}},
		{Id: "warehouse", Title: "Warehouse", Description: "Barrels and rope.",
			Exits: []*contentv1.ExitDefinition{exit("north", "", "pier")}},
	}}
	wilds := &contentv1.ZoneDefinition{FormatVersion: 1, Id: "wilds", Name: "Wilds", Rooms: []*contentv1.RoomDefinition{
		{Id: "trail", Title: "Forest Trail", Description: "A narrow path under pines.",
			Exits: []*contentv1.ExitDefinition{exit("west", "town", "plaza"), exit("east", "", "clearing")}},
		{Id: "clearing", Title: "Clearing", Description: "Sunlight on moss.",
			Exits: []*contentv1.ExitDefinition{exit("west", "", "trail")}},
	}}
	w, errs := sim.BuildWorld([]sim.Input{
		{File: "town.json", Def: town}, {File: "docks.json", Def: docks}, {File: "wilds.json", Def: wilds},
	}, sim.Options{})
	for _, e := range errs {
		if !sim.IsWarning(e, false) {
			return nil, e
		}
	}
	return w, nil
}

// Place puts a Character in a Room of e's World, the way AW-SRV-014's
// spawn will: the andara.core.Character Template, no Components, the
// EntityID as its name.
func Place(e *sim.Engine, id, zone, room string) {
	e.State().Zones[sim.ZoneID(zone)].Entities[sim.EntityID(id)] = &sim.EntityState{
		ID: sim.EntityID(id), Template: "andara.core.Character", ContentVersion: "core@1", Room: sim.RoomID(room),
	}
}

// NewVerbEngine builds an Engine over CrossingWorld with the real verb
// handlers (sim.Handlers) — the fixture for look and move.
func NewVerbEngine(seed uint64) (*sim.Engine, error) {
	w, err := CrossingWorld()
	if err != nil {
		return nil, err
	}
	reg, err := Templates()
	if err != nil {
		return nil, err
	}
	return sim.NewEngine(w, reg, sim.Config{Seed: seed, Partitions: AllPartitions(), Handlers: sim.Handlers()}), nil
}

func tdef(name string, kind contentv1.TemplateKind, chain []string, comps ...*contentv1.ComponentValue) sim.TemplateInput {
	return sim.TemplateInput{File: name + ".json", Def: &contentv1.TemplateDefinition{
		FormatVersion: sim.TemplateFormatVersion, Name: name, Kind: kind, Chain: chain, Components: comps, Resolved: true,
	}}
}

// Templates is the core seed plus town.Merchant.
func Templates() (*sim.TemplateRegistry, error) {
	entity := contentv1.TemplateKind_ENTITY
	reg, errs := sim.BuildTemplates([]sim.TemplateInput{
		tdef("andara.core.Entity", entity, []string{"andara.core.Entity"}),
		tdef("andara.core.Character", entity, []string{"andara.core.Entity", "andara.core.Character"}),
		tdef("andara.core.Npc", entity, []string{"andara.core.Entity", "andara.core.Npc"}, &contentv1.ComponentValue{Type: "andara.core.Memory"}),
		tdef("town.Merchant", entity, []string{"andara.core.Entity", "andara.core.Npc", "town.Merchant"},
			&contentv1.ComponentValue{Type: "andara.core.Memory"},
			&contentv1.ComponentValue{Type: "andara.core.Behavior", Fields: []*contentv1.ComponentField{{Name: "name", Value: &contentv1.ComponentField_StringValue{StringValue: "town.merchant"}}}}),
	}, sim.TemplateOptions{})
	if len(errs) != 0 {
		return nil, errs[0]
	}
	return reg, nil
}

// AllPartitions is 0..63.
func AllPartitions() []int32 {
	out := make([]int32, sim.PartitionCount)
	for i := range out {
		out[i] = int32(i)
	}
	return out
}

// Handlers exercise state: look spawns an Entity whose ID comes from the
// RNG and whose Behavior name records the tick it was spawned on; move
// renames every Entity's Behavior, rejects "nowhere", panics on "panic",
// and produces a cross-Zone Command on "elsewhere". Together they touch the
// RNG, the tick, the Entity map, and Component fields, so the hash means
// something and batching matters.
func Handlers(reg *sim.TemplateRegistry) map[sim.CommandKind]sim.Apply {
	return map[sim.CommandKind]sim.Apply{
		"look": func(a *sim.ApplyContext, _ *logv1.LoggedCommand) error {
			tmpl, _ := reg.Get("town.Merchant")
			id := sim.EntityID(fmt.Sprintf("e-%d", a.RNG.Intn(1000)))
			ent := sim.Instantiate(tmpl, id, "town@1")
			// Stamp the tick into the Entity, so that how records were
			// batched into ticks changes the state — which is what makes
			// replaying from recorded boundaries, rather than re-deciding
			// them, the only exact replay (AC-5).
			for i := range ent.Components {
				for j := range ent.Components[i].Fields {
					if ent.Components[i].Fields[j].Name == "name" {
						ent.Components[i].Fields[j].Str = fmt.Sprintf("spawned@%d", a.Tick)
					}
				}
			}
			a.Zone.Entities[id] = &ent
			a.Emit(sim.ScopeEntities(a.Actor()), &gamev1.EventEnvelope{Payload: &gamev1.EventEnvelope_RoomDescribed{RoomDescribed: &gamev1.RoomDescribed{ZoneId: string(a.Zone.ID), RoomId: "plaza"}}})
			return nil
		},
		"move": func(a *sim.ApplyContext, cmd *logv1.LoggedCommand) error {
			dir := cmd.GetMove().GetDirection()
			switch dir {
			case "nowhere":
				return &sim.RejectError{Code: "no_such_exit", Message: "there is no exit that way"}
			case "panic":
				panic("handler blew up")
			case "elsewhere":
				a.Produce(&logv1.LoggedCommand{ZoneId: "docks", ActorId: cmd.GetActorId(), Command: &logv1.LoggedCommand_Look{Look: &logv1.Look{}}})
				return nil
			}
			ids := make([]string, 0, len(a.Zone.Entities))
			for id := range a.Zone.Entities {
				ids = append(ids, string(id))
			}
			sort.Strings(ids)
			for _, id := range ids {
				ent := a.Zone.Entities[sim.EntityID(id)]
				for i := range ent.Components {
					for j := range ent.Components[i].Fields {
						if ent.Components[i].Fields[j].Name == "name" {
							ent.Components[i].Fields[j].Str = dir
						}
					}
				}
			}
			return nil
		},
	}
}

// Look is a look Command for zone by actor.
func Look(zone, actor string) *logv1.LoggedCommand {
	return &logv1.LoggedCommand{ZoneId: zone, ActorId: actor, ClientRef: "ref-" + actor, Command: &logv1.LoggedCommand_Look{Look: &logv1.Look{}}}
}

// Move is a move Command.
func Move(zone, actor, dir string) *logv1.LoggedCommand {
	return &logv1.LoggedCommand{ZoneId: zone, ActorId: actor, Command: &logv1.LoggedCommand_Move{Move: &logv1.Move{Direction: dir}}}
}

// Bind is a BindCharacter for actor, named name, spawning in room of zone
// when never bound (AW-SRV-014).
func Bind(zone, actor, name, room string) *logv1.LoggedCommand {
	return &logv1.LoggedCommand{ZoneId: zone, ActorId: actor, SessionId: "s-" + actor, Command: &logv1.LoggedCommand_BindCharacter{BindCharacter: &logv1.BindCharacter{
		CharacterId: actor, AccountId: "acct-" + actor, Name: name, SpawnRoomId: room,
	}}}
}

// Unbind is an UnbindCharacter{QUIT} for actor in zone.
func Unbind(zone, actor string) *logv1.LoggedCommand {
	return &logv1.LoggedCommand{ZoneId: zone, ActorId: actor, SessionId: "s-" + actor, Command: &logv1.LoggedCommand_UnbindCharacter{UnbindCharacter: &logv1.UnbindCharacter{
		CharacterId: actor, Reason: logv1.UnbindReason_QUIT,
	}}}
}

// Script is a deterministic log: n records on each Zone's Partition,
// offsets ascending from 0, cycling look, look, move, move-nowhere.
func Script(n int) map[int32][]sim.Record {
	out := map[int32][]sim.Record{}
	for _, zid := range Zones {
		p := sim.PartitionFor(sim.ZoneID(zid))
		for i := 0; i < n; i++ {
			var cmd *logv1.LoggedCommand
			switch i % 4 {
			case 0, 1:
				cmd = Look(zid, fmt.Sprintf("actor-%d", i%3))
			case 2:
				cmd = Move(zid, "actor-0", fmt.Sprintf("dir-%d", i))
			default:
				cmd = Move(zid, "actor-1", "nowhere")
			}
			out[p] = append(out[p], sim.Record{Partition: p, Offset: int64(i), Command: cmd})
		}
	}
	return out
}

// MemorySource is a RecordSource over an in-memory log.
type MemorySource map[int32][]sim.Record

// Fetch implements sim.RecordSource.
func (m MemorySource) Fetch(p int32, from, to int64) ([]sim.Record, error) {
	recs := m[p]
	if from < 0 || to > int64(len(recs)) || from > to {
		return nil, fmt.Errorf("range [%d,%d) outside partition %d (%d records)", from, to, p, len(recs))
	}
	return recs[from:to], nil
}

// Batch takes up to k records from each Partition's remaining script, in
// Partition order — the loop's selection in miniature.
func Batch(remaining map[int32][]sim.Record, k int) sim.TickInput {
	var in sim.TickInput
	parts := make([]int, 0, len(remaining))
	for p := range remaining {
		parts = append(parts, int(p))
	}
	sort.Ints(parts)
	for _, pi := range parts {
		p := int32(pi)
		n := min(k, len(remaining[p]))
		in.Records = append(in.Records, remaining[p][:n]...)
		remaining[p] = remaining[p][n:]
	}
	return in
}

// NewEngine builds an Engine over the fixture with the given seed.
func NewEngine(seed uint64) (*sim.Engine, error) {
	w, err := World()
	if err != nil {
		return nil, err
	}
	reg, err := Templates()
	if err != nil {
		return nil, err
	}
	return sim.NewEngine(w, reg, sim.Config{Seed: seed, Partitions: AllPartitions(), Handlers: Handlers(reg)}), nil
}
