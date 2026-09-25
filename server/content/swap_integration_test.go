// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

//go:build integration

package content

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"

	logv1 "github.com/valesordev/andara/gen/go/andara/log/v1"
	"github.com/valesordev/andara/internal/eventually"
	"github.com/valesordev/andara/server/ingress"
	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/simtest"
	"github.com/valesordev/andara/server/tickloop"
)

// The whole chain AC-2 and AC-3 describe, over a throwaway Redpanda: an
// Active Pointer moves; Follow debounces it; the Loader resolves, validates
// and builds the new version off-tick and produces a ContentSwap through the
// Gateway's own producer to Partition 0; the tick loop consumes it, prepares
// it through the Loader, checks the digest, and applies it after every other
// record of its tick; the Character standing in the Room the new version
// removed is relocated to the fallback in that same tick; and only then is
// the version serving. Genesis is the same path, from LoadAll.
func TestKafka_APointerMoveSwapsTheWorldThroughTheLog(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	bk := brokers(t)
	topics, cl := throwawayTopics(t)
	commands, events := logTopics(t, cl)
	p := &publisher{t: t, cl: cl, topics: topics}

	// andara.core carrying the shipped Templates and a town, at two versions:
	// the hall north of the plaza, and then no hall.
	seed := map[string]string{}
	for _, name := range []string{"Entity", "Character", "Npc", "Item"} {
		b, err := os.ReadFile(filepath.Join("..", "..", "content", "core", "templates", "andara.core."+name+".json"))
		if err != nil {
			t.Fatal(err)
		}
		seed["templates/andara.core."+name+".json"] = string(b)
	}
	with := func(town string) map[string]string {
		files := map[string]string{"town.json": town}
		for k, v := range seed {
			files[k] = v
		}
		return files
	}
	p.publish(CorePack, 1, 0, with(`{"formatVersion":1,"id":"town","name":"Town","fallbackRoom":"plaza","rooms":[
		{"id":"plaza","title":"Plaza","description":"d","exits":[{"direction":"north","toRoom":"hall"}]},
		{"id":"hall","title":"Hall","description":"d","exits":[{"direction":"south","toRoom":"plaza"}]}]}`))
	p.activate(CorePack, 1)
	p.publish(CorePack, 2, 0, with(`{"formatVersion":1,"id":"town","name":"Town","fallbackRoom":"plaza","rooms":[
		{"id":"plaza","title":"Plaza","description":"d"}]}`))

	resolver, err := NewKafkaResolver(KafkaOptions{Brokers: bk, Topics: topics})
	if err != nil {
		t.Fatal(err)
	}
	defer resolver.Close()
	if err := resolver.Pin(ctx); err != nil {
		t.Fatal(err)
	}
	m := NewMetrics(nil)
	loader := NewLoader(LoaderOptions{Store: resolver, Packs: []string{CorePack}, Metrics: m})
	prod, err := ingress.NewKafkaProducer(ingress.ProducerOptions{Brokers: bk, Topic: commands, Deadline: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = prod.Close() }()
	loader.SetProducer(producerFunc(func(cmd *logv1.LoggedCommand) error {
		_, err := prod.Produce(ctx, cmd)
		return err
	}))

	// The tick loop, as boot builds it: no content, the Loader as its source.
	e := sim.NewEngine(sim.EmptyWorld(), nil, sim.Config{Seed: 5, Partitions: simtest.AllPartitions(), Handlers: sim.Handlers(), Content: loader})
	src, err := tickloop.NewKafkaSource(ctx, tickloop.KafkaSourceOptions{Brokers: bk, Group: "andara-sim-" + commands, Start: e.State().Offsets, Topic: commands, LagEvery: 200 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	pub, err := tickloop.NewKafkaPublisher(ctx, bk, "test")
	if err != nil {
		t.Fatal(err)
	}
	pub.Commands, pub.Events = commands, events
	var (
		applied  atomic.Int64
		mu       sync.Mutex
		swapTick sim.Tick
		relocIn  sim.Tick
		heroRoom sim.RoomID
	)
	loop, err := tickloop.New(tickloop.Options{
		Engine: e, Source: src, Publisher: pub,
		TickRate: 50, TickBudget: 10 * time.Millisecond, MaxPerTick: 8, DrainTimeout: 5 * time.Second, CheckpointEvery: 5,
		Log: slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})),
		OnTick: func(res sim.StepResult, _ time.Duration) {
			applied.Add(int64(res.Completed.CommandsApplied))
			loader.Applied(res.Swaps)
			mu.Lock()
			defer mu.Unlock()
			for _, s := range res.Swaps {
				if s.Version == 2 {
					swapTick = res.Tick
				}
			}
			for _, ev := range res.Events {
				if ev.Type == sim.EvEntityRelocated && ev.Envelope.GetEntityRelocated().GetEntityName() == "Hero" {
					relocIn = res.Tick
				}
			}
			if z := e.State().Zones["town"]; z != nil {
				if hero := z.Entities["hero"]; hero != nil {
					heroRoom = hero.Room
				}
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	e.SetObserver(loop)
	loopCtx, stopLoop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- loop.Run(loopCtx) }()
	defer func() { stopLoop(); <-done }()

	// Genesis.
	if rejects, err := loader.LoadAll(ctx); err != nil || len(rejects) != 0 {
		t.Fatalf("genesis: %v %v", rejects, err)
	}
	if v := loader.Versions(); v[CorePack] != 1 {
		t.Fatalf("after genesis serving %v", v)
	}
	// Hero binds in the plaza and walks into the hall.
	for _, cmd := range []*logv1.LoggedCommand{simtest.Bind("town", "hero", "Hero", "plaza"), simtest.Move("town", "hero", "north")} {
		before := applied.Load()
		if _, err := prod.Produce(ctx, cmd); err != nil {
			t.Fatal(err)
		}
		eventually.True(t, 20*time.Second, "the command applied", func() bool { return applied.Load() > before })
	}
	eventually.True(t, 10*time.Second, "hero in the hall", func() bool { mu.Lock(); defer mu.Unlock(); return heroRoom == "hall" })

	// The pointer moves; Follow does the rest.
	fctx, stopFollow := context.WithCancel(ctx)
	followed := make(chan error, 1)
	go func() { followed <- loader.Follow(fctx, resolver, 100*time.Millisecond, nil) }()
	defer func() { stopFollow(); <-followed }()
	// Watch starts at the end of the topic; re-activating is idempotent, so
	// the pointer is written until the move is seen rather than slept on.
	eventually.Observed(t, 90*time.Second, "andara.core@2 in effect", func() (bool, string) {
		if loader.Versions()[CorePack] == 2 {
			return true, ""
		}
		p.activate(CorePack, 2)
		time.Sleep(500 * time.Millisecond)
		return false, fmt.Sprintf("serving %v", loader.Versions())
	})

	mu.Lock()
	defer mu.Unlock()
	if swapTick == 0 || relocIn != swapTick || heroRoom != "plaza" {
		t.Fatalf("swap at tick %d, relocation at tick %d, hero in %q; want the relocation in the swap's tick, to the plaza", swapTick, relocIn, heroRoom)
	}
	// Applied runs on the loop's goroutine; the counter is safe to read here.
	if n := testutil.ToFloat64(m.Relocations.WithLabelValues("town")); n != 1 {
		t.Fatalf("andara_content_relocations_total{zone=town} = %v, want 1", n)
	}
	if pend := loader.Pending()[CorePack]; pend != 0 {
		t.Fatalf("pending %v with the pointer in effect", pend)
	}
}

// logTopics creates a throwaway commands topic and events topic, 64
// Partitions each, deleted when the test ends.
func logTopics(t *testing.T, cl *kgo.Client) (commands, events string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	adm := kadm.NewClient(cl)
	stamp := time.Now().UnixNano()
	commands = fmt.Sprintf("andara.test.commands.%d", stamp)
	events = fmt.Sprintf("andara.test.events.%d", stamp)
	for _, name := range []string{commands, events} {
		if _, err := adm.CreateTopic(ctx, sim.PartitionCount, 1, nil, name); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		dctx, dcancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer dcancel()
		_, _ = adm.DeleteTopics(dctx, commands, events)
	})
	return commands, events
}
