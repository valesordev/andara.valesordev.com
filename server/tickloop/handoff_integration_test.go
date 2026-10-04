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
	"strings"
	"syscall"
	"testing"
	"time"

	logv1 "github.com/valesordev/andara/gen/go/andara/log/v1"
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

func startHandoffLoop(ctx context.Context, brokers []string, commands, events, group string, lossy bool) (*Loop, error) {
	e, err := handoffEngine(9)
	if err != nil {
		return nil, err
	}
	if _, err := Recover(ctx, brokers, commands, events, e, nil); err != nil {
		return nil, err
	}
	src, err := NewKafkaSource(ctx, KafkaSourceOptions{Brokers: brokers, Group: group, Start: e.State().Offsets, Topic: commands, LagEvery: 200 * time.Millisecond})
	if err != nil {
		return nil, err
	}
	pub, err := NewKafkaPublisher(ctx, brokers, "test")
	if err != nil {
		return nil, err
	}
	pub.Commands, pub.Events = commands, events
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
	loop, err := startHandoffLoop(context.Background(), strings.Split(parts[0], ","), parts[1], parts[2], parts[3], parts[4] == "lossy")
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
	group := "andara-sim-handoff-" + commands[len(commands)-8:]

	pub, err := NewKafkaPublisher(context.Background(), bk, "test")
	if err != nil {
		t.Fatal(err)
	}
	pub.Commands = commands
	if err := pub.Produce(context.Background(), []*logv1.LoggedCommand{simtest.Move("town", "alice", "east")}); err != nil {
		t.Fatal(err)
	}
	pub.Close()

	spawn := func(mode string) *exec.Cmd {
		cmd := exec.Command(os.Args[0], "-test.run", "^TestKafkaHandoffChild$", "-test.v")
		cmd.Env = append(os.Environ(), "ANDARA_TICKLOOP_HANDOFF_CHILD="+strings.Join(bk, ",")+"|"+commands+"|"+events+"|"+group+"|"+mode)
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		return cmd
	}

	// The first life: alice's Move applies, every Arrive (the first and the
	// retries) is dropped, and its boundaries are published. Then SIGKILL.
	first := spawn("lossy")
	waitForBoundaries(t, bk, events, 40, 30*time.Second) // well past the 10-tick retry interval
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
		t.Fatalf("recovery after the SIGKILL: %v", err)
	}
	if r.StateHash() != before[len(before)-1].StateHash {
		t.Fatal("the recovered World does not match the last recorded boundary")
	}
	st := r.State()
	if len(st.Zones["town"].Transit) != 1 || st.Zones["town"].Entities["alice"] != nil || st.Zones["wilds"].Entities["alice"] != nil {
		t.Fatalf("after the SIGKILL alice should be in town's Transit and nowhere else: transit %v", st.Zones["town"].Transit)
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
