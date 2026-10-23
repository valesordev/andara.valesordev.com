// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

//go:build integration

package tickloop

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	logv1 "github.com/valesordev/andara/gen/go/andara/log/v1"
	"github.com/valesordev/andara/internal/eventually"
	"github.com/valesordev/andara/server/kafkaclient"
	"github.com/valesordev/andara/server/kafkaclient/kafkatest"
	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/simtest"
)

// AW-SRV-028 AC-2 on the broker, with real processes: alice's Move to another
// Zone is logged, and the process that applied it loses every Arrive it
// produces (its producer drops them) and is SIGKILLed after publishing its
// boundaries and before any acknowledgement. A second process recovers: Transit
// still holds alice at the recovered hash, its first live ticks retry, and she
// arrives in wilds exactly once.

// arriveDroppingPublisher is the Kafka publisher with its cross-Zone producer dropping
// every Arrive: what a broker outage past the delivery timeout does.
type arriveDroppingPublisher struct {
	*KafkaPublisher
	lossy bool
}

func (p *arriveDroppingPublisher) Produce(ctx context.Context, cmds []*logv1.LoggedCommand) error {
	if !p.lossy {
		return p.KafkaPublisher.Produce(ctx, cmds)
	}
	var keep []*logv1.LoggedCommand
	for _, c := range cmds {
		if c.GetArrive() == nil {
			keep = append(keep, c)
		}
	}
	if len(keep) == 0 {
		return nil
	}
	return p.KafkaPublisher.Produce(ctx, keep)
}

// handoffEngine is the World both process lives and the test start from.
func handoffEngine(seed uint64) (*sim.Engine, error) {
	e, err := simtest.NewVerbEngine(seed)
	if err != nil {
		return nil, err
	}
	simtest.Place(e, "alice", "town", "plaza")
	simtest.Place(e, "bob", "town", "plaza")
	return e, nil
}

func startHandoffLoop(ctx context.Context, brokers kafkaclient.Config, commands, events, group string, lossy bool) (*Loop, error) {
	e, err := handoffEngine(9)
	if err != nil {
		return nil, err
	}
	if _, err := Recover(ctx, brokers, commands, events, e, nil); err != nil {
		return nil, err
	}
	src, err := NewKafkaSource(ctx, KafkaSourceOptions{Kafka: brokers, Group: group, Start: e.State().Offsets, Topic: commands, LagEvery: 200 * time.Millisecond})
	if err != nil {
		return nil, err
	}
	pub, err := NewKafkaPublisher(ctx, brokers, "test", commands, events)
	if err != nil {
		return nil, err
	}
	loop, err := New(Options{
		Engine: e, Source: src, Publisher: &arriveDroppingPublisher{pub, lossy},
		TickRate: 50, TickBudget: 20 * time.Millisecond, MaxPerTick: 64, DrainTimeout: 5 * time.Second, CheckpointEvery: 5,
		Log: slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})),
	})
	if err != nil {
		return nil, err
	}
	e.SetObserver(loop)
	return loop, nil
}

// TestKafkaHandoffChild is the process the test spawns: recover, then run.
func TestKafkaHandoffChild(t *testing.T) {
	spec := os.Getenv("ANDARA_TICKLOOP_HANDOFF_CHILD")
	if spec == "" {
		t.Skip("child mode only")
	}
	parts := strings.Split(spec, "|")
	loop, err := startHandoffLoop(context.Background(), kafkatest.Config(strings.Split(parts[0], ",")), parts[1], parts[2], parts[3], parts[4] == "lossy")
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

func TestKafka_AHandoffSurvivesASIGKILLWithTheArriveLost(t *testing.T) {
	bk := brokers(t)
	commands, events := topics(t, bk)
	group := "it-sim-handoff-" + commands[len(commands)-8:]

	// Only Commands are produced; the events topic is never written.
	pub, err := NewKafkaPublisher(context.Background(), bk, "test", commands, "unused-events")
	if err != nil {
		t.Fatal(err)
	}
	move := simtest.Move("town", "alice", "east")
	if err := pub.Produce(context.Background(), []*logv1.LoggedCommand{move}); err != nil {
		t.Fatal(err)
	}
	if err := pub.Close(); err != nil {
		t.Fatal(err)
	}
	// One past the Move on its Partition: a boundary whose Offsets reach it
	// recorded a World that had applied the Move.
	movePart := sim.CommandPartition(move)
	moveEnd, err := EndOffset(context.Background(), bk, commands, movePart)
	if err != nil {
		t.Fatal(err)
	}
	// The setup publisher has no OnFailure: a Move it failed to deliver
	// would leave the Partition empty, and every boundary "past" offset 0.
	if moveEnd < 1 {
		t.Fatalf("the Move is not on partition %d of %s: end offset %d", movePart, commands, moveEnd)
	}

	spawn := func(mode string) *exec.Cmd {
		cmd := exec.Command(os.Args[0], "-test.run", "^TestKafkaHandoffChild$", "-test.v")
		cmd.Env = append(os.Environ(), "ANDARA_TICKLOOP_HANDOFF_CHILD="+strings.Join(bk.Brokers, ",")+"|"+commands+"|"+events+"|"+group+"|"+mode)
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		return cmd
	}

	// The first life: alice's Move applies, every Arrive (the first and the
	// retries) is dropped, and its boundaries are published. Then SIGKILL.
	// The wait is on the Move having applied inside a recorded boundary, and
	// on two retry intervals of boundaries after it, so a retry is dropped
	// too. A count of boundaries alone is reached by an idle loop whose
	// consumer has not yet fetched the Move (AW-SRV-061).
	first := spawn("lossy")
	waitForMoveApplied(t, bk, events, movePart, moveEnd, 2*sim.DefaultHandoffRetryTicks, 30*time.Second)
	if err := first.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	_ = first.Wait()
	before, err := ReadBoundaries(context.Background(), bk, events)
	if err != nil {
		t.Fatal(err)
	}

	// What the recovered World holds, asserted through RecoverFrom as boot
	// runs it: alice is in town's Transit at the recovered hash, nowhere else.
	r, err := handoffEngine(9)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Recover(context.Background(), bk, commands, events, r, nil); err != nil {
		t.Fatalf("recovery after the SIGKILL: %v; %s", err, handoffWorld(r, before, movePart))
	}
	if r.StateHash() != before[len(before)-1].StateHash {
		t.Fatalf("the recovered World does not match the last recorded boundary: recovered %x; %s", r.StateHash(), handoffWorld(r, before, movePart))
	}
	st := r.State()
	if len(st.Zones["town"].Transit) != 1 || st.Zones["town"].Entities["alice"] != nil || st.Zones["wilds"].Entities["alice"] != nil {
		t.Fatalf("after the SIGKILL alice should be in town's Transit and nowhere else: %s", handoffWorld(r, before, movePart))
	}

	// The second life delivers: it retries from the recovered Transit.
	second := spawn("lossless")
	defer func() {
		_ = second.Process.Signal(syscall.SIGKILL)
		_ = second.Wait()
	}()
	deadline := time.Now().Add(60 * time.Second)
	var after *sim.Engine
	for time.Now().Before(deadline) {
		after, err = handoffEngine(9)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		_, err = Recover(ctx, bk, commands, events, after, nil)
		cancel()
		if err == nil && after.State().Zones["wilds"].Entities["alice"] != nil && len(after.State().Zones["town"].Transit) == 0 {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("replaying the second life's log: %v", err)
	}
	s := after.State()
	if s.Zones["wilds"].Entities["alice"] == nil || s.Zones["town"].Entities["alice"] != nil || len(s.Zones["town"].Transit) != 0 {
		t.Fatalf("alice did not arrive: wilds %v, town %v, transit %v", s.Zones["wilds"].Entities, s.Zones["town"].Entities, s.Zones["town"].Transit)
	}
	if s.Zones["wilds"].Placed["alice"].Seq != 1 || s.Zones["wilds"].Entities["alice"].HandoffSeq != 1 {
		t.Fatalf("alice should have been placed exactly once, at sequence 1: marks %v", s.Zones["wilds"].Placed)
	}
}

// waitForMoveApplied reads the events topic until a boundary's Offsets on
// movePart reach moveEnd, the Move applied inside a recorded boundary, and
// the last boundary is at least past ticks after that one. On a timeout it
// names the last boundary's tick, Offsets on movePart and CommandsApplied.
func waitForMoveApplied(t *testing.T, bk kafkaclient.Config, events string, movePart int32, moveEnd int64, past sim.Tick, within time.Duration) {
	t.Helper()
	eventually.Observed(t, within, fmt.Sprintf("a boundary past the Move (partition %d offset %d), then %d more ticks", movePart, moveEnd, past), func() (bool, string) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		bs, err := ReadBoundaries(ctx, bk, events)
		if err != nil {
			return false, err.Error()
		}
		if len(bs) == 0 {
			return false, "no boundaries"
		}
		last := bs[len(bs)-1]
		saw := fmt.Sprintf("%d boundaries, the last at tick %d with partition %d at offset %d and %d commands applied", len(bs), last.Tick, movePart, last.Offsets[movePart], last.CommandsApplied)
		for _, b := range bs {
			if b.Offsets[movePart] >= moveEnd {
				return last.Tick >= b.Tick+past, saw + fmt.Sprintf("; the Move applied by tick %d", b.Tick)
			}
		}
		return false, saw
	})
}

// handoffWorld is what a post-SIGKILL failure prints, so one CI log tells the
// outcomes apart (AW-SRV-061): the Move never applied (alice in town's
// Entities), delivered early (alice in wilds'), or lost (in neither, with an
// empty Transit).
func handoffWorld(e *sim.Engine, boundaries []sim.TickCompleted, movePart int32) string {
	st := e.State()
	ids := func(m map[sim.EntityID]*sim.EntityState) []sim.EntityID {
		out := make([]sim.EntityID, 0, len(m))
		for id := range m {
			out = append(out, id)
		}
		slices.Sort(out)
		return out
	}
	transit := make([]sim.EntityID, 0, len(st.Zones["town"].Transit))
	for id := range st.Zones["town"].Transit {
		transit = append(transit, id)
	}
	slices.Sort(transit)
	out := fmt.Sprintf("town entities %v, wilds entities %v, town transit %v, %d boundaries read",
		ids(st.Zones["town"].Entities), ids(st.Zones["wilds"].Entities), transit, len(boundaries))
	if len(boundaries) == 0 {
		return out
	}
	last := boundaries[len(boundaries)-1]
	// The Partitions past offset 0: 64 entries are mostly zeros.
	moved := map[int32]int64{}
	for p, off := range last.Offsets {
		if off > 0 {
			moved[p] = off
		}
	}
	return out + fmt.Sprintf(", the last at tick %d with state hash %x, nonzero offsets %v (the Move's partition %d at %d), %d commands applied",
		last.Tick, last.StateHash, moved, movePart, last.Offsets[movePart], last.CommandsApplied)
}
