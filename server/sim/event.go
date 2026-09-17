// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package sim

import (
	gamev1 "github.com/valesordev/andara/gen/go/andara/game/v1"
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
)

// Event is one thing the simulation said happened. IDs are monotonic across
// ticks and never reused, including across recovery: the counter is World
// state and the State Hash covers it (AW-SRV-004). Scope and redaction are
// AW-SRV-004's; this is the shape they attach to.
type Event struct {
	ID       uint64
	Tick     Tick
	Zone     ZoneID
	Type     EventType
	Envelope *gamev1.EventEnvelope
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
	}
	return ""
}
