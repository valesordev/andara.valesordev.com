// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

//go:build integration

// Against throwaway topics on a running broker (`make up`, then
// `make test-integration`). Build-tagged so `make test` needs no broker.
package tickloop

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"

	logv1 "github.com/valesordev/andara/gen/go/andara/log/v1"
	"github.com/valesordev/andara/internal/eventually"
	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/simtest"
)

func brokers(t *testing.T) []string {
	t.Helper()
	v := os.Getenv("ANDARA_KAFKA_BROKERS")
	if v == "" {
		t.Skip("ANDARA_KAFKA_BROKERS not set; run `make up` and `make test-integration`")
	}
	return strings.Split(v, ",")
}

// topics creates a throwaway commands topic (64 partitions) and events
// topic, deleted when the test ends.
func topics(t *testing.T, brokers []string) (commands, events string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cl, err := kgo.NewClient(kgo.SeedBrokers(brokers...))
	if err != nil {
		t.Fatal(err)
	}
	adm := kadm.NewClient(cl)
	nonce := time.Now().UnixNano()
	commands = fmt.Sprintf("andara.test.commands.%d", nonce)
	events = fmt.Sprintf("andara.test.events.%d", nonce)
	if _, err := adm.CreateTopic(ctx, sim.PartitionCount, 1, nil, commands); err != nil {
		t.Fatal(err)
	}
	if _, err := adm.CreateTopic(ctx, sim.PartitionCount, 1, nil, events); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		dctx, dcancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer dcancel()
		_, _ = adm.DeleteTopics(dctx, commands, events)
		cl.Close()
	})
	return commands, events
}

// produce writes the scripted log to the commands topic through the
// publisher, one record at a time so the broker's batching is its own.
func produce(t *testing.T, brokers []string, topic string, n int) int {
	t.Helper()
	pub, err := NewKafkaPublisher(context.Background(), brokers, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer pub.Close()
	pub.Commands = topic
	total := 0
	for _, recs := range simtest.Script(n) {
		for _, r := range recs {
			if err := pub.Produce(context.Background(), []*logv1.LoggedCommand{r.Command}); err != nil {
				t.Fatal(err)
			}
			total++
		}
	}
	return total
}

// startLoop recovers from the events topic, then runs a loop over the
// throwaway topics until ctx is done. What a server does at boot.
func startLoop(ctx context.Context, brokers []string, commands, events, group string, seed uint64, checkpointEvery int) (*Loop, *sim.Engine, error) {
	e, err := simtest.NewEngine(seed)
	if err != nil {
		return nil, nil, err
	}
	if _, err := Recover(ctx, brokers, commands, events, e); err != nil {
		return nil, nil, err
	}
	src, err := NewKafkaSource(ctx, KafkaSourceOptions{Brokers: brokers, Group: group, Start: e.State().Offsets, Topic: commands, LagEvery: 200 * time.Millisecond})
	if err != nil {
		return nil, nil, err
	}
	pub, err := NewKafkaPublisher(ctx, brokers, "test")
	if err != nil {
		return nil, nil, err
	}
	pub.Commands, pub.Events = commands, events
	loop, err := New(Options{
		Engine: e, Source: src, Publisher: pub,
		TickRate: 50, TickBudget: 10 * time.Millisecond, MaxPerTick: 3, DrainTimeout: 5 * time.Second, CheckpointEvery: checkpointEvery,
		Log: slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})),
	})
	if err != nil {
		return nil, nil, err
	}
	e.SetObserver(loop)
	return loop, e, nil
}

// waitForBoundaries reads the events topic until it holds at least n
// boundaries, and returns them.
func waitForBoundaries(t *testing.T, brokers []string, events string, n int, within time.Duration) []sim.TickCompleted {
	t.Helper()
	var bs []sim.TickCompleted
	eventually.Observed(t, within, fmt.Sprintf("at least %d boundaries on %s", n, events), func() (bool, string) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		var err error
		bs, err = ReadBoundaries(ctx, brokers, events)
		if err != nil {
			return false, err.Error()
		}
		return len(bs) >= n, fmt.Sprintf("%d boundaries", len(bs))
	})
	return bs
}

// AC-2, AC-4, AC-5 on the broker: a loop applies the log under the broker's
// own batching, every boundary names tick, version, offsets, and hash, and a
// fresh engine replaying those boundaries reaches the same hash — with the
// checkpoints committed under the group matching a recorded boundary.
func TestKafka_ApplyThenReplay(t *testing.T) {
	bk := brokers(t)
	commands, events := topics(t, bk)
	total := produce(t, bk, commands, 20)
	group := "andara-sim-test-" + commands[len(commands)-8:]

	ctx, cancel := context.WithCancel(context.Background())
	loop, e, err := startLoop(ctx, bk, commands, events, group, 5, 5)
	if err != nil {
		t.Fatal(err)
	}
	// Progress is read from the loop's goroutine through OnTick; the
	// engine's state is not safe to read from outside it while it runs.
	var applied atomic.Int64
	loop.opts.OnTick = func(res sim.StepResult, _ time.Duration) {
		applied.Add(int64(res.Completed.CommandsApplied))
	}
	done := make(chan error, 1)
	go func() { done <- loop.Run(ctx) }()
	// Wait until everything is applied, then a little longer so a
	// checkpoint lands, then drain.
	eventually.Observed(t, 20*time.Second, fmt.Sprintf("all %d records applied", total), func() (bool, string) {
		return applied.Load() >= int64(total), fmt.Sprintf("%d applied", applied.Load())
	})
	time.Sleep(300 * time.Millisecond)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	final, finalTick := e.StateHash(), e.Tick()

	bs, err := ReadBoundaries(context.Background(), bk, events)
	if err != nil {
		t.Fatal(err)
	}
	if len(bs) == 0 || bs[len(bs)-1].Tick != finalTick {
		t.Fatalf("%d boundaries, last tick %d, engine at %d", len(bs), bs[len(bs)-1].Tick, finalTick)
	}
	for i, b := range bs {
		if b.Tick != sim.Tick(i+1) || b.StateVersion != sim.StateVersion || len(b.Offsets) != int(sim.PartitionCount) || b.StateHash == [32]byte{} {
			t.Fatalf("boundary %d: %+v", i, b)
		}
	}
	var sum int64
	for _, o := range bs[len(bs)-1].Offsets {
		sum += o
	}
	if sum != int64(total) {
		t.Fatalf("last boundary applied %d of %d records", sum, total)
	}

	// Replay in this process.
	r, err := simtest.NewEngine(5)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Replay(bs, KafkaRecords{Brokers: bk, Topic: commands}); err != nil {
		t.Fatal(err)
	}
	if r.StateHash() != final {
		t.Fatal("replay from the broker diverged")
	}

	// The drain committed the final offsets under the group.
	cl, _ := kgo.NewClient(kgo.SeedBrokers(bk...))
	defer cl.Close()
	committed, err := kadm.NewClient(cl).FetchOffsetsForTopics(context.Background(), group, commands)
	if err != nil {
		t.Fatal(err)
	}
	for p, want := range bs[len(bs)-1].Offsets {
		got, ok := committed.Lookup(commands, p)
		if !ok || got.At != want {
			t.Fatalf("partition %d committed %v, boundary says %d", p, got, want)
		}
	}
}

// The child process for the crash test: runs a loop until killed.
func TestKafkaChild(t *testing.T) {
	spec := os.Getenv("ANDARA_TICKLOOP_CHILD")
	if spec == "" {
		t.Skip("child mode only")
	}
	parts := strings.Split(spec, "|")
	bk := strings.Split(parts[0], ",")
	loop, _, err := startLoop(context.Background(), bk, parts[1], parts[2], parts[3], 9, 5)
	if err != nil {
		fmt.Fprintln(os.Stderr, "child:", err)
		os.Exit(3)
	}
	fmt.Println("child: recovered and running")
	if err := loop.Run(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "child:", err)
		os.Exit(4)
	}
}

// AC-2 and AC-10 with real processes: a loop is killed with SIGKILL
// between applying and checkpointing; a second process recovers from the
// recorded boundaries — which fails if any hash disagrees — and continues
// from the tick after the last one recorded; a third engine replays the
// whole sequence and matches the second process's boundaries.
func TestKafka_CrashAndRecover(t *testing.T) {
	bk := brokers(t)
	commands, events := topics(t, bk)
	group := "andara-sim-crash-" + commands[len(commands)-8:]
	produce(t, bk, commands, 40)

	spawn := func() *exec.Cmd {
		cmd := exec.Command(os.Args[0], "-test.run", "^TestKafkaChild$", "-test.v")
		cmd.Env = append(os.Environ(), "ANDARA_TICKLOOP_CHILD="+strings.Join(bk, ",")+"|"+commands+"|"+events+"|"+group)
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		return cmd
	}
	first := spawn()
	before := waitForBoundaries(t, bk, events, 30, 30*time.Second)
	if err := first.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	_ = first.Wait()
	// Re-read: the child may have published more between the wait and the kill.
	before, err := ReadBoundaries(context.Background(), bk, events)
	if err != nil {
		t.Fatal(err)
	}
	lastBefore := before[len(before)-1].Tick

	second := spawn()
	defer func() {
		_ = second.Process.Signal(syscall.SIGKILL)
		_ = second.Wait()
	}()
	after := waitForBoundaries(t, bk, events, len(before)+20, 60*time.Second)
	// The recovered process continued at lastBefore+1: it replayed the
	// recorded ticks — verifying each hash — rather than starting over.
	for i := len(before); i < len(after); i++ {
		if after[i].Tick != after[i-1].Tick+1 {
			t.Fatalf("boundary %d is tick %d after tick %d", i, after[i].Tick, after[i-1].Tick)
		}
	}
	if after[len(before)].Tick != lastBefore+1 {
		t.Fatalf("recovered process resumed at tick %d, want %d", after[len(before)].Tick, lastBefore+1)
	}
	// And the whole sequence, pre- and post-crash, replays in this process.
	r, err := simtest.NewEngine(9)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Replay(after, KafkaRecords{Brokers: bk, Topic: commands}); err != nil {
		t.Fatal(err)
	}
	if r.StateHash() != after[len(after)-1].StateHash {
		t.Fatal("replay of the recovered run diverged")
	}
	// A recovery whose hashes disagree halts: a different seed is a
	// different World.
	wrong, _ := simtest.NewEngine(10)
	if _, err := Recover(context.Background(), bk, commands, events, wrong); !errors.Is(err, sim.ErrHashMismatch) {
		t.Fatalf("recovery with the wrong seed: %v", err)
	}
}
