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
	// (validate). Between a cross-Zone departure and the arrival that
	// follows it, this is where the actor is.
	CodeActorNotFound = "actor_not_found"
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
		CodeActorNotFound, CodeExitBlocked, CodeMisrouted, CodeNoSuchExit, CodeRejected,
		CodeUnknownRoom, CodeUnknownZone, CodeUnsupportedCommand, CodeZoneFaulted,
	}
}

// Sentinels for errors.Is, one per validate code. Each carries the
// player-safe message; the Detail a test asserts against lives in Message.
var (
	ErrNoSuchExit    = &RejectError{Code: CodeNoSuchExit, Stage: StageValidate}
	ErrExitBlocked   = &RejectError{Code: CodeExitBlocked, Stage: StageValidate}
	ErrActorNotFound = &RejectError{Code: CodeActorNotFound, Stage: StageValidate}
	ErrRoomGone      = &RejectError{Code: CodeUnknownRoom, Stage: StageValidate}
)

// Handlers is the apply column of the verb table: the handler for every
// CommandKind this binary can act on. Each one is validate then apply, in
// that order, and a validate failure returns before apply runs — the stage
// rule AW-SRV-003 asserts by test.
func Handlers() map[CommandKind]Apply {
	return map[CommandKind]Apply{
		KindLook:   applyLook,
		KindMove:   applyMove,
		KindArrive: applyArrive,
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

// describe renders a Room as seen by viewer: Exits in Direction order and
// occupants by display name, both sorted; the viewer is not an occupant of
// its own description.
func describe(z *ZoneState, room *Room, viewer EntityID) *gamev1.RoomDescribed {
	out := &gamev1.RoomDescribed{ZoneId: string(z.ID), RoomId: string(room.ID), Title: room.Title, Description: room.Description}
	for _, e := range room.Exits {
		out.Exits = append(out.Exits, string(e.Direction))
	}
	sort.Strings(out.Exits)
	for id, ent := range z.Entities {
		if id != viewer && ent.Room == room.ID {
			out.Occupants = append(out.Occupants, ent.DisplayName())
		}
	}
	sort.Strings(out.Occupants)
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
	dir := Direction(cmd.GetMove().GetDirection())
	for _, e := range room.Exits {
		if e.Direction == dir {
			return moveView{actor: actor, from: room, exit: e}, nil
		}
	}
	return moveView{}, &RejectError{Code: CodeNoSuchExit, Stage: StageValidate, Message: "there is no exit " + string(dir)}
}

// applyMove takes the Exit. In-Zone: the actor's Room changes and
// CharacterLeft and CharacterArrived are emitted for source and target
// (AC-2). Cross-Zone: the actor leaves this Zone's state and an Arrive is
// produced to the target Zone's Partition, where it resolves on a later
// tick — never as a call, whichever process owns the target (AC-9,
// ADR-0001 rule 4).
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
		return nil
	}
	delete(a.Zone.Entities, v.actor.ID)
	a.Produce(&logv1.LoggedCommand{
		ZoneId:    string(v.exit.To.Zone),
		ActorId:   cmd.GetActorId(),
		SessionId: cmd.GetSessionId(),
		ClientRef: cmd.GetClientRef(),
		TraceId:   cmd.GetTraceId(),
		Command: &logv1.LoggedCommand_Arrive{Arrive: &logv1.Arrive{
			RoomId:        string(v.exit.To.Room),
			FromDirection: string(from),
			Entity:        v.actor.Proto(),
			OriginZoneId:  string(a.Zone.ID),
			OriginRoomId:  string(v.from.ID),
		}},
	})
	return nil
}

// --- arrive ------------------------------------------------------------

// validateArrive checks the target Room exists in this Zone.
func validateArrive(a *ApplyContext, cmd *logv1.LoggedCommand) (*Room, error) {
	arr := cmd.GetArrive()
	room, ok := a.World.Resolve(RoomRef{Zone: a.Zone.ID, Room: RoomID(arr.GetRoomId())})
	if !ok || arr.GetEntity() == nil {
		return nil, &RejectError{Code: CodeUnknownRoom, Stage: StageValidate, Message: "the way ahead is gone"}
	}
	return room, nil
}

// applyArrive places the Entity and emits CharacterArrived. A target Room
// that no longer exists sends the Entity back where it came from, once:
// the bounce is produced with no origin, so a second failure rejects and
// the Entity is gone — reported, not silently lost.
func applyArrive(a *ApplyContext, cmd *logv1.LoggedCommand) error {
	if !a.Consumed() {
		return ErrNotConsumed
	}
	arr := cmd.GetArrive()
	room, err := validateArrive(a, cmd)
	if err != nil {
		if arr.GetOriginZoneId() != "" && arr.GetEntity() != nil {
			back, _ := Direction(arr.GetFromDirection()).Reverse()
			a.Produce(&logv1.LoggedCommand{
				ZoneId: arr.GetOriginZoneId(), ActorId: cmd.GetActorId(), SessionId: cmd.GetSessionId(),
				ClientRef: cmd.GetClientRef(), TraceId: cmd.GetTraceId(),
				Command: &logv1.LoggedCommand_Arrive{Arrive: &logv1.Arrive{
					RoomId: arr.GetOriginRoomId(), FromDirection: string(back), Entity: arr.GetEntity(),
				}},
			})
		}
		return err
	}
	ent := EntityFromProto(arr.GetEntity(), room.ID)
	a.Zone.Entities[ent.ID] = &ent
	a.Emit(ScopeRoom(a.Zone.ID, room.ID).With(ent.ID), &gamev1.EventEnvelope{Payload: &gamev1.EventEnvelope_CharacterArrived{CharacterArrived: &gamev1.CharacterArrived{
		ZoneId: string(a.Zone.ID), RoomId: string(room.ID), CharacterName: ent.DisplayName(), FromDirection: arr.GetFromDirection(),
	}}})
	return nil
}

// locate finds the Command's actor in this Zone and the Room it stands in.
// ErrActorNotFound covers both an actor the Zone does not hold and one with
// no position; the message says where the actor is not, never why.
func locate(a *ApplyContext, cmd *logv1.LoggedCommand) (*EntityState, *Room, error) {
	actor, ok := a.Zone.Entities[EntityID(cmd.GetActorId())]
	if !ok || actor.Room == "" {
		return nil, nil, &RejectError{Code: CodeActorNotFound, Stage: StageValidate, Message: "you are not here"}
	}
	room, ok := a.World.Resolve(RoomRef{Zone: a.Zone.ID, Room: actor.Room})
	if !ok {
		return nil, nil, &RejectError{Code: CodeActorNotFound, Stage: StageValidate, Message: "you are not here"}
	}
	return actor, room, nil
}
