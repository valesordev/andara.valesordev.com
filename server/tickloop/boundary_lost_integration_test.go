// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

//go:build integration

package tickloop

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/simtest"
)

// startLossyLoop recovers from the events topic and builds a loop whose
// publisher reaches the broker through pubBrokers, wired as the server wires
// it (boot.StartTickLoop): acknowledgements release checkpoints, and a lost
// boundary stops the loop.
func startLossyLoop(ctx context.Context, t *testing.T, brokers, pubBrokers []string, commands, events, group string, deliveryTimeout time.Duration) (*Loop, *sim.Engine) {
	t.Helper()
	e, err := simtest.NewEngine(26)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Recover(ctx, brokers, commands, events, e, nil); err != nil {
		t.Fatal(err)
	}
	src, err := NewKafkaSource(ctx, KafkaSourceOptions{Brokers: brokers, Group: group, Start: e.State().Offsets, Topic: commands, LagEvery: 200 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	pub, err := newKafkaPublisher(ctx, pubBrokers, "boundary-lost-test", deliveryTimeout)
	if err != nil {
		t.Fatal(err)
	}
	pub.Commands, pub.Events = commands, events
	loop, err := New(Options{
		Engine: e, Source: src, Publisher: pub, AwaitBoundaryAck: true,
		TickRate: 50, TickBudget: 10 * time.Millisecond, MaxPerTick: 1, DrainTimeout: 5 * time.Second, CheckpointEvery: 5,
		Log:      slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})),
		Registry: prometheus.NewRegistry(),
	})
	if err != nil {
		t.Fatal(err)
	}
	pub.OnBoundaryLost = loop.BoundaryLost
	pub.OnBoundaryAcked = func(tick sim.Tick, _ time.Duration) { loop.BoundaryAcked(tick) }
	e.SetObserver(loop)
	return loop, e
}

// AW-SRV-026 AC-1, AC-2, AC-3 on the broker. The publisher's broker takes no
// writes for longer than its delivery timeout: the loop stops with the
// boundary after the last delivered one lost, and commits nothing past the
// last delivered one.
//
// The outage is a broker answering every Produce with a retriable error
// rather than a severed connection: with the idempotent producer, franz-go
// never fails a batch that was sent and not answered (RecordDeliveryTimeout's
// documentation), so a cut that lands while a boundary is in flight holds it
// in retries until the broker returns — no loss, and nothing for this test to
// observe. The compose stack's stopped broker is AC-4's. A second process recovers to exactly that boundary, with its
// hash, and its next boundary is the one after it: the topic stays gapless.
func TestKafka_BoundaryLostExitsIntoRecovery(t *testing.T) {
	bk := brokers(t)
	commands, events := topics(t, bk)
	group := "andara-sim-lost-" + commands[len(commands)-8:]
	total := produce(t, bk, commands, 200)
	px := newLeaderProxy(t, bk[0], BoundaryPartition)

	// The first process: its publisher reaches the broker only through the
	// proxy, with a three-second delivery timeout.
	first, _ := startLossyLoop(context.Background(), t, bk, []string{px.Addr()}, commands, events, group, 3*time.Second)
	done := make(chan error, 1)
	go func() { done <- first.Run(context.Background()) }()
	waitForBoundaries(t, bk, events, 20, 30*time.Second)
	cut := time.Now()
	px.Refuse.Store(true)

	var err error
	select {
	case err = <-done:
	case <-time.After(60 * time.Second):
		t.Fatalf("the loop did not stop within 60 s of the broker refusing writes (%d produces refused)", px.Refused.Load())
	}
	t.Logf("stopped %v after the cut", time.Since(cut).Round(time.Millisecond))
	var lost *BoundaryLostError
	if !errors.As(err, &lost) {
		t.Fatalf("Run returned %v, want a BoundaryLostError", err)
	}
	if got := counter(first.Metrics().BoundaryLost); got != 1 {
		t.Fatalf("andara_tick_boundary_lost_total %v", got)
	}

	// The topic ends at the last delivered boundary, gapless from tick 1.
	before, err := ReadBoundaries(context.Background(), bk, events)
	if err != nil {
		t.Fatal(err)
	}
	for i, b := range before {
		if b.Tick != sim.Tick(i+1) {
			t.Fatalf("boundary %d is tick %d", i, b.Tick)
		}
	}
	last := before[len(before)-1]
	if last.Tick != lost.LastDelivered || lost.Lost != lost.LastDelivered+1 {
		t.Fatalf("topic ends at tick %d; the loop reported %d lost after %d delivered", last.Tick, lost.Lost, lost.LastDelivered)
	}

	// AC-3: the committed offsets are a delivered boundary's, none past the
	// last delivered one.
	cl, err := kgo.NewClient(kgo.SeedBrokers(bk...))
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	committed, err := kadm.NewClient(cl).FetchOffsetsForTopics(context.Background(), group, commands)
	if err != nil {
		t.Fatal(err)
	}
	matches := func(b sim.TickCompleted) bool {
		for p, want := range b.Offsets {
			got, ok := committed.Lookup(commands, p)
			if !ok || got.At != want {
				return false
			}
		}
		return true
	}
	var at sim.Tick
	for i := len(before) - 1; i >= 0 && at == 0; i-- {
		if matches(before[i]) {
			at = before[i].Tick
		}
	}
	if at == 0 {
		t.Fatalf("the committed offsets are no delivered boundary's: %v", committed)
	}
	t.Logf("committed at tick %d; last delivered %d", at, last.Tick)

	// AC-2: the restart, with the broker back, recovers to the last
	// delivered boundary — Recover verifies the hash at every tick — and
	// re-consumes from its offsets.
	px.Refuse.Store(false)
	ctx, cancel := context.WithCancel(context.Background())
	second, e := startLossyLoop(ctx, t, bk, bk, commands, events, group, DeliveryTimeout)
	if e.Tick() != last.Tick || e.StateHash() != last.StateHash {
		t.Fatalf("recovered to tick %d, want %d with its recorded hash", e.Tick(), last.Tick)
	}
	for p, want := range last.Offsets {
		if got := e.State().Offsets[p]; got != want {
			t.Fatalf("partition %d resumes at %d, the last delivered boundary says %d", p, got, want)
		}
	}
	done2 := make(chan error, 1)
	go func() { done2 <- second.Run(ctx) }()
	after := waitForBoundaries(t, bk, events, len(before)+10, 30*time.Second)
	cancel()
	if err := <-done2; err != nil {
		t.Fatal(err)
	}
	for i, b := range after {
		if b.Tick != sim.Tick(i+1) {
			t.Fatalf("after the restart, boundary %d is tick %d: the topic has a gap", i, b.Tick)
		}
	}
	if next := after[len(before)].Tick; next != last.Tick+1 {
		t.Fatalf("the restart's first boundary is tick %d, want %d", next, last.Tick+1)
	}

	// And the whole sequence replays.
	r, err := simtest.NewEngine(26)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Replay(after, KafkaRecords{Brokers: bk, Topic: commands}); err != nil {
		t.Fatal(fmt.Errorf("replay of %d records across the restart: %w", total, err))
	}
}
