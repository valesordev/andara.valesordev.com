// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package sim

import (
	gamev1 "github.com/valesordev/andara/gen/go/andara/game/v1"
	logv1 "github.com/valesordev/andara/gen/go/andara/log/v1"
)

// CharacterTemplate is the Template a Character instantiates (ADR-0010,
// AW-SRV-022): what a body is made of when a Session first enters the
// World as it. What Components it carries is content's to say; the sim
// only requires that it exists.
const CharacterTemplate TemplateRef = "andara.core.Character"

// CodeTemplateMissing: a BindCharacter for a never-bound Character reached
// a World whose content has no andara.core.Character (apply). Content
// behind the binary; the boot refuses this configuration, so the code is
// for a content swap that removed it under the log.
const CodeTemplateMissing = "template_missing"

// --- bind_character ----------------------------------------------------

// applyBindCharacter puts a Session's Character into the World
// (AW-SRV-014). The body is one of three things, decided here from Zone
// state and nothing else, so the same record does the same thing on
// replay:
//
//   - dormant in this Zone: cleared, and CharacterArrived with an empty
//     from_direction emitted to its Room (AC-6) — the Room sees it appear
//     where it was;
//   - present in this Zone: taken where it stands, the Entity untouched
//     (AC-11) — a crash or a failed teardown left it, and the Room never
//     saw it leave, so the arrival is addressed to the Character alone;
//   - absent: instantiated from andara.core.Character at spawn_room_id
//     (AC-5), the arrival emitted there.
//
// A present body is announced to the Character alone in both cases, the
// same Zone and another: the Entity-addressed CharacterArrived is what
// moves the routing table and the Session's perception to where the body
// actually stands, which after a crash need not be where the roster last
// wrote (review of PR #43). The Room learns nothing either way.
//
// A dormant body in another Zone — the roster's last knowledge was stale,
// which a crash after a cross-Zone move leaves behind — is re-routed: the
// same Command is produced to the Zone that holds it (ADR-0001 §4: a later
// tick, never a call), and the arrival that Zone emits moves the Session's
// routing there.
func applyBindCharacter(a *ApplyContext, cmd *logv1.LoggedCommand) error {
	if !a.Consumed() {
		return ErrNotConsumed
	}
	bind := cmd.GetBindCharacter()
	id := EntityID(cmd.GetActorId())
	if id == "" {
		id = EntityID(bind.GetCharacterId())
	}
	if ent, ok := a.Zone.Entities[id]; ok {
		if ent.Name == "" && bind.GetName() != "" {
			ent.Name = bind.GetName()
		}
		if !ent.Dormant {
			a.Emit(ScopeEntities(ent.ID), arrived(a.Zone.ID, ent.Room, ent.DisplayName()))
			a.bound(bind, a.Zone.ID, ent.Room, BindPresent)
			return nil
		}
		ent.Dormant, ent.DormantSince = false, 0
		a.Emit(ScopeRoom(a.Zone.ID, ent.Room).With(ent.ID), arrived(a.Zone.ID, ent.Room, ent.DisplayName()))
		a.bound(bind, a.Zone.ID, ent.Room, BindWoken)
		return nil
	}
	// Elsewhere in this process's World?
	for _, z := range a.State.Zones {
		if z == a.Zone {
			continue
		}
		ent, ok := z.Entities[id]
		if !ok {
			continue
		}
		if !ent.Dormant {
			// Present with no Session, in a Zone the roster did not
			// name: the Character alone hears where it is.
			a.Emit(ScopeEntities(ent.ID), arrived(z.ID, ent.Room, ent.DisplayName()))
			a.bound(bind, z.ID, ent.Room, BindPresent)
			return nil
		}
		a.Produce(&logv1.LoggedCommand{
			ZoneId: string(z.ID), ActorId: cmd.GetActorId(), SessionId: cmd.GetSessionId(),
			ClientRef: cmd.GetClientRef(), TraceId: cmd.GetTraceId(),
			Command: &logv1.LoggedCommand_BindCharacter{BindCharacter: &logv1.BindCharacter{
				CharacterId: bind.GetCharacterId(), AccountId: bind.GetAccountId(), Name: bind.GetName(),
			}},
		})
		a.bound(bind, z.ID, ent.Room, BindRerouted)
		return nil
	}
	// Never bound: a new body at the spawn Room.
	room, ok := a.World.Resolve(RoomRef{Zone: a.Zone.ID, Room: RoomID(bind.GetSpawnRoomId())})
	if !ok {
		return &RejectError{Code: CodeUnknownRoom, Stage: StageValidate, Message: "there is nowhere to arrive"}
	}
	tmpl, ok := a.Templates.Get(CharacterTemplate)
	if !ok {
		return &RejectError{Code: CodeTemplateMissing, Stage: StageValidate, Message: "this world cannot hold a character yet"}
	}
	// The content version is the Template's pack as the log has it in
	// effect (AW-SRV-012), so a replay reproduces it.
	ent := Instantiate(tmpl, id, a.ContentVersion(tmpl))
	ent.Room = room.ID
	ent.Name = bind.GetName()
	a.Zone.Entities[ent.ID] = &ent
	a.Emit(ScopeRoom(a.Zone.ID, room.ID).With(ent.ID), arrived(a.Zone.ID, room.ID, ent.DisplayName()))
	a.bound(bind, a.Zone.ID, room.ID, BindSpawned)
	return nil
}

// bound reports what the bind did, for the Outcome.
func (a *ApplyContext) bound(bind *logv1.BindCharacter, zone ZoneID, room RoomID, body BindBody) {
	a.bind = &BindResult{Account: bind.GetAccountId(), Zone: zone, Room: room, Body: body}
}

// --- unbind_character --------------------------------------------------

// applyUnbindCharacter makes the body dormant where it stands and emits
// CharacterLeft with an empty to_direction to its Room (AC-8): the Room
// sees it go; the body keeps its Room for the next BindCharacter. A body
// this Zone does not hold, or one already dormant, is nothing to do —
// not a rejection: the Session that produced this is gone, and a second
// teardown for the same Session must apply the same as none.
func applyUnbindCharacter(a *ApplyContext, cmd *logv1.LoggedCommand) error {
	if !a.Consumed() {
		return ErrNotConsumed
	}
	id := EntityID(cmd.GetActorId())
	if id == "" {
		id = EntityID(cmd.GetUnbindCharacter().GetCharacterId())
	}
	ent, ok := a.Zone.Entities[id]
	if !ok || !ent.Present() {
		return nil
	}
	ent.Dormant, ent.DormantSince = true, a.Tick
	a.Emit(ScopeRoom(a.Zone.ID, ent.Room).With(ent.ID), &gamev1.EventEnvelope{Payload: &gamev1.EventEnvelope_CharacterLeft{CharacterLeft: &gamev1.CharacterLeft{
		ZoneId: string(a.Zone.ID), RoomId: string(ent.Room), CharacterName: ent.DisplayName(),
	}}})
	return nil
}

func arrived(zone ZoneID, room RoomID, name string) *gamev1.EventEnvelope {
	return &gamev1.EventEnvelope{Payload: &gamev1.EventEnvelope_CharacterArrived{CharacterArrived: &gamev1.CharacterArrived{
		ZoneId: string(zone), RoomId: string(room), CharacterName: name,
	}}}
}

// --- the bodies --------------------------------------------------------

// CharacterCounts is andara_characters_total's input: how many bodies are
// present and how many dormant, over every Zone. A present body with no
// Session counts as present. Read on the loop goroutine only.
type CharacterCounts struct {
	Present int
	Dormant int
}

// Characters counts the bodies. A Character is an Entity whose Template
// chain reaches andara.core.Character; an Entity whose Template the
// registry no longer holds counts by the reference alone.
func (e *Engine) Characters() CharacterCounts {
	var c CharacterCounts
	for _, z := range e.state.Zones {
		for _, ent := range z.Entities {
			if !e.isCharacter(ent) {
				continue
			}
			if ent.Dormant {
				c.Dormant++
			} else {
				c.Present++
			}
		}
	}
	return c
}

// Read from the Entity alone, never from the Template registry: a content
// swap changes what future spawns are, not what an existing body is, and a
// registry lookup would let a swap that re-parents a pack's Template flip
// bodies already in the World (review of #87). Every Character body is
// instantiated from andara.core.Character itself — BindCharacter is the one
// spawn path — so the Template names it. A later story that spawns a subtype
// of Character records the kind on the body at spawn.
func (e *Engine) isCharacter(ent *EntityState) bool {
	return ent != nil && ent.Template == CharacterTemplate
}

// IsCharacter reports whether ent is a Character: its Template chain reaches
// andara.core.Character. Exported for the state projector, which keys a
// Character's record differently from any other Entity's.
func (e *Engine) IsCharacter(ent *EntityState) bool { return e.isCharacter(ent) }
