// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package sim

import (
	gamev1 "github.com/valesordev/andara/gen/go/andara/game/v1"
	logv1 "github.com/valesordev/andara/gen/go/andara/log/v1"
)

// KindPurgeCharacter is produced by the Gateway's retention sweep once a
// deleted Character's retention has run out (AW-SRV-032). It has no verb.
const KindPurgeCharacter CommandKind = "purge_character"

// DespawnPurge is CharacterDespawned.reason for a present body a purge took
// out of its Room (event.proto).
const DespawnPurge = "purge"

// PurgeKind names what one applied PurgeCharacter did.
type PurgeKind string

// The steps a PurgeChange reports.
const (
	// PurgeApplied: the body was removed from Zone state.
	PurgeApplied PurgeKind = "applied"
	// PurgeRerouted: the body is in another Zone, and the same Command was
	// produced to it.
	PurgeRerouted PurgeKind = "rerouted"
	// PurgeNoBody: no Zone holds a body for the Character — created and never
	// bound, or already purged. A deterministic no-op.
	PurgeNoBody PurgeKind = "no_body"
)

// PurgeChange is one applied PurgeCharacter, reported in StepResult.Purges for
// the roster's metrics and log lines; the sim neither logs nor counts.
type PurgeChange struct {
	Kind      PurgeKind
	Zone      ZoneID
	Room      RoomID
	Character EntityID
	// WasPresent: the body stood in its Room when the purge applied (a crash
	// left it), and was despawned first.
	WasPresent bool
	Session    string
	TraceID    string
}

// applyPurgeCharacter removes a deleted Character's body from Zone state
// (AW-SRV-032). The roster's zone_id is best-effort, so the body is looked for
// the way applyBindCharacter looks for it:
//
//   - in this Zone: a present body (a crash leaves one with no Session, and the
//     live flag that would have refused the delete is memory-only) is
//     despawned first, CharacterDespawned{reason: "purge"} to its Room with its
//     linkdead fields cleared; then the body is removed and CharacterPurged
//     emitted to the Character alone — no Room ever saw a dormant body;
//   - in another Zone: only that Zone may change it, so the Command is produced
//     to it (a later tick, ADR-0001 §4);
//   - between Zones: rejected in_transit, an accepted residual until
//     AW-SRV-027;
//   - nowhere: a silent no-op.
//
// Nothing here reads a clock or a config value: it applies when the record
// does, so a replay purges on the same Tick.
func applyPurgeCharacter(a *ApplyContext, cmd *logv1.LoggedCommand) error {
	if !a.Consumed() {
		return ErrNotConsumed
	}
	id := EntityID(cmd.GetActorId())
	if id == "" {
		id = EntityID(cmd.GetPurgeCharacter().GetCharacterId())
	}
	if ent, ok := a.Zone.Entities[id]; ok && a.engine.isCharacter(ent) {
		change := PurgeChange{Kind: PurgeApplied, Zone: a.Zone.ID, Room: ent.Room, Character: id, WasPresent: ent.Present()}
		name := ent.DisplayName()
		if ent.Present() {
			despawn(a.Zone, ent, a.Tick, DespawnPurge, a.Emit, a.linkdead)
		}
		delete(a.Zone.Entities, id)
		a.Emit(ScopeEntities(id), &gamev1.EventEnvelope{Payload: &gamev1.EventEnvelope_CharacterPurged{CharacterPurged: &gamev1.CharacterPurged{
			ZoneId: string(a.Zone.ID), RoomId: string(change.Room), CharacterName: name,
		}}})
		a.purged(change)
		return nil
	}
	for _, zid := range a.State.SortedZoneIDs() {
		z := a.State.Zones[zid]
		if z == a.Zone {
			continue
		}
		ent, ok := z.Entities[id]
		if !ok || !a.engine.isCharacter(ent) {
			continue
		}
		a.Produce(&logv1.LoggedCommand{
			ZoneId: string(z.ID), ActorId: cmd.GetActorId(), SessionId: cmd.GetSessionId(),
			ClientRef: cmd.GetClientRef(), TraceId: cmd.GetTraceId(),
			Command: &logv1.LoggedCommand_PurgeCharacter{PurgeCharacter: &logv1.PurgeCharacter{CharacterId: string(id)}},
		})
		a.purged(PurgeChange{Kind: PurgeRerouted, Zone: z.ID, Room: ent.Room, Character: id, WasPresent: ent.Present()})
		return nil
	}
	for _, zid := range a.State.SortedZoneIDs() {
		if _, between := a.State.Zones[zid].Transit[id]; between {
			return transitReject()
		}
	}
	a.purged(PurgeChange{Kind: PurgeNoBody, Zone: a.Zone.ID, Character: id})
	return nil
}

// purged reports a purge step from a handler.
func (a *ApplyContext) purged(c PurgeChange) {
	c.Session, c.TraceID = a.Record.Command.GetSessionId(), a.Record.Command.GetTraceId()
	if a.engine != nil && a.engine.step != nil {
		a.engine.step.Purges = append(a.engine.step.Purges, c)
	}
}
