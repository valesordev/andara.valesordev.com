// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package ingress

import (
	"context"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	gamev1 "github.com/valesordev/andara/gen/go/andara/game/v1"
	"github.com/valesordev/andara/server/command"
	"github.com/valesordev/andara/server/sim"
)

// Bindings is the Gateway's routing view: which Character each Session
// drives, and which Zone that Character was last seen in — the Partition
// its Commands go to. It is Session state, not World state. It implements
// command.Binder for the pipeline and sim.EventSink for the engine, which
// is how it stays current: a CharacterLeft addressed to a bound Character
// puts its Session in transit, and the CharacterArrived that follows
// settles it on the new Zone.
//
// Bind is the seam AW-SRV-014 fills when SelectCharacter lands; nothing in
// this story calls it in production.
type Bindings struct {
	hold time.Duration
	now  func() time.Time
	held prometheus.Gauge
	// OnChange, if set, is called after Bind and Unbind with the Session
	// whose binding changed, outside the lock: the Event stream re-reads
	// where the Session perceives from (egress.Rebind, AW-SRV-011).
	OnChange func(sessionID string)
	// OnZoneChange, if set, is called after a Publish that settles a bound
	// Session's Character in a Zone other than the one it was in — the
	// cross-Zone arrivals this table already watches. The roster writes
	// its position from it (AW-SRV-014). It runs on the tick goroutine,
	// outside the lock, and must not block.
	OnZoneChange func(sessionID string, b command.Binding)

	mu        sync.Mutex
	bySession map[string]*binding
	byActor   map[sim.EntityID]string
}

type binding struct {
	cmd     command.Binding
	transit *transit // nil when settled
}

// transit is one crossing: when it began, and the channel closed when it
// ends. expired is set once the hold ran out, after which the Session's
// Intents are rejected in_transit at once rather than held again.
type transit struct {
	since   time.Time
	settled chan struct{}
	expired bool
}

// NewBindings builds an empty table. hold is ingress.transit_hold; now is
// for tests, nil meaning the wall clock; held is andara_ingress_held_intents
// or nil.
func NewBindings(hold time.Duration, now func() time.Time, held prometheus.Gauge) *Bindings {
	if now == nil {
		now = time.Now
	}
	return &Bindings{hold: hold, now: now, held: held, bySession: map[string]*binding{}, byActor: map[sim.EntityID]string{}}
}

// Bind records that sessionID drives b's Actor in b's Zone, replacing any
// earlier binding of the Session or of the Actor. A transit in progress is
// abandoned: whoever bound knows where the Character is.
func (t *Bindings) Bind(sessionID string, b command.Binding) {
	t.mu.Lock()
	t.unbindLocked(sessionID)
	prev, hadPrev := t.byActor[b.Actor]
	if hadPrev {
		t.unbindLocked(prev)
	}
	t.bySession[sessionID] = &binding{cmd: b}
	t.byActor[b.Actor] = sessionID
	t.mu.Unlock()
	if t.OnChange != nil {
		if hadPrev && prev != sessionID {
			t.OnChange(prev)
		}
		t.OnChange(sessionID)
	}
}

// Unbind forgets sessionID. Intents held for it are released to be
// rejected as unbound.
func (t *Bindings) Unbind(sessionID string) {
	t.mu.Lock()
	_, had := t.bySession[sessionID]
	t.unbindLocked(sessionID)
	t.mu.Unlock()
	if had && t.OnChange != nil {
		t.OnChange(sessionID)
	}
}

func (t *Bindings) unbindLocked(sessionID string) {
	e, ok := t.bySession[sessionID]
	if !ok {
		return
	}
	if e.transit != nil {
		close(e.transit.settled)
	}
	delete(t.bySession, sessionID)
	if t.byActor[e.cmd.Actor] == sessionID {
		delete(t.byActor, e.cmd.Actor)
	}
}

// Lookup is the current binding without waiting: the Binding, whether
// there is one, and whether its Character is in transit.
func (t *Bindings) Lookup(sessionID string) (b command.Binding, bound, inTransit bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	e, ok := t.bySession[sessionID]
	if !ok {
		return command.Binding{}, false, false
	}
	return e.cmd, true, e.transit != nil
}

// Binding implements command.Binder. A Session in transit waits here,
// counted in andara_ingress_held_intents, until its Character arrives,
// ctx ends, or the hold runs out — ingress.transit_hold from the
// CharacterLeft, not from this call, so a stuck handoff surfaces to the
// player at a bounded time rather than to a queue.
func (t *Bindings) Binding(ctx context.Context, sessionID string) (command.Binding, error) {
	for {
		t.mu.Lock()
		e, ok := t.bySession[sessionID]
		if !ok {
			t.mu.Unlock()
			return command.Binding{}, command.ErrNoBinding
		}
		if e.transit == nil {
			b := e.cmd
			t.mu.Unlock()
			return b, nil
		}
		tr := e.transit
		if tr.expired {
			t.mu.Unlock()
			return command.Binding{}, command.ErrBindingInTransit
		}
		wait := t.hold - t.now().Sub(tr.since)
		t.mu.Unlock()

		if wait <= 0 {
			t.expire(sessionID, tr)
			return command.Binding{}, command.ErrBindingInTransit
		}
		if t.held != nil {
			t.held.Inc()
		}
		timer := time.NewTimer(wait)
		select {
		case <-tr.settled:
			timer.Stop()
		case <-ctx.Done():
			timer.Stop()
			if t.held != nil {
				t.held.Dec()
			}
			return command.Binding{}, ctx.Err()
		case <-timer.C:
			if t.held != nil {
				t.held.Dec()
			}
			t.expire(sessionID, tr)
			return command.Binding{}, command.ErrBindingInTransit
		}
		if t.held != nil {
			t.held.Dec()
		}
		// Settled: arrived, restored, or unbound. Read again.
	}
}

// expire marks tr as run out, if it is still the Session's transit.
func (t *Bindings) expire(sessionID string, tr *transit) {
	t.mu.Lock()
	if e, ok := t.bySession[sessionID]; ok && e.transit == tr {
		tr.expired = true
	}
	t.mu.Unlock()
}

// Publish implements sim.EventSink on the tick goroutine: a map update
// under the lock and nothing else. Only the Events addressed to a bound
// Character move it — CharacterLeft begins a transit and clears the Room,
// CharacterArrived ends one on the Zone and Room it names — the same
// reading the events.Hub gives an Observer. HandoffRejected (AW-SRV-028)
// will end one on the origin.
func (t *Bindings) Publish(ev sim.Event) {
	if ev.Type != sim.EvCharacterLeft && ev.Type != sim.EvCharacterArrived && ev.Type != sim.EvEntityRelocated {
		return
	}
	var moved []struct {
		session string
		binding command.Binding
	}
	t.mu.Lock()
	for _, actor := range ev.Scope.Entities {
		sessionID, ok := t.byActor[actor]
		if !ok {
			continue
		}
		e := t.bySession[sessionID]
		switch p := ev.Envelope.GetPayload().(type) {
		case *gamev1.EventEnvelope_CharacterLeft:
			e.cmd.Room = ""
			if e.transit == nil {
				e.transit = &transit{since: t.now(), settled: make(chan struct{})}
			}
		case *gamev1.EventEnvelope_CharacterArrived:
			was := e.cmd.Zone
			e.cmd.Zone = sim.ZoneID(p.CharacterArrived.GetZoneId())
			e.cmd.Room = sim.RoomID(p.CharacterArrived.GetRoomId())
			if was != e.cmd.Zone {
				moved = append(moved, struct {
					session string
					binding command.Binding
				}{sessionID, e.cmd})
			}
			if e.transit != nil {
				close(e.transit.settled)
				e.transit = nil
			}
		case *gamev1.EventEnvelope_EntityRelocated:
			// Same Zone, another Room: a content swap moved the body to its
			// Zone's fallback (AW-SRV-012). No Partition changes.
			e.cmd.Room = sim.RoomID(p.EntityRelocated.GetToRoomId())
		}
	}
	t.mu.Unlock()
	if t.OnZoneChange == nil {
		return
	}
	for _, m := range moved {
		t.OnZoneChange(m.session, m.binding)
	}
}
