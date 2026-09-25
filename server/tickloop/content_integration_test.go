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
	"sync/atomic"
	"testing"
	"time"

	contentv1 "github.com/valesordev/andara/gen/go/andara/content/v1"
	logv1 "github.com/valesordev/andara/gen/go/andara/log/v1"
	"github.com/valesordev/andara/internal/eventually"
	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/simtest"
)

// AW-SRV-012 on the broker, the Definition of Done's replay across a swap: a
// loop applies genesis, a bind, a move, and a ContentSwap that removes the
// Room the Character stands in, through Redpanda. A fresh engine recovered
// from the recorded boundaries — from no content, building it only from the
// swaps it replays — reaches the same State Hash and the same content in
// effect, with the relocation replayed. Recovered over content that changed
// since, it halts on the digest.
func TestKafka_RecoveryAcrossAContentSwap(t *testing.T) {
	bk := brokers(t)
	commands, events := topics(t, bk)
	group := "andara-sim-test-" + commands[len(commands)-8:]
	content, err := simtest.TownVersions()
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	e := sim.NewEngine(sim.EmptyWorld(), nil, sim.Config{Seed: 5, Partitions: simtest.AllPartitions(), Handlers: sim.Handlers(), Content: content})
	src, err := NewKafkaSource(ctx, KafkaSourceOptions{Brokers: bk, Group: group, Start: e.State().Offsets, Topic: commands, LagEvery: 200 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	pub, err := NewKafkaPublisher(ctx, bk, "test")
	if err != nil {
		t.Fatal(err)
	}
	pub.Commands, pub.Events = commands, events
	var swaps, relocations atomic.Int64
	var applied atomic.Int64
	loop, err := New(Options{
		Engine: e, Source: src, Publisher: pub,
		TickRate: 50, TickBudget: 10 * time.Millisecond, MaxPerTick: 8, DrainTimeout: 5 * time.Second, CheckpointEvery: 5,
		Log: slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})),
		OnTick: func(res sim.StepResult, _ time.Duration) {
			applied.Add(int64(res.Completed.CommandsApplied))
			for _, s := range res.Swaps {
				swaps.Add(1)
				relocations.Add(int64(len(s.Relocations)))
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	e.SetObserver(loop)
	done := make(chan error, 1)
	go func() { done <- loop.Run(ctx) }()

	// Each step waits for the one before it to apply: the swap for town@2 is
	// digested against town@1 in effect, as the Loader would produce it.
	var produced int64
	step := func(cmd *logv1.LoggedCommand) {
		t.Helper()
		if err := pub.Produce(context.Background(), []*logv1.LoggedCommand{cmd}); err != nil {
			t.Fatal(err)
		}
		produced++
		eventually.Observed(t, 20*time.Second, fmt.Sprintf("%d records applied", produced), func() (bool, string) {
			return applied.Load() >= produced, fmt.Sprintf("%d applied", applied.Load())
		})
	}
	swap := func(v uint64, inEffect map[string]uint64) *logv1.LoggedCommand {
		t.Helper()
		cs := &logv1.ContentSwap{PackId: "town", Version: v}
		if len(inEffect) > 0 {
			// Built on what is in effect: its base is that World's digest.
			prev, err := sim.PrepareContent(content, inEffect)
			if err != nil {
				t.Fatal(err)
			}
			base := sim.ContentDigest(prev)
			cs.BaseDigest = base[:]
		}
		topo, err := content.Prepare(inEffect, cs)
		if err != nil {
			t.Fatal(err)
		}
		d := sim.ContentDigest(topo)
		cs.WorldDigest = d[:]
		return &logv1.LoggedCommand{Command: &logv1.LoggedCommand_ContentSwap{ContentSwap: cs}}
	}
	step(swap(1, nil))
	step(simtest.Bind("town", "hero", "Hero", "plaza"))
	step(simtest.Move("town", "hero", "north"))
	step(swap(2, map[string]uint64{"town": 1}))
	step(simtest.Look("town", "hero"))
	time.Sleep(300 * time.Millisecond)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if swaps.Load() != 2 || relocations.Load() != 1 {
		t.Fatalf("live: %d swaps, %d relocations; want 2 and 1", swaps.Load(), relocations.Load())
	}
	final := e.StateHash()
	liveVersions, liveDigest := e.Content()
	if e.State().Zones["town"].Entities["hero"].Room != "plaza" {
		t.Fatal("live: hero was not relocated to the fallback")
	}

	// Recovery from no content, as a restarting server does.
	recovered := sim.NewEngine(sim.EmptyWorld(), nil, sim.Config{Seed: 5, Partitions: simtest.AllPartitions(), Handlers: sim.Handlers(), Content: content})
	var replayedSwaps int
	n, err := Recover(context.Background(), bk, commands, events, recovered, func(res sim.StepResult) error {
		replayedSwaps += len(res.Swaps)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	v, d := recovered.Content()
	if n == 0 || replayedSwaps != 2 || recovered.StateHash() != final || v["town"] != liveVersions["town"] || d != liveDigest {
		t.Fatalf("recovered %d ticks, %d swaps, content %v; hash equal %v", n, replayedSwaps, v, recovered.StateHash() == final)
	}
	if recovered.State().Zones["town"].Entities["hero"].Room != "plaza" {
		t.Fatal("recovery did not replay the relocation")
	}

	// town@2 is not the content it was when the log was written.
	changed, _ := simtest.TownVersions()
	changed.Zones["town"][2] = []*contentv1.ZoneDefinition{{FormatVersion: 1, Id: "town", Name: "Town", FallbackRoom: "plaza",
		Rooms: []*contentv1.RoomDefinition{{Id: "plaza", Title: "plaza"}, {Id: "cellar", Title: "cellar"}}}}
	other := sim.NewEngine(sim.EmptyWorld(), nil, sim.Config{Seed: 5, Partitions: simtest.AllPartitions(), Handlers: sim.Handlers(), Content: changed})
	if _, err := Recover(context.Background(), bk, commands, events, other, nil); !errors.Is(err, sim.ErrContentDigest) {
		t.Fatalf("recovery over changed content: err = %v", err)
	}
}
