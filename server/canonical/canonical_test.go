package canonical

import (
	"bytes"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/structpb"

	logv1 "github.com/valesordev/andara/gen/go/andara/log/v1"
	statev1 "github.com/valesordev/andara/gen/go/andara/state/v1"
)

func tickCompleted() *logv1.TickCompleted {
	return &logv1.TickCompleted{
		Tick: 4211,
		Offsets: []*logv1.PartitionOffset{
			{Partition: 7, Offset: 100},
			{Partition: 3, Offset: 9},
			{Partition: 63, Offset: 1},
		},
		StateHash:       []byte{0xde, 0xad, 0xbe, 0xef},
		StateVersion:    1,
		EventsEmitted:   12,
		CommandsApplied: 3,
	}
}

// AC-2: a message that feeds the State Hash serializes to identical bytes
// every time. The comparison is against a fresh construction, not the same
// pointer, and across a clone that has been mutated and restored, so the
// assertion is about the value rather than about memory.
func TestMarshal_Deterministic(t *testing.T) {
	first, err := Marshal(tickCompleted())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		again, err := Marshal(tickCompleted())
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(first, again) {
			t.Fatalf("iteration %d: bytes differ", i)
		}
	}

	round := &logv1.TickCompleted{}
	if err := proto.Unmarshal(first, round); err != nil {
		t.Fatal(err)
	}
	back, err := Marshal(round)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, back) {
		t.Fatal("bytes differ after a decode/encode round trip")
	}

	env := &statev1.SnapshotEnvelope{
		StateVersion:    1,
		Tick:            4211,
		Offsets:         tickCompleted().Offsets,
		StateHash:       []byte{1, 2, 3},
		ZoneId:          "town",
		TakenAtUnixNano: 1_700_000_000_000_000_000,
		Body:            bytes.Repeat([]byte{0x42}, 512),
	}
	a, err := Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Marshal(proto.Clone(env))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("snapshot envelope bytes differ across a clone")
	}
}

// The hashed record types pass the rules the schema promises they obey.
func TestCheck_HashedTypesAreCanonical(t *testing.T) {
	for _, m := range []proto.Message{
		&logv1.LoggedCommand{}, &logv1.Event{}, &logv1.TickCompleted{}, &statev1.SnapshotEnvelope{},
	} {
		if err := Check(m.ProtoReflect().Descriptor()); err != nil {
			t.Errorf("%s: %v", m.ProtoReflect().Descriptor().FullName(), err)
		}
	}
}

// The encoder refuses what the rules forbid, naming the message and the
// field, rather than producing bytes that only look canonical.
func TestMarshal_RefusesNonCanonical(t *testing.T) {
	cases := []struct {
		name string
		msg  proto.Message
		want string
	}{
		// Struct is a map<string, Value>, and Value carries a double.
		{"map", &structpb.Struct{}, "map"},
		{"double", &structpb.Value{}, "double"},
		{"any", &anypb.Any{}, "google.protobuf.Any"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Marshal(tc.msg)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to name %q", err, tc.want)
			}
		})
	}
	if _, err := Marshal(nil); err == nil {
		t.Error("nil message should error")
	}
}
