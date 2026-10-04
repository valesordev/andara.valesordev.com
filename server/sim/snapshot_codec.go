// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package sim

import (
	"encoding/binary"
	"fmt"
	"sort"

	contentv1 "github.com/valesordev/andara/gen/go/andara/content/v1"
	logv1 "github.com/valesordev/andara/gen/go/andara/log/v1"
	statev1 "github.com/valesordev/andara/gen/go/andara/state/v1"
	"github.com/valesordev/andara/server/canonical"
)

// PRNGStateBytes is the encoded width of the PRNG: four uint64 words,
// big-endian. Fixed rather than length-prefixed because the generator's shape
// is part of what state_version means — a different generator is a different
// interpretation of state, not a longer field.
const PRNGStateBytes = 32

// Encode serializes the Snapshot as a canonical andara.state.v1.SnapshotEnvelope.
//
// Called off the tick. Deterministic in the strong sense AC-2 needs: encoding
// the same Snapshot twice yields byte-identical output, and so does encoding
// two Snapshots of identical state. Everything that could vary is pinned —
// `repeated` fields are sorted by their key here rather than trusted to be
// sorted already, the timestamp was stamped at the boundary rather than read
// now, and canonical.Marshal refuses a descriptor carrying a map, a float, or
// an Any before it writes a byte.
func (s *Snapshot) Encode() ([]byte, error) {
	body, err := canonical.Marshal(s.BodyProto())
	if err != nil {
		return nil, fmt.Errorf("snapshot: encode body for zone %s: %w", s.Zone, err)
	}
	hash := s.StateHash()
	env := &statev1.SnapshotEnvelope{
		StateVersion:    s.StateVersion,
		Tick:            uint64(s.Tick),
		StateHash:       hash[:],
		ZoneId:          string(s.Zone),
		TakenAtUnixNano: s.TakenAtUnixNano,
		Body:            body,
		SimSeed:         s.simSeed,
	}
	for _, po := range s.Offsets {
		env.Offsets = append(env.Offsets, &logv1.PartitionOffset{Partition: po.Partition, Offset: po.Offset})
	}
	sort.Slice(env.Offsets, func(i, j int) bool { return env.Offsets[i].GetPartition() < env.Offsets[j].GetPartition() })
	// The content in effect at Tick (AW-SRV-012), sorted by pack_id, and the
	// digest it builds. Empty for a World before its first ContentSwap.
	if len(s.content) > 0 {
		for p, v := range s.content {
			env.Content = append(env.Content, &statev1.PackVersion{PackId: p, Version: v})
		}
		sort.Slice(env.Content, func(i, j int) bool { return env.Content[i].GetPackId() < env.Content[j].GetPackId() })
		env.ContentDigest = s.contentDigest[:]
	}
	out, err := canonical.Marshal(env)
	if err != nil {
		return nil, fmt.Errorf("snapshot: encode envelope for zone %s: %w", s.Zone, err)
	}
	return out, nil
}

// BodyProto renders the copied Zone state as the snapshot body.
//
// It carries everything ZoneCanonicalBytes covers, which is the invariant that
// makes a restore reproducible: a field the hash covers but the body omits
// cannot be reconstructed, and AW-SRV-007 exits on the resulting mismatch
// rather than on anything that would point at this function. The round-trip
// test is what holds the two together.
func (s *Snapshot) BodyProto() *statev1.ZoneState {
	z := s.body
	out := &statev1.ZoneState{
		ZoneId:      string(z.ID),
		Tick:        uint64(s.Tick),
		PrngState:   encodePRNG(s.prng),
		NextEventId: s.nextEventID,
		Faulted:     z.Faulted,
		FaultedTick: uint64(z.FaultedTick),
	}
	ids := make([]string, 0, len(z.Entities))
	for id := range z.Entities {
		ids = append(ids, string(id))
	}
	sort.Strings(ids)
	out.Entities = make([]*statev1.EntityState, 0, len(ids))
	for _, id := range ids {
		out.Entities = append(out.Entities, entityStateProto(z.Entities[EntityID(id)]))
	}
	// Entities in transit and the handoff marks, each sorted by Entity ID
	// (AW-SRV-028). Both hashed, so both carried: a restore that dropped them
	// could not reproduce the hash, and recovery would exit 6.
	tids := make([]string, 0, len(z.Transit))
	for id := range z.Transit {
		tids = append(tids, string(id))
	}
	sort.Strings(tids)
	for _, id := range tids {
		r := z.Transit[EntityID(id)]
		out.Transit = append(out.Transit, &statev1.TransitRecord{
			Entity: entityStateProto(&r.Entity), ToZoneId: string(r.To), RoomId: string(r.Room), Direction: string(r.Direction),
		})
	}
	pids := make([]string, 0, len(z.Placed))
	for id := range z.Placed {
		pids = append(pids, string(id))
	}
	sort.Strings(pids)
	for _, id := range pids {
		m := z.Placed[EntityID(id)]
		out.Placed = append(out.Placed, &statev1.PlacedArrival{EntityId: id, HandoffSeq: m.Seq, Rejected: m.Rejected})
	}
	// Deferred is deliberately empty; see zone_state.proto and
	// docs/feedback/AW-SRV-006-zone-snapshots.md §3.
	return out
}

func entityStateProto(e *EntityState) *statev1.EntityState {
	out := &statev1.EntityState{
		EntityId:         string(e.ID),
		RoomId:           string(e.Room),
		Template:         string(e.Template),
		ContentVersion:   e.ContentVersion,
		Name:             e.Name,
		Dormant:          e.Dormant,
		DormantSinceTick: uint64(e.DormantSince),

		LinkdeadSinceTick:      uint64(e.LinkdeadSince),
		LinkdeadDeadlineTick:   uint64(e.LinkdeadDeadline),
		LinkdeadCeilingTick:    uint64(e.LinkdeadCeiling),
		LinkdeadExtensionTicks: uint64(e.LinkdeadExtension),
		HandoffSeq:             e.HandoffSeq,
	}
	// Sorted here rather than trusted: an EntityState assembled by hand in a
	// test never went through the loader, and an encoder that silently
	// depends on its input being sorted is a determinism bug waiting for the
	// first caller who does not know that.
	comps := make([]Component, len(e.Components))
	copy(comps, e.Components)
	sortComponents(comps)
	for _, c := range comps {
		cv := &contentv1.ComponentValue{Type: string(c.Type)}
		for _, f := range c.Fields {
			fd := &contentv1.ComponentField{Name: f.Name}
			switch f.Kind {
			case FieldString:
				fd.Value = &contentv1.ComponentField_StringValue{StringValue: f.Str}
			case FieldInt:
				fd.Value = &contentv1.ComponentField_IntValue{IntValue: f.Int}
			case FieldBool:
				fd.Value = &contentv1.ComponentField_BoolValue{BoolValue: f.Bool}
			}
			cv.Fields = append(cv.Fields, fd)
		}
		out.Components = append(out.Components, cv)
	}
	return out
}

func encodePRNG(s [4]uint64) []byte {
	b := make([]byte, PRNGStateBytes)
	for i, w := range s {
		binary.BigEndian.PutUint64(b[i*8:], w)
	}
	return b
}

// DecodePRNG reads the four words back. An unexpected width is an error rather
// than a partial read: a generator restored from the wrong number of bytes
// would replay a different sequence, silently.
func DecodePRNG(b []byte) ([4]uint64, error) {
	var out [4]uint64
	if len(b) != PRNGStateBytes {
		return out, fmt.Errorf("snapshot: prng_state is %d bytes, want %d", len(b), PRNGStateBytes)
	}
	for i := range out {
		out[i] = binary.BigEndian.Uint64(b[i*8:])
	}
	return out, nil
}

// ZoneStateFromProto rebuilds a Zone's mutable state from a snapshot body. The
// inverse of BodyProto, and the half of the round trip AW-SRV-007 runs.
//
// Components are taken as carried — the process that wrote them validated them
// against the Template when it loaded it — and re-sorted, so a hand-built or
// hand-edited body cannot break the invariant every reader assumes.
func ZoneStateFromProto(p *statev1.ZoneState) *ZoneState {
	z := &ZoneState{
		ID:          ZoneID(p.GetZoneId()),
		Entities:    make(map[EntityID]*EntityState, len(p.GetEntities())),
		Faulted:     p.GetFaulted(),
		FaultedTick: Tick(p.GetFaultedTick()),
	}
	for _, ep := range p.GetEntities() {
		e := entityStateFromProto(ep)
		z.Entities[e.ID] = &e
	}
	for _, tp := range p.GetTransit() {
		if z.Transit == nil {
			z.Transit = make(map[EntityID]TransitRecord, len(p.GetTransit()))
		}
		e := entityStateFromProto(tp.GetEntity())
		z.Transit[e.ID] = TransitRecord{Entity: e, To: ZoneID(tp.GetToZoneId()), Room: RoomID(tp.GetRoomId()), Direction: Direction(tp.GetDirection())}
	}
	for _, pp := range p.GetPlaced() {
		if z.Placed == nil {
			z.Placed = make(map[EntityID]PlacedMark, len(p.GetPlaced()))
		}
		z.Placed[EntityID(pp.GetEntityId())] = PlacedMark{Seq: pp.GetHandoffSeq(), Rejected: pp.GetRejected()}
	}
	return z
}

// entityStateFromProto is the inverse of entityStateProto.
func entityStateFromProto(ep *statev1.EntityState) EntityState {
	e := EntityState{
		ID:             EntityID(ep.GetEntityId()),
		Room:           RoomID(ep.GetRoomId()),
		Template:       TemplateRef(ep.GetTemplate()),
		ContentVersion: ep.GetContentVersion(),
		Name:           ep.GetName(),
		Dormant:        ep.GetDormant(),
		DormantSince:   Tick(ep.GetDormantSinceTick()),

		LinkdeadSince:     Tick(ep.GetLinkdeadSinceTick()),
		LinkdeadDeadline:  Tick(ep.GetLinkdeadDeadlineTick()),
		LinkdeadCeiling:   Tick(ep.GetLinkdeadCeilingTick()),
		LinkdeadExtension: Tick(ep.GetLinkdeadExtensionTicks()),
		HandoffSeq:        ep.GetHandoffSeq(),
	}
	for _, cv := range ep.GetComponents() {
		c := Component{Type: ComponentType(cv.GetType())}
		for _, fd := range cv.GetFields() {
			f, _ := fieldValue(fd)
			c.Fields = append(c.Fields, f)
		}
		e.Components = append(e.Components, c)
	}
	sortComponents(e.Components)
	return e
}

// BodyStateHash is the state_hash of a decoded snapshot body: SnapshotHash over
// the Zone it rebuilds and the tick, PRNG state and next EventID it carries.
// What a reader checks an envelope's claim against (AW-SRV-006 AC-3), and the
// form AW-SRV-007 needs, since a body belongs to no WorldState yet.
//
// A body that carries a value the hash does not cover is refused rather than
// hashed, so no single-field corruption of a stored object leaves it valid:
//   - deferred, which is never written (zone_state.proto; feedback §3)
//   - linkdead_since_tick, linkdead_ceiling_tick or linkdead_extension_ticks
//     with no linkdead_deadline_tick: the linkdead record is written only for
//     a body with a deadline, and the sim never sets one without the others
//   - dormant_since_tick on a body that is not dormant: EntityCanonicalBytes
//     covers it only for a dormant body, and the sim clears it on waking
//
// TestBodyHashCoversEveryProtoField is the tripwire: it corrupts every field of
// ZoneState and EntityState in turn, and fails for any field this function
// neither hashes nor refuses.
func BodyStateHash(p *statev1.ZoneState) ([32]byte, error) {
	if n := len(p.GetDeferred()); n != 0 {
		return [32]byte{}, fmt.Errorf("snapshot: zone %s carries %d deferred commands, a field no snapshot writes", p.GetZoneId(), n)
	}
	for _, e := range p.GetEntities() {
		if e.GetLinkdeadDeadlineTick() == 0 && e.GetLinkdeadSinceTick()|e.GetLinkdeadCeilingTick()|e.GetLinkdeadExtensionTicks() != 0 {
			return [32]byte{}, fmt.Errorf("snapshot: entity %s carries linkdead fields and no linkdead_deadline_tick", e.GetEntityId())
		}
		if !e.GetDormant() && e.GetDormantSinceTick() != 0 {
			return [32]byte{}, fmt.Errorf("snapshot: entity %s carries dormant_since_tick and is not dormant", e.GetEntityId())
		}
	}
	if err := checkHandoffBody(p); err != nil {
		return [32]byte{}, err
	}
	prng, err := DecodePRNG(p.GetPrngState())
	if err != nil {
		return [32]byte{}, err
	}
	return SnapshotHash(ZoneStateFromProto(p), Tick(p.GetTick()), prng, p.GetNextEventId()), nil
}

// StateProto renders one Entity as the snapshot body carries it — the same
// message the state projector's Entity records carry (AW-SRV-019), so an
// index and a snapshot cannot disagree on an Entity's shape.
func (e *EntityState) StateProto() *statev1.EntityState { return entityStateProto(e) }

// checkHandoffBody refuses a body whose transit or placed records the hash
// would read differently from how they were written (AW-SRV-028 AC-13): the
// hash sorts by Entity ID and writes one record per ID, so an unsorted body, a
// duplicated ID, a mark of 0 (a sequence starts at 1), an Entity both here
// and in transit, or a transit Entity that is dormant or linkdead (those
// bodies never move) would hash as a different World from the one the body
// claims.
func checkHandoffBody(p *statev1.ZoneState) error {
	here := make(map[string]uint64, len(p.GetEntities()))
	held := make(map[string]bool, len(p.GetEntities()))
	for _, e := range p.GetEntities() {
		here[e.GetEntityId()], held[e.GetEntityId()] = e.GetHandoffSeq(), true
	}
	prev := ""
	for i, t := range p.GetTransit() {
		e := t.GetEntity()
		id := e.GetEntityId()
		switch {
		case id == "":
			return fmt.Errorf("snapshot: zone %s has a transit record with no entity", p.GetZoneId())
		case i > 0 && id <= prev:
			return fmt.Errorf("snapshot: zone %s transit is not sorted and unique by entity_id at %q", p.GetZoneId(), id)
		case held[id]:
			return fmt.Errorf("snapshot: zone %s holds entity %s both in entities and in transit", p.GetZoneId(), id)
		case e.GetDormant() || e.GetLinkdeadDeadlineTick() != 0:
			return fmt.Errorf("snapshot: zone %s has entity %s in transit and %s: such a body never moves", p.GetZoneId(), id, dormantOrLinkdead(e))
		case e.GetDormantSinceTick() != 0 || e.GetLinkdeadSinceTick()|e.GetLinkdeadCeilingTick()|e.GetLinkdeadExtensionTicks() != 0:
			// Not in the hash for a body that is neither dormant nor linkdead,
			// and the record can't carry them on the wire: refuse them rather
			// than let a corrupted value pass.
			return fmt.Errorf("snapshot: zone %s has entity %s in transit carrying dormant or linkdead fields", p.GetZoneId(), id)
		}
		prev = id
	}
	prev = ""
	for i, m := range p.GetPlaced() {
		id := m.GetEntityId()
		switch {
		case id == "":
			return fmt.Errorf("snapshot: zone %s has a placed mark with no entity", p.GetZoneId())
		case i > 0 && id <= prev:
			return fmt.Errorf("snapshot: zone %s placed is not sorted and unique by entity_id at %q", p.GetZoneId(), id)
		case m.GetHandoffSeq() == 0:
			return fmt.Errorf("snapshot: zone %s has a placed mark of 0 for entity %s", p.GetZoneId(), id)
		case m.GetRejected() && held[id] && here[id] == m.GetHandoffSeq():
			// A rejection places nothing: the Entity was never here at that
			// sequence, so a body that holds it there contradicts itself.
			return fmt.Errorf("snapshot: zone %s has a rejected mark for entity %s at sequence %d and holds it there", p.GetZoneId(), id, m.GetHandoffSeq())
		}
		prev = id
	}
	return nil
}

func dormantOrLinkdead(e *statev1.EntityState) string {
	if e.GetDormant() {
		return "dormant"
	}
	return "linkdead"
}
