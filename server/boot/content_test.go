// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package boot

import (
	"errors"
	"testing"

	"github.com/valesordev/andara/server/sim"
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
