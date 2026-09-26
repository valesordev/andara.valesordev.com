// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/valesordev/andara/gen/go/andara/admin/v1"
	"github.com/valesordev/andara/gen/go/andara/admin/v1/adminv1connect"
	authv1 "github.com/valesordev/andara/gen/go/andara/auth/v1"
	"github.com/valesordev/andara/gen/go/andara/auth/v1/authv1connect"
	gamev1 "github.com/valesordev/andara/gen/go/andara/game/v1"
	"github.com/valesordev/andara/gen/go/andara/game/v1/gamev1connect"
	"github.com/valesordev/andara/internal/testpki"
)

// AW-SRV-015 over a real gateway, sim.source=memory: a dropped connection
// leaves the body linkdead where it stands, visibly (AC-1); another of the
// Account's Characters is already_live (AC-16); selecting the same one
// reconnects it, and a stream carrying last_event_id resumes with what the
// Room did in between and no gap (AC-2); dropped again, the grace runs out
// and the body despawns where it stood, freeing the Account (AC-3, AC-4);
// and twenty quits each followed at once by a select are never
// already_live (AC-5). The grace is 3 s, so recovery.rto_target is 1 s to
// keep the invariant.
func TestRun_Linkdead(t *testing.T) {
	pki := testpki.New(t)
	path := abs(t, filepath.Join("..", "..", "testdata", "content", "valid"))
	grpcAddr, httpAddr := freeAddr(t), freeAddr(t)
	var stdout bytes.Buffer
	stderr := &lockedBuffer{}
	done := make(chan int, 1)
	const password = "first-operator-password"
	go func() {
		done <- run([]string{
			"--content-source=dir", "--content-path=" + path,
			"--grpc-listen=" + grpcAddr, "--http-port=" + httpAddr,
			"--tls-cert-file=" + pki.CertFile, "--tls-key-file=" + pki.KeyFile,
			"--grpc-drain-timeout=5s",
			"--auth-store=memory", "--sim-source=memory", "--auth-token-key-file=" + keyFile(t),
			"--auth-bootstrap-operator=brian:" + password,
			"--auth-argon2-memory-kib=64", "--auth-argon2-time=1", "--auth-argon2-threads=1",
			"--session-linkdead-grace=3s", "--session-linkdead-max=4s", "--session-linkdead-combat-extension=1s",
			"--recovery-rto-target=1s",
		}, emptyEnv, &stdout, stderr)
	}()
	t.Cleanup(func() {
		select {
		case <-done:
			return
		case <-time.After(time.Second):
		}
		_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Errorf("server did not exit; stderr=%s", stderr.String())
		}
	})

	// Every Session gets its own connection, so dropping one drops nothing
	// else.
	client := func() (gamev1connect.GameClient, *http.Transport) {
		tr := &http.Transport{TLSClientConfig: pki.ClientTLS(), ForceAttemptHTTP2: true}
		return gamev1connect.NewGameClient(&http.Client{Transport: tr}, "https://"+grpcAddr), tr
	}
	shared := &http.Client{Transport: &http.Transport{TLSClientConfig: pki.ClientTLS(), ForceAttemptHTTP2: true}}
	authc := authv1connect.NewAuthClient(shared, "https://"+grpcAddr)
	login := func(user, pass string) string {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			resp, err := authc.Authenticate(context.Background(), connect.NewRequest(&authv1.AuthenticateRequest{Username: user, Password: pass}))
			if err == nil {
				return resp.Msg.GetTokens().GetSessionToken()
			}
			if time.Now().After(deadline) {
				t.Fatalf("server never served: %v; stderr=%s", err, stderr.String())
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	brian := login("brian", password)
	admin := adminv1connect.NewAdminClient(shared, "https://"+grpcAddr, connect.WithInterceptors(bearerInterceptor(brian)))
	if _, err := admin.CreateAccount(context.Background(), connect.NewRequest(&adminv1.CreateAccountRequest{Username: "alice", Password: "correct horse battery"})); err != nil {
		t.Fatal(err)
	}
	alice := login("alice", "correct horse battery")

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	open := func(game gamev1connect.GameClient, token string) string {
		t.Helper()
		resp, err := game.OpenSession(ctx, connect.NewRequest(&gamev1.OpenSessionRequest{ProtocolVersion: 1, AuthToken: token, ClientName: "linkdead-test/0"}))
		if err != nil {
			t.Fatal(err)
		}
		return resp.Msg.GetSessionId()
	}
	sel := func(game gamev1connect.GameClient, session, character string) error {
		_, err := game.SelectCharacter(ctx, connect.NewRequest(&gamev1.SelectCharacterRequest{SessionId: session, CharacterId: character}))
		return err
	}
	create := func(game gamev1connect.GameClient, session, name string) string {
		t.Helper()
		resp, err := game.CreateCharacter(ctx, connect.NewRequest(&gamev1.CreateCharacterRequest{SessionId: session, Name: name}))
		if err != nil {
			t.Fatal(err)
		}
		return resp.Msg.GetCharacter().GetCharacterId()
	}
	submit := func(game gamev1connect.GameClient, session, raw string) {
		t.Helper()
		if _, err := game.Submit(ctx, connect.NewRequest(&gamev1.SubmitRequest{SessionId: session, Raw: raw})); err != nil {
			t.Fatalf("Submit %q: %v", raw, err)
		}
	}

	// Brin stands in the plaza and watches.
	gB, _ := client()
	sB := open(gB, alice)
	brin := create(gB, sB, "Brin")
	streamB := subscribe(ctx, t, gB, sB)
	if err := sel(gB, sB, brin); err != nil {
		t.Fatal(err)
	}
	streamB.next(t, "character_arrived")

	// Aldric enters on a connection of its own.
	gA, trA := client()
	sA := open(gA, brian)
	aldric := create(gA, sA, "Aldric")
	cato := create(gA, sA, "Cato")
	actx, acancel := context.WithCancel(ctx)
	streamA := subscribe(actx, t, gA, sA)
	if err := sel(gA, sA, aldric); err != nil {
		t.Fatal(err)
	}
	lastA := streamA.next(t, "character_arrived").GetEventId()
	streamB.next(t, "character_arrived")

	t.Cleanup(func() {
		if t.Failed() {
			for _, l := range strings.Split(stderr.String(), "\n") {
				if strings.Contains(l, "session closed") || strings.Contains(l, "linkdead") || strings.Contains(l, "character") {
					t.Log(l)
				}
			}
		}
	})
	// AC-1: the connection drops with no CloseSession. A canceled stream
	// leaves the connection busy for a moment, so it is closed as soon as
	// it is idle, and until the drop has been seen.
	drop := func(tr *http.Transport) (stop func()) {
		quit := make(chan struct{})
		go func() {
			for {
				tr.CloseIdleConnections()
				select {
				case <-quit:
					return
				case <-time.After(20 * time.Millisecond):
				}
			}
		}()
		return func() { close(quit) }
	}
	acancel()
	stopA := drop(trA)
	defer stopA()
	if l := streamB.next(t, "character_linkdead").GetCharacterLinkdead(); l.GetCharacterName() != "Aldric" || l.GetRoomId() != "plaza" {
		t.Fatalf("Brin's view of the drop: %v", l)
	}
	submit(gB, sB, "look")
	if r := streamB.next(t, "room_described").GetRoomDescribed(); strings.Join(r.GetOccupants(), ",") != "Aldric" || strings.Join(r.GetLinkdead(), ",") != "Aldric" {
		t.Fatalf("look while Aldric is linkdead: occupants %v, linkdead %v", r.GetOccupants(), r.GetLinkdead())
	}
	// The Room goes on while Aldric is away: Brin leaves and comes back.
	submit(gB, sB, "north")
	streamB.next(t, "character_arrived")
	submit(gB, sB, "south")
	streamB.next(t, "character_arrived")

	// AC-16: another of Brian's Characters is already_live, naming Aldric.
	gA2, trA2 := client()
	sA2 := open(gA2, brian)
	if err := sel(gA2, sA2, cato); connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "Aldric") {
		t.Fatalf("select Cato while Aldric is linkdead: %v", err)
	}

	// AC-2: selecting Aldric is the reconnect; the stream resumes from the
	// last Event the lost one saw, with Brin's walk first and no gap.
	if err := sel(gA2, sA2, aldric); err != nil {
		t.Fatalf("the reconnect: %v", err)
	}
	if r := streamB.next(t, "character_reconnected").GetCharacterReconnected(); r.GetCharacterName() != "Aldric" || r.GetRoomId() != "plaza" {
		t.Fatalf("Brin's view of the reconnect: %v", r)
	}
	a2ctx, a2cancel := context.WithCancel(ctx)
	resumed, err := gA2.Subscribe(a2ctx, connect.NewRequest(&gamev1.SubscribeRequest{SessionId: sA2, LastEventId: lastA}))
	if err != nil {
		t.Fatal(err)
	}
	// Event IDs are World-wide, so what one Character perceives is not
	// contiguous: the resume is checked as every Event Aldric's Room had
	// after lastA, in order — its own linkdead, Brin leaving and coming
	// back — then the reconnect. A Resync, or a missing one, fails.
	var got []string
	prev := lastA
	for len(got) < 4 && resumed.Receive() {
		env := resumed.Msg()
		if env.GetHeartbeat() != nil {
			continue
		}
		if env.GetResync() != nil {
			t.Fatalf("the reconnect was a Resync: %v", env)
		}
		if env.GetEventId() <= prev {
			t.Fatalf("event %d after %d", env.GetEventId(), prev)
		}
		prev = env.GetEventId()
		got = append(got, eventType(env))
	}
	if want := "character_linkdead,character_left,character_arrived,character_reconnected"; strings.Join(got, ",") != want {
		t.Fatalf("resumed with %v, want %s", got, want)
	}
	a2cancel()

	// AC-3 and AC-4: dropped again, the grace runs out; the body despawns
	// where it stood, and the Account is free.
	stopA2 := drop(trA2)
	defer stopA2()
	streamB.next(t, "character_linkdead")
	if d := streamB.next(t, "character_despawned").GetCharacterDespawned(); d.GetReason() != "linkdead" || d.GetCharacterName() != "Aldric" {
		t.Fatalf("the grace ran out: %v", d)
	}
	gA3, _ := client()
	sA3 := open(gA3, brian)
	waitUntil(t, func() bool { return sel(gA3, sA3, cato) == nil }, "the despawn to free Brian's Account")

	// AC-5: a quit, then at once a select of the same Character, twenty
	// times: never already_live.
	if _, err := gA3.CloseSession(ctx, connect.NewRequest(&gamev1.CloseSessionRequest{SessionId: sA3})); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		s := open(gA3, brian)
		if err := sel(gA3, s, aldric); err != nil {
			t.Fatalf("round %d: select after the quit: %v", i, err)
		}
		if _, err := gA3.CloseSession(ctx, connect.NewRequest(&gamev1.CloseSessionRequest{SessionId: s})); err != nil {
			t.Fatalf("round %d: %v", i, err)
		}
	}

	waitUntil(t, func() bool {
		m := metrics(t, httpAddr)
		return strings.Contains(m, `andara_linkdead_outcomes_total{outcome="reconnected"} 1`) &&
			strings.Contains(m, `andara_linkdead_outcomes_total{outcome="despawned"} 1`) &&
			strings.Contains(m, `andara_sessions_linkdead{in_combat="false"} 0`) &&
			strings.Contains(m, "andara_reconnect_resyncs_total 0")
	}, "the linkdead metrics")
	for _, want := range []string{`"msg":"character linkdead"`, `"msg":"character reconnected"`, `"msg":"character despawned"`} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("stderr lacks %s", want)
		}
	}
	// The expiry's line names the Session that went linkdead, from the
	// roster's hold, and the trace of the tick that applied it (§8 review).
	var despawned map[string]any
	for _, l := range strings.Split(stderr.String(), "\n") {
		if strings.Contains(l, `"msg":"character despawned"`) {
			if err := json.Unmarshal([]byte(l), &despawned); err != nil {
				t.Fatal(err)
			}
		}
	}
	if despawned["session_id"] != sA2 || despawned["outcome"] != "despawned" {
		t.Errorf("the despawn line: session_id %v outcome %v, want %s despawned", despawned["session_id"], despawned["outcome"], sA2)
	}
	if tr, _ := despawned["trace_id"].(string); len(tr) != 32 || strings.Trim(tr, "0") == "" {
		t.Errorf("the despawn line's trace_id is %q", tr)
	}
}
