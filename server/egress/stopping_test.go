// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package egress

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/valesordev/andara/server/auth"
)

// AW-SRV-054 AC-2 and AC-4: ServerStopping goes to an open stream ahead of
// what follows, with event_id 0 and the configured expected_back_seconds, and
// it is a stream frame only: a resume from the last Event replays no
// ServerStopping, because it was never retained.
func TestAnnounceStoppingIsAStreamFrameOutsideTheHistory(t *testing.T) {
	f := newFixture(t, nil)
	p := auth.Principal{AccountID: "acct-alice", Roles: []auth.Role{auth.RolePlayer}}
	f.place("s-alice", "alice", "town", "plaza")
	c := f.subscribe("s-alice", p, 0, false, nil)
	id := f.emit(plaza())
	if got := c.next().GetEventId(); got != id {
		t.Fatalf("first Event %d, want %d", got, id)
	}

	if n := f.e.AnnounceStopping("", 60*time.Second); n != 1 {
		t.Fatalf("AnnounceStopping queued on %d streams, want 1", n)
	}
	env := c.next()
	ss := env.GetServerStopping()
	if ss == nil || env.GetEventId() != 0 || ss.GetExpectedBackSeconds() != 60 || ss.GetMessage() != "" {
		t.Fatalf("frame = %v, want ServerStopping{expected_back_seconds: 60} with event_id 0", env)
	}
	if got := testutil.ToFloat64(f.e.Metrics().Sent.WithLabelValues(TypeServerStopping)); got != 1 {
		t.Fatalf("sent{type=server_stopping} = %v, want 1", got)
	}
	next := f.emit(plaza())
	if got := c.next().GetEventId(); got != next {
		t.Fatalf("after the notice, Event %d, want %d", got, next)
	}
	f.settled("s-alice", next)

	// Not in the Resume Window: the Session's history holds the two Events
	// and nothing else, and a resume from the first one replays only the
	// second.
	f.e.mu.Lock()
	s := f.e.sessions["s-alice"]
	f.e.mu.Unlock()
	s.mu.Lock()
	for seq := s.hist.start; seq < s.hist.end(); seq++ {
		if fr := s.hist.at(seq); fr.env.GetServerStopping() != nil || fr.id == 0 {
			s.mu.Unlock()
			t.Fatalf("the history retains a frame with event_id %d: %v", fr.id, fr.env)
		}
	}
	s.mu.Unlock()
	c.cancel()
	_ = c.wait()
	r := f.subscribe("s-alice", p, id, false, nil)
	if got := r.next(); got.GetEventId() != next {
		t.Fatalf("resume from %d replayed %v, want only Event %d", id, got, next)
	}
	r.quiet()
}
