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
	}
	for _, po := range s.Offsets {
		env.Offsets = append(env.Offsets, &logv1.PartitionOffset{Partition: po.Partition, Offset: po.Offset})
	}
	sort.Slice(env.Offsets, func(i, j int) bool { return env.Offsets[i].GetPartition() < env.Offsets[j].GetPartition() })
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
		e := &EntityState{
			ID:             EntityID(ep.GetEntityId()),
			Room:           RoomID(ep.GetRoomId()),
			Template:       TemplateRef(ep.GetTemplate()),
			ContentVersion: ep.GetContentVersion(),
			Name:           ep.GetName(),
			Dormant:        ep.GetDormant(),
			DormantSince:   Tick(ep.GetDormantSinceTick()),
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
		z.Entities[e.ID] = e
	}
	return z
}
