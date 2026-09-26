// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package roster_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/valesordev/andara/server/gateway"
	"github.com/valesordev/andara/server/roster"
	"github.com/valesordev/andara/server/sim"
)

// withLinkdead is the 10 Hz defaults: 180 s, 60 s, 300 s.
func withLinkdead() func(*roster.Options) {
	return func(o *roster.Options) {
		o.Linkdead = roster.LinkdeadTicks{Grace: 1800, Extension: 600, Max: 3000}
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
	f, id, s := selected(t, withLinkdead())
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
	f, id, s := selected(t, withLinkdead())
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
	f, id, s := selected(t, withLinkdead())
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
	f, id, s := selected(t, withLinkdead())
	<-f.roster.ReleaseSession(s, gateway.EndLinkdead)
	// A step that ends somebody else's grace changes nothing.
	f.roster.ObserveLinkdead(100, []sim.LinkdeadChange{{Kind: sim.LinkdeadEnded, Character: "someone-else"}, {Kind: sim.LinkdeadExtended, Character: sim.EntityID(id)}})
	if _, _, ok := f.roster.Live(f.account); !ok {
		t.Fatal("the flag was freed by another body's despawn")
	}
	f.roster.ObserveLinkdead(100, []sim.LinkdeadChange{{Kind: sim.LinkdeadEnded, Character: sim.EntityID(id), Reason: sim.DespawnLinkdead}})
	if _, _, ok := f.roster.Live(f.account); ok {
		t.Fatal("the flag survived the despawn")
	}
}

// A despawn that raced a reconnect frees nothing: the flag belongs to the
// new Session, whose BindCharacter wakes the body the despawn left.
func TestRoster_DespawnAfterAReconnectLeavesTheFlag(t *testing.T) {
	f, id, s := selected(t, withLinkdead())
	<-f.roster.ReleaseSession(s, gateway.EndLinkdead)
	if _, err := f.roster.SelectCharacter(context.Background(), f.session("s2"), id); err != nil {
		t.Fatal(err)
	}
	f.roster.ObserveLinkdead(100, []sim.LinkdeadChange{{Kind: sim.LinkdeadEnded, Character: sim.EntityID(id)}})
	if sess, _, ok := f.roster.Live(f.account); !ok || sess != "s2" {
		t.Fatalf("live %v %s, want s2", ok, sess)
	}
}

// No wall clock frees the hold: only the sim's LinkdeadEnded does, or a
// reconnect. The sim's deadline starts when the mark applies and runs in
// Ticks, so a timer here could free the Account while the body stands in
// the World (review of #114).
func TestRoster_LinkdeadHoldWaitsForTheSim(t *testing.T) {
	f, id, s := selected(t, withLinkdead())
	<-f.roster.ReleaseSession(s, gateway.EndLinkdead)
	time.Sleep(100 * time.Millisecond)
	if _, char, ok := f.roster.Live(f.account); !ok || char != id {
		t.Fatal("the hold went without the sim ending the grace")
	}
}

// A reconnect whose BindCharacter never reaches the log leaves the body
// linkdead, and the Account still holds it: another Character is still
// already_live. If the grace ended meanwhile, there is nothing to hold.
func TestRoster_FailedReconnectRestoresTheHold(t *testing.T) {
	f, id, s := selected(t, withLinkdead())
	other := f.create("Brin")
	<-f.roster.ReleaseSession(s, gateway.EndLinkdead)

	f.log.failWith(context.DeadlineExceeded)
	if _, err := f.roster.SelectCharacter(context.Background(), f.session("s2"), id); err == nil {
		t.Fatal("the reconnect produced despite the failure")
	}
	if _, char, ok := f.roster.Live(f.account); !ok || char != id {
		t.Fatalf("after a failed reconnect: live %v %s, want the hold on Aldric", ok, char)
	}
	f.log.fail.Store(nil)
	if _, err := f.roster.SelectCharacter(context.Background(), f.session("s3"), other); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("select another after a failed reconnect: %v, want already_live", err)
	}
	// And the reconnect, retried, works.
	if _, err := f.roster.SelectCharacter(context.Background(), f.session("s4"), id); err != nil {
		t.Fatalf("the retried reconnect: %v", err)
	}
}

func TestRoster_FailedReconnectAfterTheDespawnHoldsNothing(t *testing.T) {
	f, id, s := selected(t, withLinkdead())
	<-f.roster.ReleaseSession(s, gateway.EndLinkdead)
	f.log.entered, f.log.gate = make(chan struct{}, 1), make(chan struct{})
	f.log.failWith(context.DeadlineExceeded)
	done := make(chan error, 1)
	go func() {
		_, err := f.roster.SelectCharacter(context.Background(), f.session("s2"), id)
		done <- err
	}()
	<-f.log.entered
	// The grace ends while the reconnect is being produced.
	f.roster.ObserveLinkdead(100, []sim.LinkdeadChange{{Kind: sim.LinkdeadEnded, Character: sim.EntityID(id), Reason: sim.DespawnLinkdead}})
	close(f.log.gate)
	if err := <-done; err == nil {
		t.Fatal("the reconnect produced despite the failure")
	}
	if _, _, ok := f.roster.Live(f.account); ok {
		t.Fatal("a hold was restored for a body that already despawned")
	}
}

// A drop mid-crossing: the body is in neither Zone until the Arrive
// applies, so the teardown waits for the crossing to settle and marks the
// Zone the body arrived in, not the one it left (review of #114).
func TestRoster_LinkdeadMidCrossingMarksTheArrivalZone(t *testing.T) {
	f, id, s := selected(t, withLinkdead())
	f.bindings.Publish(sim.Event{Type: sim.EvCharacterLeft, Scope: sim.ScopeEntities(sim.EntityID(id)), Envelope: charLeft("town", "plaza", "east")})
	released := make(chan (<-chan struct{}), 1)
	go func() { released <- f.roster.ReleaseSession(s, gateway.EndLinkdead) }()
	time.Sleep(50 * time.Millisecond)
	f.bindings.Publish(sim.Event{Type: sim.EvCharacterArrived, Scope: sim.ScopeEntities(sim.EntityID(id)), Envelope: charArrived("wilds", "edge", "west")})
	<-<-released
	recs := f.log.records()
	last := recs[len(recs)-1]
	if last.GetMarkLinkdead() == nil || last.GetZoneId() != "wilds" {
		t.Fatalf("marked %v, want a MarkLinkdead to wilds", last)
	}
}

// A MarkLinkdead that cannot be produced frees the flag, as a failed unbind
// does: the body stays present with no Session, and the next select takes it.
func TestRoster_LinkdeadProduceFailureFreesTheFlag(t *testing.T) {
	f, _, s := selected(t, withLinkdead())
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

// The lifecycle's observability: the gauge follows the bodies, split by
// combat; each end is an outcome and a duration at sim.tick_rate; a combat
// interaction and a ceiling despawn are counted.
func TestRoster_LinkdeadMetrics(t *testing.T) {
	f := newFixture(t, withLinkdead())
	f.roster.SeedLinkdead([]sim.EntityID{"recovered"})
	f.roster.ObserveLinkdead(10, []sim.LinkdeadChange{
		{Kind: sim.LinkdeadEntered, Character: "a", Since: 10, Deadline: 1810},
		{Kind: sim.LinkdeadEntered, Character: "b", Since: 10, Deadline: 1810},
	})
	f.roster.ObserveLinkdead(20, []sim.LinkdeadChange{{Kind: sim.LinkdeadExtended, Character: "b", Since: 10, Deadline: 1810}})
	m := f.metrics()
	for _, want := range []string{
		`andara_sessions_linkdead{in_combat="false"} 2`, `andara_sessions_linkdead{in_combat="true"} 1`,
		"andara_linkdead_combat_extensions_total 1",
	} {
		if !strings.Contains(m, want) {
			t.Fatalf("want %s in\n%s", want, m)
		}
	}
	f.roster.ObserveLinkdead(310, []sim.LinkdeadChange{
		{Kind: sim.LinkdeadReconnected, Character: "a", Since: 10},
		{Kind: sim.LinkdeadEnded, Character: "b", Since: 10, Reason: sim.DespawnLinkdeadCeiling},
		{Kind: sim.LinkdeadEnded, Character: "recovered", Since: 0, Reason: sim.DespawnLinkdead},
	})
	m = f.metrics()
	for _, want := range []string{
		`andara_sessions_linkdead{in_combat="false"} 0`, `andara_sessions_linkdead{in_combat="true"} 0`,
		`andara_linkdead_outcomes_total{outcome="reconnected"} 1`, `andara_linkdead_outcomes_total{outcome="ceiling"} 1`,
		`andara_linkdead_outcomes_total{outcome="despawned"} 1`, "andara_linkdead_ceiling_despawns_total 1",
		// a and b were linkdead 300 Ticks, 30 s at 10 Hz; b in combat.
		`andara_linkdead_duration_seconds_sum{in_combat="true"} 30`,
	} {
		if !strings.Contains(m, want) {
			t.Fatalf("want %s in\n%s", want, m)
		}
	}
}

// The expiry's info line (§8 review of AW-SRV-015). A held body's names the
// Session that went linkdead, which the hold kept, and the trace of the tick
// that applied it. A body recovery left has no Session: its line omits
// session_id and says recovered, with character_id and deadline_tick to
// correlate.
func TestRoster_ExpiryLine(t *testing.T) {
	var buf bytes.Buffer
	logTo := func(o *roster.Options) { o.Logger = slog.New(slog.NewJSONHandler(&buf, nil)) }
	f, id, s := selected(t, withLinkdead(), logTo)
	<-f.roster.ReleaseSession(s, gateway.EndLinkdead)
	f.roster.SeedLinkdead([]sim.EntityID{"recovered"})
	const tick = "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"
	f.roster.ObserveLinkdead(1900, []sim.LinkdeadChange{
		{Kind: sim.LinkdeadEnded, Character: sim.EntityID(id), Reason: sim.DespawnLinkdead, Since: 100, Deadline: 1900, TraceID: tick},
		{Kind: sim.LinkdeadEnded, Character: "recovered", Reason: sim.DespawnLinkdead, Deadline: 1800, TraceID: tick},
	})
	lines := map[string]map[string]any{}
	for _, l := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatal(err)
		}
		if m["msg"] == "character despawned" {
			lines[m["character_id"].(string)] = m
		}
	}
	held, recovered := lines[id], lines["recovered"]
	if held == nil || recovered == nil {
		t.Fatalf("despawn lines %v", lines)
	}
	if held["session_id"] != "s1" || held["recovered"] != nil || held["trace_id"] != "0af7651916cd43dd8448eb211c80319c" || held["deadline_tick"] != float64(1900) {
		t.Errorf("the held body's line: %v", held)
	}
	if _, ok := recovered["session_id"]; ok || recovered["recovered"] != true || recovered["trace_id"] != "0af7651916cd43dd8448eb211c80319c" || recovered["deadline_tick"] != float64(1800) {
		t.Errorf("the recovered body's line: %v", recovered)
	}
}
