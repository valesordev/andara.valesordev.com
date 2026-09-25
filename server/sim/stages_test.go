// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package sim

import (
	"bytes"
	"errors"
	"testing"

	contentv1 "github.com/valesordev/andara/gen/go/andara/content/v1"
	gamev1 "github.com/valesordev/andara/gen/go/andara/game/v1"
	logv1 "github.com/valesordev/andara/gen/go/andara/log/v1"
)

// stageWorld is one Zone, two Rooms, one Exit north.
func stageWorld(t *testing.T) *World {
	t.Helper()
	w, errs := BuildWorld([]Input{{File: "z.json", Def: &contentv1.ZoneDefinition{FormatVersion: 1, Id: "z", Name: "Z", FallbackRoom: "a", Rooms: []*contentv1.RoomDefinition{
		{Id: "a", Title: "A", Exits: []*contentv1.ExitDefinition{{Direction: "north", ToRoom: "b"}}},
		{Id: "b", Title: "B", Exits: []*contentv1.ExitDefinition{{Direction: "south", ToRoom: "a"}}},
	}}}}, Options{})
	for _, e := range errs {
		if !IsWarning(e, false) {
			t.Fatal(e)
		}
	}
	return w
}

// consumedContext builds the ApplyContext Step would, recording emits and
// produces, so a stage can be called on its own.
func consumedContext(t *testing.T, w *World, cmd *logv1.LoggedCommand) (*ApplyContext, *[]*gamev1.EventEnvelope, *[]*logv1.LoggedCommand) {
	t.Helper()
	st := NewWorldState(w, 1, []int32{PartitionFor("z")})
	st.Zones["z"].Entities["p"] = &EntityState{ID: "p", Template: "andara.core.Character", Room: "a"}
	var emitted []*gamev1.EventEnvelope
	var outbound []*logv1.LoggedCommand
	a := &ApplyContext{
		Tick: 1, World: w, Zone: st.Zones["z"], State: st, RNG: st.RNG,
		Record:   Record{Partition: PartitionFor("z"), Command: cmd},
		emit:     func(_ ZoneID, _, _ string, _ Scope, env *gamev1.EventEnvelope) { emitted = append(emitted, env) },
		outbound: &outbound,
		consumed: true,
	}
	return a, &emitted, &outbound
}

func move(dir string) *logv1.LoggedCommand {
	return &logv1.LoggedCommand{ZoneId: "z", ActorId: "p", Command: &logv1.LoggedCommand_Move{Move: &logv1.Move{Direction: dir}}}
}

// Stage rule: validate is read-only with respect to World state — the
// State Hash is unchanged after it, whether it passes or fails.
func TestValidate_IsReadOnly(t *testing.T) {
	w := stageWorld(t)
	for _, cmd := range []*logv1.LoggedCommand{move("north"), move("west"), move("up"),
		{ZoneId: "z", ActorId: "ghost", Command: &logv1.LoggedCommand_Move{Move: &logv1.Move{Direction: "north"}}},
		{ZoneId: "z", ActorId: "p", Command: &logv1.LoggedCommand_Look{Look: &logv1.Look{}}},
		{ZoneId: "z", ActorId: "p", Command: &logv1.LoggedCommand_Arrive{Arrive: &logv1.Arrive{RoomId: "nowhere", Entity: &logv1.Entity{Id: "q"}}}},
	} {
		a, emitted, outbound := consumedContext(t, w, cmd)
		before := a.State.Hash()
		var err error
		switch KindOf(cmd) {
		case KindMove:
			_, err = validateMove(a, cmd)
		case KindLook:
			_, err = validateLook(a, cmd)
		case KindArrive:
			_, err = validateArrive(a, cmd)
		}
		if a.State.Hash() != before {
			t.Errorf("%v: validate changed the State Hash (err=%v)", cmd.GetCommand(), err)
		}
		if len(*emitted) != 0 || len(*outbound) != 0 {
			t.Errorf("%v: validate emitted or produced", cmd.GetCommand())
		}
	}
}

// Stage rule: a failure at validate means apply did not execute. The
// handler returns the validate error and the state, the Events, and the
// outbound are exactly what they were.
func TestValidateFailure_SkipsApply(t *testing.T) {
	w := stageWorld(t)
	a, emitted, outbound := consumedContext(t, w, move("west"))
	before := a.State.Hash()
	err := applyMove(a, move("west"))
	if !errors.Is(err, ErrNoSuchExit) {
		t.Fatalf("err = %v", err)
	}
	var re *RejectError
	if !errors.As(err, &re) || re.Stage != StageValidate || re.Message != "there is no exit west" {
		t.Fatalf("rejection = %+v", re)
	}
	if a.State.Hash() != before || len(*emitted) != 0 || len(*outbound) != 0 || a.Zone.Entities["p"].Room != "a" {
		t.Fatal("apply ran after validate failed")
	}

	// And when validate passes, apply runs: the position moves and the
	// Events are emitted, in order.
	a, emitted, _ = consumedContext(t, w, move("north"))
	if err := applyMove(a, move("north")); err != nil {
		t.Fatal(err)
	}
	if a.Zone.Entities["p"].Room != "b" || len(*emitted) != 2 || (*emitted)[0].GetCharacterLeft() == nil || (*emitted)[1].GetCharacterArrived() == nil {
		t.Fatalf("apply: room=%s emitted=%v", a.Zone.Entities["p"].Room, *emitted)
	}
}

// The guard is checked first: an unconsumed context is refused before
// validate reads anything, so even a context whose validate would fail
// returns ErrNotConsumed, not the validate error.
func TestGuard_PrecedesValidate(t *testing.T) {
	w := stageWorld(t)
	a, _, _ := consumedContext(t, w, move("west"))
	a.consumed = false
	if err := applyMove(a, move("west")); !errors.Is(err, ErrNotConsumed) {
		t.Fatalf("err = %v", err)
	}
	var nilCtx *ApplyContext
	if nilCtx.Consumed() {
		t.Fatal("nil context is consumed")
	}
}

// Step marks its contexts consumed, so the same handlers accept them.
func TestStep_BuildsConsumedContexts(t *testing.T) {
	w := stageWorld(t)
	var seen bool
	e := NewEngine(w, nil, Config{Seed: 1, Partitions: []int32{PartitionFor("z")}, Handlers: map[CommandKind]Apply{
		KindMove: func(a *ApplyContext, cmd *logv1.LoggedCommand) error {
			seen = a.Consumed()
			return applyMove(a, cmd)
		},
	}})
	e.State().Zones["z"].Entities["p"] = &EntityState{ID: "p", Room: "a"}
	res, err := e.Step(TickInput{Records: []Record{{Partition: PartitionFor("z"), Offset: 0, Command: move("north")}}})
	if err != nil || !seen {
		t.Fatalf("err=%v consumed=%v", err, seen)
	}
	if len(res.Events) != 2 || e.State().Zones["z"].Entities["p"].Room != "b" {
		t.Fatalf("events=%v", res.Events)
	}
	// The Observer saw the outcome with its stage.
	var outs []Outcome
	e.SetObserver(observerFunc(func(_ ZoneID, _ Record) func(Outcome) { return func(o Outcome) { outs = append(outs, o) } }))
	if _, err := e.Step(TickInput{Records: []Record{{Partition: PartitionFor("z"), Offset: 1, Command: move("west")}}}); err != nil {
		t.Fatal(err)
	}
	if len(outs) != 1 || outs[0] != (Outcome{Kind: KindMove, Code: CodeNoSuchExit, Stage: StageValidate}) {
		t.Fatalf("outcomes = %+v", outs)
	}
	if !bytes.Equal(EntityCanonicalBytes(*e.State().Zones["z"].Entities["p"]), EntityCanonicalBytes(EntityState{ID: "p", Room: "b"})) {
		t.Fatal("rejected move changed state")
	}
}

type observerFunc func(ZoneID, Record) func(Outcome)

func (f observerFunc) Begin(z ZoneID, r Record) func(Outcome) { return f(z, r) }
