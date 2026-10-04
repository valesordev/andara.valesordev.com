// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package sim

import (
	"sort"

	gamev1 "github.com/valesordev/andara/gen/go/andara/game/v1"
	logv1 "github.com/valesordev/andara/gen/go/andara/log/v1"
)

// The post-log rejection codes (AW-SRV-003's taxonomy, CommandRejected.code).
// Stable, snake_case, additive-only: a client and a dashboard both key on
// them. The set is closed by construction — RejectCodes lists it — which is
// what makes `code` safe as a metric label (CLAUDE.md §7).
const (
	// CodeNoSuchExit: the Room has no Exit in that Direction (validate).
	CodeNoSuchExit = "no_such_exit"
	// CodeExitBlocked: the Exit exists and cannot be taken (validate). No
	// rule produces it yet — doors and locks arrive with their own story —
	// but the code is part of the contract a client already reads.
	CodeExitBlocked = "exit_blocked"
	// CodeActorNotFound: the actor is not in the Zone the Command named
	// (validate).
	CodeActorNotFound = "actor_not_found"
	// CodeInTransit: the actor is between Zones: it left this one and the
	// target's acknowledgement hasn't come back (validate, AW-SRV-028). The
	// Gateway holds a Session's Commands through a crossing, so a well-behaved
	// client reaches this only past ingress.transit_hold.
	CodeInTransit = "in_transit"
	// CodeActorLinkdead: a Move or Goto by a linkdead body (validate). It
	// stays where it stands; log.v1.Entity doesn't carry the linkdead fields,
	// so a body that left would arrive with its despawn timer gone.
	CodeActorLinkdead = "actor_linkdead"
	// CodeEntityPresent: an Arrive for an Entity the Zone already holds, with a
	// sequence its marks don't explain (validate). It can't happen with
	// sequences that only grow, so it is logged at error.
	CodeEntityPresent = "entity_present"
	// CodeInvalidArrival: an Arrive whose handoff_seq is 0 or differs from its
	// entity's, or with no entity or an empty entity id (validate).
	CodeInvalidArrival = "invalid_arrival"
	// CodeIDReused: a BindCharacter that would create a body for an Entity ID
	// some Zone holds a handoff mark for (validate). An Entity ID is never
	// reused: marks are kept for good, and a new body at sequence 0 would have
	// its handoffs read as stale.
	CodeIDReused = "id_reused"
	// CodeUnknownRoom: an Arrive names a Room the Zone no longer has
	// (validate). Content moved under the log; AW-SRV-012 owns that.
	CodeUnknownRoom = "unknown_room"
	// CodeZoneFaulted: the handler panicked; the Zone is quarantined (AW-SRV-002).
	CodeZoneFaulted = "zone_faulted"
	// CodeUnsupportedCommand: no handler for this arm — a binary behind
	// its content (AW-SRV-002).
	CodeUnsupportedCommand = "unsupported_command"
	// CodeUnknownZone: the Command names a Zone this World does not have.
	CodeUnknownZone = "unknown_zone"
	// CodeMisrouted: the Command arrived on a Partition that does not own
	// its Zone.
	CodeMisrouted = "misrouted"
	// CodeRejected: a handler returned an error that was not a RejectError.
	CodeRejected = "rejected"
)

// RejectCodes is every post-log code, sorted, for metric pre-seeding.
func RejectCodes() []string {
	return []string{
		CodeActorLinkdead, CodeActorNotFound, CodeEntityPresent, CodeExitBlocked, CodeIDReused, CodeInTransit,
		CodeInvalidArrival, CodeMisrouted, CodeNoSuchExit, CodeRejected,
		CodeTemplateMissing, CodeUnknownRoom, CodeUnknownZone, CodeUnsupportedCommand, CodeZoneFaulted,
	}
}

// Sentinels for errors.Is, one per validate code. Each carries the
// player-safe message; the Detail a test asserts against lives in Message.
var (
	ErrNoSuchExit    = &RejectError{Code: CodeNoSuchExit, Stage: StageValidate}
	ErrExitBlocked   = &RejectError{Code: CodeExitBlocked, Stage: StageValidate}
	ErrActorNotFound = &RejectError{Code: CodeActorNotFound, Stage: StageValidate}
	ErrRoomGone      = &RejectError{Code: CodeUnknownRoom, Stage: StageValidate}
	ErrInTransit     = &RejectError{Code: CodeInTransit, Stage: StageValidate}
	ErrActorLinkdead = &RejectError{Code: CodeActorLinkdead, Stage: StageValidate}
)

// Handlers is the apply column of the verb table: the handler for every
// CommandKind this binary can act on. Each one is validate then apply, in
// that order, and a validate failure returns before apply runs — the stage
// rule AW-SRV-003 asserts by test.
func Handlers() map[CommandKind]Apply {
	return map[CommandKind]Apply{
		KindLook:            applyLook,
		KindMove:            applyMove,
		KindGoto:            applyGoto,
		KindArrive:          applyArrive,
		KindHandoffAck:      applyHandoffAck,
		KindBindCharacter:   applyBindCharacter,
		KindUnbindCharacter: applyUnbindCharacter,
		KindMarkLinkdead:    applyMarkLinkdead,
	}
}

// --- look --------------------------------------------------------------

// lookView is what validateLook found: the actor and its Room.
type lookView struct {
	actor *EntityState
	room  *Room
}

// validateLook reads state and mutates nothing.
func validateLook(a *ApplyContext, cmd *logv1.LoggedCommand) (lookView, error) {
	actor, room, err := locate(a, cmd)
	return lookView{actor: actor, room: room}, err
}

// applyLook emits exactly one RoomDescribed: title, description, every
// Exit Direction, and every other Entity present, sorted (AC-1). Look
// changes nothing, so its apply is the emit.
func applyLook(a *ApplyContext, cmd *logv1.LoggedCommand) error {
	if !a.Consumed() {
		return ErrNotConsumed
	}
	v, err := validateLook(a, cmd)
	if err != nil {
		return err
	}
	// A description is what one Character saw: addressed to it alone. A
	// bystander in the same Room does not learn what the actor looked at.
	a.Emit(ScopeEntities(v.actor.ID), &gamev1.EventEnvelope{Payload: &gamev1.EventEnvelope_RoomDescribed{RoomDescribed: describe(a.Zone, v.room, v.actor.ID)}})
	return nil
}

// describe renders a Room as seen by viewer: Exits in Direction order,
// occupants by display name, and the linkdead among them, all sorted; the
// viewer is not an occupant of its own description.
func describe(z *ZoneState, room *Room, viewer EntityID) *gamev1.RoomDescribed {
	out := &gamev1.RoomDescribed{ZoneId: string(z.ID), RoomId: string(room.ID), Title: room.Title, Description: room.Description}
	for _, e := range room.Exits {
		out.Exits = append(out.Exits, string(e.Direction))
	}
	sort.Strings(out.Exits)
	for id, ent := range z.Entities {
		if id != viewer && ent.Room == room.ID && !ent.Dormant {
			out.Occupants = append(out.Occupants, ent.DisplayName())
			if ent.Linkdead() {
				// The linkdead subset of occupants (AW-SRV-015 AC-1).
				out.Linkdead = append(out.Linkdead, ent.DisplayName())
			}
		}
	}
	sort.Strings(out.Occupants)
	sort.Strings(out.Linkdead)
	return out
}

// --- move --------------------------------------------------------------

// moveView is what validateMove found: the actor, its Room, and the Exit
// it will take.
type moveView struct {
	actor *EntityState
	from  *Room
	exit  Exit
}

// validateMove reads state and mutates nothing: ErrActorNotFound if the
// actor is not in this Zone, ErrNoSuchExit if its Room has no Exit in the
// Direction. The message names the Direction and nothing the actor cannot
// perceive.
func validateMove(a *ApplyContext, cmd *logv1.LoggedCommand) (moveView, error) {
	actor, room, err := locate(a, cmd)
	if err != nil {
		return moveView{}, err
	}
	if actor.Linkdead() {
		return moveView{}, &RejectError{Code: CodeActorLinkdead, Stage: StageValidate, Message: "you can't move right now"}
	}
	dir := Direction(cmd.GetMove().GetDirection())
	for _, e := range room.Exits {
		if e.Direction == dir {
			return moveView{actor: actor, from: room, exit: e}, nil
		}
	}
	return moveView{}, &RejectError{Code: CodeNoSuchExit, Stage: StageValidate, Message: "there is no exit " + string(dir)}
}

// applyMove takes the Exit. In-Zone: the actor's Room changes,
// CharacterLeft and CharacterArrived are emitted for source and target
// (AC-2), and the new Room is described to the mover alone (AW-SRV-038). Cross-Zone: the actor leaves this Zone's Entities for its
// Transit and an Arrive is produced to the target Zone's Partition, where it
// resolves on a later tick — never as a call, whichever process owns the
// target (AC-9, ADR-0001 rule 4). The Entity stays in Transit until the
// target's HandoffAck, and the Arrive is retried until it comes
// (AW-SRV-028).
func applyMove(a *ApplyContext, cmd *logv1.LoggedCommand) error {
	if !a.Consumed() {
		return ErrNotConsumed
	}
	v, err := validateMove(a, cmd)
	if err != nil {
		return err
	}
	name := v.actor.DisplayName()
	dir := string(v.exit.Direction)
	// The source Room sees the departure and the target Room the arrival;
	// the mover is addressed on both, so it sees its own move wherever the
	// fan-out currently places it.
	a.Emit(ScopeRoom(a.Zone.ID, v.from.ID).With(v.actor.ID), &gamev1.EventEnvelope{Payload: &gamev1.EventEnvelope_CharacterLeft{CharacterLeft: &gamev1.CharacterLeft{
		ZoneId: string(a.Zone.ID), RoomId: string(v.from.ID), CharacterName: name, ToDirection: dir,
	}}})
	from, _ := v.exit.Direction.Reverse()
	if !v.exit.CrossZone {
		v.actor.Room = v.exit.To.Room
		a.Emit(ScopeRoom(a.Zone.ID, v.exit.To.Room).With(v.actor.ID), &gamev1.EventEnvelope{Payload: &gamev1.EventEnvelope_CharacterArrived{CharacterArrived: &gamev1.CharacterArrived{
			ZoneId: string(a.Zone.ID), RoomId: string(v.exit.To.Room), CharacterName: name, FromDirection: string(from),
		}}})
		// The mover sees where they walked in (AW-SRV-038); a cross-Zone
		// move's Arrive does the same in the target Zone.
		if to, ok := a.World.Resolve(RoomRef{Zone: a.Zone.ID, Room: v.exit.To.Room}); ok {
			describeTo(a, to, v.actor.ID)
		}
		return nil
	}
	a.depart(cmd, v.actor, v.exit.To, v.exit.Direction)
	return nil
}

// --- goto --------------------------------------------------------------

// gotoView is what validateGoto found: the actor, its Room, and the target.
type gotoView struct {
	actor  *EntityState
	from   *Room
	target RoomRef
	to     *Room
}

// validateGoto reads state and mutates nothing: the actor is here, and the
// target Zone and Room exist in the World in effect at this tick. The parser
// resolved both fields before the log, so apply never guesses where the
// actor thought it was.
func validateGoto(a *ApplyContext, cmd *logv1.LoggedCommand) (gotoView, error) {
	actor, room, err := locate(a, cmd)
	if err != nil {
		return gotoView{}, err
	}
	if actor.Linkdead() {
		return gotoView{}, &RejectError{Code: CodeActorLinkdead, Stage: StageValidate, Message: "you can't move right now"}
	}
	g := cmd.GetGoto()
	target := RoomRef{Zone: ZoneID(g.GetTargetZoneId()), Room: RoomID(g.GetTargetRoomId())}
	msg := "there is no room " + string(target.Zone) + "/" + string(target.Room)
	if _, ok := a.World.Zones[target.Zone]; !ok {
		return gotoView{}, &RejectError{Code: CodeUnknownZone, Stage: StageValidate, Message: msg}
	}
	to, ok := a.World.Resolve(target)
	if !ok {
		return gotoView{}, &RejectError{Code: CodeUnknownRoom, Stage: StageValidate, Message: msg}
	}
	return gotoView{actor: actor, from: room, target: target, to: to}, nil
}

// applyGoto jumps the actor to the target Room. The Room it's already in is
// a fresh look and nothing else (AC-6). Within the Zone it's a relocation,
// with CharacterLeft and CharacterArrived carrying no direction and the new
// Room described to the jumper. Across Zones it leaves as a cross-Zone move
// does: gone from this Zone's state, and an Arrive produced to the target
// Zone's Partition, whose apply places and describes it (ADR-0001 rule 4).
func applyGoto(a *ApplyContext, cmd *logv1.LoggedCommand) error {
	if !a.Consumed() {
		return ErrNotConsumed
	}
	v, err := validateGoto(a, cmd)
	if err != nil {
		return err
	}
	from := RoomRef{Zone: a.Zone.ID, Room: v.from.ID}
	a.jump = &JumpResult{From: from, To: v.target}
	if v.target == from {
		describeTo(a, v.from, v.actor.ID)
		return nil
	}
	name := v.actor.DisplayName()
	a.Emit(ScopeRoom(a.Zone.ID, v.from.ID).With(v.actor.ID), &gamev1.EventEnvelope{Payload: &gamev1.EventEnvelope_CharacterLeft{CharacterLeft: &gamev1.CharacterLeft{
		ZoneId: string(a.Zone.ID), RoomId: string(v.from.ID), CharacterName: name,
	}}})
	if v.target.Zone == a.Zone.ID {
		v.actor.Room = v.to.ID
		a.Emit(ScopeRoom(a.Zone.ID, v.to.ID).With(v.actor.ID), &gamev1.EventEnvelope{Payload: &gamev1.EventEnvelope_CharacterArrived{CharacterArrived: &gamev1.CharacterArrived{
			ZoneId: string(a.Zone.ID), RoomId: string(v.to.ID), CharacterName: name,
		}}})
		describeTo(a, v.to, v.actor.ID)
		return nil
	}
	a.depart(cmd, v.actor, v.target, "")
	return nil
}

// --- arrive ------------------------------------------------------------

// depart takes the actor out of this Zone's Entities into its Transit and
// produces the Arrive that carries it to to (AW-SRV-028). The Entity as it
// will arrive keeps the Room it left, which the transit record holds for a
// restore at home, and its handoff sequence is incremented here, so the first
// handoff is 1. dir is the Exit the move took, empty for a Goto. The
// departure is the first attempt, so the schedule has the Arrive as sent this
// tick and it isn't re-sent on it.
func (a *ApplyContext) depart(cmd *logv1.LoggedCommand, actor *EntityState, to RoomRef, dir Direction) {
	actor.HandoffSeq++
	rec := TransitRecord{Entity: *actor.Clone(), To: to.Zone, Room: to.Room, Direction: dir}
	delete(a.Zone.Entities, actor.ID)
	a.Zone.setTransit(rec)
	a.engine.noteHandoff(actor.ID, actor.HandoffSeq, a.Tick)
	a.Produce(&logv1.LoggedCommand{
		ZoneId:    string(to.Zone),
		ActorId:   cmd.GetActorId(),
		SessionId: cmd.GetSessionId(),
		ClientRef: cmd.GetClientRef(),
		TraceId:   cmd.GetTraceId(),
		Command:   &logv1.LoggedCommand_Arrive{Arrive: arriveFor(a.Zone.ID, rec)},
	})
}

// arriveFor is the Arrive a transit record produces, the first time and on
// every retry: the same handoff_seq and the same Entity bytes, which is what
// makes a retry recognizable. origin is the Zone the record is in; the
// target's HandoffAck goes there.
func arriveFor(origin ZoneID, rec TransitRecord) *logv1.Arrive {
	from := ""
	if rev, ok := rec.Direction.Reverse(); ok {
		from = string(rev)
	}
	return &logv1.Arrive{
		RoomId:        string(rec.Room),
		FromDirection: from,
		Entity:        rec.Entity.Proto(),
		OriginZoneId:  string(origin),
		OriginRoomId:  string(rec.Entity.Room),
		HandoffSeq:    rec.Entity.HandoffSeq,
	}
}

// invalidArrival is the Arrive nothing can decide: no Entity or an empty id,
// a sequence of 0 (the source increments before it produces, so the first is
// 1), or one that disagrees with the Entity's own.
func invalidArrival(arr *logv1.Arrive) bool {
	e := arr.GetEntity()
	return e == nil || e.GetId() == "" || arr.GetHandoffSeq() == 0 || arr.GetHandoffSeq() != e.GetHandoffSeq()
}

// ack produces the HandoffAck for an Arrive this Zone decided, to the Zone
// the Arrive came from. An Arrive that names no origin gets none.
func (a *ApplyContext) ack(cmd *logv1.LoggedCommand, id EntityID, seq uint64) {
	origin := cmd.GetArrive().GetOriginZoneId()
	if origin == "" {
		return
	}
	a.Produce(&logv1.LoggedCommand{
		ZoneId:  origin,
		ActorId: string(id),
		TraceId: cmd.GetTraceId(),
		Command: &logv1.LoggedCommand_HandoffAck{HandoffAck: &logv1.HandoffAck{EntityId: string(id), HandoffSeq: seq}},
	})
}

// arriveView is what validateArrive decided about an Arrive: whose handoff
// it is, whether it is an implicit ack of a transit record this Zone holds
// (drop), and whether it is a retry or stale (nothing to place) or new.
type arriveView struct {
	id    EntityID
	seq   uint64
	drop  bool
	stale bool // placed nothing; counted
	retry bool // placed nothing; the Zone already holds it at this sequence
	// reissue: the retry of a handoff this Zone rejected (AW-SRV-027). The
	// same HandoffRejected goes back, never an ack: acking it would make the
	// source drop a record whose Entity was never placed. 027 produces it;
	// until then nothing sets a rejected mark, so this is the data 028 reads.
	reissue bool
}

// validateArrive decides an Arrive by this Zone's own state alone
// (AW-SRV-028), reading and mutating nothing, so the same record does the
// same thing on replay and from any process life, in any order records on
// different Partitions arrive:
//
//   - A malformed Arrive is rejected invalid_arrival.
//   - If this Zone holds the Entity in its own Transit with a lower sequence,
//     the target placed it and moved it on: that is an ack, and the record is
//     dropped before the arrival is decided. A sequence at or above is a
//     misrouted Arrive, rejected entity_present.
//   - At the Zone's mark when the mark records a rejection (AW-SRV-027): a
//     retry of a rejected handoff, which gets the same rejection back and is
//     never acked. Nothing sets such a mark in this story.
//   - At or below the Zone's mark for the Entity, it is a retry or stale and
//     is acked, never placed: untouched if the Zone holds the Entity at that
//     very sequence, counted as stale otherwise. No Event either way.
//   - Above the mark with the Entity held, an invariant violation, rejected
//     entity_present.
//   - Otherwise a new handoff.
func validateArrive(a *ApplyContext, cmd *logv1.LoggedCommand) (arriveView, error) {
	arr := cmd.GetArrive()
	if invalidArrival(arr) {
		return arriveView{}, &RejectError{Code: CodeInvalidArrival, Stage: StageValidate, Message: "the way ahead is gone"}
	}
	v := arriveView{id: EntityID(arr.GetEntity().GetId()), seq: arr.GetHandoffSeq()}
	if rec, ok := a.Zone.Transit[v.id]; ok {
		if rec.Entity.HandoffSeq >= v.seq {
			return arriveView{}, &RejectError{Code: CodeEntityPresent, Stage: StageValidate, Message: "the way ahead is gone"}
		}
		v.drop = true
	}
	held := a.Zone.Entities[v.id]
	switch mark := a.Zone.Placed[v.id]; {
	case v.seq == mark.Seq && mark.Rejected:
		v.reissue = true
	case v.seq <= mark.Seq:
		v.retry = held != nil && held.HandoffSeq == v.seq
		v.stale = !v.retry
	case held != nil:
		return arriveView{}, &RejectError{Code: CodeEntityPresent, Stage: StageValidate, Message: "the way ahead is gone"}
	}
	return v, nil
}

// applyArrive carries out validateArrive's decision. A new handoff is placed,
// its mark set, and acked; a retry or a stale Arrive is acked and nothing
// else. A target Room the content in effect no longer has — a swap removed
// it while the Entity was in transit — lands it in this Zone's fallback Room
// instead, with EntityRelocated{room_removed} (AW-SRV-012): every Zone has a
// fallback, so an arrival is never bounced and never lost.
func applyArrive(a *ApplyContext, cmd *logv1.LoggedCommand) error {
	if !a.Consumed() {
		return ErrNotConsumed
	}
	v, err := validateArrive(a, cmd)
	if err != nil {
		return err
	}
	arr := cmd.GetArrive()
	if v.drop {
		a.dropTransit(v.id)
	}
	if v.reissue {
		return nil // AW-SRV-027 reissues the HandoffRejected here
	}
	if v.stale || v.retry {
		if v.stale {
			a.staleArrival()
		}
		a.ack(cmd, v.id, v.seq)
		return nil
	}
	room, ok := a.World.Resolve(RoomRef{Zone: a.Zone.ID, Room: RoomID(arr.GetRoomId())})
	if !ok {
		zone := a.World.Zones[a.Zone.ID]
		if zone == nil {
			return &RejectError{Code: CodeUnknownRoom, Stage: StageValidate, Message: "the way ahead is gone"}
		}
		fallback := zone.Rooms[zone.Fallback]
		ent := EntityFromProto(arr.GetEntity(), fallback.ID)
		a.Zone.Entities[ent.ID] = &ent
		a.Zone.markPlaced(v.id, v.seq)
		a.Emit(ScopeRoom(a.Zone.ID, fallback.ID).With(ent.ID), &gamev1.EventEnvelope{Payload: &gamev1.EventEnvelope_EntityRelocated{EntityRelocated: &gamev1.EntityRelocated{
			ZoneId: string(a.Zone.ID), EntityName: ent.DisplayName(), FromRoomId: arr.GetRoomId(), ToRoomId: string(fallback.ID), Reason: ReasonRoomRemoved,
		}}})
		describeTo(a, fallback, ent.ID)
		a.ack(cmd, v.id, v.seq)
		return nil
	}
	ent := EntityFromProto(arr.GetEntity(), room.ID)
	a.Zone.Entities[ent.ID] = &ent
	a.Zone.markPlaced(v.id, v.seq)
	a.Emit(ScopeRoom(a.Zone.ID, room.ID).With(ent.ID), &gamev1.EventEnvelope{Payload: &gamev1.EventEnvelope_CharacterArrived{CharacterArrived: &gamev1.CharacterArrived{
		ZoneId: string(a.Zone.ID), RoomId: string(room.ID), CharacterName: ent.DisplayName(), FromDirection: arr.GetFromDirection(),
	}}})
	// Every Arrive describes the Room it lands in to the arrival
	// (AW-SRV-036): an Arrive can't tell a goto from a move, and a player
	// who crossed into another Zone should see where they are.
	describeTo(a, room, ent.ID)
	a.ack(cmd, v.id, v.seq)
	return nil
}

// applyHandoffAck drops the transit record the target's acknowledgement
// names. It matches by Entity and sequence: in A→B→C→B a late ack(e, 1) can
// reach a source holding Transit(e, 3), and matching by Entity alone would
// drop the record and lose the Entity if Arrive(e, 3) was lost. An ack that
// matches no record (a duplicate, or one an implicit ack got ahead of) is
// ignored, deterministically.
func applyHandoffAck(a *ApplyContext, cmd *logv1.LoggedCommand) error {
	if !a.Consumed() {
		return ErrNotConsumed
	}
	ack := cmd.GetHandoffAck()
	id := EntityID(ack.GetEntityId())
	if rec, ok := a.Zone.Transit[id]; ok && rec.Entity.HandoffSeq == ack.GetHandoffSeq() {
		a.dropTransit(id)
	}
	return nil
}

// describeTo emits one RoomDescribed of room to viewer alone, as a look
// does.
func describeTo(a *ApplyContext, room *Room, viewer EntityID) {
	a.Emit(ScopeEntities(viewer), &gamev1.EventEnvelope{Payload: &gamev1.EventEnvelope_RoomDescribed{RoomDescribed: describe(a.Zone, room, viewer)}})
}

// locate finds the Command's actor in this Zone and the Room it stands in.
// ErrInTransit covers an actor this Zone has sent on and not yet had
// acknowledged. ErrActorNotFound covers an actor the Zone does not hold, one with no
// position, and a dormant one — a body no Session drives acts for nobody;
// the message says where the actor is not, never why.
func locate(a *ApplyContext, cmd *logv1.LoggedCommand) (*EntityState, *Room, error) {
	if _, between := a.Zone.Transit[EntityID(cmd.GetActorId())]; between {
		return nil, nil, &RejectError{Code: CodeInTransit, Stage: StageValidate, Message: "you are between places"}
	}
	actor, ok := a.Zone.Entities[EntityID(cmd.GetActorId())]
	if !ok || !actor.Present() {
		return nil, nil, &RejectError{Code: CodeActorNotFound, Stage: StageValidate, Message: "you are not here"}
	}
	room, ok := a.World.Resolve(RoomRef{Zone: a.Zone.ID, Room: actor.Room})
	if !ok {
		return nil, nil, &RejectError{Code: CodeActorNotFound, Stage: StageValidate, Message: "you are not here"}
	}
	return actor, room, nil
}
