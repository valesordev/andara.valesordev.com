// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

//go:build integration

// AW-SRV-006's broker-backed test: the SnapshotWritten round trip against a
// throwaway Redpanda topic (`make up`, then `make test-integration`).
package tickloop

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"

	logv1 "github.com/valesordev/andara/gen/go/andara/log/v1"
	statev1 "github.com/valesordev/andara/gen/go/andara/state/v1"
	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/simtest"
	"github.com/valesordev/andara/server/store"
)

// AC-7: a Zone with a SnapshotWritten record on andara.events.v1 has a key
// that resolves to an object whose envelope hash matches the record's.
//
// Against a real broker rather than the in-process fake, because what is being
// asserted here is the framing: the record goes to the Zone's Partition under
// its own key, it survives protobuf on the wire, and a consumer reading the
// events topic can tell it apart from an Event and from a TickCompleted.
func TestSnapshotWrittenRoundTripsThroughTheBroker(t *testing.T) {
	bs := brokers(t)
	_, events := topics(t, bs)

	pub, err := NewKafkaPublisher(context.Background(), bs, "snapshot-test")
	if err != nil {
		t.Fatal(err)
	}
	defer pub.Close()
	pub.Events = events

	dir := t.TempDir()
	ws := store.NewFS(dir)
	done := make(chan error, 1)
	now := time.Unix(1758500000, 0)
	snapshotter, err := NewSnapshotter(SnapshotOptions{
		Store:         ws,
		Interval:      time.Second,
		MaxStall:      5 * time.Millisecond,
		UploadTimeout: 30 * time.Second,
		Manifest:      pub,
		Now:           func() time.Time { return now },
		OnRound:       func(_ sim.Tick, err error) { done <- err },
	})
	if err != nil {
		t.Fatal(err)
	}

	e, err := simtest.NewEngine(1)
	if err != nil {
		t.Fatal(err)
	}
	simtest.Place(e, "hero", "town", "plaza")
	for e.Tick() < 42 {
		if _, err := e.Step(sim.TickInput{}); err != nil {
			t.Fatal(err)
		}
	}

	now = now.Add(time.Second)
	snapshotter.Maybe(context.Background(), e)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("round: %v", err)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("the snapshot round did not finish")
	}
	if err := pub.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}

	records := readSnapshotWritten(t, bs, events, len(simtest.Zones))
	if len(records) != len(simtest.Zones) {
		t.Fatalf("read %d SnapshotWritten records, want one per Zone (%d)", len(records), len(simtest.Zones))
	}
	for _, r := range records {
		// The key the record names resolves in the store.
		b, err := ws.Get(context.Background(), r.GetKey())
		if err != nil {
			t.Fatalf("zone %s: key %q does not resolve: %v", r.GetZoneId(), r.GetKey(), err)
		}
		env, _, err := store.Decode(b)
		if err != nil {
			t.Fatalf("zone %s: Decode: %v", r.GetZoneId(), err)
		}
		// And the envelope's hash is the record's.
		if !bytes.Equal(env.GetStateHash(), r.GetStateHash()) {
			t.Errorf("zone %s: record hash %x, envelope hash %x", r.GetZoneId(), r.GetStateHash(), env.GetStateHash())
		}
		var body statev1.ZoneState
		if err := proto.Unmarshal(env.GetBody(), &body); err != nil {
			t.Fatalf("zone %s: body: %v", r.GetZoneId(), err)
		}
		if got, err := sim.BodyStateHash(&body); err != nil || !bytes.Equal(got[:], r.GetStateHash()) {
			t.Errorf("zone %s: the object's Zone does not hash to what the record claims", r.GetZoneId())
		}
		if r.GetTick() != 42 {
			t.Errorf("zone %s: tick %d, want 42", r.GetZoneId(), r.GetTick())
		}
		if r.GetSizeBytes() != uint64(len(b)) {
			t.Errorf("zone %s: size_bytes %d, object is %d bytes", r.GetZoneId(), r.GetSizeBytes(), len(b))
		}
		// The record went to its Zone's Partition (ADR-0001), which is what
		// lets a per-Zone consumer find it.
		if r.GetStateVersion() != sim.StateVersion {
			t.Errorf("zone %s: state_version %d, want %d", r.GetZoneId(), r.GetStateVersion(), sim.StateVersion)
		}
	}
}

// A manifest must not be mistaken for a boundary record. ReadBoundaries scans
// the boundary Partition and filters by key; a SnapshotWritten that landed on
// that Partition — which it will, for whichever Zone hashes to it — must be
// skipped rather than decoded as a TickCompleted.
func TestSnapshotWrittenIsNotReadAsABoundary(t *testing.T) {
	bs := brokers(t)
	_, events := topics(t, bs)

	pub, err := NewKafkaPublisher(context.Background(), bs, "snapshot-boundary-test")
	if err != nil {
		t.Fatal(err)
	}
	defer pub.Close()
	pub.Events = events

	// A Zone on the boundary Partition, so the manifest lands where
	// ReadBoundaries looks.
	var onBoundary sim.ZoneID
	for i := 0; i < 10000 && onBoundary == ""; i++ {
		id := sim.ZoneID("zone-" + string(rune('a'+i%26)) + string(rune('a'+(i/26)%26)) + string(rune('a'+(i/676)%26)))
		if sim.PartitionFor(id) == BoundaryPartition {
			onBoundary = id
		}
	}
	if onBoundary == "" {
		t.Skip("no Zone id found on the boundary partition")
	}
	rec := &logv1.SnapshotWritten{ZoneId: string(onBoundary), Tick: 7, StateVersion: sim.StateVersion, Key: "k"}
	if err := pub.ProduceSnapshots(context.Background(), []*logv1.SnapshotWritten{rec}); err != nil {
		t.Fatal(err)
	}
	if err := pub.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}

	boundaries, err := ReadBoundaries(context.Background(), bs, events)
	if err != nil {
		t.Fatalf("ReadBoundaries: %v", err)
	}
	if len(boundaries) != 0 {
		t.Fatalf("ReadBoundaries returned %d records; a SnapshotWritten was read as a boundary", len(boundaries))
	}
}

// readSnapshotWritten drains the events topic and returns every
// SnapshotWritten on it, waiting until want of them have arrived.
func readSnapshotWritten(t *testing.T, bs []string, topic string, want int) []*logv1.SnapshotWritten {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cl, err := kgo.NewClient(
		kgo.SeedBrokers(bs...),
		kgo.ConsumeTopics(topic),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()

	var out []*logv1.SnapshotWritten
	for len(out) < want {
		fetches := cl.PollFetches(ctx)
		if err := fetches.Err0(); err != nil {
			t.Fatalf("poll: %v (got %d of %d records)", err, len(out), want)
		}
		fetches.EachRecord(func(r *kgo.Record) {
			if string(r.Key) != SnapshotKey {
				return
			}
			var rec logv1.SnapshotWritten
			if err := proto.Unmarshal(r.Value, &rec); err != nil {
				t.Errorf("a record under the %q key did not decode as SnapshotWritten: %v", SnapshotKey, err)
				return
			}
			out = append(out, &rec)
		})
	}
	return out
}

// AC-8 against a real broker: the round waits for the TickCompleted it copied
// at to be acknowledged, through the same callback the boot wires, and then
// completes.
func TestSnapshotRoundWaitsForTheBrokersBoundaryAck(t *testing.T) {
	bs := brokers(t)
	_, events := topics(t, bs)

	pub, err := NewKafkaPublisher(context.Background(), bs, "snapshot-ack-test")
	if err != nil {
		t.Fatal(err)
	}
	defer pub.Close()
	pub.Events = events

	done := make(chan error, 1)
	now := time.Unix(1758500000, 0)
	snapshotter, err := NewSnapshotter(SnapshotOptions{
		Store:            store.NewFS(t.TempDir()),
		Interval:         time.Second,
		MaxStall:         5 * time.Millisecond,
		UploadTimeout:    30 * time.Second,
		AwaitBoundaryAck: true,
		Now:              func() time.Time { return now },
		OnRound:          func(_ sim.Tick, err error) { done <- err },
	})
	if err != nil {
		t.Fatal(err)
	}
	acked := make(chan sim.Tick, 64)
	pub.OnBoundaryAcked = func(tick sim.Tick, _ time.Duration) {
		snapshotter.OnBoundaryAcked(tick)
		acked <- tick
	}
	pub.OnBoundaryLost = snapshotter.OnBoundaryLost

	e, err := simtest.NewEngine(1)
	if err != nil {
		t.Fatal(err)
	}
	var last sim.TickCompleted
	for e.Tick() < 42 {
		res, err := e.Step(sim.TickInput{})
		if err != nil {
			t.Fatal(err)
		}
		last = res.Completed
	}
	// The round starts first, so it has to wait for the acknowledgement
	// rather than find it already recorded.
	now = now.Add(time.Second)
	snapshotter.Maybe(context.Background(), e)
	if err := pub.Publish(context.Background(), nil, last); err != nil {
		t.Fatalf("publish the boundary: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("round: %v", err)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("the snapshot round did not finish after its boundary was published")
	}
	select {
	case tick := <-acked:
		if tick != 42 {
			t.Fatalf("acknowledged tick %d, want 42", tick)
		}
	default:
		t.Fatal("the round completed without the broker acknowledging its boundary")
	}
	if got := counter(snapshotter.Metrics().Rounds.WithLabelValues("complete")); got != 1 {
		t.Fatalf("rounds_total{complete} = %v, want 1", got)
	}
}
