// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

// Package projector is the state projector (AW-SRV-019): a replica of the
// simulation that replays the same Commands at the same recorded boundaries
// the live server applied, verifies its State Hash against every
// TickCompleted, and renders the aggregates each tick touched as
// andara.state.v1 records.
//
// It is a replica, not a second implementation. It runs sim.Engine through
// sim.Engine.ReplayEach and holds no Apply of its own — a depguard rule and a
// test hold that — so "the indexes describe the World" is a hash comparison,
// not a belief.
package projector

import (
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"

	statev1 "github.com/valesordev/andara/gen/go/andara/state/v1"
	"github.com/valesordev/andara/server/canonical"
	"github.com/valesordev/andara/server/sim"
	"google.golang.org/protobuf/proto"
)

// Out is one andara.state.v1 record, ready to produce. A nil Value is a
// tombstone.
type Out struct {
	Key       string
	Partition int32
	Kind      statev1.AggregateKind
	Tick      sim.Tick
	Value     []byte
}

// Tombstone reports whether the record deletes its key.
func (o Out) Tombstone() bool { return o.Value == nil }

// Options configures a Projector.
type Options struct {
	// ContentVersion names the packID@version a Zone's topology came from,
	// for Room and Zone records (AC-7). Nil, or "", leaves the field empty;
	// an Entity's comes from the Entity itself.
	ContentVersion func(sim.ZoneID) string
	// OnSwaps is told the ContentSwaps each replayed tick applied, before the
	// tick is rendered, so ContentVersion names the new versions in the
	// records the swap's tick produces (AW-SRV-012).
	OnSwaps func([]sim.SwapApplied)
}

// Projector renders a replica Engine's state as records. It is not safe for
// concurrent use; the replay loop owns it.
type Projector struct {
	e    *sim.Engine
	opts Options
	// emitted is the key last written for each Entity, per Zone: what a
	// tombstone deletes once the Entity is gone and its kind can no longer
	// be read from state.
	emitted map[sim.ZoneID]map[sim.EntityID]emittedKey
	// world is the topology last rendered: what a ContentSwap moved the
	// replica away from, so the Rooms it removed can be tombstoned.
	world *sim.World
}

type emittedKey struct {
	key  string
	kind statev1.AggregateKind
}

// New wraps a replica Engine. The index of emitted keys is seeded from the
// Engine's current state, which is correct only when the topic holds exactly
// that state — after a silent replay to the last committed tick. When the
// bootstrap tick is anything else (a round newer than the topic, a rebuild),
// the topic may hold keys for Entities that left while the projector was not
// watching, and the index cannot know them: call Dump and then Reconcile with
// the keys the topic actually holds.
func New(e *sim.Engine, opts Options) *Projector {
	p := &Projector{e: e, opts: opts, emitted: map[sim.ZoneID]map[sim.EntityID]emittedKey{}, world: e.World()}
	st := e.State()
	for _, zid := range st.SortedZoneIDs() {
		for _, ent := range st.Zones[zid].Entities {
			p.remember(zid, ent)
		}
	}
	return p
}

// Engine is the replica.
func (p *Projector) Engine() *sim.Engine { return p.e }

// Dump renders every aggregate in the replica's state: every Zone, every
// Room, every Entity. A rebuild produces it once, at the bootstrap tick; after
// that only Render runs.
func (p *Projector) Dump() ([]Out, error) {
	var out []Out
	for _, zid := range p.e.State().SortedZoneIDs() {
		recs, err := p.dumpZone(zid)
		if err != nil {
			return nil, err
		}
		out = append(out, recs...)
	}
	return out, nil
}

// dumpZone renders one Zone whole: its summary, every Room, every Entity.
func (p *Projector) dumpZone(zid sim.ZoneID) ([]Out, error) {
	zs := p.e.State().Zones[zid]
	rec, err := p.zoneRecord(zid)
	if err != nil {
		return nil, err
	}
	out := []Out{rec}
	if z, ok := p.e.World().Zones[zid]; ok {
		for _, rid := range sortedRooms(z) {
			rec, err := p.roomRecord(zid, rid)
			if err != nil {
				return nil, err
			}
			out = append(out, rec)
		}
	}
	for _, id := range sortedEntities(zs) {
		rec, err := p.entityRecord(zid, zs.Entities[id])
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, nil
}

// Render produces the records for one replayed tick: every aggregate its
// Events touched, and a tombstone for every Entity that left a touched Zone.
// Called after the tick's hash has verified and never before (AC-3).
//
// Output is a pure function of the replica's state and the tick's Events,
// both deterministic, and every record is canonically encoded — which is
// what makes redelivery after a crash produce the same bytes (AC-4).
func (p *Projector) Render(res sim.StepResult) ([]Out, error) {
	st := p.e.State()
	var out []Out
	tombstoned := map[sim.ZoneID]map[sim.EntityID]bool{}
	tomb := func(zid sim.ZoneID, id sim.EntityID) {
		k, ok := p.emitted[zid][id]
		if !ok || tombstoned[zid][id] {
			return
		}
		if tombstoned[zid] == nil {
			tombstoned[zid] = map[sim.EntityID]bool{}
		}
		tombstoned[zid][id] = true
		delete(p.emitted[zid], id)
		out = append(out, Out{Key: k.key, Partition: sim.PartitionFor(zid), Kind: k.kind, Tick: st.Tick})
	}
	touched := Touched(res.Events)
	if len(res.Swaps) > 0 {
		// A ContentSwap changes topology without an Event per Room (AW-SRV-012):
		// every Room the new content removed is tombstoned, and every Zone is
		// rendered whole, so the topic holds the new topology and each record
		// names the content version now in effect.
		out = append(out, p.removedRooms(st.Tick)...)
		for _, zid := range st.SortedZoneIDs() {
			touched = append(touched, Aggregate{Zone: zid, All: true})
		}
		sortAggregates(touched)
	}
	p.world = p.e.World()
	// A Zone touched whole is rendered whole, once; its other aggregates are
	// already in that rendering.
	whole := map[sim.ZoneID]bool{}
	for _, a := range touched {
		if a.All {
			whole[a.Zone] = true
		}
	}
	for _, a := range touched {
		zs, ok := st.Zones[a.Zone]
		if !ok || (whole[a.Zone] && !a.All) {
			continue
		}
		switch {
		case a.All:
			recs, err := p.dumpZone(a.Zone)
			if err != nil {
				return nil, err
			}
			out = append(out, recs...)
			p.sweep(a.Zone, zs, tomb)
		case a.Entity != "":
			ent, present := zs.Entities[a.Entity]
			if !present {
				tomb(a.Zone, a.Entity)
				continue
			}
			rec, err := p.entityRecord(a.Zone, ent)
			if err != nil {
				return nil, err
			}
			out = append(out, rec)
		case a.Room != "":
			rec, err := p.roomRecord(a.Zone, a.Room)
			if err != nil {
				return nil, err
			}
			out = append(out, rec)
		default:
			rec, err := p.zoneRecord(a.Zone)
			if err != nil {
				return nil, err
			}
			out = append(out, rec)
			p.sweep(a.Zone, zs, tomb)
		}
	}
	return out, nil
}

// Reconcile returns a tombstone for every key the topic holds that the
// replica's current state does not produce, on the Partition it was found on,
// sorted by key. onTopic maps each live key on andara.state.v1 to its
// Partition — read from the compacted topic, which costs live-state time.
//
// This is what makes a bootstrap from a round newer than the topic correct
// (Codex review of PR #64): an Entity that left its Zone, or a Room or Zone
// that content removed, while the projector was down has a key on the topic
// and nothing in state, and no later tick will ever name it. Dump followed by
// Reconcile is a rebuild: afterwards the topic holds exactly the replica's
// state.
func (p *Projector) Reconcile(onTopic map[string]int32) []Out {
	st := p.e.State()
	live := map[string]bool{}
	for _, zid := range st.SortedZoneIDs() {
		live[Key(statev1.AggregateKind_ZONE, zid, "")] = true
		if z, ok := p.e.World().Zones[zid]; ok {
			for rid := range z.Rooms {
				live[Key(statev1.AggregateKind_ROOM, zid, string(rid))] = true
			}
		}
		for _, ent := range st.Zones[zid].Entities {
			live[Key(p.KindOf(ent), zid, string(ent.ID))] = true
		}
	}
	stale := make([]string, 0)
	for k := range onTopic {
		if !live[k] {
			stale = append(stale, k)
		}
	}
	sort.Strings(stale)
	out := make([]Out, 0, len(stale))
	for _, k := range stale {
		out = append(out, Out{Key: k, Partition: onTopic[k], Kind: KindOfKey(k), Tick: st.Tick})
	}
	return out
}

// KindOfKey reads the kind a record key's prefix names;
// AGGREGATE_KIND_UNSPECIFIED for a key this projector does not write.
func KindOfKey(key string) statev1.AggregateKind {
	prefix, _, ok := strings.Cut(key, ":")
	if !ok {
		return statev1.AggregateKind_AGGREGATE_KIND_UNSPECIFIED
	}
	switch prefix {
	case "character":
		return statev1.AggregateKind_CHARACTER
	case "npc":
		return statev1.AggregateKind_NPC
	case "item":
		return statev1.AggregateKind_ITEM
	case "room":
		return statev1.AggregateKind_ROOM
	case "zone":
		return statev1.AggregateKind_ZONE
	}
	return statev1.AggregateKind_AGGREGATE_KIND_UNSPECIFIED
}

// sweep tombstones every Entity this projector wrote in zid that the Zone no
// longer holds, whether or not an Event addressed it by ID.
func (p *Projector) sweep(zid sim.ZoneID, zs *sim.ZoneState, tomb func(sim.ZoneID, sim.EntityID)) {
	gone := make([]string, 0)
	for id := range p.emitted[zid] {
		if _, present := zs.Entities[id]; !present {
			gone = append(gone, string(id))
		}
	}
	sort.Strings(gone)
	for _, id := range gone {
		tomb(zid, sim.EntityID(id))
	}
}

// Key is the record key for an Entity of kind in zone.
func Key(kind statev1.AggregateKind, zone sim.ZoneID, id string) string {
	switch kind {
	case statev1.AggregateKind_ZONE:
		return "zone:" + string(zone)
	case statev1.AggregateKind_CHARACTER:
		return "character:" + string(zone) + "/" + id
	case statev1.AggregateKind_NPC:
		return "npc:" + string(zone) + "/" + id
	case statev1.AggregateKind_ITEM:
		return "item:" + string(zone) + "/" + id
	case statev1.AggregateKind_ROOM:
		return "room:" + string(zone) + "/" + id
	}
	return ""
}

// KindOf classifies an Entity: a Character when its chain reaches
// andara.core.Character, an item when its Template's kind is ITEM, and an npc
// otherwise.
func (p *Projector) KindOf(ent *sim.EntityState) statev1.AggregateKind {
	if p.e.IsCharacter(ent) {
		return statev1.AggregateKind_CHARACTER
	}
	if reg := p.e.Templates(); reg != nil {
		if t, ok := reg.Get(ent.Template); ok && t.Kind == sim.KindItem {
			return statev1.AggregateKind_ITEM
		}
	}
	return statev1.AggregateKind_NPC
}

func (p *Projector) remember(zid sim.ZoneID, ent *sim.EntityState) emittedKey {
	kind := p.KindOf(ent)
	k := emittedKey{key: Key(kind, zid, string(ent.ID)), kind: kind}
	if p.emitted[zid] == nil {
		p.emitted[zid] = map[sim.EntityID]emittedKey{}
	}
	p.emitted[zid][ent.ID] = k
	return k
}

func (p *Projector) entityRecord(zid sim.ZoneID, ent *sim.EntityState) (Out, error) {
	k := p.remember(zid, ent)
	return p.record(zid, k.key, k.kind, ent.ContentVersion, ent.StateProto())
}

func (p *Projector) roomRecord(zid sim.ZoneID, rid sim.RoomID) (Out, error) {
	body := &statev1.RoomState{ZoneId: string(zid), RoomId: string(rid)}
	for _, id := range sortedEntities(p.e.State().Zones[zid]) {
		ent := p.e.State().Zones[zid].Entities[id]
		if ent.Room == rid && ent.Present() {
			body.Occupants = append(body.Occupants, string(id))
		}
	}
	return p.record(zid, Key(statev1.AggregateKind_ROOM, zid, string(rid)), statev1.AggregateKind_ROOM, p.contentVersion(zid), body)
}

func (p *Projector) zoneRecord(zid sim.ZoneID) (Out, error) {
	zs := p.e.State().Zones[zid]
	body := &statev1.ZoneSummary{
		ZoneId:      string(zid),
		Entities:    uint32(len(zs.Entities)),
		Faulted:     zs.Faulted,
		FaultedTick: uint64(zs.FaultedTick),
	}
	if z, ok := p.e.World().Zones[zid]; ok {
		body.Rooms = uint32(len(z.Rooms))
	}
	for _, ent := range zs.Entities {
		if p.e.IsCharacter(ent) {
			body.Characters++
		}
	}
	return p.record(zid, Key(statev1.AggregateKind_ZONE, zid, ""), statev1.AggregateKind_ZONE, p.contentVersion(zid), body)
}

// removedRooms is a tombstone for every Room the last rendered topology had
// and the replica's current one does not, sorted by key.
func (p *Projector) removedRooms(tick sim.Tick) []Out {
	var out []Out
	if p.world == nil {
		return nil
	}
	now := p.e.World()
	zids := make([]string, 0, len(p.world.Zones))
	for id := range p.world.Zones {
		zids = append(zids, string(id))
	}
	sort.Strings(zids)
	for _, zs := range zids {
		zid := sim.ZoneID(zs)
		nz := now.Zones[zid]
		for _, rid := range sortedRooms(p.world.Zones[zid]) {
			if nz != nil {
				if _, ok := nz.Rooms[rid]; ok {
					continue
				}
			}
			out = append(out, Out{Key: Key(statev1.AggregateKind_ROOM, zid, string(rid)), Partition: sim.PartitionFor(zid), Kind: statev1.AggregateKind_ROOM, Tick: tick})
		}
	}
	return out
}

func (p *Projector) contentVersion(zid sim.ZoneID) string {
	if p.opts.ContentVersion == nil {
		return ""
	}
	return p.opts.ContentVersion(zid)
}

func (p *Projector) record(zid sim.ZoneID, key string, kind statev1.AggregateKind, contentVersion string, body proto.Message) (Out, error) {
	st := p.e.State()
	b, err := canonical.Marshal(body)
	if err != nil {
		return Out{}, fmt.Errorf("projector: encode %s: %w", key, err)
	}
	digest := sha256.Sum256(b)
	part := sim.PartitionFor(zid)
	source := int64(-1)
	if next, ok := st.Offsets[part]; ok {
		source = next - 1
	}
	rec := &statev1.StateRecord{
		Key:            key,
		Kind:           kind,
		Tick:           uint64(st.Tick),
		SourceOffset:   source,
		ContentVersion: contentVersion,
		StateVersion:   st.Version,
		Digest:         digest[:],
		Body:           b,
	}
	v, err := canonical.Marshal(rec)
	if err != nil {
		return Out{}, fmt.Errorf("projector: encode record %s: %w", key, err)
	}
	return Out{Key: key, Partition: part, Kind: kind, Tick: st.Tick, Value: v}, nil
}

func sortedEntities(zs *sim.ZoneState) []sim.EntityID {
	out := make([]sim.EntityID, 0, len(zs.Entities))
	for id := range zs.Entities {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func sortedRooms(z *sim.Zone) []sim.RoomID {
	out := make([]sim.RoomID, 0, len(z.Rooms))
	for id := range z.Rooms {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
