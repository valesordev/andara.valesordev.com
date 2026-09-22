// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package sim

import (
	"bytes"
	"reflect"
	"testing"

	statev1 "github.com/valesordev/andara/gen/go/andara/state/v1"
	"google.golang.org/protobuf/proto"
)

// entityStateFields is how many fields sim.EntityState has. The snapshot body
// must carry every one of them that the State Hash covers, and this number is
// the tripwire: adding a field to the Go struct without adding it to
// zone_state.proto and to the round trip below fails here, loudly, instead of
// failing months later as an unrecoverable World.
//
// That is not hypothetical. The story's contract sketch was written before
// AW-SRV-014 and AW-SRV-022 landed and omits Name, Template, and
// ContentVersion — all three of which EntityCanonicalBytes hashes. See
// docs/feedback/AW-SRV-006-zone-snapshots.md §1.
const entityStateFields = 8

// zoneStateFields is the same tripwire for ZoneState.
const zoneStateFields = 4

func TestSnapshotBodyCoversEveryStateField(t *testing.T) {
	t.Parallel()
	if got := reflect.TypeOf(EntityState{}).NumField(); got != entityStateFields {
		t.Fatalf("sim.EntityState has %d fields, the snapshot body was written for %d.\n"+
			"A field the State Hash covers but the body omits cannot be restored, and it surfaces as "+
			"AW-SRV-007 exiting on a hash mismatch rather than as anything pointing here.\n"+
			"Add it to andara/state/v1/zone_state.proto, to BodyProto and ZoneStateFromProto, to the "+
			"round-trip fixture below, and then update this count.", got, entityStateFields)
	}
	if got := reflect.TypeOf(ZoneState{}).NumField(); got != zoneStateFields {
		t.Fatalf("sim.ZoneState has %d fields, the snapshot body was written for %d (see above)", got, zoneStateFields)
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
	if HashZone(ZoneStateFromProto(&back)) != HashZone(s.Body()) {
		t.Fatal("the decoded Zone hashed differently from the original")
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
