// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package sim

import (
	"bytes"
	"fmt"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	logv1 "github.com/valesordev/andara/gen/go/andara/log/v1"
	statev1 "github.com/valesordev/andara/gen/go/andara/state/v1"
)

// The tripwire (AC-3, as amended): every field of the ZoneState and
// EntityState protos, and of the Components an Entity carries, is corrupted in
// turn in an otherwise valid body, and the body must then either hash
// differently or be refused. A field the hash neither covers nor refuses fails
// here. The fields are read from the proto descriptors, not listed by hand, so
// a field added to zone_state.proto is checked the day it is added — which is
// what keeps "covering every field the body carries" true after the next
// story touches the body.
func TestBodyHashCoversEveryProtoField(t *testing.T) {
	t.Parallel()
	s := fullSnapshot()
	base := s.BodyProto()
	want, err := BodyStateHash(base)
	if err != nil || want != s.StateHash() {
		t.Fatalf("the uncorrupted body: hash %x, err %v; the Snapshot says %x", want, err, s.StateHash())
	}
	check := func(path string, fd protoreflect.FieldDescriptor, pick func(*statev1.ZoneState) protoreflect.Message) {
		t.Helper()
		body := proto.Clone(base).(*statev1.ZoneState)
		if !corrupt(pick(body), fd) {
			t.Errorf("%s: the tripwire cannot corrupt a %s field; teach corrupt() its kind", path, fd.Kind())
			return
		}
		if got, err := BodyStateHash(body); err == nil && got == want {
			t.Errorf("%s: corrupting it leaves the body hash-valid.\n"+
				"Cover it in ZoneCanonicalBytes or the snapshot record (SnapshotCanonicalBytes), or refuse "+
				"a body that carries it in BodyStateHash.", path)
		}
	}
	fields := func(m protoreflect.Message) protoreflect.FieldDescriptors { return m.Descriptor().Fields() }

	zf := fields(base.ProtoReflect())
	for i := 0; i < zf.Len(); i++ {
		fd := zf.Get(i)
		check(string(fd.FullName()), fd, func(b *statev1.ZoneState) protoreflect.Message { return b.ProtoReflect() })
	}
	// Every Entity: the full one and the sparse one, whose zero values take
	// the encoder's omit-when-unset paths.
	for ei, ent := range base.GetEntities() {
		ef := fields(ent.ProtoReflect())
		for i := 0; i < ef.Len(); i++ {
			fd := ef.Get(i)
			check(fmt.Sprintf("%s[%s]", fd.FullName(), ent.GetEntityId()), fd, func(b *statev1.ZoneState) protoreflect.Message {
				return b.GetEntities()[ei].ProtoReflect()
			})
		}
	}
	// A Component and one of its fields, on the full Entity.
	hero := base.GetEntities()[0]
	cf := fields(hero.GetComponents()[0].ProtoReflect())
	for i := 0; i < cf.Len(); i++ {
		fd := cf.Get(i)
		check(string(fd.FullName()), fd, func(b *statev1.ZoneState) protoreflect.Message {
			return b.GetEntities()[0].GetComponents()[0].ProtoReflect()
		})
	}
	ff := fields(hero.GetComponents()[0].GetFields()[0].ProtoReflect())
	for i := 0; i < ff.Len(); i++ {
		fd := ff.Get(i)
		check(string(fd.FullName()), fd, func(b *statev1.ZoneState) protoreflect.Message {
			return b.GetEntities()[0].GetComponents()[0].GetFields()[0].ProtoReflect()
		})
	}
}

// corrupt changes fd's value in m to a different valid one, and reports
// whether it knew how. A list gains an element; a scalar moves by one.
func corrupt(m protoreflect.Message, fd protoreflect.FieldDescriptor) bool {
	switch {
	case fd.IsMap():
		return false
	case fd.IsList():
		l := m.Mutable(fd).List()
		l.Append(l.NewElement())
		return true
	}
	v := m.Get(fd)
	switch fd.Kind() {
	case protoreflect.BoolKind:
		m.Set(fd, protoreflect.ValueOfBool(!v.Bool()))
	case protoreflect.StringKind:
		m.Set(fd, protoreflect.ValueOfString(v.String()+"x"))
	case protoreflect.BytesKind:
		b := append([]byte(nil), v.Bytes()...)
		if len(b) == 0 {
			b = []byte{1}
		} else {
			b[len(b)-1] ^= 1
		}
		m.Set(fd, protoreflect.ValueOfBytes(b))
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		m.Set(fd, protoreflect.ValueOfUint64(v.Uint()+1))
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		m.Set(fd, protoreflect.ValueOfUint32(uint32(v.Uint())+1))
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		m.Set(fd, protoreflect.ValueOfInt64(v.Int()+1))
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		m.Set(fd, protoreflect.ValueOfInt32(int32(v.Int())+1))
	case protoreflect.EnumKind:
		m.Set(fd, protoreflect.ValueOfEnum(v.Enum()+1))
	default:
		return false
	}
	return true
}

// The three values the snapshot record adds, named: each one corrupted alone
// changes the hash. Against HashZone, the Zone-only hash the envelope carried
// before AC-3 was amended, all three pass unnoticed — which is the gap.
func TestBodyHashCoversTickPRNGAndNextEventID(t *testing.T) {
	t.Parallel()
	s := fullSnapshot()
	want := s.StateHash()
	for name, edit := range map[string]func(*statev1.ZoneState){
		"tick":          func(b *statev1.ZoneState) { b.Tick++ },
		"prng_state":    func(b *statev1.ZoneState) { b.PrngState[0] ^= 0x80 },
		"next_event_id": func(b *statev1.ZoneState) { b.NextEventId++ },
	} {
		body := s.BodyProto()
		edit(body)
		got, err := BodyStateHash(body)
		if err != nil || got == want {
			t.Errorf("%s: hash %x, err %v; want a different hash", name, got, err)
		}
		if HashZone(ZoneStateFromProto(body)) != HashZone(s.Body()) {
			t.Errorf("%s: the Zone-only hash moved, so this case no longer shows the gap", name)
		}
	}
}

// The values the hash does not cover are refused, not ignored.
func TestBodyHashRefusesWhatItDoesNotCover(t *testing.T) {
	t.Parallel()
	for name, edit := range map[string]func(*statev1.ZoneState){
		"deferred":                   func(b *statev1.ZoneState) { b.Deferred = append(b.Deferred, &logv1.LoggedCommand{ZoneId: "village"}) },
		"linkdead_deadline_tick":     func(b *statev1.ZoneState) { b.Entities[0].LinkdeadDeadlineTick = 9 },
		"dormant_since, not dormant": func(b *statev1.ZoneState) { b.Entities[1].DormantSinceTick = 9 },
	} {
		body := fullSnapshot().BodyProto()
		edit(body)
		if _, err := BodyStateHash(body); err == nil {
			t.Errorf("%s: a body carrying it was hashed", name)
		}
	}
}

// fullZone is a Zone in which every field the State Hash covers is non-zero,
// including all three ComponentField kinds. A round trip over a fixture with
// zero values proves almost nothing: a body that dropped a field would still
// compare equal.
func fullZone() *ZoneState {
	return &ZoneState{
		ID:          "village",
		Faulted:     true,
		FaultedTick: 4200,
		Entities: map[EntityID]*EntityState{
			"hero": {
				ID:             "hero",
				Room:           "square",
				Template:       "andara.core.Character",
				ContentVersion: "core@3",
				Name:           "Hero of the Vale",
				Dormant:        true,
				DormantSince:   4100,
				Components: []Component{
					{Type: "andara.core.Behavior", Fields: []ComponentField{
						{Name: "flag", Kind: FieldBool, Bool: true},
						{Name: "name", Kind: FieldString, Str: "town.merchant"},
						{Name: "weight", Kind: FieldInt, Int: -17},
					}},
					{Type: "andara.core.Memory", Fields: []ComponentField{
						{Name: "slots", Kind: FieldInt, Int: 16},
					}},
				},
			},
			// A second Entity, nowhere and unnamed, so the encoder's
			// omit-when-unset paths are exercised alongside the full one.
			"pebble": {ID: "pebble", Template: "andara.core.Entity", ContentVersion: "core@3"},
		},
	}
}

func fullSnapshot() *Snapshot {
	return &Snapshot{
		Zone:            "village",
		Tick:            4200,
		StateVersion:    StateVersion,
		Offsets:         []PartitionOffset{{Partition: 3, Offset: 91}, {Partition: 7, Offset: 12}},
		TakenAtUnixNano: 1758500000000000000,
		body:            fullZone(),
		prng:            [4]uint64{1, 2, 3, ^uint64(0)},
		nextEventID:     777,
	}
}

// The round trip that matters: a body decoded and re-encoded produces the same
// canonical bytes the original Zone did, so its State Hash is the same.
func TestZoneStateRoundTripsThroughTheBody(t *testing.T) {
	t.Parallel()
	s := fullSnapshot()
	want := ZoneCanonicalBytes(s.Body())

	wire, err := proto.Marshal(s.BodyProto())
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var back statev1.ZoneState
	if err := proto.Unmarshal(wire, &back); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	got := ZoneCanonicalBytes(ZoneStateFromProto(&back))
	if !bytes.Equal(want, got) {
		t.Fatalf("a Zone did not survive the body round trip:\n want %q\n got  %q", want, got)
	}
	// The hash is a function of those bytes, so equal bytes are the whole
	// claim — but assert it against the decoded Zone anyway, because that is
	// the comparison AW-SRV-007 actually makes against the envelope.
	if got, err := BodyStateHash(&back); err != nil || got != s.StateHash() {
		t.Fatalf("the decoded body hashed %x (%v), the Snapshot %x", got, err, s.StateHash())
	}
}

// AC-2: identical Zone state, snapshotted twice, is byte-identical.
func TestEncodeIsByteIdenticalForIdenticalState(t *testing.T) {
	t.Parallel()
	a, err := fullSnapshot().Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	b, err := fullSnapshot().Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("two encodings of identical state differ")
	}
	// And encoding the same Snapshot twice, which is the case where a
	// timestamp read at Encode rather than at the boundary would show up.
	s := fullSnapshot()
	c, err := s.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	d, err := s.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if !bytes.Equal(c, d) {
		t.Fatal("encoding one Snapshot twice produced different bytes")
	}
}

// Component order is re-established by the encoder, not trusted, so an
// unsorted set encodes the same as a sorted one.
func TestEncodeSortsUnsortedInput(t *testing.T) {
	t.Parallel()
	sorted := fullSnapshot()
	unsorted := fullSnapshot()
	e := unsorted.body.Entities["hero"]
	e.Components[0], e.Components[1] = e.Components[1], e.Components[0]
	f := e.Components[1].Fields
	f[0], f[2] = f[2], f[0]

	a, err := sorted.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	b, err := unsorted.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("an unsorted Component set encoded differently from a sorted one")
	}
}

// AC-3: the envelope names state_version, tick, zone_id, the per-Partition
// offsets, and the Zone's hash.
func TestEnvelopeNamesEverythingRecoveryNeeds(t *testing.T) {
	t.Parallel()
	s := fullSnapshot()
	b, err := s.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	var env statev1.SnapshotEnvelope
	if err := proto.Unmarshal(b, &env); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if env.GetStateVersion() != StateVersion {
		t.Errorf("state_version = %d, want %d", env.GetStateVersion(), StateVersion)
	}
	if env.GetTick() != 4200 {
		t.Errorf("tick = %d, want 4200", env.GetTick())
	}
	if env.GetZoneId() != "village" {
		t.Errorf("zone_id = %q, want village", env.GetZoneId())
	}
	if env.GetTakenAtUnixNano() != s.TakenAtUnixNano {
		t.Errorf("taken_at_unix_nano = %d, want %d", env.GetTakenAtUnixNano(), s.TakenAtUnixNano)
	}
	hash := s.StateHash()
	if !bytes.Equal(env.GetStateHash(), hash[:]) {
		t.Errorf("state_hash = %x, want the Zone's hash %x", env.GetStateHash(), hash)
	}
	offs := env.GetOffsets()
	if len(offs) != 2 {
		t.Fatalf("offsets = %v, want 2", offs)
	}
	for i := 1; i < len(offs); i++ {
		if offs[i-1].GetPartition() >= offs[i].GetPartition() {
			t.Fatalf("offsets are not sorted by partition: %v", offs)
		}
	}

	// And the body decodes to the Zone, with the process-wide values on it.
	var body statev1.ZoneState
	if err := proto.Unmarshal(env.GetBody(), &body); err != nil {
		t.Fatalf("Unmarshal body: %v", err)
	}
	if body.GetNextEventId() != 777 {
		t.Errorf("next_event_id = %d, want 777", body.GetNextEventId())
	}
	prng, err := DecodePRNG(body.GetPrngState())
	if err != nil {
		t.Fatalf("DecodePRNG: %v", err)
	}
	if prng != s.PRNG() {
		t.Errorf("prng_state = %v, want %v", prng, s.PRNG())
	}
	if len(body.GetDeferred()) != 0 {
		t.Errorf("deferred = %v, want empty (see feedback §3)", body.GetDeferred())
	}
}

// A generator restored from the wrong number of bytes would replay a different
// sequence, so the width is checked rather than truncated or padded.
func TestDecodePRNGRefusesAWrongWidth(t *testing.T) {
	t.Parallel()
	for _, b := range [][]byte{nil, make([]byte, 31), make([]byte, 33)} {
		if _, err := DecodePRNG(b); err == nil {
			t.Fatalf("DecodePRNG(%d bytes) succeeded, want an error", len(b))
		}
	}
	round, err := DecodePRNG(encodePRNG([4]uint64{9, 8, 7, 6}))
	if err != nil {
		t.Fatalf("DecodePRNG: %v", err)
	}
	if round != [4]uint64{9, 8, 7, 6} {
		t.Fatalf("PRNG round trip = %v", round)
	}
}
