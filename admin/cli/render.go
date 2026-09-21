// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package cli

import (
	"fmt"
	"strings"

	"google.golang.org/protobuf/reflect/protoreflect"

	gamev1 "github.com/valesordev/andara/gen/go/andara/game/v1"
)

// renderEvent is the rendering contract of AW-CLI-004: a pure function from
// an EventEnvelope to the lines a player reads, and nothing else. It holds
// no state, reads no clock, and is what the golden-file test covers row by
// row — so the Phase 2 client substitutes a different pure function against
// the same recorded stream rather than reverse-engineering this one.
//
// The lines are the World's voice where the Event carries prose (a Room's
// description, a rejection's message) and the system's where it does not.
// Nothing here says where in the pipeline anything happened: a
// CommandRejected reads exactly like a pre-log refusal printed by the
// Submit path (AC-4, AC-5).
func renderEvent(env *gamev1.EventEnvelope) []string {
	switch p := env.GetPayload().(type) {
	case *gamev1.EventEnvelope_RoomDescribed:
		return renderRoom(p.RoomDescribed)
	case *gamev1.EventEnvelope_CharacterArrived:
		a := p.CharacterArrived
		if a.GetFromDirection() == "" {
			return []string{a.GetCharacterName() + " arrives."}
		}
		return []string{fmt.Sprintf("%s arrives from the %s.", a.GetCharacterName(), a.GetFromDirection())}
	case *gamev1.EventEnvelope_CharacterLeft:
		l := p.CharacterLeft
		if l.GetToDirection() == "" {
			return []string{l.GetCharacterName() + " leaves."}
		}
		return []string{fmt.Sprintf("%s leaves %s.", l.GetCharacterName(), l.GetToDirection())}
	case *gamev1.EventEnvelope_CommandRejected:
		// The message, verbatim: it is player-facing by contract
		// (AW-SRV-003), and the code is for scripts, not people.
		return []string{p.CommandRejected.GetMessage()}
	case *gamev1.EventEnvelope_Heartbeat:
		return nil
	case *gamev1.EventEnvelope_Resync:
		return []string{"You may have missed some events; the world continues from here."}
	case *gamev1.EventEnvelope_ZoneFaulted:
		return []string{"This part of the world has stopped responding; your commands here are not being applied."}
	case *gamev1.EventEnvelope_SubscriberDropped:
		// The reasons the server emits (AW-SRV-011, AW-SRV-008 AC-12),
		// as prose; the token itself stays visible under /protocol.
		switch p.SubscriberDropped.GetReason() {
		case "buffer_full":
			return []string{"The server stopped sending you events: you fell behind."}
		case "revoked":
			return []string{"The server stopped sending you events: your session was revoked."}
		}
		return []string{"The server stopped sending you events."}
	case *gamev1.EventEnvelope_SimulationStopped:
		if r := p.SimulationStopped.GetReason(); r != "" {
			return []string{"The world has stopped: " + r}
		}
		return []string{"The world has stopped."}
	}
	// A payload this client does not know: a newer server's Event under
	// ADR-0007's additive evolution. One generic line, never a crash, and
	// the event_id so the player can name what they saw.
	return []string{fmt.Sprintf("Something happened here that this client cannot describe (event %d).", env.GetEventId())}
}

// renderRoom is RoomDescribed's row: title, description, exits, and who is
// present, in that order.
func renderRoom(r *gamev1.RoomDescribed) []string {
	lines := []string{r.GetTitle()}
	if d := r.GetDescription(); d != "" {
		lines = append(lines, d)
	}
	if exits := r.GetExits(); len(exits) > 0 {
		lines = append(lines, "Exits: "+strings.Join(exits, ", "))
	} else {
		lines = append(lines, "Exits: none")
	}
	if who := r.GetOccupants(); len(who) > 0 {
		lines = append(lines, "Here: "+strings.Join(who, ", "))
	}
	return lines
}

// eventName is the payload's message name — RoomDescribed, Heartbeat — for
// protocol visibility, derived from the oneof field so it cannot disagree
// with what was on the wire. A payload this client does not know reports
// as "unknown".
func eventName(env *gamev1.EventEnvelope) string {
	if env == nil {
		return "unknown"
	}
	m := env.ProtoReflect()
	od := m.Descriptor().Oneofs().ByName("payload")
	if od == nil {
		return "unknown"
	}
	fd := m.WhichOneof(od)
	if fd == nil {
		return "unknown"
	}
	if fd.Kind() == protoreflect.MessageKind {
		return string(fd.Message().Name())
	}
	return string(fd.Name())
}
