// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package main

import (
	"bytes"
	"context"
	"io"
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
	"github.com/valesordev/andara/internal/eventually"
	"github.com/valesordev/andara/internal/testpki"
)

// The M1 gate over a real gateway (AW-SRV-014): two Accounts create a
// Character each and enter the World; one looks, sees the other, walks
// north through the log and the tick, and looks again; the other sees
// the arrival and the departure; a quit makes the body dormant where it
// stands, and the next select takes it there. Everything a player would
// see, asserted from the Event stream; everything the sim did, through
// sim.source=memory — the same handlers the broker feeds.
func TestRun_M1Gate(t *testing.T) {
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
		}, emptyEnv, &stdout, stderr)
	}()
	exited := false
	t.Cleanup(func() {
		if exited {
			return
		}
		// The server installs the handler; a SIGTERM before it has would
		// end the test binary. Give it a moment, then ask it to stop.
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

	hc := &http.Client{Transport: &http.Transport{TLSClientConfig: pki.ClientTLS(), ForceAttemptHTTP2: true}}
	base := "https://" + grpcAddr
	authc := authv1connect.NewAuthClient(hc, base)
	login := func(user, pass string) string {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			resp, err := authc.Authenticate(context.Background(), connect.NewRequest(&authv1.AuthenticateRequest{Username: user, Password: pass}))
			if err == nil {
				return resp.Msg.GetTokens().GetSessionToken()
			}
			select {
			case code := <-done:
				exited = true
				t.Fatalf("server exited %d before serving; stderr=%s", code, stderr.String())
			default:
			}
			if time.Now().After(deadline) {
				t.Fatalf("server never served: %v; stderr=%s", err, stderr.String())
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	brian := login("brian", password)
	// A second Account, so the two Sessions are two players.
	admin := adminv1connect.NewAdminClient(hc, base, connect.WithInterceptors(bearerInterceptor(brian)))
	if _, err := admin.CreateAccount(context.Background(), connect.NewRequest(&adminv1.CreateAccountRequest{Username: "alice", Password: "correct horse battery"})); err != nil {
		t.Fatal(err)
	}
	alice := login("alice", "correct horse battery")

	game := gamev1connect.NewGameClient(hc, base)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	open := func(token string) string {
		t.Helper()
		resp, err := game.OpenSession(ctx, connect.NewRequest(&gamev1.OpenSessionRequest{ProtocolVersion: 1, AuthToken: token, ClientName: "m1-test/0"}))
		if err != nil {
			t.Fatal(err)
		}
		return resp.Msg.GetSessionId()
	}
	sA, sB := open(brian), open(alice)

	// Create: Brian's Aldric, Alice's Brin. The roster says where each
	// will spawn.
	create := func(session, name string) string {
		t.Helper()
		resp, err := game.CreateCharacter(ctx, connect.NewRequest(&gamev1.CreateCharacterRequest{SessionId: session, Name: name}))
		if err != nil {
			t.Fatalf("CreateCharacter %s: %v", name, err)
		}
		c := resp.Msg.GetCharacter()
		if c.GetZoneId() != "town" || c.GetRoomId() != "plaza" || c.GetLive() || resp.Msg.GetMaxPerAccount() != 5 {
			t.Fatalf("created %v", resp.Msg)
		}
		return c.GetCharacterId()
	}
	aldric, brin := create(sA, "Aldric"), create(sB, "Brin")
	if _, err := game.CreateCharacter(ctx, connect.NewRequest(&gamev1.CreateCharacterRequest{SessionId: sA, Name: "brin"})); connect.CodeOf(err) != connect.CodeAlreadyExists {
		t.Fatalf("a name another Account holds: %v", err)
	}

	// Streams first, so the arrivals are seen; then select.
	streamA, streamB := subscribe(ctx, t, game, sA), subscribe(ctx, t, game, sB)
	sel := func(session, id string) {
		t.Helper()
		if _, err := game.SelectCharacter(ctx, connect.NewRequest(&gamev1.SelectCharacterRequest{SessionId: session, CharacterId: id})); err != nil {
			t.Fatalf("SelectCharacter: %v", err)
		}
	}
	sel(sB, brin)
	arr := streamB.next(t, "character_arrived")
	if a := arr.GetCharacterArrived(); a.GetCharacterName() != "Brin" || a.GetRoomId() != "plaza" || a.GetFromDirection() != "" {
		t.Fatalf("Brin's own arrival: %v", arr)
	}
	sel(sA, aldric)
	if a := streamA.next(t, "character_arrived").GetCharacterArrived(); a.GetCharacterName() != "Aldric" || a.GetRoomId() != "plaza" || a.GetFromDirection() != "" {
		t.Fatalf("Aldric's own arrival: %v", a)
	}
	// AC-3: the other player in the plaza saw it appear.
	if a := streamB.next(t, "character_arrived").GetCharacterArrived(); a.GetCharacterName() != "Aldric" || a.GetFromDirection() != "" {
		t.Fatalf("Brin's view of the arrival: %v", a)
	}
	// AC-4: Brian, on another Session, cannot select Aldric while it is live.
	sA2 := open(brian)
	_, err := game.SelectCharacter(ctx, connect.NewRequest(&gamev1.SelectCharacterRequest{SessionId: sA2, CharacterId: aldric}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "Aldric") {
		t.Fatalf("select while live: %v", err)
	}
	list, err := game.ListCharacters(ctx, connect.NewRequest(&gamev1.ListCharactersRequest{SessionId: sA2}))
	if err != nil || len(list.Msg.GetCharacters()) != 1 || !list.Msg.GetCharacters()[0].GetLive() {
		t.Fatalf("list while live: %v %v", list, err)
	}

	// AC-5: look answers with the Room, Here: naming the other.
	submit := func(session, raw, ref string) {
		t.Helper()
		if _, err := game.Submit(ctx, connect.NewRequest(&gamev1.SubmitRequest{SessionId: session, Raw: raw, ClientRef: ref})); err != nil {
			t.Fatalf("Submit %q: %v", raw, err)
		}
	}
	submit(sA, "look", "a-look-1")
	room := streamA.next(t, "room_described")
	if r := room.GetRoomDescribed(); r.GetRoomId() != "plaza" || r.GetTitle() != "Market Plaza" || strings.Join(r.GetOccupants(), ",") != "Brin" || room.GetClientRef() != "a-look-1" {
		t.Fatalf("look: %v", room)
	}

	// AC-10: north, through the log and the tick; both streams carry it.
	submit(sA, "north", "a-north")
	if l := streamA.next(t, "character_left").GetCharacterLeft(); l.GetToDirection() != "north" || l.GetRoomId() != "plaza" {
		t.Fatalf("own departure: %v", l)
	}
	if a := streamA.next(t, "character_arrived").GetCharacterArrived(); a.GetRoomId() != "hall" || a.GetFromDirection() != "south" {
		t.Fatalf("own arrival: %v", a)
	}
	if l := streamB.next(t, "character_left").GetCharacterLeft(); l.GetCharacterName() != "Aldric" || l.GetToDirection() != "north" {
		t.Fatalf("Brin's view of the move: %v", l)
	}
	submit(sA, "look", "a-look-2")
	if r := streamA.next(t, "room_described").GetRoomDescribed(); r.GetRoomId() != "hall" || len(r.GetOccupants()) != 0 {
		t.Fatalf("look in the hall: %v", r)
	}
	// Back to the plaza, so Brin sees the quit.
	submit(sA, "south", "a-south")
	streamA.next(t, "character_left")
	streamA.next(t, "character_arrived")
	streamB.next(t, "character_arrived")

	// AC-8: a CloseSession produces the UnbindCharacter; the Room sees
	// the departure with no direction; the occupants no longer name it.
	if _, err := game.CloseSession(ctx, connect.NewRequest(&gamev1.CloseSessionRequest{SessionId: sA})); err != nil {
		t.Fatal(err)
	}
	if l := streamB.next(t, "character_left").GetCharacterLeft(); l.GetCharacterName() != "Aldric" || l.GetToDirection() != "" {
		t.Fatalf("the quit as Brin sees it: %v", l)
	}
	submit(sB, "look", "b-look")
	if r := streamB.next(t, "room_described").GetRoomDescribed(); len(r.GetOccupants()) != 0 {
		t.Fatalf("a dormant body is listed: %v", r)
	}
	// The roster knows where the body went dormant, and that it is free.
	waitUntil(t, func() bool {
		list, err := game.ListCharacters(ctx, connect.NewRequest(&gamev1.ListCharactersRequest{SessionId: sA2}))
		return err == nil && len(list.Msg.GetCharacters()) == 1 && !list.Msg.GetCharacters()[0].GetLive() && list.Msg.GetCharacters()[0].GetRoomId() == "plaza"
	}, "the roster to free Aldric in the plaza")

	// AC-6: selected again, the body wakes where it was; Brin sees it.
	streamA2 := subscribe(ctx, t, game, sA2)
	sel(sA2, aldric)
	if a := streamA2.next(t, "character_arrived").GetCharacterArrived(); a.GetRoomId() != "plaza" || a.GetFromDirection() != "" {
		t.Fatalf("woken arrival: %v", a)
	}
	if a := streamB.next(t, "character_arrived").GetCharacterArrived(); a.GetCharacterName() != "Aldric" {
		t.Fatalf("Brin's view of the waking: %v", a)
	}
	submit(sA2, "look", "a2-look")
	if r := streamA2.next(t, "room_described").GetRoomDescribed(); r.GetRoomId() != "plaza" || strings.Join(r.GetOccupants(), ",") != "Brin" {
		t.Fatalf("look after waking: %v", r)
	}

	// The instruments, from the running server.
	waitUntil(t, func() bool {
		m := metrics(t, httpAddr)
		return strings.Contains(m, `andara_characters_total{state="present"} 2`) &&
			strings.Contains(m, "andara_sessions_bound 2") &&
			strings.Contains(m, `andara_character_unbinds_total{outcome="ok",reason="quit"} 1`) &&
			strings.Contains(m, `andara_character_bindings_total{outcome="ok"} 3`) &&
			strings.Contains(m, `andara_character_bindings_total{outcome="already_live"} 1`) &&
			strings.Contains(m, `andara_character_creations_total{outcome="ok"} 2`)
	}, "the roster metrics to settle")
	m := metrics(t, httpAddr)
	if !strings.Contains(m, `andara_characters_total{state="dormant"} 0`) {
		t.Errorf("dormant gauge:\n%s", m)
	}

	// A drain unbinds both: the log lines say so and the exit is clean.
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-done:
		exited = true
		if code != 0 {
			t.Fatalf("exit %d after SIGTERM; stderr=%s", code, stderr.String())
		}
	case <-time.After(15 * time.Second):
		t.Fatalf("server did not exit; stderr=%s", stderr.String())
	}
	out := stderr.String()
	for _, want := range []string{"character created", "character selected", `"msg":"character unbound"`, "character roster configured"} {
		if !strings.Contains(out, want) {
			t.Errorf("stderr lacks %q", want)
		}
	}
	if n := strings.Count(out, `"msg":"character unbound"`); n != 3 {
		t.Errorf("%d unbind lines, want 3 (the quit and the two drained)", n)
	}
	if strings.Contains(out, `"name":"Aldric"`) {
		t.Error("a Character name is a log key")
	}
}

// eventStream reads a Subscribe stream on its own goroutine.
type eventStream struct {
	events chan *gamev1.EventEnvelope
	errs   chan error
}

func subscribe(ctx context.Context, t *testing.T, game gamev1connect.GameClient, session string) *eventStream {
	t.Helper()
	stream, err := game.Subscribe(ctx, connect.NewRequest(&gamev1.SubscribeRequest{SessionId: session}))
	if err != nil {
		t.Fatal(err)
	}
	es := &eventStream{events: make(chan *gamev1.EventEnvelope, 64), errs: make(chan error, 1)}
	go func() {
		for stream.Receive() {
			es.events <- stream.Msg()
		}
		es.errs <- stream.Err()
	}()
	return es
}

// next returns the next Event of the named type, skipping frames and
// anything else; it fails after ten seconds.
func (es *eventStream) next(t *testing.T, typ string) *gamev1.EventEnvelope {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case env := <-es.events:
			if eventType(env) == typ {
				return env
			}
		case err := <-es.errs:
			t.Fatalf("stream ended: %v", err)
		case <-deadline:
			t.Fatalf("no %s within 10s", typ)
		}
	}
}

func eventType(env *gamev1.EventEnvelope) string {
	switch env.GetPayload().(type) {
	case *gamev1.EventEnvelope_RoomDescribed:
		return "room_described"
	case *gamev1.EventEnvelope_CharacterArrived:
		return "character_arrived"
	case *gamev1.EventEnvelope_CharacterLeft:
		return "character_left"
	case *gamev1.EventEnvelope_CommandRejected:
		return "command_rejected"
	}
	return ""
}

func bearerInterceptor(token string) connect.Interceptor {
	return connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			req.Header().Set("Authorization", "Bearer "+token)
			return next(ctx, req)
		}
	})
}

func metrics(t *testing.T, httpAddr string) string {
	t.Helper()
	resp, err := http.Get("http://" + httpAddr + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

// waitUntil is eventually.True with the deadline a server under -race needs.
func waitUntil(t *testing.T, cond func() bool, what string) {
	t.Helper()
	eventually.True(t, 10*time.Second, what, cond)
}
