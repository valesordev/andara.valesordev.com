// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package roster_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/expfmt"
	"google.golang.org/genproto/googleapis/rpc/errdetails"

	gamev1 "github.com/valesordev/andara/gen/go/andara/game/v1"
	logv1 "github.com/valesordev/andara/gen/go/andara/log/v1"
	"github.com/valesordev/andara/server/auth"
	"github.com/valesordev/andara/server/command"
	"github.com/valesordev/andara/server/gateway"
	"github.com/valesordev/andara/server/ingress"
	"github.com/valesordev/andara/server/recordlog"
	"github.com/valesordev/andara/server/roster"
	"github.com/valesordev/andara/server/sim"
)

// fakeLog is command.Producer over a slice, with a switch to fail.
type fakeLog struct {
	mu   sync.Mutex
	recs []*logv1.LoggedCommand
	fail atomic.Pointer[error]
	// slow, when set, is how long a produce takes.
	slow time.Duration
}

func (f *fakeLog) Produce(ctx context.Context, cmd *logv1.LoggedCommand) (command.Accepted, error) {
	if f.slow > 0 {
		select {
		case <-time.After(f.slow):
		case <-ctx.Done():
			return command.Accepted{}, ctx.Err()
		}
	}
	if p := f.fail.Load(); p != nil {
		return command.Accepted{}, *p
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recs = append(f.recs, cmd)
	return command.Accepted{Partition: sim.PartitionFor(sim.ZoneID(cmd.GetZoneId())), Offset: int64(len(f.recs) - 1)}, nil
}

func (f *fakeLog) records() []*logv1.LoggedCommand {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*logv1.LoggedCommand(nil), f.recs...)
}

func (f *fakeLog) failWith(err error) { f.fail.Store(&err) }

type fixture struct {
	t        *testing.T
	store    *auth.Store
	bindings *ingress.Bindings
	log      *fakeLog
	roster   *roster.Roster
	reg      *prometheus.Registry
	account  string
	rebinds  atomic.Int64
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	kr, err := auth.ParseKeyring(strings.NewReader("k1: " + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32)) + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	store, err := auth.Open(context.Background(), auth.Options{
		Accounts: recordlog.NewMemory(), Audit: recordlog.NewMemory(), Keys: kr,
		Argon2:     auth.Argon2Params{MemoryKiB: 64, Time: 1, Threads: 1},
		SessionTTL: time.Hour, RefreshTTL: time.Hour, InviteTTL: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	opCtx := auth.WithPrincipal(context.Background(), auth.Principal{AccountID: "op", Roles: []auth.Role{auth.RoleOperator}})
	acct, err := store.CreateAccount(opCtx, "alice", "correct horse battery", nil)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, store: store, bindings: ingress.NewBindings(time.Second, nil, nil), log: &fakeLog{}, reg: prometheus.NewRegistry(), account: acct}
	f.bindings.OnChange = func(string) { f.rebinds.Add(1) }
	r, err := roster.New(roster.Options{
		Accounts: store, Bindings: f.bindings, Log: f.log,
		SpawnRoom: sim.RoomRef{Zone: "town", Room: "plaza"}, ProduceDeadline: time.Second,
		Metrics: roster.NewMetrics(f.reg),
	})
	if err != nil {
		t.Fatal(err)
	}
	f.roster = r
	return f
}

func (f *fixture) session(id string) *gateway.Session {
	return &gateway.Session{ID: id, Principal: auth.Principal{AccountID: f.account, Roles: []auth.Role{auth.RolePlayer}}}
}

func (f *fixture) create(name string) string {
	f.t.Helper()
	resp, err := f.roster.CreateCharacter(context.Background(), f.session("s-create"), name)
	if err != nil {
		f.t.Fatal(err)
	}
	return resp.GetCharacter().GetCharacterId()
}

func (f *fixture) metrics() string {
	f.t.Helper()
	mfs, err := f.reg.Gather()
	if err != nil {
		f.t.Fatal(err)
	}
	var b bytes.Buffer
	for _, mf := range mfs {
		_, _ = expfmt.MetricFamilyToText(&b, mf)
	}
	return b.String()
}

// reason is the ErrorInfo.reason on a wire error, with its domain.
func reason(t *testing.T, err error) (string, string) {
	t.Helper()
	var ce *connect.Error
	if !errors.As(err, &ce) {
		t.Fatalf("not a connect error: %v", err)
	}
	for _, d := range ce.Details() {
		v, err := d.Value()
		if err != nil {
			continue
		}
		if info, ok := v.(*errdetails.ErrorInfo); ok {
			return info.GetReason(), info.GetDomain()
		}
	}
	return "", ""
}

// The wire taxonomy: each roster refusal with its code, reason, and
// domain; the roster response says how many may be held.
func TestRoster_Taxonomy(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	s := f.session("s1")

	_, err := f.roster.CreateCharacter(ctx, s, "x")
	if r, d := reason(t, err); connect.CodeOf(err) != connect.CodeInvalidArgument || r != roster.ReasonNameInvalid || d != roster.Domain {
		t.Fatalf("invalid name: %v (%s %s)", err, r, d)
	}
	resp, err := f.roster.CreateCharacter(ctx, s, "Aldric")
	if err != nil {
		t.Fatal(err)
	}
	if c := resp.GetCharacter(); c.GetName() != "Aldric" || c.GetZoneId() != "town" || c.GetRoomId() != "plaza" || c.GetLive() || resp.GetMaxPerAccount() != 5 {
		t.Fatalf("created %v", resp)
	}
	_, err = f.roster.CreateCharacter(ctx, s, "aldric")
	if r, _ := reason(t, err); connect.CodeOf(err) != connect.CodeAlreadyExists || r != roster.ReasonNameTaken {
		t.Fatalf("taken: %v", err)
	}
	for _, n := range []string{"Brin", "Cael", "Dara", "Eryn"} {
		f.create(n)
	}
	_, err = f.roster.CreateCharacter(ctx, s, "Finn")
	if r, _ := reason(t, err); connect.CodeOf(err) != connect.CodeResourceExhausted || r != roster.ReasonRosterFull || !strings.Contains(err.Error(), "5") {
		t.Fatalf("full: %v", err)
	}
	_, err = f.roster.SelectCharacter(ctx, s, "nope")
	if r, _ := reason(t, err); connect.CodeOf(err) != connect.CodeNotFound || r != roster.ReasonNoSuchCharacter {
		t.Fatalf("unknown: %v", err)
	}
	// Another Account's Character is not found either.
	other := &gateway.Session{ID: "s-other", Principal: auth.Principal{AccountID: "someone-else"}}
	_, err = f.roster.SelectCharacter(ctx, other, resp.GetCharacter().GetCharacterId())
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("someone else's: %v", err)
	}
	list, err := f.roster.ListCharacters(ctx, s)
	if err != nil || len(list.GetCharacters()) != 5 || list.GetMaxPerAccount() != 5 {
		t.Fatalf("list %v %v", list, err)
	}
}

// The binding protocol: the routing table is bound before the produce,
// the BindCharacter names the Character and its spawn Room, the live
// flag and the gauge are set, the list flags it, and the same Account on
// another Session is already_live naming it (AC-4).
func TestRoster_SelectBindsThenProduces(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	id := f.create("Aldric")
	s1 := f.session("s1")

	// Bound before produced: observe the table from inside the produce by
	// wrapping the log.
	var boundAtProduce bool
	produceSeen := make(chan struct{})
	wrapped := producerFunc(func(ctx context.Context, cmd *logv1.LoggedCommand) (command.Accepted, error) {
		b, bound, _ := f.bindings.Lookup("s1")
		boundAtProduce = bound && b.Actor == sim.EntityID(id) && b.Zone == "town" && b.Room == "plaza"
		close(produceSeen)
		return f.log.Produce(ctx, cmd)
	})
	r, err := roster.New(roster.Options{Accounts: f.store, Bindings: f.bindings, Log: wrapped, SpawnRoom: sim.RoomRef{Zone: "town", Room: "plaza"}, Metrics: roster.NewMetrics(nil)})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := r.SelectCharacter(ctx, s1, id)
	if err != nil {
		t.Fatal(err)
	}
	<-produceSeen
	if !boundAtProduce {
		t.Fatal("the routing table was not bound before the produce")
	}
	if resp.GetPartition() != sim.PartitionFor("town") || resp.GetAcceptedOffset() != 0 {
		t.Fatalf("resp %v", resp)
	}
	recs := f.log.records()
	if len(recs) != 1 {
		t.Fatalf("records %v", recs)
	}
	b := recs[0].GetBindCharacter()
	if recs[0].GetZoneId() != "town" || recs[0].GetActorId() != id || recs[0].GetSessionId() != "s1" || b.GetCharacterId() != id || b.GetName() != "Aldric" || b.GetSpawnRoomId() != "plaza" || b.GetAccountId() != f.account {
		t.Fatalf("BindCharacter %v", recs[0])
	}
	if f.rebinds.Load() != 1 {
		t.Fatalf("rebinds %d, want 1 (Bind)", f.rebinds.Load())
	}
	if sess, ch, ok := r.Live(f.account); !ok || sess != "s1" || ch != id {
		t.Fatalf("live %s %s %v", sess, ch, ok)
	}
	list, _ := r.ListCharacters(ctx, s1)
	if !list.GetCharacters()[0].GetLive() {
		t.Fatal("list does not flag the live Character")
	}

	// AC-4: another Session of the Account.
	_, err = r.SelectCharacter(ctx, f.session("s2"), id)
	if rs, _ := reason(t, err); connect.CodeOf(err) != connect.CodeFailedPrecondition || rs != roster.ReasonAlreadyLive || !strings.Contains(err.Error(), "Aldric") {
		t.Fatalf("already live: %v", err)
	}
	// And the same Session again: switching is AW-SRV-032's.
	if _, err := r.SelectCharacter(ctx, s1, id); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("re-select on the same Session: %v", err)
	}
	if len(f.log.records()) != 1 {
		t.Fatal("a refused select produced")
	}
}

type producerFunc func(ctx context.Context, cmd *logv1.LoggedCommand) (command.Accepted, error)

func (p producerFunc) Produce(ctx context.Context, cmd *logv1.LoggedCommand) (command.Accepted, error) {
	return p(ctx, cmd)
}

// A produce failure on select leaves no live flag and no binding, and is
// answered as Submit would answer it; the next select succeeds.
func TestRoster_ProduceFailureClearsEverything(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	id := f.create("Aldric")
	s := f.session("s1")

	f.log.failWith(ingress.ErrUnavailable)
	_, err := f.roster.SelectCharacter(ctx, s, id)
	if connect.CodeOf(err) != connect.CodeUnavailable {
		t.Fatalf("read-only produce: %v", err)
	}
	if r, d := reason(t, err); r != ingress.ReasonReadOnly || d != ingress.ErrorDomain {
		t.Fatalf("reason %s %s", r, d)
	}
	if _, _, ok := f.roster.Live(f.account); ok {
		t.Fatal("live flag survived a failed produce")
	}
	if _, bound, _ := f.bindings.Lookup("s1"); bound {
		t.Fatal("binding survived a failed produce")
	}
	if !strings.Contains(f.metrics(), `andara_character_bindings_total{outcome="produce_failed"} 1`) {
		t.Fatal("produce_failed not counted")
	}

	// The deadline case, as a retry after DEADLINE_EXCEEDED would meet it.
	f.log.failWith(ingress.ErrDeadline)
	if _, err := f.roster.SelectCharacter(ctx, s, id); connect.CodeOf(err) != connect.CodeDeadlineExceeded {
		t.Fatalf("deadline: %v", err)
	}
	f.log.fail.Store(nil)
	if _, err := f.roster.SelectCharacter(ctx, s, id); err != nil {
		t.Fatalf("the retry: %v", err)
	}
	if !strings.Contains(f.metrics(), "andara_sessions_bound 1") {
		t.Fatal("sessions_bound")
	}
}

// AC-7: twenty Sessions select the same Character at once; exactly one
// produces a BindCharacter and the rest are already_live.
func TestRoster_ConcurrentSelect(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	id := f.create("Aldric")
	f.log.slow = 20 * time.Millisecond

	const n = 20
	var wg sync.WaitGroup
	var ok, refused atomic.Int64
	start := make(chan struct{})
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := f.roster.SelectCharacter(ctx, f.session("s-"+string(rune('a'+i))), id)
			switch {
			case err == nil:
				ok.Add(1)
			case connect.CodeOf(err) == connect.CodeFailedPrecondition:
				refused.Add(1)
			default:
				t.Errorf("unexpected: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()
	if ok.Load() != 1 || refused.Load() != n-1 {
		t.Fatalf("ok %d refused %d", ok.Load(), refused.Load())
	}
	if len(f.log.records()) != 1 {
		t.Fatalf("%d BindCharacters produced", len(f.log.records()))
	}
	m := f.metrics()
	if !strings.Contains(m, `andara_character_bindings_total{outcome="ok"} 1`) {
		t.Fatal("ok not counted once")
	}
	// The losers of the race lost against a tentative flag.
	if !strings.Contains(m, `andara_character_bindings_total{outcome="race_lost"}`) || strings.Contains(m, `outcome="race_lost"} 0`) {
		t.Fatalf("race_lost not counted:\n%s", m)
	}
}

// AC-8's Gateway half: a Session's end produces UnbindCharacter{QUIT} to
// the body's Zone as the routing table last knew it, records the roster's
// position from the table, clears the binding, and frees the flag —
// after which the Account may select again, and not before.
func TestRoster_ReleaseProducesUnbind(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	id := f.create("Aldric")
	s := f.session("s1")
	if _, err := f.roster.SelectCharacter(ctx, s, id); err != nil {
		t.Fatal(err)
	}
	// The sim moved the body: the table followed (AW-SRV-010).
	f.bindings.Publish(sim.Event{Type: sim.EvCharacterLeft, Scope: sim.ScopeEntities(sim.EntityID(id)), Envelope: charLeft("town", "plaza", "north")})
	f.bindings.Publish(sim.Event{Type: sim.EvCharacterArrived, Scope: sim.ScopeEntities(sim.EntityID(id)), Envelope: charArrived("town", "hall", "south")})

	// Hold the produce so the window is observable.
	f.log.slow = 100 * time.Millisecond
	f.roster.ReleaseSession(s)
	// Inside the window: still live.
	if _, err := f.roster.SelectCharacter(ctx, f.session("s2"), id); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("select during teardown: %v, want already_live", err)
	}
	f.roster.ReleaseSession(s) // idempotent
	f.roster.Wait()

	recs := f.log.records()
	if len(recs) != 2 {
		t.Fatalf("records %v", recs)
	}
	u := recs[1]
	if u.GetZoneId() != "town" || u.GetActorId() != id || u.GetSessionId() != "s1" || u.GetUnbindCharacter().GetReason() != logv1.UnbindReason_QUIT {
		t.Fatalf("UnbindCharacter %v", u)
	}
	if _, bound, _ := f.bindings.Lookup("s1"); bound {
		t.Fatal("binding survived the teardown")
	}
	if _, _, ok := f.roster.Live(f.account); ok {
		t.Fatal("live flag survived the teardown")
	}
	ref, err := f.store.Character(f.account, id)
	if err != nil || ref.GetZoneId() != "town" || ref.GetRoomId() != "hall" {
		t.Fatalf("roster position %v %v, want town/hall", ref, err)
	}
	m := f.metrics()
	if !strings.Contains(m, `andara_character_unbinds_total{outcome="ok",reason="quit"} 1`) || !strings.Contains(m, "andara_sessions_bound 0") {
		t.Fatalf("metrics:\n%s", m)
	}
	// Free again.
	f.log.slow = 0
	if _, err := f.roster.SelectCharacter(ctx, f.session("s2"), id); err != nil {
		t.Fatalf("select after teardown: %v", err)
	}
	if last := f.log.records()[2]; last.GetBindCharacter().GetSpawnRoomId() != "hall" || last.GetZoneId() != "town" {
		t.Fatalf("the next bind is not routed to where the body went dormant: %v", last)
	}
}

// A teardown whose produce fails is logged and counted, and still frees
// the flag: the body stays present until the next select takes it.
func TestRoster_ReleaseProduceFailure(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	id := f.create("Aldric")
	s := f.session("s1")
	if _, err := f.roster.SelectCharacter(ctx, s, id); err != nil {
		t.Fatal(err)
	}
	f.log.failWith(ingress.ErrUnavailable)
	f.roster.ReleaseSession(s)
	f.roster.Wait()
	if _, _, ok := f.roster.Live(f.account); ok {
		t.Fatal("live flag survived")
	}
	if !strings.Contains(f.metrics(), `andara_character_unbinds_total{outcome="produce_failed",reason="quit"} 1`) {
		t.Fatal("produce_failed not counted")
	}
	// A Session that never selected releases nothing.
	f.roster.ReleaseSession(f.session("s-idle"))
	f.roster.Wait()
	if len(f.log.records()) != 1 {
		t.Fatal("an idle Session produced")
	}
}

// A Session that dies while its BindCharacter is still being produced is
// torn down once the produce finishes — the flag is never left set.
func TestRoster_ReleaseDuringSelect(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	id := f.create("Aldric")
	s := f.session("s1")
	f.log.slow = 50 * time.Millisecond
	done := make(chan error, 1)
	go func() {
		_, err := f.roster.SelectCharacter(ctx, s, id)
		done <- err
	}()
	time.Sleep(10 * time.Millisecond)
	f.roster.ReleaseSession(s)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	f.roster.Wait()
	if _, _, ok := f.roster.Live(f.account); ok {
		t.Fatal("live flag left set")
	}
	if !strings.Contains(f.metrics(), "andara_sessions_bound 0") {
		t.Fatalf("gauge:\n%s", f.metrics())
	}
	if n := len(f.log.records()); n != 2 {
		t.Fatalf("%d records, want the bind and the unbind", n)
	}
}

func charLeft(zone, room, dir string) *gamev1.EventEnvelope {
	return &gamev1.EventEnvelope{Payload: &gamev1.EventEnvelope_CharacterLeft{CharacterLeft: &gamev1.CharacterLeft{ZoneId: zone, RoomId: room, ToDirection: dir}}}
}

func charArrived(zone, room, dir string) *gamev1.EventEnvelope {
	return &gamev1.EventEnvelope{Payload: &gamev1.EventEnvelope_CharacterArrived{CharacterArrived: &gamev1.CharacterArrived{ZoneId: zone, RoomId: room, FromDirection: dir}}}
}
