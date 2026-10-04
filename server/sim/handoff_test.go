// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package sim_test

import (
	"bytes"
	"fmt"
	"slices"
	"testing"

	"google.golang.org/protobuf/proto"

	contentv1 "github.com/valesordev/andara/gen/go/andara/content/v1"
	logv1 "github.com/valesordev/andara/gen/go/andara/log/v1"
	statev1 "github.com/valesordev/andara/gen/go/andara/state/v1"
	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/simtest"
)

// AW-SRV-028: the durable cross-Zone handoff. The World is CrossingWorld:
// town/plaza east to wilds/trail, south to docks/pier, and back.

// hx drives an Engine the way the live loop does: a tick of records, then the
// retry pass.
type hx struct {
	t *testing.T
	e *sim.Engine
}

func newHx(t *testing.T, mutate func(*sim.Config)) *hx {
	t.Helper()
	w, err := simtest.CrossingWorld()
	if err != nil {
		t.Fatal(err)
	}
	reg, err := simtest.Templates()
	if err != nil {
		t.Fatal(err)
	}
	cfg := sim.Config{Seed: 7, Partitions: simtest.AllPartitions(), Handlers: sim.Handlers()}
	if mutate != nil {
		mutate(&cfg)
	}
	return &hx{t: t, e: sim.NewEngine(w, reg, cfg)}
}

// step applies cmds in one tick, in order, with no retry pass: the schedule is
// what applying them alone wrote.
func (h *hx) step(cmds ...*logv1.LoggedCommand) sim.StepResult {
	h.t.Helper()
	next := map[int32]int64{}
	var recs []sim.Record
	for _, c := range cmds {
		p := sim.PartitionFor(sim.ZoneID(c.GetZoneId()))
		off, ok := next[p]
		if !ok {
			off = h.e.State().Offsets[p]
		}
		recs = append(recs, sim.Record{Partition: p, Offset: off, Command: c})
		next[p] = off + 1
	}
	res, err := h.e.Step(sim.TickInput{Records: recs})
	if err != nil {
		h.t.Fatal(err)
	}
	return res
}

// tick applies cmds in one tick, in order, then runs the retry pass.
func (h *hx) tick(cmds ...*logv1.LoggedCommand) (sim.StepResult, []sim.HandoffRetry) {
	h.t.Helper()
	res := h.step(cmds...)
	return res, h.e.DueHandoffs(res.Tick)
}

// idle runs n ticks with no input and returns every retry produced, by tick.
func (h *hx) idle(n int) map[sim.Tick][]sim.HandoffRetry {
	h.t.Helper()
	out := map[sim.Tick][]sim.HandoffRetry{}
	for i := 0; i < n; i++ {
		res, due := h.tick()
		if len(due) > 0 {
			out[res.Tick] = due
		}
	}
	return out
}

func (h *hx) zone(id string) *sim.ZoneState { return h.e.State().Zones[sim.ZoneID(id)] }

// held is the Entity in zone's Entities, or nil.
func (h *hx) held(zone, id string) *sim.EntityState {
	return h.zone(zone).Entities[sim.EntityID(id)]
}

// entityOnce asserts no two Zones hold one Entity ID in Entities, which is
// what a Transit copy is allowed to be the exception to, and never.
func (h *hx) entityOnce() {
	h.t.Helper()
	seen := map[sim.EntityID]sim.ZoneID{}
	for zid, z := range h.e.State().Zones {
		for id := range z.Entities {
			if other, dup := seen[id]; dup {
				h.t.Fatalf("Entity %s is in the Entities of both %s and %s", id, other, zid)
			}
			seen[id] = zid
		}
	}
}

func arrivalOf(t *testing.T, cmds []*logv1.LoggedCommand) *logv1.LoggedCommand {
	t.Helper()
	for _, c := range cmds {
		if c.GetArrive() != nil {
			return c
		}
	}
	t.Fatalf("no Arrive in %v", cmds)
	return nil
}

func ackOf(t *testing.T, cmds []*logv1.LoggedCommand) *logv1.LoggedCommand {
	t.Helper()
	for _, c := range cmds {
		if c.GetHandoffAck() != nil {
			return c
		}
	}
	t.Fatalf("no HandoffAck in %v", cmds)
	return nil
}

// cloneArrive is a copy of an Arrive Command, so a test can deliver the same
// one twice without the first apply's mutation reaching the second.
func cloneArrive(c *logv1.LoggedCommand) *logv1.LoggedCommand {
	return proto.Clone(c).(*logv1.LoggedCommand)
}

// walk moves id through exits, delivering each Arrive and applying each ack,
// and returns the Arrive Commands produced, in order.
func (h *hx) walk(id string, steps ...[2]string) []*logv1.LoggedCommand {
	h.t.Helper()
	var arrives []*logv1.LoggedCommand
	for _, s := range steps {
		res, _ := h.tick(simtest.Move(s[0], id, s[1]))
		arr := arrivalOf(h.t, res.Outbound)
		arrives = append(arrives, arr)
		res2, _ := h.tick(cloneArrive(arr))
		h.tick(ackOf(h.t, res2.Outbound))
		h.entityOnce()
	}
	return arrives
}

// AC-1: a lost Arrive leaves the Character in the source's Transit, answers
// its Commands in_transit, is produced again after the retry interval with
// the same sequence and Entity bytes, and on delivery the target places it,
// acks, and the record is gone on the tick that applies the ack, with its
// schedule entry.
func TestHandoff_ALostArriveIsRetriedAndThenAcknowledged(t *testing.T) {
	h := newHx(t, func(c *sim.Config) { c.HandoffRetryTicks = 5 })
	simtest.Place(h.e, "alice", "town", "plaza")
	res, due := h.tick(simtest.Move("town", "alice", "east"))
	first := arrivalOf(t, res.Outbound)
	departed := res.Tick
	if len(due) != 0 {
		t.Fatalf("the departure tick re-sent the Arrive: %v", due)
	}
	if h.held("town", "alice") != nil || len(h.zone("town").Transit) != 1 || h.e.HandoffScheduleSize() != 1 {
		t.Fatalf("alice should be in town's Transit with one schedule entry: %+v", h.zone("town").Transit)
	}
	// Its Commands are answered in_transit until the ack.
	res, _ = h.tick(simtest.Look("town", "alice"))
	if rej := rejection(t, res.Events); rej.GetCode() != sim.CodeInTransit {
		t.Fatalf("rejection = %v, want in_transit", rej)
	}

	// Nothing is produced before the interval; one Arrive at it.
	var retry *logv1.LoggedCommand
	for res.Tick < departed+5 {
		var due []sim.HandoffRetry
		res, due = h.tick()
		if res.Tick < departed+5 && len(due) != 0 {
			t.Fatalf("a retry on tick %d, before departure %d + 5", res.Tick, departed)
		}
		if res.Tick == departed+5 {
			if len(due) != 1 {
				t.Fatalf("retries on tick %d = %v, want one", res.Tick, due)
			}
			retry = due[0].Command
		}
	}
	if !proto.Equal(first.GetArrive(), retry.GetArrive()) {
		t.Fatalf("the retry differs from the first Arrive:\nfirst %v\nretry %v", first.GetArrive(), retry.GetArrive())
	}
	if retry.GetSessionId() != "" || retry.GetClientRef() != "" || retry.GetTraceId() != "" || retry.GetActorId() != "alice" {
		t.Fatalf("a retry carries no session, client ref or trace, and names the Entity: %v", retry)
	}

	// Delivery: the target places it and acks; the source drops the record.
	res2, _ := h.tick(retry)
	if h.held("wilds", "alice") == nil {
		t.Fatal("wilds did not place alice")
	}
	ack := ackOf(t, res2.Outbound)
	if ack.GetZoneId() != "town" || ack.GetHandoffAck().GetHandoffSeq() != 1 {
		t.Fatalf("ack = %v", ack)
	}
	if len(h.zone("town").Transit) != 1 {
		t.Fatal("the record went before the ack was applied")
	}
	h.tick(ack)
	if len(h.zone("town").Transit) != 0 || h.e.HandoffScheduleSize() != 0 {
		t.Fatalf("after the ack: transit %v, schedule entries %d", h.zone("town").Transit, h.e.HandoffScheduleSize())
	}
	h.entityOnce()
}

// AC-3: a retried Arrive delivered twice is re-acked and changes nothing: no
// Event, and the Zone's canonical bytes and marks are as they were.
func TestHandoff_ADuplicateArriveIsReAckedAndChangesNothing(t *testing.T) {
	h := newHx(t, nil)
	simtest.Place(h.e, "alice", "town", "plaza")
	res, _ := h.tick(simtest.Move("town", "alice", "east"))
	arr := arrivalOf(t, res.Outbound)
	h.tick(cloneArrive(arr))
	before := sim.ZoneCanonicalBytes(h.zone("wilds"))
	res2, _ := h.tick(cloneArrive(arr))
	if len(res2.Events) != 0 {
		t.Fatalf("a duplicate Arrive emitted %v", res2.Events)
	}
	if got := ackOf(t, res2.Outbound); got.GetHandoffAck().GetHandoffSeq() != 1 {
		t.Fatalf("not re-acked: %v", res2.Outbound)
	}
	if !bytes.Equal(before, sim.ZoneCanonicalBytes(h.zone("wilds"))) {
		t.Fatal("a duplicate Arrive changed the Zone")
	}
	if res2.StaleArrivals != 0 {
		t.Fatalf("a retry of an Arrive the Zone holds is not stale; StaleArrivals = %d", res2.StaleArrivals)
	}
}

// AC-4: alice moved A→B→C and a retry of Arrive(seq 1) reaches B after it left
// with seq 2: stale, placed nowhere, acked, however long after, because B's
// mark is 1 and nothing prunes it.
func TestHandoff_ALateRetryIsStaleHoweverLateItComes(t *testing.T) {
	h := newHx(t, nil)
	simtest.Place(h.e, "alice", "town", "plaza")
	// town → wilds (seq 1) → town (seq 2): the retry of seq 1 reaches wilds
	// after alice left it.
	arrives := h.walk("alice", [2]string{"town", "east"}, [2]string{"wilds", "west"})
	if h.held("wilds", "alice") != nil || h.held("town", "alice") == nil {
		t.Fatal("alice should be back in town")
	}
	h.idle(1000) // more than any retry window
	before := sim.ZoneCanonicalBytes(h.zone("wilds"))
	res, _ := h.tick(cloneArrive(arrives[0]))
	if h.held("wilds", "alice") != nil {
		t.Fatal("a stale retry placed a second copy of alice")
	}
	if res.StaleArrivals != 1 || len(res.Events) != 0 {
		t.Fatalf("stale arrivals %d, events %v: want one stale, no Event", res.StaleArrivals, res.Events)
	}
	if got := ackOf(t, res.Outbound).GetHandoffAck(); got.GetEntityId() != "alice" || got.GetHandoffSeq() != 1 {
		t.Fatalf("ack = %v", got)
	}
	if !bytes.Equal(before, sim.ZoneCanonicalBytes(h.zone("wilds"))) {
		t.Fatal("a stale Arrive changed the Zone")
	}
	if h.zone("wilds").Placed["alice"].Seq != 1 {
		t.Fatalf("wilds' mark for alice = %d, want 1", h.zone("wilds").Placed["alice"].Seq)
	}
}

// AC-5: A→B→C→B (seq 1, 2, 3): a retry of seq 1 reaches B while it holds the
// Entity at 3, and is stale. And a mark is per Entity, not per Zone: Y arriving
// for the first time at seq 1 is placed though X was decided here at 5.
func TestHandoff_AMarkIsPerEntity(t *testing.T) {
	h := newHx(t, nil)
	simtest.Place(h.e, "alice", "wilds", "trail")
	arrives := h.walk("alice", [2]string{"wilds", "west"}, [2]string{"town", "south"}, [2]string{"docks", "north"})
	if h.held("town", "alice") == nil || h.held("town", "alice").HandoffSeq != 3 {
		t.Fatalf("alice should be in town at seq 3: %+v", h.held("town", "alice"))
	}
	res, _ := h.tick(cloneArrive(arrives[0]))
	if res.StaleArrivals != 1 || h.held("town", "alice").HandoffSeq != 3 || len(res.Events) != 0 {
		t.Fatalf("a retry of seq 1 at a Zone holding seq 3: stale %d, events %v", res.StaleArrivals, res.Events)
	}

	// Two Entities: X decided here at 5, Y arriving for the first time at 1.
	h.zone("town").Placed["x"] = sim.PlacedMark{Seq: 5}
	y := sim.EntityState{ID: "y", Template: "andara.core.Character", ContentVersion: "core@1", HandoffSeq: 1}
	res, _ = h.tick(&logv1.LoggedCommand{ZoneId: "town", ActorId: "y", Command: &logv1.LoggedCommand_Arrive{Arrive: &logv1.Arrive{
		RoomId: "plaza", Entity: y.Proto(), OriginZoneId: "wilds", OriginRoomId: "trail", HandoffSeq: 1,
	}}})
	if h.held("town", "y") == nil || res.StaleArrivals != 0 || h.zone("town").Placed["y"].Seq != 1 {
		t.Fatalf("y was not placed by its own first arrival: held=%v stale=%d marks=%v", h.held("town", "y"), res.StaleArrivals, h.zone("town").Placed)
	}
}

// AC-14: an ack is matched by Entity and sequence. A late ack(e, 1) reaching a
// source that holds Transit(e, 3) leaves the record; ack(e, 3) drops it.
func TestHandoff_AnAckMatchesBySequenceNotJustEntity(t *testing.T) {
	h := newHx(t, nil)
	ent := sim.EntityState{ID: "e", Template: "andara.core.Character", ContentVersion: "core@1", Room: "plaza", HandoffSeq: 3}
	h.zone("town").Transit = map[sim.EntityID]sim.TransitRecord{"e": {Entity: ent, To: "wilds", Room: "trail", Direction: "east"}}
	ack := func(seq uint64) *logv1.LoggedCommand {
		return &logv1.LoggedCommand{ZoneId: "town", ActorId: "e", Command: &logv1.LoggedCommand_HandoffAck{HandoffAck: &logv1.HandoffAck{EntityId: "e", HandoffSeq: seq}}}
	}
	h.tick(ack(1))
	if len(h.zone("town").Transit) != 1 {
		t.Fatal("a late ack for seq 1 dropped the record for seq 3: the Entity is lost if Arrive(e, 3) was")
	}
	h.tick(ack(4)) // an ack from the future matches nothing either
	if len(h.zone("town").Transit) != 1 {
		t.Fatal("an ack for a sequence the record doesn't hold dropped it")
	}
	h.tick(ack(3))
	if len(h.zone("town").Transit) != 0 {
		t.Fatal("the matching ack did not drop the record")
	}
	h.tick(ack(3)) // a duplicate is ignored
}

// AC-12: an Arrive into a Zone that holds the Entity in its own Transit with a
// lower sequence proves the target placed it and moved it on: the record is
// dropped, then the arrival is decided by the dedup rule.
func TestHandoff_AnArriveIsAnImplicitAckOfALowerTransitRecord(t *testing.T) {
	h := newHx(t, nil)
	simtest.Place(h.e, "alice", "town", "plaza")
	h.tick(simtest.Move("town", "alice", "east")) // town holds Transit(alice, 1); the Arrive is lost
	if len(h.zone("town").Transit) != 1 {
		t.Fatal("no transit record")
	}
	// alice went on from wilds back to town at seq 2.
	back := sim.EntityState{ID: "alice", Template: "andara.core.Character", ContentVersion: "core@1", HandoffSeq: 2}
	res, _ := h.tick(&logv1.LoggedCommand{ZoneId: "town", ActorId: "alice", Command: &logv1.LoggedCommand_Arrive{Arrive: &logv1.Arrive{
		RoomId: "plaza", FromDirection: "east", Entity: back.Proto(), OriginZoneId: "wilds", OriginRoomId: "trail", HandoffSeq: 2,
	}}})
	if len(h.zone("town").Transit) != 0 || h.e.HandoffScheduleSize() != 0 {
		t.Fatalf("the transit record outlived the implicit ack: %v, schedule %d", h.zone("town").Transit, h.e.HandoffScheduleSize())
	}
	if h.held("town", "alice") == nil || h.zone("town").Placed["alice"].Seq != 2 {
		t.Fatalf("the arrival was not decided after the drop: held %v marks %v", h.held("town", "alice"), h.zone("town").Placed)
	}
	if len(ofType(res.Events, sim.EvCharacterArrived)) != 1 {
		t.Fatalf("events = %v", res.Events)
	}
}

// AC-9: what no sequence of marks can explain is rejected, and places nothing.
func TestHandoff_ImpossibleArrivesAreRejected(t *testing.T) {
	arr := func(zone string, e *sim.EntityState, seq uint64) *logv1.LoggedCommand {
		a := &logv1.Arrive{RoomId: "plaza", OriginZoneId: "wilds", OriginRoomId: "trail", HandoffSeq: seq}
		if e != nil {
			a.Entity = e.Proto()
		}
		return &logv1.LoggedCommand{ZoneId: zone, ActorId: "alice", Command: &logv1.LoggedCommand_Arrive{Arrive: a}}
	}
	alice := func(seq uint64) *sim.EntityState {
		return &sim.EntityState{ID: "alice", Template: "andara.core.Character", ContentVersion: "core@1", HandoffSeq: seq}
	}
	cases := []struct {
		name  string
		setup func(h *hx)
		cmd   *logv1.LoggedCommand
		code  string
	}{
		{"held, seq above the mark", func(h *hx) { simtest.Place(h.e, "alice", "town", "plaza") }, arr("town", alice(2), 2), sim.CodeEntityPresent},
		{"held in its own Transit at the same seq", func(h *hx) {
			e := alice(2)
			h.zone("town").Transit = map[sim.EntityID]sim.TransitRecord{"alice": {Entity: *e, To: "wilds", Room: "trail"}}
		}, arr("town", alice(2), 2), sim.CodeEntityPresent},
		{"held in its own Transit at a higher seq", func(h *hx) {
			e := alice(5)
			h.zone("town").Transit = map[sim.EntityID]sim.TransitRecord{"alice": {Entity: *e, To: "wilds", Room: "trail"}}
		}, arr("town", alice(2), 2), sim.CodeEntityPresent},
		{"seq 0", nil, arr("town", alice(0), 0), sim.CodeInvalidArrival},
		{"seq differs from the entity's", nil, arr("town", alice(3), 2), sim.CodeInvalidArrival},
		{"no entity", nil, arr("town", nil, 1), sim.CodeInvalidArrival},
		{"empty entity id", nil, arr("town", &sim.EntityState{Template: "andara.core.Character", HandoffSeq: 1}, 1), sim.CodeInvalidArrival},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHx(t, nil)
			if c.setup != nil {
				c.setup(h)
			}
			before := sim.ZoneCanonicalBytes(h.zone("town"))
			res, _ := h.tick(c.cmd)
			if rej := rejection(t, res.Events); rej.GetCode() != c.code {
				t.Fatalf("code = %q, want %q", rej.GetCode(), c.code)
			}
			if !bytes.Equal(before, sim.ZoneCanonicalBytes(h.zone("town"))) {
				t.Fatal("a rejected Arrive changed the Zone")
			}
			if len(res.Outbound) != 0 {
				t.Fatalf("a rejected Arrive produced %v", res.Outbound)
			}
		})
	}
}

// AC-8: every Command for an Entity in transit is rejected in_transit, after
// the log, and nothing is applied: the verbs, UnbindCharacter and MarkLinkdead.
// And a linkdead actor can't depart.
func TestHandoff_CommandsForAnEntityInTransitAreRejected(t *testing.T) {
	cmds := map[string]*logv1.LoggedCommand{
		"look":           simtest.Look("town", "alice"),
		"move":           simtest.Move("town", "alice", "south"),
		"goto":           simtest.Goto("town", "alice", "docks", "pier"),
		"unbind":         simtest.Unbind("town", "alice"),
		"mark linkdead":  simtest.MarkLinkdead("town", "alice", 30, 10, 100),
		"bind (in town)": simtest.Bind("town", "alice", "Alice", "plaza"),
	}
	for name, cmd := range cmds {
		t.Run(name, func(t *testing.T) {
			h := newHx(t, nil)
			simtest.Place(h.e, "alice", "town", "plaza")
			h.tick(simtest.Move("town", "alice", "east"))
			before := sim.ZoneCanonicalBytes(h.zone("town"))
			res, _ := h.tick(cmd)
			if rej := rejection(t, res.Events); rej.GetCode() != sim.CodeInTransit {
				t.Fatalf("rejection = %v, want in_transit", rej)
			}
			if !bytes.Equal(before, sim.ZoneCanonicalBytes(h.zone("town"))) {
				t.Fatal("a command for an Entity in transit changed the Zone")
			}
			if len(res.Outbound) != 0 {
				t.Fatalf("produced %v", res.Outbound)
			}
		})
	}
}

func TestHandoff_ALinkdeadBodyCannotDepart(t *testing.T) {
	for name, cmd := range map[string]*logv1.LoggedCommand{
		"move across a Zone": simtest.Move("town", "alice", "east"),
		"move in the Zone":   simtest.Move("town", "alice", "north"),
		"goto":               simtest.Goto("town", "alice", "docks", "pier"),
	} {
		t.Run(name, func(t *testing.T) {
			h := newHx(t, nil)
			simtest.Place(h.e, "alice", "town", "plaza")
			h.tick(simtest.MarkLinkdead("town", "alice", 30, 10, 100))
			res, _ := h.tick(cmd)
			if rej := rejection(t, res.Events); rej.GetCode() != sim.CodeActorLinkdead {
				t.Fatalf("rejection = %v, want actor_linkdead", rej)
			}
			if got := h.held("town", "alice"); got == nil || got.Room != "plaza" || len(h.zone("town").Transit) != 0 || len(res.Outbound) != 0 {
				t.Fatalf("the body moved: %+v, transit %v, outbound %v", got, h.zone("town").Transit, res.Outbound)
			}
		})
	}
}

// AC-15: a Goto across Zones goes through the same Transit, Arrive and
// HandoffAck path as a Move, with an empty Direction.
func TestHandoff_AGotoUsesTheSameHandshake(t *testing.T) {
	h := newHx(t, nil)
	simtest.Place(h.e, "alice", "town", "plaza")
	res, _ := h.tick(simtest.Goto("town", "alice", "docks", "warehouse"))
	rec, ok := h.zone("town").Transit["alice"]
	if !ok || rec.Direction != "" || rec.To != "docks" || rec.Room != "warehouse" || rec.Entity.HandoffSeq != 1 || rec.Entity.Room != "plaza" {
		t.Fatalf("transit = %+v", h.zone("town").Transit)
	}
	arr := arrivalOf(t, res.Outbound)
	if arr.GetArrive().GetFromDirection() != "" || arr.GetArrive().GetHandoffSeq() != 1 || arr.GetArrive().GetOriginZoneId() != "town" {
		t.Fatalf("arrive = %v", arr.GetArrive())
	}
	res2, _ := h.tick(arr)
	h.tick(ackOf(t, res2.Outbound))
	if len(h.zone("town").Transit) != 0 || h.held("docks", "alice") == nil {
		t.Fatal("the Goto did not complete the handshake")
	}
}

// AC-11: a Bind for a Character in transit, at the source, is rejected
// in_transit and creates no body. When the target has placed the Entity and
// the source hasn't applied the ack, the Bind finds it in Entities, where it
// is. Across the whole exchange no two Zones hold one ID in Entities.
func TestHandoff_ABindNeverMakesASecondBody(t *testing.T) {
	h := newHx(t, nil)
	step := func(cmds ...*logv1.LoggedCommand) sim.StepResult {
		res, _ := h.tick(cmds...)
		h.entityOnce()
		return res
	}
	// Spawn through a Bind so the Character exists as the roster would make it.
	step(simtest.Bind("town", "ch-1", "Aldric", "plaza"))
	res := step(simtest.Move("town", "ch-1", "east"))
	arr := arrivalOf(t, res.Outbound)

	res = step(simtest.Bind("town", "ch-1", "Aldric", "plaza")) // at the source, in transit
	if rej := rejection(t, res.Events); rej.GetCode() != sim.CodeInTransit {
		t.Fatalf("a Bind for a Character in transit: %v", rej)
	}
	if h.held("town", "ch-1") != nil || len(h.zone("town").Transit) != 1 {
		t.Fatal("a Bind in transit created a body or lost the record")
	}

	// The target places it; the source hasn't applied the ack: in both.
	res = step(cloneArrive(arr))
	ack := ackOf(t, res.Outbound)
	if h.held("wilds", "ch-1") == nil || len(h.zone("town").Transit) != 1 {
		t.Fatal("the Entity should be placed in wilds and still in town's Transit")
	}
	res = step(simtest.Bind("town", "ch-1", "Aldric", "plaza"))
	if len(ofType(res.Events, sim.EvCommandRejected)) != 0 || h.held("town", "ch-1") != nil {
		t.Fatalf("the Bind should have found the Entity in wilds: events %v", res.Events)
	}
	step(ack)
	if len(h.zone("town").Transit) != 0 {
		t.Fatal("the ack did not drop the record")
	}
}

// AC-16: a Bind that would create a body for an ID some Zone holds a mark for
// is refused id_reused, and nothing is created.
func TestHandoff_AnEntityIDIsNeverReused(t *testing.T) {
	h := newHx(t, nil)
	h.tick(simtest.Bind("town", "ch-1", "Aldric", "plaza"))
	h.walk("ch-1", [2]string{"town", "east"})
	// The body is gone from the World (a later story's purge); the mark stays.
	delete(h.zone("wilds").Entities, "ch-1")
	res, _ := h.tick(simtest.Bind("town", "ch-1", "Aldric", "plaza"))
	if rej := rejection(t, res.Events); rej.GetCode() != sim.CodeIDReused {
		t.Fatalf("rejection = %v, want id_reused", rej)
	}
	if h.held("town", "ch-1") != nil {
		t.Fatal("id_reused created a body")
	}
}

// AC-6 (hash half): the same records replayed from boundaries hash the same,
// including through a retry, and the schedule isn't in the hash: a recovery
// under a retuned config still matches.
func TestHandoff_ReplayMatchesAndTheScheduleIsNotHashed(t *testing.T) {
	h := newHx(t, func(c *sim.Config) { c.HandoffRetryTicks = 4 })
	simtest.Place(h.e, "alice", "town", "plaza")
	var cmds [][]*logv1.LoggedCommand
	var boundaries []sim.TickCompleted
	record := func(cs ...*logv1.LoggedCommand) {
		res, due := h.tick(cs...)
		cmds = append(cmds, cs)
		boundaries = append(boundaries, res.Completed)
		_ = due
	}
	record(simtest.Move("town", "alice", "east"))
	for i := 0; i < 12; i++ { // retries are produced, never delivered
		record()
	}
	if len(h.zone("town").Transit) != 1 {
		t.Fatal("setup: alice should still be in transit")
	}

	for _, cfg := range []func(*sim.Config){
		nil,
		func(c *sim.Config) { c.HandoffRetryTicks, c.HandoffRetryMaxTicks = 50, 500 },
	} {
		w, _ := simtest.CrossingWorld()
		reg, _ := simtest.Templates()
		c := sim.Config{Seed: 7, Partitions: simtest.AllPartitions(), Handlers: sim.Handlers()}
		if cfg != nil {
			cfg(&c)
		}
		replica := sim.NewEngine(w, reg, c)
		simtest.Place(replica, "alice", "town", "plaza")
		src := newLogSource(cmds)
		if err := replica.Replay(boundaries, src); err != nil {
			t.Fatalf("replay: %v", err)
		}
		if replica.StateHash() != h.e.StateHash() {
			t.Fatal("a replayed World hashes differently")
		}
		// Replay wrote no schedule entry, even for a departure inside the
		// replayed range, so the record is due on the first live call.
		if n := replica.HandoffScheduleSize(); n != 0 {
			t.Fatalf("replay wrote %d schedule entries", n)
		}
		if due := replica.DueHandoffs(replica.Tick() + 1); len(due) != 1 || due[0].Entity != "alice" {
			t.Fatalf("the first live call after a replay should retry alice: %v", due)
		}
	}
}

// logSource serves the records a test log held, by Partition and offset.
type logSource struct{ byPart map[int32][]sim.Record }

func newLogSource(cmds [][]*logv1.LoggedCommand) *logSource {
	s := &logSource{byPart: map[int32][]sim.Record{}}
	for _, tick := range cmds {
		for _, c := range tick {
			p := sim.PartitionFor(sim.ZoneID(c.GetZoneId()))
			s.byPart[p] = append(s.byPart[p], sim.Record{Partition: p, Offset: int64(len(s.byPart[p])), Command: c})
		}
	}
	return s
}

func (s *logSource) Fetch(p int32, from, to int64) ([]sim.Record, error) {
	recs := s.byPart[p]
	if int64(len(recs)) < to {
		return nil, fmt.Errorf("partition %d has %d records, want up to %d", p, len(recs), to)
	}
	return slices.Clone(recs[from:to]), nil
}

// AC-6 (recovery half): a World restored from a snapshot with n records in
// Transit has no schedule, so every record is due on the first live ticks, at
// most the batch per tick, none re-sent twice, and the restore itself
// produced no retry.
func TestHandoff_ARecoveredWorldRetriesEveryRecordWithinTheBatchCap(t *testing.T) {
	const n, batch = 7, 3
	h := newHx(t, func(c *sim.Config) { c.HandoffRetryTicks = 10; c.HandoffRetryBatch = batch })
	var cmds []*logv1.LoggedCommand
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("e%02d", i)
		simtest.Place(h.e, id, "town", "plaza")
		cmds = append(cmds, simtest.Move("town", id, "east"))
	}
	h.tick(cmds...)
	if len(h.zone("town").Transit) != n {
		t.Fatalf("setup: %d in transit", len(h.zone("town").Transit))
	}

	snaps := h.e.SnapshotAll(0)
	w, _ := simtest.CrossingWorld()
	reg, _ := simtest.Templates()
	restored := sim.NewEngine(w, reg, sim.Config{Seed: 7, Partitions: simtest.AllPartitions(), Handlers: sim.Handlers(), HandoffRetryTicks: 10, HandoffRetryBatch: batch})
	for _, s := range snaps {
		restored.State().Zones[s.Zone] = s.Body().Clone()
	}
	restored.State().Tick = h.e.Tick()
	for p, o := range h.e.State().Offsets {
		restored.State().Offsets[p] = o
	}
	if restored.HandoffScheduleSize() != 0 {
		t.Fatal("a restored engine has schedule entries")
	}

	seen := map[sim.EntityID]int{}
	ticks := 0
	for len(seen) < n && ticks < 10 {
		ticks++
		res, err := restored.Step(sim.TickInput{})
		if err != nil {
			t.Fatal(err)
		}
		due := restored.DueHandoffs(res.Tick)
		if len(due) > batch {
			t.Fatalf("tick %d produced %d retries, over the batch of %d", ticks, len(due), batch)
		}
		for _, d := range due {
			seen[d.Entity]++
		}
	}
	if want := (n + batch - 1) / batch; ticks != want || len(seen) != n {
		t.Fatalf("every record retried within ceil(%d/%d) = %d ticks: took %d, saw %d of %d", n, batch, want, ticks, len(seen), n)
	}
	for id, c := range seen {
		if c != 1 {
			t.Errorf("%s retried %d times in the recovery burst", id, c)
		}
	}
}

// AC-7: no Arrive is produced for a faulted source Zone, and the first tick it
// can apply again retries. The gaps between attempts double from the first
// interval up to the maximum and stay there, past 64 attempts.
func TestHandoff_NoRetryWhileTheSourceIsFaulted(t *testing.T) {
	h := newHx(t, func(c *sim.Config) { c.HandoffRetryTicks = 3 })
	simtest.Place(h.e, "alice", "town", "plaza")
	h.tick(simtest.Move("town", "alice", "east"))
	h.zone("town").Faulted = true
	if got := h.idle(30); len(got) != 0 {
		t.Fatalf("retries while the source is faulted: %v", got)
	}
	h.zone("town").Faulted = false
	if _, due := h.tick(); len(due) != 1 {
		t.Fatalf("the first tick the Zone can apply again should retry: %v", due)
	}
}

func TestHandoff_TheBackoffDoublesToTheMaximumAndStaysThere(t *testing.T) {
	h := newHx(t, func(c *sim.Config) { c.HandoffRetryTicks = 1; c.HandoffRetryMaxTicks = 5 })
	simtest.Place(h.e, "alice", "town", "plaza")
	res, _ := h.tick(simtest.Move("town", "alice", "east"))
	last := res.Tick
	var gaps []sim.Tick
	attempts := 1
	for _, due := range sortedDue(h.idle(700)) {
		for _, d := range due.rs {
			gaps = append(gaps, due.tick-last)
			last = due.tick
			attempts = d.Attempt
		}
	}
	want := []sim.Tick{1, 2, 4, 5, 5, 5}
	if !slices.Equal(gaps[:len(want)], want) {
		t.Fatalf("gaps = %v, want it to open %v", gaps[:12], want)
	}
	for i, g := range gaps[3:] {
		if g != 5 {
			t.Fatalf("gap %d is %d: the interval must stay at the maximum, never wrap to zero, at attempt counts of 64 and beyond", i+3, g)
		}
	}
	if attempts < 70 {
		t.Fatalf("only %d attempts in 700 ticks; the test must reach 64 and beyond", attempts)
	}
}

type dueAt struct {
	tick sim.Tick
	rs   []sim.HandoffRetry
}

func sortedDue(m map[sim.Tick][]sim.HandoffRetry) []dueAt {
	var out []dueAt
	for t, rs := range m {
		out = append(out, dueAt{t, rs})
	}
	slices.SortFunc(out, func(a, b dueAt) int { return int(a.tick) - int(b.tick) })
	return out
}

// DueHandoffs orders earliest due first, then by Entity ID. It shows only when
// the batch cap cuts a set that became due at different times: here three
// records, a Zone that couldn't apply for a while, then a batch of one.
func TestHandoff_DueRecordsAreOrderedEarliestFirstThenByEntityID(t *testing.T) {
	h := newHx(t, func(c *sim.Config) { c.HandoffRetryTicks = 4; c.HandoffRetryBatch = 1 })
	for _, id := range []string{"a", "b", "c"} {
		simtest.Place(h.e, id, "town", "plaza")
	}
	// Departures on ticks 1, 2, 3 in the order c, a, b: due at 5, 6, 7.
	h.tick(simtest.Move("town", "c", "east"))
	h.tick(simtest.Move("town", "a", "east"))
	h.tick(simtest.Move("town", "b", "east"))
	h.zone("town").Faulted = true
	h.idle(20)
	h.zone("town").Faulted = false
	var order []string
	for i := 0; i < 3; i++ {
		_, due := h.tick()
		if len(due) != 1 {
			t.Fatalf("batch of one returned %d retries", len(due))
		}
		order = append(order, string(due[0].Entity))
	}
	if want := []string{"c", "a", "b"}; !slices.Equal(order, want) {
		t.Fatalf("retry order = %v, want %v: earliest due first (c, a, b), not by Entity ID (a, b, c)", order, want)
	}
	// Due at the same time, Entity ID breaks the tie.
	h2 := newHx(t, func(c *sim.Config) { c.HandoffRetryTicks = 4; c.HandoffRetryBatch = 10 })
	for _, id := range []string{"c", "a", "b"} {
		simtest.Place(h2.e, id, "town", "plaza")
	}
	h2.tick(simtest.Move("town", "c", "east"), simtest.Move("town", "a", "east"), simtest.Move("town", "b", "east"))
	var tied []string
	for _, due := range sortedDue(h2.idle(5)) {
		for _, d := range due.rs {
			tied = append(tied, string(d.Entity))
		}
	}
	if want := []string{"a", "b", "c"}; !slices.Equal(tied, want) {
		t.Fatalf("records due together = %v, want Entity-ID order %v", tied, want)
	}
}

// AC-13: a Zone with Transit records and Placed marks, snapshotted and
// restored, hashes the same; BodyStateHash refuses what it would hash
// differently from how it was written.
func TestHandoff_SnapshotCarriesTransitAndMarks(t *testing.T) {
	h := newHx(t, nil)
	simtest.Place(h.e, "alice", "town", "plaza")
	simtest.Place(h.e, "bob", "wilds", "trail")
	h.tick(simtest.Move("town", "alice", "east"))
	h.walk("bob", [2]string{"wilds", "west"})
	snaps := h.e.SnapshotAll(0)
	for _, s := range snaps {
		body := s.BodyProto()
		got, err := sim.BodyStateHash(body)
		if err != nil {
			t.Fatalf("%s: %v", s.Zone, err)
		}
		if got != s.StateHash() {
			t.Fatalf("%s: the body's hash differs from the snapshot's", s.Zone)
		}
		restored := sim.ZoneStateFromProto(body)
		if sim.HashZone(restored) != sim.HashZone(h.zone(string(s.Zone))) {
			t.Fatalf("%s: HashZone differs after a restore", s.Zone)
		}
	}
	// The Zones that hold records: town a Transit record, town a mark for bob.
	town := h.zone("town")
	if len(town.Transit) != 1 || town.Placed["bob"].Seq != 1 {
		t.Fatalf("setup: transit %v placed %v", town.Transit, town.Placed)
	}
}

// A body whose transit or placed records the hash would read differently is
// refused.
func TestHandoff_BodyStateHashRefusesWhatItWouldReadDifferently(t *testing.T) {
	fresh := func() *sim.Snapshot {
		h := newHx(t, nil)
		for _, id := range []string{"alice", "carol"} {
			simtest.Place(h.e, id, "town", "plaza")
			h.tick(simtest.Move("town", id, "east"))
		}
		simtest.Place(h.e, "dave", "town", "hall")
		h.zone("town").Placed = map[sim.EntityID]sim.PlacedMark{"x": {Seq: 1}, "y": {Seq: 2}}
		for _, s := range h.e.SnapshotAll(0) {
			if s.Zone == "town" {
				return &s
			}
		}
		t.Fatal("no town snapshot")
		return nil
	}
	check := func(name string, tamper func(b *statev1.ZoneState)) {
		t.Run(name, func(t *testing.T) {
			s := fresh()
			body := s.BodyProto()
			tamper(body)
			if _, err := sim.BodyStateHash(body); err == nil {
				t.Fatal("BodyStateHash accepted it")
			}
		})
	}
	check("transit unsorted", func(b *statev1.ZoneState) { b.Transit[0], b.Transit[1] = b.Transit[1], b.Transit[0] })
	check("transit duplicated", func(b *statev1.ZoneState) { b.Transit[1] = b.Transit[0] })
	check("placed unsorted", func(b *statev1.ZoneState) { b.Placed[0], b.Placed[1] = b.Placed[1], b.Placed[0] })
	check("placed duplicated", func(b *statev1.ZoneState) { b.Placed[1] = b.Placed[0] })
	check("placed mark of 0", func(b *statev1.ZoneState) { b.Placed[0].HandoffSeq = 0 })
	check("an Entity in both entities and transit", func(b *statev1.ZoneState) { b.Entities = append(b.Entities, b.Transit[0].Entity) })
	check("a dormant Entity in transit", func(b *statev1.ZoneState) { b.Transit[0].Entity.Dormant = true })
	check("a linkdead Entity in transit", func(b *statev1.ZoneState) { b.Transit[0].Entity.LinkdeadDeadlineTick = 9 })
}

// AC-6: a replay that fails part way leaves the engine live: the flag that
// stops replay writing schedule entries is cleared by defer, so a boundary gap
// or a hash mismatch can't leave a departure that follows unscheduled.
func TestHandoff_AFailedReplayLeavesTheEngineLive(t *testing.T) {
	h := newHx(t, nil)
	simtest.Place(h.e, "alice", "town", "plaza")
	simtest.Place(h.e, "bob", "town", "plaza")
	// A boundary for a tick that doesn't follow the engine's: ErrBoundaryGap.
	err := h.e.Replay([]sim.TickCompleted{{Tick: 5}}, newLogSource(nil))
	if err == nil {
		t.Fatal("the replay of a gapped log succeeded")
	}
	h.step(simtest.Move("town", "alice", "east"))
	if n := h.e.HandoffScheduleSize(); n != 1 {
		t.Fatalf("a live departure after a failed replay wrote %d schedule entries, want 1", n)
	}
	// And a hash mismatch, the other early return.
	h2 := newHx(t, nil)
	simtest.Place(h2.e, "alice", "town", "plaza")
	err = h2.e.Replay([]sim.TickCompleted{{Tick: 1, StateVersion: sim.StateVersion, StateHash: [32]byte{9}}}, newLogSource(nil))
	if err == nil {
		t.Fatal("a replay that mismatched succeeded")
	}
	h2.step(simtest.Move("town", "alice", "east"))
	if n := h2.e.HandoffScheduleSize(); n != 1 {
		t.Fatalf("a live departure after a hash mismatch wrote %d schedule entries, want 1", n)
	}
}

// An Arrive whose Room the content no longer has lands in the fallback Room
// and sets the mark all the same, so a duplicate is re-acked and a stale one
// is stale, never entity_present.
func TestHandoff_AnArriveAtTheFallbackSetsTheMark(t *testing.T) {
	h := newHx(t, nil)
	simtest.Place(h.e, "alice", "town", "plaza")
	res, _ := h.tick(simtest.Move("town", "alice", "east"))
	arr := arrivalOf(t, res.Outbound)
	arr.GetArrive().RoomId = "vanished"
	h.tick(cloneArrive(arr))
	fallback := h.e.World().Zones["wilds"].Fallback
	if got := h.held("wilds", "alice"); got == nil || got.Room != fallback {
		t.Fatalf("alice = %+v, want her in %s", got, fallback)
	}
	if m := h.zone("wilds").Placed["alice"]; m.Seq != 1 || m.Rejected {
		t.Fatalf("the fallback landing set the mark to %+v, want {1 false}", m)
	}
	res2, _ := h.tick(cloneArrive(arr))
	if len(ofType(res2.Events, sim.EvCommandRejected)) != 0 || ackOf(t, res2.Outbound).GetHandoffAck().GetHandoffSeq() != 1 {
		t.Fatalf("a duplicate of a fallback landing: events %v outbound %v", res2.Events, res2.Outbound)
	}
}

// AC-7, the other half: nothing is produced for a healthy Zone whose
// Partition is frozen by another Zone's fault. A Partition freezes for every
// Zone that hashes to it.
func TestHandoff_NoRetryWhileThePartitionIsFrozen(t *testing.T) {
	seen := map[int32]string{}
	var a, b string
	for i := 0; a == ""; i++ {
		id := fmt.Sprintf("z%d", i)
		p := sim.PartitionFor(sim.ZoneID(id))
		if o, ok := seen[p]; ok {
			a, b = o, id
		}
		seen[p] = id
	}
	zone := func(id string) sim.Input {
		return sim.Input{File: id + ".json", Def: &contentv1.ZoneDefinition{FormatVersion: 1, Id: id, Name: id, FallbackRoom: "r",
			Rooms: []*contentv1.RoomDefinition{{Id: "r", Title: "r", Description: "r"}}}}
	}
	w, errs := sim.BuildWorld([]sim.Input{zone(a), zone(b), zone("elsewhere")}, sim.Options{})
	for _, e := range errs {
		if !sim.IsWarning(e, false) {
			t.Fatal(e)
		}
	}
	e := sim.NewEngine(w, nil, sim.Config{Seed: 1, Partitions: simtest.AllPartitions(), Handlers: sim.Handlers()})
	e.State().Zones[sim.ZoneID(a)].Transit = map[sim.EntityID]sim.TransitRecord{
		"e": {Entity: sim.EntityState{ID: "e", Template: "andara.core.Character", Room: "r", HandoffSeq: 1}, To: "elsewhere", Room: "r"},
	}
	// b shares a's Partition and faults: a is healthy and frozen.
	e.State().Zones[sim.ZoneID(b)].Faulted = true
	if e.State().Zones[sim.ZoneID(a)].Faulted {
		t.Fatal("setup: a should be healthy")
	}
	if due := e.DueHandoffs(1); len(due) != 0 {
		t.Fatalf("a retry for a Zone on a frozen Partition: %v", due)
	}
	e.State().Zones[sim.ZoneID(b)].Faulted = false
	if due := e.DueHandoffs(2); len(due) != 1 {
		t.Fatalf("the first call after the Partition thaws should retry: %v", due)
	}
}

// A mark that records a rejection (AW-SRV-027 sets it) is carried and hashed:
// it round-trips through the body, losing the flag changes HashZone, and a
// body that holds the Entity at the rejected sequence is refused. A retry of
// a rejected handoff is neither placed nor acked here: 027 reissues the
// rejection.
func TestHandoff_ARejectedMarkIsCarriedHashedAndNeverAcked(t *testing.T) {
	h := newHx(t, nil)
	h.zone("wilds").Placed = map[sim.EntityID]sim.PlacedMark{"ghost": {Seq: 2, Rejected: true}}
	var snap sim.Snapshot
	for _, s := range h.e.SnapshotAll(0) {
		if s.Zone == "wilds" {
			snap = s
		}
	}
	body := snap.BodyProto()
	if len(body.GetPlaced()) != 1 || !body.GetPlaced()[0].GetRejected() {
		t.Fatalf("the body lost the flag: %v", body.GetPlaced())
	}
	if got := sim.ZoneStateFromProto(body); got.Placed["ghost"] != (sim.PlacedMark{Seq: 2, Rejected: true}) || sim.HashZone(got) != sim.HashZone(h.zone("wilds")) {
		t.Fatalf("the mark did not round-trip: %+v", got.Placed)
	}
	lost := proto.Clone(body).(*statev1.ZoneState)
	lost.GetPlaced()[0].Rejected = false
	if sim.HashZone(sim.ZoneStateFromProto(lost)) == sim.HashZone(h.zone("wilds")) {
		t.Fatal("restoring a mark with the flag lost leaves HashZone unchanged")
	}
	held := proto.Clone(body).(*statev1.ZoneState)
	held.Entities = append(held.Entities, &statev1.EntityState{EntityId: "ghost", Template: "andara.core.Character", HandoffSeq: 2})
	if _, err := sim.BodyStateHash(held); err == nil {
		t.Fatal("BodyStateHash accepted a rejected mark whose Entity is held at that sequence")
	}
	// A retry of the rejected handoff: nothing placed, no ack.
	before := sim.ZoneCanonicalBytes(h.zone("wilds"))
	ghost := sim.EntityState{ID: "ghost", Template: "andara.core.Character", ContentVersion: "core@1", HandoffSeq: 2}
	res, _ := h.tick(&logv1.LoggedCommand{ZoneId: "wilds", ActorId: "ghost", Command: &logv1.LoggedCommand_Arrive{Arrive: &logv1.Arrive{
		RoomId: "trail", Entity: ghost.Proto(), OriginZoneId: "town", OriginRoomId: "plaza", HandoffSeq: 2,
	}}})
	if len(res.Outbound) != 0 || len(res.Events) != 0 || !bytes.Equal(before, sim.ZoneCanonicalBytes(h.zone("wilds"))) {
		t.Fatalf("a retry of a rejected handoff must place nothing and ack nothing: events %v outbound %v", res.Events, res.Outbound)
	}
}

// AC-4, the lost-ack window: B placed alice (seq 1) and its ack was lost, then
// moved her on, so B holds Transit(alice, 2) and its mark for her is 1. A's
// retry of seq 1 reaches B. The mark decides first: it is a retry, stale-acked
// with no rejection, no error and no Event, and A drops its record when the
// ack applies. (Deciding the Transit record first rejected it entity_present.)
func TestHandoff_ALostAckWindowIsStaleAckedAndTheSourceDropsItsRecord(t *testing.T) {
	h := newHx(t, nil)
	simtest.Place(h.e, "alice", "town", "plaza")
	res, _ := h.tick(simtest.Move("town", "alice", "east"))
	first := arrivalOf(t, res.Outbound) // seq 1, town → wilds
	h.tick(cloneArrive(first))          // wilds places her; its ack is lost
	if h.zone("wilds").Placed["alice"].Seq != 1 || len(h.zone("town").Transit) != 1 {
		t.Fatal("setup: wilds should have placed alice at seq 1 with town still holding its record")
	}
	h.tick(simtest.Move("wilds", "alice", "west")) // wilds → town (seq 2), the Arrive lost too
	if rec, ok := h.zone("wilds").Transit["alice"]; !ok || rec.Entity.HandoffSeq != 2 {
		t.Fatalf("setup: wilds should hold Transit(alice, 2): %+v", h.zone("wilds").Transit)
	}

	res, _ = h.tick(cloneArrive(first)) // A's retry of seq 1 reaches wilds
	if len(ofType(res.Events, sim.EvCommandRejected)) != 0 || len(res.Events) != 0 {
		t.Fatalf("the late retry was rejected or emitted: %v", res.Events)
	}
	if res.StaleArrivals != 1 {
		t.Fatalf("stale arrivals = %d, want the late retry counted", res.StaleArrivals)
	}
	ack := ackOf(t, res.Outbound)
	if ack.GetZoneId() != "town" || ack.GetHandoffAck().GetHandoffSeq() != 1 {
		t.Fatalf("ack = %v", ack)
	}
	if rec := h.zone("wilds").Transit["alice"]; rec.Entity.HandoffSeq != 2 {
		t.Fatal("the stale retry disturbed wilds' own Transit record")
	}
	h.tick(ack)
	if len(h.zone("town").Transit) != 0 {
		t.Fatal("town kept its seq-1 record after the ack the late retry earned")
	}
}

// The implicit ack applies only above the mark: an Arrive at or below it is a
// retry however the Zone's own Transit stands, so it can't drop a record.
func TestHandoff_AnArriveAtTheMarkIsNotAnImplicitAck(t *testing.T) {
	h := newHx(t, nil)
	simtest.Place(h.e, "alice", "town", "plaza")
	h.tick(simtest.Move("town", "alice", "east")) // town holds Transit(alice, 1)
	h.zone("town").Placed = map[sim.EntityID]sim.PlacedMark{"alice": {Seq: 1}}
	// An Arrive at seq 1 is at the mark: stale-acked, and town's own Transit(1)
	// stays (it is not an implicit ack: that needs a sequence above the mark).
	e := sim.EntityState{ID: "alice", Template: "andara.core.Character", ContentVersion: "core@1", HandoffSeq: 1}
	res, _ := h.tick(&logv1.LoggedCommand{ZoneId: "town", ActorId: "alice", Command: &logv1.LoggedCommand_Arrive{Arrive: &logv1.Arrive{
		RoomId: "plaza", Entity: e.Proto(), OriginZoneId: "wilds", OriginRoomId: "trail", HandoffSeq: 1,
	}}})
	if len(ofType(res.Events, sim.EvCommandRejected)) != 0 || len(h.zone("town").Transit) != 1 {
		t.Fatalf("an Arrive at the mark dropped or rejected: events %v transit %v", res.Events, h.zone("town").Transit)
	}
	ackOf(t, res.Outbound)
}
