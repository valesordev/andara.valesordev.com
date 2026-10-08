// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package roster_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	accountsv1 "github.com/valesordev/andara/gen/go/andara/accounts/v1"
	logv1 "github.com/valesordev/andara/gen/go/andara/log/v1"
	"github.com/valesordev/andara/server/auth"
	"github.com/valesordev/andara/server/command"
	"github.com/valesordev/andara/server/gateway"
	"github.com/valesordev/andara/server/roster"
	"github.com/valesordev/andara/server/sim"
)

// rosterOver is a second Roster over the fixture's store and routing table,
// producing through log.
func (f *fixture) rosterOver(log command.Producer, opts ...func(*roster.Options)) *roster.Roster {
	f.t.Helper()
	ro := roster.Options{
		Accounts: f.store, Bindings: f.bindings, Log: log,
		SpawnRoom: sim.RoomRef{Zone: "town", Room: "plaza"}, ProduceDeadline: time.Second,
		Metrics: roster.NewMetrics(nil),
	}
	for _, o := range opts {
		o(&ro)
	}
	r, err := roster.New(ro)
	if err != nil {
		f.t.Fatal(err)
	}
	return r
}

func kinds(recs []*logv1.LoggedCommand) []string {
	var out []string
	for _, r := range recs {
		switch c := r.GetCommand().(type) {
		case *logv1.LoggedCommand_BindCharacter:
			out = append(out, "bind:"+c.BindCharacter.GetCharacterId())
		case *logv1.LoggedCommand_UnbindCharacter:
			out = append(out, "unbind-"+strings.ToLower(c.UnbindCharacter.GetReason().String())+":"+c.UnbindCharacter.GetCharacterId())
		case *logv1.LoggedCommand_PurgeCharacter:
			out = append(out, "purge:"+c.PurgeCharacter.GetCharacterId())
		default:
			out = append(out, "other")
		}
	}
	return out
}

func (f *fixture) quit(s *gateway.Session) {
	f.t.Helper()
	select {
	case <-f.roster.ReleaseSession(s, gateway.EndQuit):
	case <-time.After(5 * time.Second):
		f.t.Fatal("teardown did not finish")
	}
}

// AC-5: a live Character is refused character_live and nothing is logged by
// the delete; once the Session quits the delete lands, the list shows it
// DELETED, and it can no longer be selected.
func TestDelete_RefusesALiveCharacterThenSoftDeletes(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	id := f.create("Aldric")
	s := f.session("s1")
	if _, err := f.roster.SelectCharacter(ctx, s, id); err != nil {
		t.Fatal(err)
	}

	// The live Character, from this Session or another of the Account's.
	for _, sess := range []*gateway.Session{s, f.session("s2")} {
		_, err := f.roster.DeleteCharacter(ctx, sess, id)
		if r, d := reason(t, err); connect.CodeOf(err) != connect.CodeFailedPrecondition || r != roster.ReasonCharacterLive || d != roster.Domain {
			t.Fatalf("delete of a live Character from %s: %v (%s %s)", sess.ID, err, r, d)
		}
	}
	f.quit(s)

	resp, err := f.roster.DeleteCharacter(ctx, f.session("s2"), id)
	if err != nil {
		t.Fatal(err)
	}
	if c := resp.GetCharacter(); c.GetStatus() != accountsv1.CharacterStatus_CHARACTER_STATUS_DELETED || c.GetDeletedUnix() == 0 || c.GetCharacterId() != id {
		t.Fatalf("response %v", c)
	}
	// Delete produced nothing: the log holds only the bind and the quit.
	if got := strings.Join(kinds(f.log.records()), ","); got != "bind:"+id+",unbind-quit:"+id {
		t.Fatalf("log %s", got)
	}
	list, _ := f.roster.ListCharacters(ctx, f.session("s2"))
	if len(list.GetCharacters()) != 1 || list.GetCharacters()[0].GetStatus() != accountsv1.CharacterStatus_CHARACTER_STATUS_DELETED || list.GetCharacters()[0].GetDeletedUnix() == 0 {
		t.Fatalf("list %v", list)
	}
	// Not selectable, not deletable twice, not someone else's.
	_, err = f.roster.SelectCharacter(ctx, f.session("s3"), id)
	if r, _ := reason(t, err); connect.CodeOf(err) != connect.CodeNotFound || r != roster.ReasonNoSuchCharacter {
		t.Fatalf("select of a deleted Character: %v", err)
	}
	_, err = f.roster.DeleteCharacter(ctx, f.session("s3"), id)
	if r, _ := reason(t, err); connect.CodeOf(err) != connect.CodeNotFound || r != roster.ReasonNoSuchCharacter {
		t.Fatalf("second delete: %v", err)
	}
	other := &gateway.Session{ID: "s-other", Principal: auth.Principal{AccountID: "someone-else"}}
	if _, err := f.roster.DeleteCharacter(ctx, other, id); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("delete of someone else's Character: %v", err)
	}
	if m := f.metrics(); !strings.Contains(m, `andara_roster_characters{status="deleted"} 1`) || !strings.Contains(m, `andara_roster_characters{status="active"} 0`) {
		t.Fatalf("roster gauge:\n%s", m)
	}
}

// A Character the Account's Session is not driving can be deleted while
// another is live.
func TestDelete_AnotherCharacterWhileOneIsLive(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	a, b := f.create("Aldric"), f.create("Brenna")
	if _, err := f.roster.SelectCharacter(ctx, f.session("s1"), a); err != nil {
		t.Fatal(err)
	}
	if _, err := f.roster.DeleteCharacter(ctx, f.session("s1"), b); err != nil {
		t.Fatalf("delete of the dormant Character: %v", err)
	}
	if _, id, ok := f.roster.Live(f.account); !ok || id != a {
		t.Fatalf("live = %q, %v", id, ok)
	}
}

// AC-6: selecting another Character on a Session that drives one produces
// UnbindCharacter{SWITCH} then BindCharacter, in that order, with the routing
// table naming the second before either produce; the flag moves.
func TestSwitch_UnbindsThenBinds(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	a, b := f.create("Aldric"), f.create("Brenna")
	s := f.session("s1")

	var mu sync.Mutex
	var actorAtProduce []string
	var seen []*logv1.LoggedCommand
	wrapped := producerFunc(func(ctx context.Context, cmd *logv1.LoggedCommand) (command.Accepted, error) {
		bd, _, _ := f.bindings.Lookup("s1")
		mu.Lock()
		actorAtProduce = append(actorAtProduce, string(bd.Actor))
		seen = append(seen, cmd)
		mu.Unlock()
		return f.log.Produce(ctx, cmd)
	})
	r := f.rosterOver(wrapped)
	if _, err := r.SelectCharacter(ctx, s, a); err != nil {
		t.Fatal(err)
	}
	// Where the first body stands, as the table last knew it.
	f.bindings.Bind("s1", command.Binding{Actor: sim.EntityID(a), Zone: "town", Room: "hall"})

	if _, err := r.SelectCharacter(ctx, s, b); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if got := strings.Join(kinds(seen), ","); got != "bind:"+a+",unbind-switch:"+a+",bind:"+b {
		t.Fatalf("produced %s", got)
	}
	// The table named the second Character before either produce of the switch.
	if actorAtProduce[1] != b || actorAtProduce[2] != b {
		t.Fatalf("table at the unbind and the bind named %v, want %s twice", actorAtProduce[1:], b)
	}
	if un := seen[1]; un.GetZoneId() != "town" || un.GetActorId() != a || un.GetSessionId() != "s1" {
		t.Fatalf("unbind %v", un)
	}
	mu.Unlock()
	if sid, id, ok := r.Live(f.account); !ok || id != b || sid != "s1" {
		t.Fatalf("live = %s %s %v", sid, id, ok)
	}
	// The first body's position is recorded where it stood.
	ref, err := f.store.Character(f.account, a)
	if err != nil || ref.GetRoomId() != "hall" {
		t.Fatalf("first body recorded at %v, %v", ref, err)
	}
	// Selecting the one it already drives, or from another Session, is
	// already_live; so is the first, whose flag is gone.
	if _, err := r.SelectCharacter(ctx, s, b); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("reselect of the live Character: %v", err)
	}
	if _, err := r.SelectCharacter(ctx, f.session("s2"), a); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("select from another Session: %v", err)
	}
	// And the Session's end quits the second, not the first.
	select {
	case <-r.ReleaseSession(s, gateway.EndQuit):
	case <-time.After(5 * time.Second):
		t.Fatal("teardown")
	}
	recs := f.log.records()
	if last := recs[len(recs)-1]; last.GetActorId() != b || last.GetUnbindCharacter().GetReason() != logv1.UnbindReason_QUIT {
		t.Fatalf("last record %v", last)
	}
}

// A switch whose unbind does not reach the log puts everything back: the
// Session still drives the first.
func TestSwitch_FailedUnbindKeepsTheFirst(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	a, b := f.create("Aldric"), f.create("Brenna")
	s := f.session("s1")
	boom := errors.New("broker down")
	wrapped := producerFunc(func(ctx context.Context, cmd *logv1.LoggedCommand) (command.Accepted, error) {
		if cmd.GetUnbindCharacter().GetReason() == logv1.UnbindReason_SWITCH {
			return command.Accepted{}, boom
		}
		return f.log.Produce(ctx, cmd)
	})
	r := f.rosterOver(wrapped)
	if _, err := r.SelectCharacter(ctx, s, a); err != nil {
		t.Fatal(err)
	}
	if _, err := r.SelectCharacter(ctx, s, b); err == nil {
		t.Fatal("a switch whose unbind failed succeeded")
	}
	if sid, id, ok := r.Live(f.account); !ok || id != a || sid != "s1" {
		t.Fatalf("live = %s %s %v, want the first Character back", sid, id, ok)
	}
	if bd, bound, _ := f.bindings.Lookup("s1"); !bound || bd.Actor != sim.EntityID(a) {
		t.Fatalf("table %+v %v: want the first Character", bd, bound)
	}
	// The first Character is still the one the Session's end releases.
	select {
	case <-r.ReleaseSession(s, gateway.EndQuit):
	case <-time.After(5 * time.Second):
		t.Fatal("teardown")
	}
	recs := f.log.records()
	if last := recs[len(recs)-1]; last.GetActorId() != a {
		t.Fatalf("last record %v", last)
	}
}

// A switch whose bind does not reach the log leaves the Session unbound with
// the first body dormant.
func TestSwitch_FailedBindLeavesTheSessionUnbound(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	a, b := f.create("Aldric"), f.create("Brenna")
	s := f.session("s1")
	boom := errors.New("broker down")
	wrapped := producerFunc(func(ctx context.Context, cmd *logv1.LoggedCommand) (command.Accepted, error) {
		if cmd.GetBindCharacter().GetCharacterId() == b {
			return command.Accepted{}, boom
		}
		return f.log.Produce(ctx, cmd)
	})
	r := f.rosterOver(wrapped)
	if _, err := r.SelectCharacter(ctx, s, a); err != nil {
		t.Fatal(err)
	}
	if _, err := r.SelectCharacter(ctx, s, b); err == nil {
		t.Fatal("a switch whose bind failed succeeded")
	}
	if _, _, ok := r.Live(f.account); ok {
		t.Fatal("the Account still has a live flag")
	}
	if _, bound, _ := f.bindings.Lookup("s1"); bound {
		t.Fatal("the Session is still bound")
	}
	if got := strings.Join(kinds(f.log.records()), ","); got != "bind:"+a+",unbind-switch:"+a {
		t.Fatalf("log %s", got)
	}
	// The Account can select again: nothing is stuck.
	if _, err := r.SelectCharacter(ctx, s, a); err != nil {
		t.Fatalf("select after the failed switch: %v", err)
	}
}

// AC-3, AC-8 (roster side): the sweep produces one PurgeCharacter per expired
// Character to its last-known Zone, once; the entry stays DELETED; the name
// stays reserved; the slot is freed.
func TestSweep_ProducesEachPurgeOnce(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	id := f.create("Aldric")
	if _, err := f.roster.DeleteCharacter(ctx, f.session("s1"), id); err != nil {
		t.Fatal(err)
	}

	f.clock.advance(719 * time.Hour)
	if res := f.roster.Sweep(ctx); res.Expired != 0 || res.Produced != 0 {
		t.Fatalf("sweep before expiry: %+v", res)
	}
	f.clock.advance(2 * time.Hour)
	res := f.roster.Sweep(ctx)
	if res.Expired != 1 || res.Produced != 1 || res.Failed != 0 {
		t.Fatalf("sweep at expiry: %+v", res)
	}
	recs := f.log.records()
	if len(recs) != 1 || recs[0].GetPurgeCharacter().GetCharacterId() != id || recs[0].GetActorId() != id || recs[0].GetZoneId() != "town" {
		t.Fatalf("log %v", recs)
	}
	// Idempotent.
	if res := f.roster.Sweep(ctx); res.Expired != 0 || res.Produced != 0 {
		t.Fatalf("second sweep: %+v", res)
	}
	if n := len(f.log.records()); n != 1 {
		t.Fatalf("a purged Character was produced again: %d records", n)
	}
	// The entry stays DELETED and purged; the name stays reserved.
	all := f.store.AllCharacters(f.account)
	if len(all) != 1 || all[0].GetStatus() != accountsv1.CharacterStatus_CHARACTER_STATUS_DELETED || all[0].GetPurgedUnix() == 0 {
		t.Fatalf("roster %v", all)
	}
	if _, err := f.roster.CreateCharacter(ctx, f.session("s1"), "ALDRIC"); connect.CodeOf(err) != connect.CodeAlreadyExists {
		t.Fatalf("a purged name was creatable: %v", err)
	}
	if m := f.metrics(); !strings.Contains(m, `andara_roster_characters{status="deleted"} 1`) || !strings.Contains(m, "andara_character_sweep_duration_seconds_count 3") {
		t.Fatalf("metrics:\n%s", m)
	}
}

// A purge that does not reach the log is not marked, and the next sweep
// produces it.
func TestSweep_RetriesAProduceThatFailed(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	id := f.create("Aldric")
	if _, err := f.roster.DeleteCharacter(ctx, f.session("s1"), id); err != nil {
		t.Fatal(err)
	}
	f.clock.advance(721 * time.Hour)

	f.log.failWith(errors.New("broker down"))
	if res := f.roster.Sweep(ctx); res.Expired != 1 || res.Produced != 0 || res.Failed != 1 {
		t.Fatalf("failing sweep: %+v", res)
	}
	f.log.fail.Store(nil)
	if res := f.roster.Sweep(ctx); res.Produced != 1 {
		t.Fatalf("retrying sweep: %+v", res)
	}
	if got := strings.Join(kinds(f.log.records()), ","); got != "purge:"+id {
		t.Fatalf("log %s", got)
	}
}

// The sim's purge outcomes are counted where they are known: ok and no_body
// on the apply, nothing on a re-route.
func TestObservePurges_CountsWhatTheSimDid(t *testing.T) {
	f := newFixture(t)
	f.roster.ObservePurges(7, []sim.PurgeChange{
		{Kind: sim.PurgeRerouted, Zone: "docks", Character: "c1"},
		{Kind: sim.PurgeApplied, Zone: "docks", Room: "pier", Character: "c1", WasPresent: true},
		{Kind: sim.PurgeNoBody, Zone: "town", Character: "c2"},
	})
	m := f.metrics()
	for _, want := range []string{`andara_character_purges_total{outcome="ok"} 1`, `andara_character_purges_total{outcome="no_body"} 1`, `andara_character_purges_total{outcome="reclaimed"} 0`} {
		if !strings.Contains(m, want) {
			t.Fatalf("missing %s in\n%s", want, m)
		}
	}
}

// AC-11: a delete and a select of the same Character from two Sessions: one
// wins. Either the delete lands and the select is no_such_character, or the
// select binds and the delete is character_live — never a live body with
// status DELETED. Run under -race.
func TestDeleteAndSelectRace_ExactlyOneWins(t *testing.T) {
	for i := 0; i < 200; i++ {
		f := newFixture(t)
		ctx := context.Background()
		id := f.create("Aldric")
		var wg sync.WaitGroup
		var delErr, selErr error
		start := make(chan struct{})
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_, delErr = f.roster.DeleteCharacter(ctx, f.session("s-del"), id)
		}()
		go func() {
			defer wg.Done()
			<-start
			_, selErr = f.roster.SelectCharacter(ctx, f.session("s-sel"), id)
		}()
		close(start)
		wg.Wait()

		switch {
		case delErr == nil && selErr != nil:
			if r, _ := reason(t, selErr); r != roster.ReasonNoSuchCharacter {
				t.Fatalf("iteration %d: delete won but select said %v", i, selErr)
			}
			if _, _, ok := f.roster.Live(f.account); ok {
				t.Fatalf("iteration %d: a deleted Character is live", i)
			}
		case selErr == nil && delErr != nil:
			if r, _ := reason(t, delErr); r != roster.ReasonCharacterLive {
				t.Fatalf("iteration %d: select won but delete said %v", i, delErr)
			}
			if ref, err := f.store.Character(f.account, id); err != nil || ref.GetStatus() != accountsv1.CharacterStatus_CHARACTER_STATUS_ACTIVE {
				t.Fatalf("iteration %d: a live Character is not ACTIVE: %v %v", i, ref, err)
			}
		default:
			t.Fatalf("iteration %d: delete %v, select %v: want exactly one winner", i, delErr, selErr)
		}
	}
}

// holdUnbind wraps log so a SWITCH unbind waits on release, then fails with
// boom when failIt is set; entered is signaled when it arrives.
func holdUnbind(log command.Producer, entered chan<- struct{}, release <-chan struct{}) command.Producer {
	return producerFunc(func(ctx context.Context, cmd *logv1.LoggedCommand) (command.Accepted, error) {
		if cmd.GetUnbindCharacter().GetReason() == logv1.UnbindReason_SWITCH {
			entered <- struct{}{}
			<-release
			return command.Accepted{}, errors.New("broker down")
		}
		return log.Produce(ctx, cmd)
	})
}

// Review finding: a switch whose unbind fails after the Session ended must not
// put the first Character's flag back for a Session that is gone.
func TestSwitch_FailedUnbindAfterTheSessionEndedFreesTheAccount(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	a, b := f.create("Aldric"), f.create("Brenna")
	s := f.session("s1")
	entered, release := make(chan struct{}, 1), make(chan struct{})
	r := f.rosterOver(holdUnbind(f.log, entered, release))
	if _, err := r.SelectCharacter(ctx, s, a); err != nil {
		t.Fatal(err)
	}
	errc := make(chan error, 1)
	go func() { _, err := r.SelectCharacter(ctx, s, b); errc <- err }()
	<-entered
	select {
	case <-r.ReleaseSession(s, gateway.EndQuit):
	case <-time.After(5 * time.Second):
		t.Fatal("teardown")
	}
	close(release)
	if err := <-errc; err == nil {
		t.Fatal("the switch succeeded")
	}
	r.Wait()
	if sid, id, ok := r.Live(f.account); ok {
		t.Fatalf("LEAK: live flag %s %s after the Session ended", sid, id)
	}
	if _, bound, _ := f.bindings.Lookup("s1"); bound {
		t.Fatal("the Session's routing entry outlived it")
	}
	if m := f.metrics(); strings.Contains(m, "andara_sessions_bound 1") {
		t.Fatalf("sessions_bound not settled:\n%s", m)
	}
	if _, err := f.roster.DeleteCharacter(ctx, f.session("s2"), a); err != nil {
		t.Fatalf("the orphaned first Character is stuck undeletable: %v", err)
	}
	recs := f.log.records()
	if last := recs[len(recs)-1]; last.GetActorId() != a || last.GetUnbindCharacter().GetReason() != logv1.UnbindReason_QUIT {
		t.Fatalf("the first body was not released: last record %v", last)
	}
}

// Review finding: during a switch the first Character is still live, so it
// cannot be deleted; a failed unbind would otherwise restore a live DELETED body.
func TestSwitch_FirstCharacterCannotBeDeletedMidSwitch(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	a, b := f.create("Aldric"), f.create("Brenna")
	s := f.session("s1")
	entered, release := make(chan struct{}, 1), make(chan struct{})
	r := f.rosterOver(holdUnbind(f.log, entered, release))
	if _, err := r.SelectCharacter(ctx, s, a); err != nil {
		t.Fatal(err)
	}
	errc := make(chan error, 1)
	go func() { _, err := r.SelectCharacter(ctx, s, b); errc <- err }()
	<-entered
	_, err := r.DeleteCharacter(ctx, f.session("s2"), a)
	if rs, _ := reason(t, err); rs != roster.ReasonCharacterLive || !strings.Contains(err.Error(), "Aldric") {
		t.Fatalf("delete of the Character being switched away from: %v", err)
	}
	if _, err := r.SelectCharacter(ctx, f.session("s2"), a); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("select of the Character being switched away from: %v", err)
	}
	close(release)
	<-errc
	if ref, err := f.store.Character(f.account, a); err != nil || ref.GetStatus() != accountsv1.CharacterStatus_CHARACTER_STATUS_ACTIVE {
		t.Fatalf("first Character %v %v", ref, err)
	}
	// Once the switch has resolved (here: restored), live again, still refused.
	if _, id, ok := r.Live(f.account); !ok || id != a {
		t.Fatalf("live = %s %v", id, ok)
	}
}

// Review finding: the retention boundary is inclusive, to the second.
func TestSweep_ExpiryIsInclusiveAtTheBoundary(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	id := f.create("Aldric")
	if _, err := f.roster.DeleteCharacter(ctx, f.session("s1"), id); err != nil {
		t.Fatal(err)
	}
	f.clock.advance(720*time.Hour - time.Second)
	if res := f.roster.Sweep(ctx); res.Expired != 0 {
		t.Fatalf("expired one second early: %+v", res)
	}
	f.clock.advance(time.Second)
	if res := f.roster.Sweep(ctx); res.Expired != 1 {
		t.Fatalf("not expired at the boundary: %+v", res)
	}
}

// After a successful switch the first Character is an ordinary dormant one:
// the mid-switch claim is gone and it can be deleted.
func TestSwitch_FirstCharacterIsDeletableAfterwards(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	a, b := f.create("Aldric"), f.create("Brenna")
	s := f.session("s1")
	if _, err := f.roster.SelectCharacter(ctx, s, a); err != nil {
		t.Fatal(err)
	}
	if _, err := f.roster.SelectCharacter(ctx, s, b); err != nil {
		t.Fatal(err)
	}
	if _, err := f.roster.DeleteCharacter(ctx, f.session("s2"), a); err != nil {
		t.Fatalf("delete after the switch: %v", err)
	}
}

// holdBindOf holds the BindCharacter of one Character until release is closed,
// then appends it: a produce that is in flight while another record is produced.
func holdBindOf(log command.Producer, id string, entered chan<- struct{}, release <-chan struct{}) command.Producer {
	return producerFunc(func(ctx context.Context, cmd *logv1.LoggedCommand) (command.Accepted, error) {
		if cmd.GetBindCharacter().GetCharacterId() == id {
			entered <- struct{}{}
			<-release
		}
		return log.Produce(ctx, cmd)
	})
}

// Review finding (Codex, #475): a Session that ends while the switch's bind is
// still being produced must not have its QUIT overtake the bind, which would
// leave the second body present with no Session and no flag.
func TestSwitch_TeardownWaitsForTheInFlightBind(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	a, b := f.create("Aldric"), f.create("Brenna")
	s := f.session("s1")
	entered, release := make(chan struct{}, 1), make(chan struct{})
	r := f.rosterOver(holdBindOf(f.log, b, entered, release))
	if _, err := r.SelectCharacter(ctx, s, a); err != nil {
		t.Fatal(err)
	}
	errc := make(chan error, 1)
	go func() { _, err := r.SelectCharacter(ctx, s, b); errc <- err }()
	<-entered
	done := r.ReleaseSession(s, gateway.EndQuit)
	time.Sleep(50 * time.Millisecond) // let a wrongly-eager teardown produce
	close(release)
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("teardown")
	}
	want := "unbind-switch:" + a + ",bind:" + b + ",unbind-quit:" + b
	if got := strings.Join(kinds(f.log.records()[1:]), ","); got != want {
		t.Fatalf("log order %s, want %s", got, want)
	}
}
