// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package boot

import (
	"errors"
	"testing"

	logv1 "github.com/valesordev/andara/gen/go/andara/log/v1"
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

// The review of #88, reproduced live: a log recorded before content was in the
// log fails its State Hash at tick 1 when replayed on an empty topology,
// before any Command can be seen applying with no content. Recovery reports
// that as the pre-rule refusal, not as corruption.
func TestRecovery_APreRuleLogIsRefusedByName(t *testing.T) {
	// A log as a pre-rule server wrote it: the World built in, no swap.
	live, err := simtest.NewVerbEngine(5)
	if err != nil {
		t.Fatal(err)
	}
	log := simtest.MemorySource{}
	var boundaries []sim.TickCompleted
	for _, cmd := range []*logv1.LoggedCommand{simtest.Bind("town", "ch-1", "Aldric", "plaza"), simtest.Look("town", "ch-1")} {
		p := sim.CommandPartition(cmd)
		rec := sim.Record{Partition: p, Offset: int64(len(log[p])), Command: cmd}
		log[p] = append(log[p], rec)
		res, err := live.Step(sim.TickInput{Records: []sim.Record{rec}})
		if err != nil {
			t.Fatal(err)
		}
		boundaries = append(boundaries, res.Completed)
	}

	// Recovery as StartTickLoop runs it: no content, the hook, the mapping.
	rt, _ := runtime(t, fixture(t, "valid"), false)
	c, err := simtest.CrossingContent()
	if err != nil {
		t.Fatal(err)
	}
	e := sim.NewEngine(sim.EmptyWorld(), nil, sim.Config{Seed: 5, Partitions: simtest.AllPartitions(), Handlers: sim.Handlers(), Content: c})
	err = RecoveryError(e.ReplayEach(boundaries, log, rt.recovered()), e)
	if !errors.Is(err, ErrPreRuleLog) {
		t.Fatalf("recovery of a pre-rule log: %v", err)
	}

	// A post-rule log that diverges is still corruption, not the refusal.
	post := sim.NewEngine(sim.EmptyWorld(), nil, sim.Config{Seed: 5, Partitions: simtest.AllPartitions(), Handlers: sim.Handlers(), Content: c})
	gen := c.Genesis()
	glog := simtest.MemorySource{sim.WorldPartition: {{Partition: sim.WorldPartition, Command: gen}}}
	res, err := post.Step(sim.TickInput{Records: glog[sim.WorldPartition]})
	if err != nil {
		t.Fatal(err)
	}
	bad := res.Completed
	bad.StateHash[0] ^= 0xff
	replay := sim.NewEngine(sim.EmptyWorld(), nil, sim.Config{Seed: 5, Partitions: simtest.AllPartitions(), Handlers: sim.Handlers(), Content: c})
	err = RecoveryError(replay.ReplayEach([]sim.TickCompleted{bad}, glog, rt.recovered()), replay)
	var hm *sim.HashMismatchError
	if errors.Is(err, ErrPreRuleLog) || !errors.As(err, &hm) {
		t.Fatalf("a corrupt post-rule log: %v", err)
	}
}
