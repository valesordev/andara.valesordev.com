// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package sim

import (
	"sort"

	gamev1 "github.com/valesordev/andara/gen/go/andara/game/v1"
	logv1 "github.com/valesordev/andara/gen/go/andara/log/v1"
)

// The linkdead lifecycle (AW-SRV-015, ADR-0006). A body whose Session lost
// its stream stays where it stands, marked linkdead, until a new Session
// selects it (BindCharacter reconnects it) or its deadline Tick is reached
// and the sim despawns it. Every Tick here comes from Zone state and the
// MarkLinkdead that set it, so a replay despawns on the same Tick whatever
// the replaying binary's configuration says.

// KindMarkLinkdead is produced by the Gateway when a playing Session loses
// its stream (AW-SRV-015). It has no verb.
const KindMarkLinkdead CommandKind = "mark_linkdead"

// The CharacterDespawned reasons (event.proto).
const (
	DespawnQuit            = "quit"
	DespawnSwitch          = "switch"
	DespawnLinkdead        = "linkdead"
	DespawnLinkdeadCeiling = "linkdead_ceiling"
)

// LinkdeadKind names one step of a body's linkdead lifecycle.
type LinkdeadKind string

// The steps a LinkdeadChange reports.
const (
	LinkdeadEntered     LinkdeadKind = "entered"
	LinkdeadReconnected LinkdeadKind = "reconnected"
	LinkdeadExtended    LinkdeadKind = "extended"
	// LinkdeadEnded is a linkdead body despawned: its deadline or ceiling
	// was reached, or an UnbindCharacter took it. Reason says which.
	LinkdeadEnded LinkdeadKind = "ended"
)

// LinkdeadChange is one lifecycle step, reported in StepResult.Linkdead for
// the loop's metrics and log lines; the sim neither logs nor counts. Session
// is the Session on the record that caused it, empty for an expiry.
type LinkdeadChange struct {
	Kind      LinkdeadKind
	Zone      ZoneID
	Room      RoomID
	Character EntityID
	Session   string
	TraceID   string
	// Reason is the despawn reason for LinkdeadEnded.
	Reason string
	// Since and Deadline are the body's linkdead_since_tick and its deadline
	// after the step (before it, for LinkdeadEnded and LinkdeadReconnected).
	Since    Tick
	Deadline Tick
}

// applyMarkLinkdead marks a present body linkdead where it stands, with the
// Tick that applies it as linkdead_since_tick and the durations the record
// carries, and emits CharacterLinkdead to its Room. A body that is already
// linkdead, dormant, or not in this Zone is a deterministic no-op (AC-17):
// a drain racing a drop produces it twice.
func applyMarkLinkdead(a *ApplyContext, cmd *logv1.LoggedCommand) error {
	if !a.Consumed() {
		return ErrNotConsumed
	}
	mark := cmd.GetMarkLinkdead()
	id := EntityID(cmd.GetActorId())
	if id == "" {
		id = EntityID(mark.GetCharacterId())
	}
	if _, between := a.Zone.Transit[id]; between {
		// The body is on its way out: marking nothing linkdead would leave the
		// roster waiting for a LinkdeadEnded that never comes (AW-SRV-028).
		return transitReject()
	}
	ent, ok := a.Zone.Entities[id]
	if !ok || !ent.Present() || ent.Linkdead() {
		return nil
	}
	t := a.Tick
	ent.LinkdeadSince = t
	ent.LinkdeadDeadline = t + Tick(mark.GetGraceTicks())
	ent.LinkdeadCeiling = t + Tick(mark.GetMaxTicks())
	ent.LinkdeadExtension = Tick(mark.GetExtensionTicks())
	if ent.LinkdeadDeadline > ent.LinkdeadCeiling {
		ent.LinkdeadDeadline = ent.LinkdeadCeiling
	}
	a.Emit(ScopeRoom(a.Zone.ID, ent.Room).With(ent.ID), &gamev1.EventEnvelope{Payload: &gamev1.EventEnvelope_CharacterLinkdead{CharacterLinkdead: &gamev1.CharacterLinkdead{
		ZoneId: string(a.Zone.ID), RoomId: string(ent.Room), CharacterName: ent.DisplayName(),
	}}})
	a.linkdead(LinkdeadChange{Kind: LinkdeadEntered, Zone: a.Zone.ID, Room: ent.Room, Character: ent.ID, Since: t, Deadline: ent.LinkdeadDeadline})
	return nil
}

// reconnect takes a linkdead body back for a new Session: the four fields
// cleared and CharacterReconnected emitted to its Room in place of
// CharacterArrived — the body never left (AC-2).
func reconnect(a *ApplyContext, ent *EntityState) {
	change := LinkdeadChange{Kind: LinkdeadReconnected, Zone: a.Zone.ID, Room: ent.Room, Character: ent.ID, Since: ent.LinkdeadSince, Deadline: ent.LinkdeadDeadline}
	ent.clearLinkdead()
	a.Emit(ScopeRoom(a.Zone.ID, ent.Room).With(ent.ID), &gamev1.EventEnvelope{Payload: &gamev1.EventEnvelope_CharacterReconnected{CharacterReconnected: &gamev1.CharacterReconnected{
		ZoneId: string(a.Zone.ID), RoomId: string(ent.Room), CharacterName: ent.DisplayName(),
	}}})
	a.linkdead(change)
}

// despawn makes a present body dormant where it stands and emits
// CharacterDespawned{reason} to its Room. A linkdead body's fields are
// cleared, and the end of its grace is reported.
func despawn(zone *ZoneState, ent *EntityState, tick Tick, reason string, emit func(Scope, *gamev1.EventEnvelope), report func(LinkdeadChange)) {
	was := LinkdeadChange{Kind: LinkdeadEnded, Zone: zone.ID, Room: ent.Room, Character: ent.ID, Reason: reason, Since: ent.LinkdeadSince, Deadline: ent.LinkdeadDeadline}
	linkdead := ent.Linkdead()
	ent.Dormant, ent.DormantSince = true, tick
	ent.clearLinkdead()
	emit(ScopeRoom(zone.ID, ent.Room).With(ent.ID), &gamev1.EventEnvelope{Payload: &gamev1.EventEnvelope_CharacterDespawned{CharacterDespawned: &gamev1.CharacterDespawned{
		ZoneId: string(zone.ID), RoomId: string(ent.Room), CharacterName: ent.DisplayName(), Reason: reason,
	}}})
	if linkdead {
		report(was)
	}
}

// expireLinkdead despawns every linkdead body whose deadline is this tick or
// earlier (AC-3), in Zone then Entity order so the Events and their IDs are
// the same on replay. A body at its ceiling despawns with linkdead_ceiling
// (AC-11). A faulted Zone is frozen, and is skipped: its state is not
// advanced by anything until the process restarts.
func (e *Engine) expireLinkdead(tick Tick, emit func(ZoneID, string, string, Scope, *gamev1.EventEnvelope), res *StepResult) {
	for _, zid := range e.state.SortedZoneIDs() {
		z := e.state.Zones[zid]
		if z.Faulted {
			continue
		}
		var due []EntityID
		for id, ent := range z.Entities {
			if ent.Linkdead() && tick >= ent.LinkdeadDeadline {
				due = append(due, id)
			}
		}
		sort.Slice(due, func(i, j int) bool { return due[i] < due[j] })
		for _, id := range due {
			ent := z.Entities[id]
			reason := DespawnLinkdead
			if tick >= ent.LinkdeadCeiling {
				reason = DespawnLinkdeadCeiling
			}
			despawn(z, ent, tick, reason,
				func(s Scope, env *gamev1.EventEnvelope) { emit(zid, "", "", s, env) },
				func(c LinkdeadChange) { res.Linkdead = append(res.Linkdead, c) })
		}
	}
}

// LinkdeadBodies is every linkdead body, sorted: what recovery left, for the
// roster's gauge. Read on the loop goroutine only.
func (e *Engine) LinkdeadBodies() []EntityID {
	var out []EntityID
	for _, z := range e.state.Zones {
		for id, ent := range z.Entities {
			if ent.Linkdead() {
				out = append(out, id)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// OnCombatInteraction is the one hook combat calls (ADR-0006): any Apply
// that constitutes a combat interaction against target calls it, and nothing
// else about linkdead. A linkdead target's deadline becomes
// max(deadline, now + extension), never past its ceiling — a refresh, not an
// accumulation (AC-10). Anything else is untouched. Outside a Step it does
// nothing: only a tick may move a deadline.
func (e *Engine) OnCombatInteraction(target EntityID) {
	if e.step == nil {
		return
	}
	for _, zid := range e.state.SortedZoneIDs() {
		ent, ok := e.state.Zones[zid].Entities[target]
		if !ok {
			continue
		}
		if !ent.Linkdead() {
			return
		}
		next := max(ent.LinkdeadDeadline, e.step.Tick+ent.LinkdeadExtension)
		next = min(next, ent.LinkdeadCeiling)
		if next != ent.LinkdeadDeadline {
			ent.LinkdeadDeadline = next
		}
		e.step.Linkdead = append(e.step.Linkdead, LinkdeadChange{Kind: LinkdeadExtended, Zone: zid, Room: ent.Room, Character: ent.ID, Since: ent.LinkdeadSince, Deadline: ent.LinkdeadDeadline})
		return
	}
}

// OnCombatInteraction is the handler's way to Engine.OnCombatInteraction.
func (a *ApplyContext) OnCombatInteraction(target EntityID) {
	if a.engine != nil {
		a.engine.OnCombatInteraction(target)
	}
}

// linkdead reports a lifecycle step from a handler.
func (a *ApplyContext) linkdead(c LinkdeadChange) {
	c.Session, c.TraceID = a.Record.Command.GetSessionId(), a.Record.Command.GetTraceId()
	if a.engine != nil && a.engine.step != nil {
		a.engine.step.Linkdead = append(a.engine.step.Linkdead, c)
	}
}

// despawnReason is CharacterDespawned.reason for an UnbindCharacter.
func despawnReason(r logv1.UnbindReason) string {
	switch r {
	case logv1.UnbindReason_SWITCH:
		return DespawnSwitch
	case logv1.UnbindReason_LINKDEAD:
		return DespawnLinkdead
	}
	return DespawnQuit
}
