// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package tickloop

import (
	"bytes"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	gamev1 "github.com/valesordev/andara/gen/go/andara/game/v1"
	logv1 "github.com/valesordev/andara/gen/go/andara/log/v1"
	"github.com/valesordev/andara/server/sim"
)

// AC-3: an Event serialized twice is byte-identical, and the record
// carries the sim's Scope.
func TestEventRecord_Canonical(t *testing.T) {
	ev := sim.Event{ID: 7, Tick: 3, Zone: "town", Type: sim.EvCharacterArrived,
		Scope: sim.ScopeRoom("town", "hall").With("zed", "alice"),
		Envelope: &gamev1.EventEnvelope{EventId: 7, Tick: 3, ClientRef: "c", Payload: &gamev1.EventEnvelope_CharacterArrived{
			CharacterArrived: &gamev1.CharacterArrived{ZoneId: "town", RoomId: "hall", CharacterName: "alice", FromDirection: "south"}}}}
	a, err := EventRecord(ev)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := EventRecord(ev)
	ab, _ := canonical.Marshal(a)
	bb, _ := canonical.Marshal(b)
	if !bytes.Equal(ab, bb) || len(ab) == 0 {
		t.Fatal("two serializations differ")
	}
	if a.GetScope().GetRoomZoneId() != "town" || a.GetScope().GetRoomId() != "hall" || len(a.GetScope().GetEntityIds()) != 2 || a.GetScope().GetEntityIds()[0] != "alice" {
		t.Fatalf("scope = %v", a.GetScope())
	}
	if a.GetSchemaVersion() != EventSchemaVersion || a.GetEventId() != 7 || a.GetTick() != 3 {
		t.Fatalf("record = %v", a)
	}
	if got := sim.ScopeFromProto(a.GetScope()); got.Room != ev.Scope.Room || len(got.Entities) != 2 {
		t.Fatalf("round trip = %+v", got)
	}
}

// ADR-0007 rule 3, mechanically: nothing in andara.log.v1 or the Event
// payloads it carries has a map field, so Deterministic marshaling is
// canonical.
func TestLogSchema_NoMapFields(t *testing.T) {
	var walk func(md protoreflect.MessageDescriptor, seen map[string]bool)
	walk = func(md protoreflect.MessageDescriptor, seen map[string]bool) {
		if seen[string(md.FullName())] {
			return
		}
		seen[string(md.FullName())] = true
		fields := md.Fields()
		for i := 0; i < fields.Len(); i++ {
			fd := fields.Get(i)
			if fd.IsMap() {
				t.Errorf("%s.%s is a map field; map ordering is unspecified and the log is hashed", md.FullName(), fd.Name())
			}
			if fd.Kind() == protoreflect.FloatKind || fd.Kind() == protoreflect.DoubleKind {
				t.Errorf("%s.%s is a float field", md.FullName(), fd.Name())
			}
			if fd.Message() != nil {
				walk(fd.Message(), seen)
			}
		}
	}
	seen := map[string]bool{}
	for _, m := range []proto.Message{&logv1.Event{}, &logv1.LoggedCommand{}, &logv1.TickCompleted{}, &gamev1.EventEnvelope{}} {
		walk(m.ProtoReflect().Descriptor(), seen)
	}
}

// AC-4: a record with a field this binary does not know parses, the field
// is ignored, and schema_version says what wrote it.
func TestEventRecord_UnknownFieldTolerated(t *testing.T) {
	rec := &logv1.Event{EventId: 1, Tick: 1, ZoneId: "z", SchemaVersion: EventSchemaVersion + 1, Scope: &logv1.Scope{World: true}}
	body, err := canonical.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	// Field 99, a string a later schema might add.
	body = protowire.AppendTag(body, 99, protowire.BytesType)
	body = protowire.AppendString(body, "from the future")
	var got logv1.Event
	if err := proto.Unmarshal(body, &got); err != nil {
		t.Fatalf("an unknown field failed the parse: %v", err)
	}
	if got.GetEventId() != 1 || got.GetSchemaVersion() != EventSchemaVersion+1 || !got.GetScope().GetWorld() {
		t.Fatalf("got %v", &got)
	}
	if len(got.ProtoReflect().GetUnknown()) == 0 {
		t.Fatal("the unknown field was not preserved")
	}
}
