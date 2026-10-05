// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

//go:build integration

package projector_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"

	dto "github.com/prometheus/client_model/go"

	"github.com/valesordev/andara/internal/eventually"
	"github.com/valesordev/andara/server/projector"
	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/simtest"
	"github.com/valesordev/andara/server/store"
	"github.com/valesordev/andara/server/tickloop"
)

// defaultScaleTail is the tail a rebuild replays past its round. Short by
// default, because a short tail is what lets the assertion catch a rebuild
// that reads history: at 24 h the scan cost 6.7 s, which a 600-tick tail's
// ~40 s of replay (varying by seconds between runs) would hide under the
// bound (review of #108). ANDARA_AC6_TAIL_TICKS=600, one snapshot interval
// at 10 Hz, is the measurement the story records.
const defaultScaleTail = 10

// AC-6 at the stated scale (§8 item 3): --rebuild of the sizing fixture
// (simtest.SizingEntities, more than the story's 10,000) over H ticks of
// history. ANDARA_AC6_HISTORY_TICKS sets H; 864000 is the story's 24 h at
// 10 Hz; ANDARA_AC6_TAIL_TICKS sets the tail. Unset, the test is skipped: 24 h of boundaries is ~0.5 GB, which the
// shared stack's broker should not carry, so the record says which broker it
// ran on.
//
// The history is filler ahead of the World: a Tick Boundary Record and an
// Event per tick, with the live World restored at tick H behind it. Nothing
// replays it — the round is newer — so its bytes are what matter, not its
// hashes. The same World is mirrored to a second set of topics with no
// history, and the rebuild over H ticks must not cost more than the one over
// none by more than the in-process snapshot load plus tail replay: that
// difference is what a rebuild spends on history it never uses.
func TestRun_RebuildAtScaleIsBoundedByTheRoundAndTheTail(t *testing.T) {
	raw := os.Getenv("ANDARA_AC6_HISTORY_TICKS")
	if raw == "" {
		t.Skip("ANDARA_AC6_HISTORY_TICKS not set; AC-6 at scale is a measurement, run on a throwaway broker")
	}
	history, err := strconv.Atoi(raw)
	if err != nil || history < 1 {
		t.Fatalf("ANDARA_AC6_HISTORY_TICKS=%q: want a positive tick count", raw)
	}

	scaleTail := defaultScaleTail
	if raw := os.Getenv("ANDARA_AC6_TAIL_TICKS"); raw != "" {
		if scaleTail, err = strconv.Atoi(raw); err != nil || scaleTail < 1 {
			t.Fatalf("ANDARA_AC6_TAIL_TICKS=%q: want a positive tick count", raw)
		}
	}

	src, w := scaleWorld(t, sim.Tick(history))
	ws := store.NewFS(t.TempDir())
	for i := 0; i < 4; i++ {
		w.submit(simtest.Bind("z00", fmt.Sprintf("walker%d", i), fmt.Sprintf("Walker %d", i), "r0000"))
	}
	w.tick()
	putRound(t, ws, w)
	round := w.live.Tick()
	dirs := [2]string{"south", "north"}
	for i := 0; i < scaleTail; i++ {
		w.submit(simtest.Move("z00", fmt.Sprintf("walker%d", i%4), dirs[(i/4)%2]))
		w.tick()
	}

	reference := loadAndReplay(t, src, ws, w, round)

	quiet := newBroker(t)
	quiet.mirror(w, 0)
	loud := newBroker(t)
	loud.fill(t, w.boundaries[0], history)
	loud.mirror(w, 0)

	none := timeRebuild(t, quiet, w, src, ws)
	full := timeRebuild(t, loud, w, src, ws)

	t.Logf("AC-6 at scale: %d zones, %d entities, %d state records, round at tick %d, %d-tick tail, %d ticks of history",
		simtest.SizingZones, simtest.SizingEntities, len(dumpOf(t, w)), round, scaleTail, history)
	t.Logf("  in process: snapshot load %s + tail replay %s = %s",
		reference.load.Round(time.Millisecond), reference.replay.Round(time.Millisecond), (reference.load + reference.replay).Round(time.Millisecond))
	t.Logf("  --rebuild, no history:   %s (bootstrap %s, replay %s)", none.total.Round(time.Millisecond), none.bootstrap.Round(time.Millisecond), none.replay.Round(time.Millisecond))
	t.Logf("  --rebuild, %d ticks:  %s (bootstrap %s, replay %s)", history, full.total.Round(time.Millisecond), full.bootstrap.Round(time.Millisecond), full.replay.Round(time.Millisecond))

	if extra := full.total - none.total; extra > reference.load+reference.replay {
		t.Errorf("the history cost the rebuild %s more, past snapshot load + tail replay (%s): it reads history it never replays",
			extra.Round(time.Millisecond), (reference.load + reference.replay).Round(time.Millisecond))
	}
}

// scaleWorld is the sizing fixture brought into effect by a genesis swap and
// then restored at tick at, as a World that has been running that long.
func scaleWorld(t *testing.T, at sim.Tick) (*simtest.FixedContent, *world) {
	t.Helper()
	topo, err := simtest.SizingWorld()
	if err != nil {
		t.Fatal(err)
	}
	reg, err := simtest.Templates()
	if err != nil {
		t.Fatal(err)
	}
	src := &simtest.FixedContent{Topology: sim.Topology{World: topo, Templates: reg}}
	e, err := simtest.SizingEngineWith(seed, src)
	if err != nil {
		t.Fatal(err)
	}
	genesis := newWorldOn(t, e)
	genesis.submit(src.Genesis())
	genesis.tick()

	ws := store.NewFS(t.TempDir())
	putRound(t, ws, genesis)
	_, state, ok, err := store.NewestComplete(context.Background(), ws, e.State().SortedZoneIDs(), nil)
	if err != nil || !ok {
		t.Fatalf("the genesis round: ok %v, %v", ok, err)
	}
	state.Tick = at
	// The round is fabricated at a tick no boundary recorded, so its own
	// restored hash is the one it records: restore once to learn it
	// (AW-SRV-043 requires one).
	state.RecordedHash = make([]byte, 32)
	// The sizing Engine started over its World, not empty as a server does,
	// so it derived another default seed. The fabricated World runs on the
	// server's instead, as it did before rounds recorded a seed.
	state.SimSeed = 0
	var rm *sim.RestoreMismatch
	if _, err := sim.RestoreEngine(topo, reg, sim.Config{Seed: seed, Handlers: sim.Handlers(), Content: src}, state); !errors.As(err, &rm) {
		t.Fatalf("learning the fabricated round's hash: %v", err)
	}
	state.RecordedHash = rm.Restored
	live, err := sim.RestoreEngine(topo, reg, sim.Config{Seed: seed, Handlers: sim.Handlers(), Content: src}, state)
	if err != nil {
		t.Fatal(err)
	}
	w := newWorldOn(t, live)
	w.log = genesis.log // the swap is on the log, behind the offsets the World restored at
	return src, w
}

func putRound(t *testing.T, ws sim.WorldStore, w *world) {
	t.Helper()
	for _, s := range w.live.SnapshotAll(time.Now().UnixNano()) {
		body, err := s.Encode()
		if err != nil {
			t.Fatal(err)
		}
		if err := ws.Put(context.Background(), s.Key(), body); err != nil {
			t.Fatal(err)
		}
	}
}

// fill writes history ticks of filler to the events topic's boundary
// Partition: per tick, a boundary shaped like like and an Event.
func (b *broker) fill(t *testing.T, like sim.TickCompleted, history int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	const batch = 10000
	for from := 1; from <= history; from += batch {
		recs := make([]*kgo.Record, 0, 2*batch)
		for tick := from; tick < from+batch && tick <= history; tick++ {
			like.Tick = sim.Tick(tick)
			body, err := proto.MarshalOptions{Deterministic: true}.Marshal(like.Proto())
			if err != nil {
				t.Fatal(err)
			}
			recs = append(recs,
				&kgo.Record{Topic: b.events, Partition: tickloop.BoundaryPartition, Key: []byte("z00"), Value: body},
				&kgo.Record{Topic: b.events, Partition: tickloop.BoundaryPartition, Key: []byte(tickloop.BoundaryKey), Value: body})
		}
		if err := b.cl.ProduceSync(ctx, recs...).FirstErr(); err != nil {
			t.Fatalf("filler at tick %d: %v", from, err)
		}
	}
}

type rebuildTimes struct{ total, bootstrap, replay time.Duration }

// timeRebuild runs --rebuild from ws over b to the head, checks the topic it
// wrote against the live World, and says how long it took.
func timeRebuild(t *testing.T, b *broker, w *world, src *simtest.FixedContent, ws sim.WorldStore) rebuildTimes {
	t.Helper()
	o := b.options(w)
	o.Content, o.Store, o.Rebuild = src, ws, true
	o.BatchTicks = 100
	o.Poll = 20 * time.Millisecond
	m := projector.NewMetrics(nil)
	o.Metrics = m
	began := time.Now()
	r := start(o)
	eventually.True(t, 30*time.Minute, "the rebuild catches up", r.ready.Load)
	total := time.Since(began)
	b.waitCommitted(t, w.live.Tick())
	r.stop(t)
	sameContent(t, b.topic(t), dumpOf(t, w))
	return rebuildTimes{
		total:     total,
		bootstrap: histogramSum(t, m, "bootstrap"),
		replay:    histogramSum(t, m, "replay"),
	}
}

func histogramSum(t *testing.T, m *projector.Metrics, phase string) time.Duration {
	t.Helper()
	var d dto.Metric
	h, ok := m.RebuildDuration.WithLabelValues(phase).(interface{ Write(*dto.Metric) error })
	if !ok {
		t.Fatal("RebuildDuration is not a histogram")
	}
	if err := h.Write(&d); err != nil {
		t.Fatal(err)
	}
	return time.Duration(d.GetHistogram().GetSampleSum() * float64(time.Second))
}

type referenceTimes struct{ load, replay time.Duration }

// loadAndReplay is the cost AC-6 bounds, in process: load the round from ws
// and replay the tail past it, with no broker in the way.
func loadAndReplay(t *testing.T, src *simtest.FixedContent, ws sim.WorldStore, w *world, round sim.Tick) referenceTimes {
	t.Helper()
	began := time.Now()
	_, state, ok, err := store.NewestComplete(context.Background(), ws, w.live.State().SortedZoneIDs(), nil)
	if err != nil || !ok {
		t.Fatalf("the round: ok %v, %v", ok, err)
	}
	for _, b := range w.boundaries {
		if b.Tick == state.Tick {
			state.RecordedHash = b.StateHash[:]
		}
	}
	eng, err := sim.RestoreEngine(src.Topology.World, src.Topology.Templates, sim.Config{Seed: seed, Handlers: sim.Handlers(), Content: src}, state)
	if err != nil {
		t.Fatal(err)
	}
	load := time.Since(began)

	var tail []sim.TickCompleted
	for _, b := range w.boundaries {
		if b.Tick > round {
			tail = append(tail, b)
		}
	}
	began = time.Now()
	p := projector.New(eng, projector.Options{ContentVersion: func(sim.ZoneID) string { return "fixture@1" }})
	if err := p.Replay(tail, simtest.MemorySource(w.log), func(sim.TickCompleted, []projector.Out) error { return nil }); err != nil {
		t.Fatal(err)
	}
	return referenceTimes{load: load, replay: time.Since(began)}
}

// produceBoundaries writes a boundary per tick in ticks to the events topic's
// boundary Partition, each behind an Event, as a live log interleaves them.
func (b *broker) produceBoundaries(t *testing.T, ticks ...sim.Tick) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var recs []*kgo.Record
	for _, tick := range ticks {
		body, err := proto.Marshal(sim.TickCompleted{Tick: tick, StateVersion: sim.StateVersion}.Proto())
		if err != nil {
			t.Fatal(err)
		}
		recs = append(recs,
			&kgo.Record{Topic: b.events, Partition: tickloop.BoundaryPartition, Key: []byte("z00"), Value: []byte("event")},
			&kgo.Record{Topic: b.events, Partition: tickloop.BoundaryPartition, Key: []byte(tickloop.BoundaryKey), Value: body})
	}
	if err := b.cl.ProduceSync(ctx, recs...).FirstErr(); err != nil {
		t.Fatal(err)
	}
}

// firstBoundary is the tick of the first boundary r delivers.
func firstBoundary(t *testing.T, r *projector.BoundaryReader) sim.Tick {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		got, err := r.Next(context.Background(), 1, 200*time.Millisecond)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) > 0 {
			return got[0].Tick
		}
	}
	t.Fatal("no boundary delivered")
	return 0
}

// AC-6: after a round at tick R, the reader starts at R+1's boundary without
// reading the ones before it, wherever R falls in the Partition.
func TestBoundaryReader_SeekAfterStartsAtTheNextTick(t *testing.T) {
	b := newBroker(t)
	ticks := make([]sim.Tick, 500)
	for i := range ticks {
		ticks[i] = sim.Tick(i + 1)
	}
	b.produceBoundaries(t, ticks...)
	for _, round := range []sim.Tick{1, 2, 137, 250, 499} {
		r, err := projector.NewBoundaryReader(context.Background(), b.brokers, b.events)
		if err != nil {
			t.Fatal(err)
		}
		at, err := r.SeekAfter(context.Background(), round)
		if err != nil {
			t.Fatal(err)
		}
		// Each tick is an Event then its boundary: tick k's boundary is at
		// offset 2k-1, and the reader lands no later than the Event before
		// R+1's boundary.
		if want := int64(2*round) + 1; at > want || at < want-1 {
			t.Errorf("round %d: positioned at offset %d, want %d or just before it", round, at, want)
		}
		if got := firstBoundary(t, r); got != round+1 {
			t.Errorf("round %d: first boundary delivered is tick %d, want %d", round, got, round+1)
		}
		r.Close()
	}
}

// A Partition whose boundaries are out of tick order defeats the search. The
// reader notices from the first boundary it delivers and reads from the
// start, which is where it read from before it could seek.
func TestBoundaryReader_SeekAfterFallsBackToTheStart(t *testing.T) {
	b := newBroker(t)
	b.produceBoundaries(t, 1, 2, 3, 40, 41, 42, 4, 5, 6, 7, 8, 9)
	r, err := projector.NewBoundaryReader(context.Background(), b.brokers, b.events)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if _, err := r.SeekAfter(context.Background(), 3); err != nil { // lands on 40
		t.Fatal(err)
	}
	if got := firstBoundary(t, r); got != 1 {
		t.Fatalf("first boundary delivered is tick %d, want 1: the reader should have gone back to the start", got)
	}
}
