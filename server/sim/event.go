// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package sim

import (
	"sort"

	gamev1 "github.com/valesordev/andara/gen/go/andara/game/v1"
	logv1 "github.com/valesordev/andara/gen/go/andara/log/v1"
)

// EventType is the string form of an EventEnvelope payload, derived from
// the oneof so the two cannot disagree (AW-SRV-004).
type EventType string

// The Event types the envelope carries.
const (
	EvRoomDescribed     EventType = "room_described"
	EvCharacterArrived  EventType = "character_arrived"
	EvCharacterLeft     EventType = "character_left"
	EvCommandRejected   EventType = "command_rejected"
	EvZoneFaulted       EventType = "zone_faulted"
	EvSubscriberDropped EventType = "subscriber_dropped"
	EvSimulationStopped EventType = "simulation_stopped"
	EvEntityRelocated   EventType = "entity_relocated"
)

// Scope answers "who may perceive this" (AW-SRV-004). It is computed inside
// the sim at emit, never by a transport: scoping is a security boundary, and
// a filter every transport reimplements is a filter one of them gets wrong.
//
// The shapes: a Room (every observer in it), a Zone (Room.Room empty — every
// Room in it), explicitly addressed Entities, and World — privileged, Game
// Master and Operator visibility only. A shape is additive: an Event scoped
// to a Room and an Entity reaches the Room's observers and that Entity
// wherever it is. An Event with an empty Scope reaches nobody but a World
// observer, which sees every Event (AW-SRV-004 AC-8).
type Scope struct {
	// Room is the Room, or with Room.Room empty the whole Zone. A zero
	// RoomRef is no Room.
	Room RoomRef
	// Entities are explicitly addressed observers, sorted, deduplicated.
	Entities []EntityID
	// World is privileged visibility.
	World bool
}

// ScopeRoom scopes to one Room.
func ScopeRoom(zone ZoneID, room RoomID) Scope { return Scope{Room: RoomRef{Zone: zone, Room: room}} }

// ScopeZone scopes to every Room in a Zone.
func ScopeZone(zone ZoneID) Scope { return Scope{Room: RoomRef{Zone: zone}} }

// ScopeEntities scopes to the named observers.
func ScopeEntities(ids ...EntityID) Scope { return Scope{}.With(ids...) }

// ScopeWorld is privileged visibility only.
func ScopeWorld() Scope { return Scope{World: true} }

// AndWorld adds privileged visibility: the operator sees it whole.
func (s Scope) AndWorld() Scope { s.World = true; return s }

// With adds addressed Entities, keeping the set sorted and unique.
func (s Scope) With(ids ...EntityID) Scope {
	seen := make(map[EntityID]bool, len(s.Entities)+len(ids))
	out := make([]EntityID, 0, len(s.Entities)+len(ids))
	for _, id := range append(append([]EntityID(nil), s.Entities...), ids...) {
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	s.Entities = out
	return s
}

// Zoned reports whether the Scope names a Zone (with or without a Room).
func (s Scope) Zoned() bool { return s.Room.Zone != "" }

// Empty reports whether the Scope names no unprivileged observer.
func (s Scope) Empty() bool { return !s.Zoned() && len(s.Entities) == 0 && !s.World }

// Proto renders the Scope for the log record. Entity IDs are already sorted.
func (s Scope) Proto() *logv1.Scope {
	out := &logv1.Scope{RoomZoneId: string(s.Room.Zone), RoomId: string(s.Room.Room), World: s.World}
	for _, id := range s.Entities {
		out.EntityIds = append(out.EntityIds, string(id))
	}
	return out
}

// ScopeFromProto reads a Scope back from the log record.
func ScopeFromProto(p *logv1.Scope) Scope {
	s := Scope{Room: RoomRef{Zone: ZoneID(p.GetRoomZoneId()), Room: RoomID(p.GetRoomId())}, World: p.GetWorld()}
	for _, id := range p.GetEntityIds() {
		s.Entities = append(s.Entities, EntityID(id))
	}
	return s.With()
}

// Event is one thing the simulation said happened. IDs are monotonic across
// ticks and never reused, including across recovery: the counter is World
// state and the State Hash covers it (AW-SRV-004).
//
// Envelope is the whole Event. Redacted, when set, is the form an
// unprivileged observer receives instead: the sim decides what a player may
// not see and strips it before Publish, so the fan-out chooses a form by
// privilege and never edits one (AW-SRV-004 AC-7).
type Event struct {
	ID       uint64
	Tick     Tick
	Zone     ZoneID
	Type     EventType
	Scope    Scope
	Envelope *gamev1.EventEnvelope
	Redacted *gamev1.EventEnvelope
	// Session is the Session whose Command caused this Event, or empty.
	// In-process correlation only — it is not on the log record — so the
	// fan-out can hand the Command's client_ref to that Session alone.
	Session string
}

// Deliverable returns the form an observer with the given privilege may
// receive: the whole Event for World visibility, else the redacted form
// when the type carries privileged detail.
func (e Event) Deliverable(privileged bool) *gamev1.EventEnvelope {
	if !privileged && e.Redacted != nil {
		return e.Redacted
	}
	return e.Envelope
}

// redact returns the player-safe form of an envelope, or nil when the type
// carries nothing a player may not see. What is privileged: the operator's
// diagnostics — a Zone's internal ID on ZoneFaulted, the reason on
// SimulationStopped. The Event still arrives; its detail does not.
func redact(env *gamev1.EventEnvelope) *gamev1.EventEnvelope {
	switch env.GetPayload().(type) {
	case *gamev1.EventEnvelope_ZoneFaulted:
		return &gamev1.EventEnvelope{EventId: env.EventId, Tick: env.Tick, ClientRef: env.ClientRef,
			Payload: &gamev1.EventEnvelope_ZoneFaulted{ZoneFaulted: &gamev1.ZoneFaulted{}}}
	case *gamev1.EventEnvelope_SimulationStopped:
		return &gamev1.EventEnvelope{EventId: env.EventId, Tick: env.Tick, ClientRef: env.ClientRef,
			Payload: &gamev1.EventEnvelope_SimulationStopped{SimulationStopped: &gamev1.SimulationStopped{}}}
	}
	return nil
}

// EventSink is the seam through which everything outside the core observes
// the World (AW-SRV-004). Publish is called synchronously from the tick and
// must return promptly; fan-out and delivery are the subscriber's problem.
type EventSink interface {
	Publish(Event)
}

// typeOf names an envelope's payload.
func typeOf(env *gamev1.EventEnvelope) EventType {
	switch env.GetPayload().(type) {
	case *gamev1.EventEnvelope_RoomDescribed:
		return EvRoomDescribed
	case *gamev1.EventEnvelope_CharacterArrived:
		return EvCharacterArrived
	case *gamev1.EventEnvelope_CharacterLeft:
		return EvCharacterLeft
	case *gamev1.EventEnvelope_CommandRejected:
		return EvCommandRejected
	case *gamev1.EventEnvelope_ZoneFaulted:
		return EvZoneFaulted
	case *gamev1.EventEnvelope_SubscriberDropped:
		return EvSubscriberDropped
	case *gamev1.EventEnvelope_SimulationStopped:
		return EvSimulationStopped
	case *gamev1.EventEnvelope_EntityRelocated:
		return EvEntityRelocated
	}
	return ""
}

// TypeOf names an envelope's payload; "" for a payload that is not a
// simulation Event (a transport's Heartbeat or Resync). Exported for the state
// projector's Touched table, whose completeness test walks the oneof.
func TypeOf(env *gamev1.EventEnvelope) EventType { return typeOf(env) }

// EventTypes is every EventType the simulation emits.
func EventTypes() []EventType {
	return []EventType{EvRoomDescribed, EvCharacterArrived, EvCharacterLeft, EvCommandRejected, EvZoneFaulted, EvSubscriberDropped, EvSimulationStopped, EvEntityRelocated}
}
