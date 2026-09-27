// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package egress

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/valesordev/andara/server/events"
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

// A Rebind of the lost Session that looked its state up before the park
// runs after it (review of #124): e.mu is released before s.rebind is
// taken. By then the teardown has unbound the lost Session, so its
// perception reads empty, and a resubscribe would reset the ring the
// reconnect resumes from. Parked, and again once adopted, the state is left
// alone.
func TestLinkdead_StaleRebindLeavesTheParkedRing(t *testing.T) {
	f := newFixture(t, func(o *Options) { o.ParkFor = time.Minute })
	f.place("s1", "aldric", "town", "plaza")
	a := f.subscribe("s1", player, 0, false, nil)
	waitFor(t, func() bool { return counter(t, f.e.Metrics().Streams) == 1 }, "subscribed")
	seen := f.emit(plaza())
	a.next()
	f.e.mu.Lock()
	stale := f.e.sessions["s1"] // what a Rebind racing the park holds
	f.e.mu.Unlock()

	a.cancel()
	a.wait()
	f.e.ParkSession("s1")
	f.mu.Lock()
	delete(f.obs, "s1") // the teardown's Unbind
	f.mu.Unlock()
	missed := f.emit(plaza())
	waitFor(t, func() bool {
		stale.mu.Lock()
		defer stale.mu.Unlock()
		return stale.hist.newest >= missed
	}, "the parked pump to retain the missed Event")
	f.e.rebindSession("s1", stale)

	f.place("s2", "aldric", "town", "plaza")
	b := f.subscribe("s2", player, seen, false, nil)
	if got := b.next(); got.GetEventId() != missed {
		t.Fatalf("resumed with %v, want event %d", got, missed)
	}
	// Adopted, and the lost Session's end now signaled: still s2's.
	close(a.ended)
	f.e.rebindSession("s1", stale)
	live := f.emit(plaza())
	if got := b.next().GetEventId(); got != live {
		t.Fatalf("live %d, want %d", got, live)
	}
	b.end()
	b.wait()
	f.forgotten(0, 0)
}

// #127: a Session's first Subscribe that fails — events.max_subscribers
// reached — discards the state it made, while the same connection drops
// and the gateway parks it. ParkSession takes e.mu then s.rebind; the
// failing Subscribe must not take e.mu while it still holds s.rebind, or
// each waits on the other and every later Subscribe, park and adopt on
// the process hangs behind e.mu. The observer hook holds the Subscribe
// under s.rebind until ParkSession is waiting on it (live-assertions.md
// rule 4).
func TestLinkdead_FailedFirstSubscribeRacingThePark(t *testing.T) {
	inObserver, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	var hub *events.Hub
	f := newFixture(t, func(o *Options) {
		o.ParkFor = time.Minute
		hub = events.New(events.Options{Buffer: 64, MaxSubscribers: 1})
		t.Cleanup(hub.Close)
		o.Hub = hub
		next := o.Observers
		o.Observers = ObserverFunc(func(id string) (events.Observer, bool) {
			// The first call is the adopt lookup, before s.rebind; the
			// second is the subscription's own, under it.
			if id == "s1" && calls.Add(1) == 2 {
				close(inObserver)
				<-release
			}
			return next.Observer(id)
		})
	})
	if _, err := hub.Subscribe(context.Background(), events.Subscriber{Observer: events.Observer{Entity: "other"}, Principal: player}); err != nil {
		t.Fatal(err)
	}
	f.place("s1", "aldric", "town", "plaza")
	a := f.subscribe("s1", player, 0, false, nil)
	<-inObserver

	parked := make(chan struct{})
	go func() {
		f.e.ParkSession("s1")
		close(parked)
	}()
	// ParkSession holds e.mu once it is waiting on s.rebind.
	waitFor(t, func() bool {
		if f.e.mu.TryLock() {
			f.e.mu.Unlock()
			return false
		}
		return true
	}, "ParkSession to hold e.mu")
	close(release)

	deadline := time.After(5 * time.Second)
	select {
	case err := <-a.done:
		if err == nil {
			t.Fatal("the first Subscribe succeeded past a full events.max_subscribers")
		}
	case <-deadline:
		t.Fatal("the failed first Subscribe hung: its discard waits on e.mu, which ParkSession holds waiting on s.rebind")
	}
	select {
	case <-parked:
	case <-deadline:
		t.Fatal("ParkSession hung")
	}
	f.e.mu.Lock()
	defer f.e.mu.Unlock()
	if f.e.sessions["s1"] != nil || f.e.parked["aldric"] != nil {
		t.Fatal("a Session that never subscribed left state behind")
	}
}
