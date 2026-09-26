// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package roster_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/valesordev/andara/server/gateway"
	"github.com/valesordev/andara/server/roster"
	"github.com/valesordev/andara/server/sim"
)

// withLinkdead is the 10 Hz defaults: 180 s, 60 s, 300 s.
func withLinkdead(max time.Duration) func(*roster.Options) {
	return func(o *roster.Options) {
		o.Linkdead = roster.LinkdeadTicks{Grace: 1800, Extension: 600, Max: 3000, MaxWall: max}
	}
}

// selected is a fixture with Aldric created and bound to s1.
func selected(t *testing.T, opts ...func(*roster.Options)) (*fixture, string, *gateway.Session) {
	t.Helper()
	f := newFixture(t, opts...)
	id := f.create("Aldric")
	s := f.session("s1")
	if _, err := f.roster.SelectCharacter(context.Background(), s, id); err != nil {
		t.Fatal(err)
	}
	return f, id, s
}

// AC-1 and AC-15, the Gateway's half: a lost stream or a drain produces a
// MarkLinkdead carrying the configured durations in Ticks, not an
// UnbindCharacter. The binding goes with the Session; the flag stays, on the
// linkdead body, and andara_sessions_bound no longer counts it.
func TestRoster_LinkdeadEndMarksTheBody(t *testing.T) {
	f, id, s := selected(t, withLinkdead(time.Hour))
	<-f.roster.ReleaseSession(s, gateway.EndLinkdead)

	recs := f.log.records()
	if len(recs) != 2 {
		t.Fatalf("records %v", recs)
	}
	m := recs[1].GetMarkLinkdead()
	if m == nil || recs[1].GetActorId() != id || recs[1].GetZoneId() != "town" || m.GetCharacterId() != id ||
		m.GetGraceTicks() != 1800 || m.GetExtensionTicks() != 600 || m.GetMaxTicks() != 3000 {
		t.Fatalf("want MarkLinkdead{%s, 1800, 600, 3000} to town, got %v", id, recs[1])
	}
	if _, bound, _ := f.bindings.Lookup("s1"); bound {
		t.Fatal("the binding survived the Session")
	}
	if _, char, ok := f.roster.Live(f.account); !ok || char != id {
		t.Fatalf("the flag was freed: live %v %s", ok, char)
	}
	met := f.metrics()
	if !strings.Contains(met, `andara_character_unbinds_total{outcome="ok",reason="linkdead"} 1`) || !strings.Contains(met, "andara_sessions_bound 0") {
		t.Fatalf("metrics:\n%s", met)
	}
}

// AC-2 and AC-16: while Aldric is linkdead, selecting Aldric is the
// reconnect — a BindCharacter, the flag on the new Session — and selecting
// any other Character of the Account is already_live naming Aldric, with
// nothing produced.
func TestRoster_LinkdeadReconnectAndAlreadyLive(t *testing.T) {
	f, id, s := selected(t, withLinkdead(time.Hour))
	other := f.create("Brin")
	<-f.roster.ReleaseSession(s, gateway.EndLinkdead)
	before := len(f.log.records())

	_, err := f.roster.SelectCharacter(context.Background(), f.session("s2"), other)
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "Aldric") {
		t.Fatalf("select another while linkdead: %v, want already_live naming Aldric", err)
	}
	if len(f.log.records()) != before {
		t.Fatal("a refused select produced")
	}

	s3 := f.session("s3")
	if _, err := f.roster.SelectCharacter(context.Background(), s3, id); err != nil {
		t.Fatalf("the reconnect: %v", err)
	}
	recs := f.log.records()
	if b := recs[len(recs)-1].GetBindCharacter(); b == nil || b.GetCharacterId() != id || recs[len(recs)-1].GetSessionId() != "s3" {
		t.Fatalf("the reconnect produced %v", recs[len(recs)-1])
	}
	if sess, char, ok := f.roster.Live(f.account); !ok || sess != "s3" || char != id {
		t.Fatalf("live %v %s %s, want s3 driving Aldric", ok, sess, char)
	}
	if !strings.Contains(f.metrics(), "andara_sessions_bound 1") {
		t.Fatal("the reconnected Session is not counted bound")
	}
}

// A reconnect that arrives while the lost Session's MarkLinkdead is still
// being produced waits for it rather than being answered already_live.
func TestRoster_ReconnectWaitsForTheLinkdeadTeardown(t *testing.T) {
	f, id, s := selected(t, withLinkdead(time.Hour))
	f.log.entered, f.log.gate = make(chan struct{}, 1), make(chan struct{})
	released := f.roster.ReleaseSession(s, gateway.EndLinkdead)
	<-f.log.entered

	done := make(chan error, 1)
	go func() {
		_, err := f.roster.SelectCharacter(context.Background(), f.session("s2"), id)
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("the reconnect answered during the teardown: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(f.log.gate)
	<-released
	if err := <-done; err != nil {
		t.Fatalf("the reconnect after the teardown: %v", err)
	}
}

// The body's despawn frees the flag: the sim's LinkdeadEnded reaches the
// roster through the loop, and the Account may select anyone again.
func TestRoster_DespawnFreesTheLinkdeadFlag(t *testing.T) {
	f, id, s := selected(t, withLinkdead(time.Hour))
	<-f.roster.ReleaseSession(s, gateway.EndLinkdead)
	// A step that ends somebody else's grace changes nothing.
	f.roster.ObserveLinkdead([]sim.LinkdeadChange{{Kind: sim.LinkdeadEnded, Character: "someone-else"}, {Kind: sim.LinkdeadExtended, Character: sim.EntityID(id)}})
	if _, _, ok := f.roster.Live(f.account); !ok {
		t.Fatal("the flag was freed by another body's despawn")
	}
	f.roster.ObserveLinkdead([]sim.LinkdeadChange{{Kind: sim.LinkdeadEnded, Character: sim.EntityID(id), Reason: sim.DespawnLinkdead}})
	if _, _, ok := f.roster.Live(f.account); ok {
		t.Fatal("the flag survived the despawn")
	}
}

// A despawn that raced a reconnect frees nothing: the flag belongs to the
// new Session, whose BindCharacter wakes the body the despawn left.
func TestRoster_DespawnAfterAReconnectLeavesTheFlag(t *testing.T) {
	f, id, s := selected(t, withLinkdead(time.Hour))
	<-f.roster.ReleaseSession(s, gateway.EndLinkdead)
	if _, err := f.roster.SelectCharacter(context.Background(), f.session("s2"), id); err != nil {
		t.Fatal(err)
	}
	f.roster.ObserveLinkdead([]sim.LinkdeadChange{{Kind: sim.LinkdeadEnded, Character: sim.EntityID(id)}})
	if sess, _, ok := f.roster.Live(f.account); !ok || sess != "s2" {
		t.Fatalf("live %v %s, want s2", ok, sess)
	}
}

// The roster's own bound: a flag the sim never frees — a MarkLinkdead that
// applied as a no-op — is freed at linkdead_max whatever.
func TestRoster_LinkdeadFlagIsBoundedByTheCeiling(t *testing.T) {
	f, _, s := selected(t, withLinkdead(50*time.Millisecond))
	<-f.roster.ReleaseSession(s, gateway.EndLinkdead)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, _, ok := f.roster.Live(f.account); !ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the linkdead flag outlived linkdead_max")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A MarkLinkdead that cannot be produced frees the flag, as a failed unbind
// does: the body stays present with no Session, and the next select takes it.
func TestRoster_LinkdeadProduceFailureFreesTheFlag(t *testing.T) {
	f, _, s := selected(t, withLinkdead(time.Hour))
	f.log.failWith(context.DeadlineExceeded)
	<-f.roster.ReleaseSession(s, gateway.EndLinkdead)
	if _, _, ok := f.roster.Live(f.account); ok {
		t.Fatal("the flag survived a failed MarkLinkdead")
	}
	if !strings.Contains(f.metrics(), `andara_character_unbinds_total{outcome="produce_failed",reason="linkdead"} 1`) {
		t.Fatal("not counted")
	}
}

// With no linkdead durations configured, a lost stream is a quit, as
// AW-SRV-014 had it.
func TestRoster_NoGraceMeansQuit(t *testing.T) {
	f, _, s := selected(t)
	<-f.roster.ReleaseSession(s, gateway.EndLinkdead)
	recs := f.log.records()
	if recs[len(recs)-1].GetUnbindCharacter() == nil {
		t.Fatalf("want an UnbindCharacter, got %v", recs[len(recs)-1])
	}
}
