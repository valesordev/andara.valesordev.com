// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package boot

import (
	"context"
	"errors"
	"testing"
	"time"

	logv1 "github.com/valesordev/andara/gen/go/andara/log/v1"
	"github.com/valesordev/andara/server/content"
	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/simtest"
)

// A log that applied Commands while no content was in effect predates the
// rule that the log records the content in effect, and recovery refuses it
// (AW-SRV-012, forward-only). Empty leading ticks are not Commands, and once a
// swap has applied everything after it is ordinary history.
func TestRecovered_RefusesALogThatPredatesTheContentRule(t *testing.T) {
	rt, _ := runtime(t, fixture(t, "valid"), false)

	hook := rt.recovered()
	if err := hook(sim.StepResult{Tick: 1}); err != nil {
		t.Fatalf("an empty tick: %v", err)
	}
	if err := hook(sim.StepResult{Tick: 2, Completed: sim.TickCompleted{CommandsApplied: 1}}); !errors.Is(err, ErrPreRuleLog) {
		t.Fatalf("a Command before any swap: err = %v", err)
	}

	hook = rt.recovered()
	genesis := sim.StepResult{Tick: 1, Swaps: []sim.SwapApplied{{Pack: "dir"}}, Completed: sim.TickCompleted{CommandsApplied: 1}}
	if err := hook(genesis); err != nil {
		t.Fatalf("the genesis tick: %v", err)
	}
	if err := hook(sim.StepResult{Tick: 2, Completed: sim.TickCompleted{CommandsApplied: 3}}); err != nil {
		t.Fatalf("Commands after genesis: %v", err)
	}

	// A swap and a Command in the same first tick: the Command applied first,
	// on no content, which a log written under the rule cannot contain.
	hook = rt.recovered()
	mixed := sim.StepResult{Tick: 1, Swaps: []sim.SwapApplied{{Pack: "dir"}}, Completed: sim.TickCompleted{CommandsApplied: 2}}
	if err := hook(mixed); !errors.Is(err, ErrPreRuleLog) {
		t.Fatalf("a Command beside genesis: err = %v", err)
	}
}

// memLog is a recorded log for StartTickLoop's recovery: boundaries, the
// Commands, and each Partition's end.
type memLog struct {
	boundaries []sim.TickCompleted
	recs       simtest.MemorySource
}

func (m *memLog) Boundaries(context.Context) ([]sim.TickCompleted, error) { return m.boundaries, nil }
func (m *memLog) Fetch(p int32, from, to int64) ([]sim.Record, error) {
	return m.recs.Fetch(p, from, to)
}
func (m *memLog) End(_ context.Context, p int32) (int64, error) { return int64(len(m.recs[p])), nil }

// record steps live over cmds, one tick each (nil is an idle tick), and
// keeps the log it writes.
func (m *memLog) record(t *testing.T, live *sim.Engine, cmds ...*logv1.LoggedCommand) {
	t.Helper()
	for _, cmd := range cmds {
		var in sim.TickInput
		if cmd != nil {
			p := sim.CommandPartition(cmd)
			rec := sim.Record{Partition: p, Offset: int64(len(m.recs[p])), Command: cmd}
			m.recs[p] = append(m.recs[p], rec)
			in.Records = []sim.Record{rec}
		}
		res, err := live.Step(in)
		if err != nil {
			t.Fatal(err)
		}
		m.boundaries = append(m.boundaries, res.Completed)
	}
}

// recoveringRuntime is a Runtime whose StartTickLoop recovers from log.
func recoveringRuntime(t *testing.T, log *memLog) *Runtime {
	t.Helper()
	rt, logs := runtime(t, fixture(t, "valid"), false)
	if code := rt.LoadContent(context.Background()); code != ExitOK {
		t.Fatalf("load: %s", logs.String())
	}
	rt.Cfg.SimSource = "kafka"
	rt.Cfg.SimSeed = 5
	rt.Cfg.SimPartitions = allPartitionsForTest()
	rt.replay = log
	return rt
}

// The review of #88, reproduced live, through StartTickLoop: a log recorded
// before content was in the log fails its State Hash at tick 1 on an empty
// topology, and recovery refuses it by name rather than as corruption.
func TestStartTickLoop_RefusesAPreRuleLogByName(t *testing.T) {
	live, err := simtest.NewVerbEngine(5) // the World built in, no swap
	if err != nil {
		t.Fatal(err)
	}
	log := &memLog{recs: simtest.MemorySource{}}
	log.record(t, live, simtest.Bind("town", "ch-1", "Aldric", "plaza"), simtest.Look("town", "ch-1"))

	_, err = recoveringRuntime(t, log).StartTickLoop(context.Background())
	if !errors.Is(err, ErrPreRuleLog) {
		t.Fatalf("recovery of a pre-rule log: %v", err)
	}
}

// The review of #91: a post-rule log also runs idle ticks before genesis, so
// a mismatch at tick 1 — here, a changed sim.seed — happens before any content
// is in effect. The log records content, so it is not pre-rule: recovery
// reports the mismatch and never tells the operator to wipe the log.
func TestStartTickLoop_APostRuleMismatchBeforeGenesisIsNotPreRule(t *testing.T) {
	rt := recoveringRuntime(t, &memLog{recs: simtest.MemorySource{}})
	swap := &logv1.ContentSwap{PackId: content.DirPack}
	topo, err := rt.Content.Prepare(nil, swap)
	if err != nil {
		t.Fatal(err)
	}
	d := sim.ContentDigest(topo)
	swap.WorldDigest = d[:]
	live := sim.NewEngine(sim.EmptyWorld(), nil, sim.Config{Seed: 999, Partitions: allPartitionsForTest(), Handlers: sim.Handlers(), Content: rt.Content})
	log := &memLog{recs: simtest.MemorySource{}}
	log.record(t, live, nil, &logv1.LoggedCommand{Command: &logv1.LoggedCommand_ContentSwap{ContentSwap: swap}})
	rt.replay = log

	_, err = rt.StartTickLoop(context.Background())
	var hm *sim.HashMismatchError
	if errors.Is(err, ErrPreRuleLog) || !errors.As(err, &hm) || hm.Tick != 1 {
		t.Fatalf("a post-rule log that mismatches at tick 1: %v", err)
	}
}

// The loop keeps its World Partition position current, so worldBarrier is
// satisfied once everything on it has been consumed: after genesis applied,
// a barrier returns at once rather than waiting out its bound.
func TestWorldBarrier_FollowsTheLoop(t *testing.T) {
	rt, logs := runtime(t, fixture(t, "valid"), false)
	if code := rt.LoadContent(context.Background()); code != ExitOK {
		t.Fatalf("load: %s", logs.String())
	}
	serveMemory(t, rt)
	if end := rt.memSource.End(sim.WorldPartition); end != 1 || rt.worldNext.Load() != end {
		t.Fatalf("world partition end %d, loop at %d; want the genesis swap consumed", end, rt.worldNext.Load())
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := rt.worldBarrier(ctx); err != nil {
		t.Fatalf("barrier after genesis: %v", err)
	}
}
