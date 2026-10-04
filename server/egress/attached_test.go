// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package egress

import (
	"testing"
	"time"

	gamev1 "github.com/valesordev/andara/gen/go/andara/game/v1"
)

// lastSentOf is what a client's resume point is made of: the ID of the last
// Event the stream sent, read under the Session's lock.
func lastSentOf(t *testing.T, f *fixture, session string) uint64 {
	t.Helper()
	f.e.mu.Lock()
	s := f.e.sessions[session]
	f.e.mu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stream == nil {
		t.Fatalf("session %s has no stream", session)
	}
	return s.stream.lastSent
}

// AW-SRV-011 AC-11: a Command submitted once Attached has arrived has its
// Events on the stream. The hook stands for that Command: it publishes an
// Event as the Attached frame is written, and does not return until the
// Session's ring holds it, so the test doesn't depend on the pump's timing.
// With the Attached write moved above attach, the ring holds the Event
// before the cursor is placed, and the stream skips it.
func TestAttached_ACommandAfterItIsDelivered(t *testing.T) {
	f := newFixture(t, nil)
	f.place("a", "alice", "town", "plaza")
	var id uint64
	a := f.subscribe("a", player, 0, false, func(c *client) {
		c.onAttached = func() {
			id = f.emit(plaza())
			f.settled("a", id)
		}
	})
	defer a.cancel()
	if got := a.attached.GetAttached().GetCursorEventId(); got != 0 {
		t.Errorf("cursor_event_id = %d on a Session that had retained nothing, want 0", got)
	}
	if got := a.next().GetEventId(); got != id {
		t.Fatalf("the Event of a Command submitted after Attached: got %d, want %d", got, id)
	}
	a.quiet()
}

// A resume that holds: Attached carries the resume point, then the Events
// after it.
func TestAttached_ResumeThatHolds(t *testing.T) {
	f := newFixture(t, nil)
	f.place("a", "alice", "town", "plaza")
	a := f.subscribe("a", player, 0, false, nil)
	waitFor(t, func() bool { return counter(t, f.e.Metrics().Streams) == 1 }, "subscribed")
	first, second := f.emit(plaza()), f.emit(plaza())
	if got := ids(a.next(), a.next()); got[0] != first || got[1] != second {
		t.Fatalf("got %v", got)
	}
	a.cancel()
	if err := a.wait(); err == nil {
		t.Fatal("the canceled stream returned no error")
	}
	third, fourth := f.emit(plaza()), f.emit(plaza())
	f.settled("a", fourth)

	b := f.subscribe("a", player, second, false, func(c *client) { c.ended = a.ended })
	defer b.cancel()
	if got := b.attached.GetAttached().GetCursorEventId(); got != second || b.attached.GetEventId() != 0 {
		t.Fatalf("Attached = %v, want cursor_event_id %d at event_id 0", b.attached, second)
	}
	if got := ids(b.next(), b.next()); got[0] != third || got[1] != fourth {
		t.Fatalf("after Attached got %v, want [%d %d]", got, third, fourth)
	}
	b.quiet()
	if got := lastSentOf(t, f, "a"); got != fourth {
		t.Errorf("lastSent = %d, want %d: Attached must not move the resume point", got, fourth)
	}
}

// A resume that can't hold: Attached carries the newest retained ID, then the
// Resync, then live.
func TestAttached_ResumeThatCannotHold(t *testing.T) {
	f := newFixture(t, nil)
	f.place("a", "alice", "town", "plaza")
	a := f.subscribe("a", player, 0, false, nil)
	waitFor(t, func() bool { return counter(t, f.e.Metrics().Streams) == 1 }, "subscribed")
	first := f.emit(plaza())
	a.next()
	a.cancel()
	a.wait()
	var pushed uint64
	for i := 0; i < 20; i++ { // past the window (16)
		pushed = f.emit(plaza())
	}
	f.settled("a", pushed)

	c := f.subscribe("a", player, first, false, func(c *client) { c.ended = a.ended })
	defer c.cancel()
	if got := c.attached.GetAttached().GetCursorEventId(); got != pushed {
		t.Fatalf("Attached.cursor_event_id = %d, want the newest retained, %d", got, pushed)
	}
	if r := c.next().GetResync(); r.GetReason() != ResyncWindowExceeded || r.GetLastEventId() != first {
		t.Fatalf("the frame after Attached = %v, want the window_exceeded Resync", r)
	}
	live := f.emit(plaza())
	if got := c.next().GetEventId(); got != live || got <= pushed {
		t.Fatalf("live Event after the Resync = %d, want %d, greater than the cursor %d", got, live, pushed)
	}
	// An ID the Session was never sent. One stream per Session, so the last
	// one ends first.
	c.cancel()
	c.wait()
	d := f.subscribe("a", player, live+1000, false, func(c *client) { c.ended = a.ended })
	defer d.cancel()
	if d.attached.GetAttached() == nil || d.next().GetResync().GetReason() != ResyncNoHistory {
		t.Fatalf("a resume with no history: Attached then a no_history Resync, got %v", d.attached)
	}
}

// Exactly one Attached per stream, at event_id 0 and counted under its own
// type, and the client's resume point isn't moved by it, nor is a second sent
// when a rebind re-bases the cursor.
func TestAttached_OnePerStreamAndLeavesTheResumePointAlone(t *testing.T) {
	f := newFixture(t, func(o *Options) { o.HeartbeatInterval = 20 * time.Millisecond })
	a := f.subscribe("a", player, 0, false, nil) // nowhere yet: heartbeats only
	defer a.cancel()
	if a.attached.GetEventId() != 0 {
		t.Fatalf("Attached has event_id %d", a.attached.GetEventId())
	}
	if got := lastSentOf(t, f, "a"); got != 0 {
		t.Errorf("lastSent = %d after Attached on a first Subscribe, want 0", got)
	}
	var seen []*gamev1.EventEnvelope
	seen = append(seen, a.next()) // a Heartbeat
	f.place("a", "alice", "town", "plaza")
	f.e.Rebind("a")
	waitFor(t, func() bool { return counter(t, f.hub.Metrics().Drops.WithLabelValues("unsubscribed")) == 1 }, "rebound")
	id := f.emit(plaza())
	for {
		env := a.next()
		seen = append(seen, env)
		if env.GetEventId() == id {
			break
		}
	}
	for _, env := range seen {
		if env.GetAttached() != nil {
			t.Fatalf("a second Attached on the stream: %v", seen)
		}
	}
	if got := counter(t, f.e.Metrics().Sent.WithLabelValues(TypeAttached)); got != 1 {
		t.Errorf("andara_stream_events_sent_total{type=attached} = %v, want 1", got)
	}
}
