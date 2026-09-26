// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package egress

import (
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"context"
)

// AW-SRV-015 AC-2: a Session that ends linkdead leaves its retained state
// parked under its Character, the pump still filling it; a new Session
// driving that Character adopts it, and a stream carrying last_event_id
// resumes from it with no gap — including what happened while nobody was
// connected.
func TestLinkdead_ReconnectResumesWithNoGap(t *testing.T) {
	f := newFixture(t, func(o *Options) { o.ParkFor = time.Minute })
	f.place("s1", "aldric", "town", "plaza")
	a := f.subscribe("s1", player, 0, false, nil)
	waitFor(t, func() bool { return counter(t, f.e.Metrics().Streams) == 1 }, "subscribed")
	seen := f.emit(plaza())
	if got := a.next().GetEventId(); got != seen {
		t.Fatalf("got %d", got)
	}

	// The stream drops and the Session ends linkdead: the gateway parks it
	// before the Session's context goes.
	f.e.ParkSession("s1")
	a.end()
	if err := a.wait(); err != nil && !errors.Is(err, context.Canceled) && connect.CodeOf(err) != connect.CodeCanceled {
		t.Fatalf("the dropped stream ended with %v", err)
	}
	waitFor(t, func() bool {
		f.e.mu.Lock()
		defer f.e.mu.Unlock()
		return f.e.parked["aldric"] != nil && f.e.sessions["s1"] == nil
	}, "the Session parked under its Character")

	// While nobody is connected, the Room goes on.
	missed1, missed2 := f.emit(plaza()), f.emit(plaza())
	waitFor(t, func() bool {
		f.e.mu.Lock()
		s := f.e.parked["aldric"]
		f.e.mu.Unlock()
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.hist.newest >= missed2
	}, "the parked pump to retain what the Room did")

	// The reconnect: a new Session, bound to the same Character, subscribes
	// with the last Event the old stream saw.
	f.place("s2", "aldric", "town", "plaza")
	b := f.subscribe("s2", player, seen, false, nil)
	if got := ids(b.next(), b.next()); got[0] != missed1 || got[1] != missed2 {
		t.Fatalf("resumed %v, want [%d %d]", got, missed1, missed2)
	}
	b.quiet()
	if got := counter(t, f.e.Metrics().ReconnectResyncs); got != 0 {
		t.Fatalf("andara_reconnect_resyncs_total = %v on a clean reconnect", got)
	}
	live := f.emit(plaza())
	if got := b.next().GetEventId(); got != live {
		t.Fatalf("live %d, want %d", got, live)
	}
	b.end()
	b.wait()
	f.forgotten(0, 0)
}

// A reconnect whose resume point the parked ring no longer reaches is a
// Resync, and counted: the AC-7 signal that the window is too small.
func TestLinkdead_ReconnectPastTheWindowIsCounted(t *testing.T) {
	f := newFixture(t, func(o *Options) { o.ParkFor = time.Minute })
	f.place("s1", "aldric", "town", "plaza")
	a := f.subscribe("s1", player, 0, false, nil)
	waitFor(t, func() bool { return counter(t, f.e.Metrics().Streams) == 1 }, "subscribed")
	seen := f.emit(plaza())
	a.next()
	f.e.ParkSession("s1")
	a.end()
	a.wait()
	var last uint64
	for i := 0; i < 20; i++ { // past the window of 16
		last = f.emit(plaza())
	}
	waitFor(t, func() bool {
		f.e.mu.Lock()
		s := f.e.parked["aldric"]
		f.e.mu.Unlock()
		if s == nil {
			return false
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.hist.newest >= last
	}, "retained")
	f.place("s2", "aldric", "town", "plaza")
	b := f.subscribe("s2", player, seen, false, nil)
	if got := b.next(); got.GetResync().GetReason() != ResyncWindowExceeded {
		t.Fatalf("want a resync, got %v", got)
	}
	if got := counter(t, f.e.Metrics().ReconnectResyncs); got != 1 {
		t.Fatalf("andara_reconnect_resyncs_total = %v", got)
	}
	b.end()
	b.wait()
}

// Parked state nobody adopts is released after ParkFor: the fan-out
// subscription and the ring go. A Session that ends as a quit is never
// parked.
func TestLinkdead_UnadoptedStateIsReleased(t *testing.T) {
	f := newFixture(t, func(o *Options) { o.ParkFor = 50 * time.Millisecond })
	f.place("s1", "aldric", "town", "plaza")
	a := f.subscribe("s1", player, 0, false, nil)
	waitFor(t, func() bool { return counter(t, f.e.Metrics().Streams) == 1 }, "subscribed")
	f.e.ParkSession("s1")
	a.end()
	a.wait()
	f.forgotten(0, 0)
	f.e.mu.Lock()
	n := len(f.e.parked)
	f.e.mu.Unlock()
	if n != 0 {
		t.Fatalf("%d parked after ParkFor", n)
	}

	f.place("s3", "brin", "town", "plaza")
	c := f.subscribe("s3", player, 0, false, nil)
	waitFor(t, func() bool { return counter(t, f.e.Metrics().Streams) == 1 }, "subscribed")
	c.end() // no ParkSession: a quit
	c.wait()
	f.forgotten(0, 0)
	f.e.mu.Lock()
	n = len(f.e.parked)
	f.e.mu.Unlock()
	if n != 0 {
		t.Fatal("a quit was parked")
	}
}

// The park is ParkSession's, done before it returns (#121). Parking when
// forget woke on the Session's end left a window, unordered against the
// reconnect's Subscribe, in which the new Session found nothing to adopt and
// resumed with Resync{no_history}. Here the lost Session's end has not been
// signaled at all when the reconnect subscribes: the widest that window
// gets.
func TestLinkdead_ReconnectBeforeTheOldSessionEnds(t *testing.T) {
	f := newFixture(t, func(o *Options) { o.ParkFor = time.Minute })
	f.place("s1", "aldric", "town", "plaza")
	a := f.subscribe("s1", player, 0, false, nil)
	waitFor(t, func() bool { return counter(t, f.e.Metrics().Streams) == 1 }, "subscribed")
	seen := f.emit(plaza())
	if got := a.next().GetEventId(); got != seen {
		t.Fatalf("got %d", got)
	}

	// The connection is gone and the gateway parks the Session; its
	// context has not been canceled yet.
	a.cancel()
	a.wait()
	f.e.ParkSession("s1")
	missed := f.emit(plaza())

	f.place("s2", "aldric", "town", "plaza")
	b := f.subscribe("s2", player, seen, false, nil)
	if got := b.next(); got.GetEventId() != missed {
		t.Fatalf("resumed with %v, want event %d", got, missed)
	}

	// The lost Session's end arriving now takes nothing from the reconnect.
	close(a.ended)
	live := f.emit(plaza())
	if got := b.next().GetEventId(); got != live {
		t.Fatalf("live %d, want %d", got, live)
	}
	b.end()
	b.wait()
	f.forgotten(0, 0)
}
