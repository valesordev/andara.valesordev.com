// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package sim_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	logv1 "github.com/valesordev/andara/gen/go/andara/log/v1"
	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/simtest"
)

func newEngine(t *testing.T, seed uint64) *sim.Engine {
	t.Helper()
	e, err := simtest.NewEngine(seed)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

type recordingSink struct{ events []sim.Event }

func (r *recordingSink) Publish(e sim.Event) { r.events = append(r.events, e) }

// --- RNG -----------------------------------------------------------------

func TestRNG_Golden(t *testing.T) {
	r := sim.NewRNG(42)
	got := make([]uint64, 0, 5)
	for range 5 {
		got = append(got, r.Uint64())
	}
	// Reference values: splitmix64(42) → xoshiro256**, first five outputs,
	// computed independently in Python from Blackman and Vigna's C.
	want := []uint64{1546998764402558742, 6990951692964543102, 12544586762248559009, 17057574109182124193, 18295552978065317476}
	if !slices.Equal(got, want) {
		t.Fatalf("RNG(42) = %v\nwant %v", got, want)
	}
	st := r.State()
	a := r.Intn(7)
	r2 := sim.NewRNG(0)
	r2.Restore(st)
	if b := r2.Intn(7); a != b || a < 0 || a >= 7 {
		t.Fatalf("Intn after restore: %d vs %d", a, b)
	}
	if sim.NewRNG(1).Uint64() == sim.NewRNG(2).Uint64() {
		t.Fatal("seeds 1 and 2 agree")
	}
}

// --- Step ----------------------------------------------------------------

func TestStep_RefusesBadInput(t *testing.T) {
	e := newEngine(t, 1)
	before := e.StateHash()
	p := sim.PartitionFor("town")
	_, err := e.Step(sim.TickInput{Records: []sim.Record{{Partition: p, Offset: 3, Command: simtest.Look("town", "a")}}})
	if !errors.Is(err, sim.ErrOffsetGap) {
		t.Fatalf("gap: %v", err)
	}
	w, _ := simtest.World()
	e2 := sim.NewEngine(w, nil, sim.Config{Seed: 1, Partitions: []int32{p}})
	_, err = e2.Step(sim.TickInput{Records: []sim.Record{{Partition: p + 1, Offset: 0, Command: simtest.Look("docks", "a")}}})
	if !errors.Is(err, sim.ErrUnownedPartition) {
		t.Fatalf("unowned: %v", err)
	}
	if e.StateHash() != before || e.Tick() != 0 {
		t.Fatal("a refused Step moved state")
	}
}

func TestStep_RejectionsAndOffsets(t *testing.T) {
	e := newEngine(t, 1)
	p := sim.PartitionFor("town")
	res, err := e.Step(sim.TickInput{Records: []sim.Record{
		{Partition: p, Offset: 0, Command: simtest.Look("town", "a")},
		{Partition: p, Offset: 1, Command: simtest.Move("town", "a", "nowhere")},
		{Partition: p, Offset: 2, Command: simtest.Look("nowhere-zone", "a")},
		{Partition: p, Offset: 3, Command: &logv1.LoggedCommand{ZoneId: "town", ActorId: "a"}}, // empty oneof
	}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Tick != 1 || e.Tick() != 1 || res.Completed.CommandsApplied != 4 || res.Completed.Offsets[p] != 4 {
		t.Fatalf("result %+v", res.Completed)
	}
	types := make([]sim.EventType, len(res.Events))
	var codes []string
	for i, ev := range res.Events {
		types[i] = ev.Type
		if ev.Type == sim.EvCommandRejected {
			codes = append(codes, ev.Envelope.GetCommandRejected().GetCode())
		}
	}
	if !slices.Equal(types, []sim.EventType{sim.EvRoomDescribed, sim.EvCommandRejected, sim.EvCommandRejected, sim.EvCommandRejected}) {
		t.Fatalf("types %v", types)
	}
	if !slices.Equal(codes, []string{"no_such_exit", "unknown_zone", "unsupported_command"}) {
		t.Fatalf("codes %v", codes)
	}
	for i, ev := range res.Events {
		if ev.ID != uint64(i+1) || ev.Envelope.GetEventId() != ev.ID || ev.Envelope.GetTick() != 1 {
			t.Errorf("event %d: %+v", i, ev)
		}
	}
	if res.Events[0].Envelope.GetClientRef() != "ref-a" {
		t.Error("client_ref not echoed")
	}
	if res.Completed.EventsEmitted != 4 || res.Completed.StateVersion != sim.StateVersion || res.Completed.StateHash != e.StateHash() {
		t.Errorf("completed %+v", res.Completed)
	}
	if len(res.Completed.Offsets) != int(sim.PartitionCount) {
		t.Errorf("boundary names %d partitions", len(res.Completed.Offsets))
	}
	back := sim.TickCompletedFromProto(res.Completed.Proto())
	if back.Tick != 1 || back.StateHash != res.Completed.StateHash || back.Offsets[p] != 4 || len(back.Offsets) != int(sim.PartitionCount) {
		t.Errorf("proto round trip %+v", back)
	}
}

// AC-11: a cross-Zone effect is produced, never applied in place.
func TestStep_CrossZoneIsProduced(t *testing.T) {
	e := newEngine(t, 1)
	p := sim.PartitionFor("town")
	res, err := e.Step(sim.TickInput{Records: []sim.Record{{Partition: p, Offset: 0, Command: simtest.Move("town", "a", "elsewhere")}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Outbound) != 1 || res.Outbound[0].GetZoneId() != "docks" {
		t.Fatalf("outbound %v", res.Outbound)
	}
	if len(e.State().Zones["docks"].Entities) != 0 {
		t.Fatal("the docks changed on the same tick: the effect was applied, not produced")
	}
}

// AC-12: a panic inside one Zone is contained, the Zone is marked faulted
// with an Event, its Partition freezes, and other Zones keep ticking.
func TestStep_ZoneFaultIsContained(t *testing.T) {
	town, docks := sim.PartitionFor("town"), sim.PartitionFor("docks")
	input := sim.TickInput{Records: []sim.Record{
		{Partition: town, Offset: 0, Command: simtest.Look("town", "a")},
		{Partition: town, Offset: 1, Command: simtest.Move("town", "a", "panic")},
		{Partition: town, Offset: 2, Command: simtest.Look("town", "b")},
		{Partition: docks, Offset: 0, Command: simtest.Look("docks", "c")},
	}}
	e := newEngine(t, 1)
	sink := &recordingSink{}
	e.Subscribe(sink)
	res, err := e.Step(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Faults) != 1 || res.Faults[0].Zone != "town" || res.Faults[0].Offset != 1 || !strings.Contains(res.Faults[0].Panic, "blew up") {
		t.Fatalf("faults %+v", res.Faults)
	}
	if len(res.Unapplied) != 2 || res.Unapplied[0].Offset != 1 || res.Unapplied[1].Offset != 2 {
		t.Fatalf("unapplied %+v", res.Unapplied)
	}
	if got := res.Completed.Offsets[town]; got != 1 {
		t.Errorf("town partition offset advanced to %d past the fault", got)
	}
	if got := res.Completed.Offsets[docks]; got != 1 {
		t.Errorf("docks did not advance: %d", got)
	}
	z := e.State().Zones["town"]
	if !z.Faulted || z.FaultedTick != 1 {
		t.Errorf("zone state %+v", z)
	}
	if !slices.Equal(e.FaultedPartitions(), []int32{town}) {
		t.Errorf("faulted partitions %v", e.FaultedPartitions())
	}
	faulted := false
	for _, ev := range sink.events {
		if ev.Type == sim.EvZoneFaulted && ev.Envelope.GetZoneFaulted().GetZoneId() == "town" {
			faulted = true
		}
	}
	if !faulted {
		t.Error("no ZoneFaulted event published")
	}
	if _, err := e.Step(sim.TickInput{Records: []sim.Record{{Partition: town, Offset: 1, Command: simtest.Look("town", "b")}}}); !errors.Is(err, sim.ErrZoneFaulted) {
		t.Fatalf("frozen partition accepted a record: %v", err)
	}
	if _, err := e.Step(sim.TickInput{Records: []sim.Record{{Partition: docks, Offset: 1, Command: simtest.Look("docks", "c")}}}); err != nil {
		t.Fatal(err)
	}
	if e.Tick() != 2 {
		t.Errorf("tick %d", e.Tick())
	}
	// The fault is deterministic: a fresh engine over the same records
	// faults at the same tick with the same hash.
	res2, _ := newEngine(t, 1).Step(input)
	if res2.Completed.StateHash != res.Completed.StateHash {
		t.Error("a replayed fault hashed differently")
	}
}

func TestStop_EmitsAndDoesNotTick(t *testing.T) {
	e := newEngine(t, 1)
	sink := &recordingSink{}
	e.Subscribe(sink)
	ev := e.Stop("draining")
	if ev.Type != sim.EvSimulationStopped || ev.Envelope.GetSimulationStopped().GetReason() != "draining" || ev.ID != 1 {
		t.Fatalf("event %+v", ev)
	}
	if e.Tick() != 0 || len(sink.events) != 1 {
		t.Error("Stop ticked, or did not publish")
	}
}

// --- determinism -----------------------------------------------------------

// Different seeds diverge; the same seed agrees; the seed is derived from
// content when unset.
func TestEngine_SeedMatters(t *testing.T) {
	step := func(seed uint64) [32]byte {
		e := newEngine(t, seed)
		rem := simtest.Script(4)
		for range 4 {
			if _, err := e.Step(simtest.Batch(rem, 1)); err != nil {
				t.Fatal(err)
			}
		}
		return e.StateHash()
	}
	first, again := step(1), step(1)
	if first != again {
		t.Fatal("same seed, different hash")
	}
	if first == step(2) {
		t.Fatal("different seeds, same hash")
	}
	w, _ := simtest.World()
	e := sim.NewEngine(w, nil, sim.Config{Partitions: simtest.AllPartitions()})
	if e.State().Seed != sim.DeriveSeed(w) || e.State().Seed == 0 {
		t.Errorf("derived seed %d", e.State().Seed)
	}
}

// AC-2 and AC-5 in-process: replaying from recorded boundaries reproduces
// every hash, whatever batch shape the records were originally applied in
// — the boundaries are read, never re-derived.
func TestEngine_ReplayFromBoundaries(t *testing.T) {
	log := simtest.Script(60)
	src := simtest.MemorySource(log)

	e := newEngine(t, 3)
	remaining := map[int32][]sim.Record{}
	for p, r := range log {
		remaining[p] = r
	}
	var boundaries []sim.TickCompleted
	for tick := 1; tick <= 40; tick++ {
		k := (tick * 7) % 5 // 0..4 records per partition per tick: uneven, like a broker
		res, err := e.Step(simtest.Batch(remaining, k))
		if err != nil {
			t.Fatal(err)
		}
		boundaries = append(boundaries, res.Completed)
	}
	final := e.StateHash()

	r := newEngine(t, 3)
	if err := r.Replay(boundaries, src); err != nil {
		t.Fatal(err)
	}
	if r.StateHash() != final || r.Tick() != 40 {
		t.Fatal("replay diverged")
	}

	bad := newEngine(t, 4)
	if err := bad.Replay(boundaries, src); !errors.Is(err, sim.ErrHashMismatch) || !strings.Contains(err.Error(), "tick 1") {
		t.Fatalf("mismatch: %v", err)
	}
	if err := newEngine(t, 3).Replay(boundaries[1:], src); err == nil {
		t.Fatal("replay accepted boundaries starting at tick 2")
	}
	b0 := boundaries[0]
	b0.StateVersion = 99
	if err := newEngine(t, 3).Replay([]sim.TickCompleted{b0}, src); err == nil || !strings.Contains(err.Error(), "state_version") {
		t.Fatalf("version: %v", err)
	}
	// A log missing records is a gap, never a skip.
	short := simtest.MemorySource{}
	for p, r := range log {
		short[p] = r[:len(r)-1]
	}
	if err := newEngine(t, 3).Replay(boundaries, short); err == nil {
		t.Fatal("replay over a truncated log succeeded")
	}
}

// The hash covers what the story says it covers.
func TestStateHash_Coverage(t *testing.T) {
	w, _ := simtest.World()
	base := func() *sim.WorldState { return sim.NewWorldState(w, 5, []int32{0, 1}) }
	h := base().Hash()
	mutations := map[string]func(*sim.WorldState){
		"tick":     func(s *sim.WorldState) { s.Tick++ },
		"seed":     func(s *sim.WorldState) { s.Seed++ },
		"rng":      func(s *sim.WorldState) { s.RNG.Uint64() },
		"event id": func(s *sim.WorldState) { s.NextEventID++ },
		"offset":   func(s *sim.WorldState) { s.Offsets[1] = 9 },
		"faulted":  func(s *sim.WorldState) { s.Zones["town"].Faulted = true },
		"entity": func(s *sim.WorldState) {
			s.Zones["town"].Entities["x"] = &sim.EntityState{ID: "x", Template: "town.Merchant"}
		},
		"version":       func(s *sim.WorldState) { s.Version++ },
		"new partition": func(s *sim.WorldState) { s.Offsets[2] = 0 },
	}
	for name, m := range mutations {
		s := base()
		m(s)
		if s.Hash() == h {
			t.Errorf("%s is not covered by the hash", name)
		}
	}
	a, b := base(), base()
	a.Zones["town"].Entities["b"] = &sim.EntityState{ID: "b"}
	a.Zones["town"].Entities["a"] = &sim.EntityState{ID: "a"}
	b.Zones["town"].Entities["a"] = &sim.EntityState{ID: "a"}
	b.Zones["town"].Entities["b"] = &sim.EntityState{ID: "b"}
	if a.Hash() != b.Hash() {
		t.Error("insertion order leaked into the hash")
	}
}
