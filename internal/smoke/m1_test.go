// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

//go:build smoke

package smoke

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/valesordev/andara/gen/go/andara/admin/v1"
	"github.com/valesordev/andara/gen/go/andara/admin/v1/adminv1connect"
	authv1 "github.com/valesordev/andara/gen/go/andara/auth/v1"
	"github.com/valesordev/andara/gen/go/andara/auth/v1/authv1connect"
	gamev1 "github.com/valesordev/andara/gen/go/andara/game/v1"
	"github.com/valesordev/andara/gen/go/andara/game/v1/gamev1connect"
)

// The M1 gate against the running stack (AW-SRV-014): two players enter the
// World, one looks and sees the other, walks north — the Command through
// Redpanda, applied by the tick, the move on both Event streams — looks
// again, and quits; the other sees the arrival and the departure; the next
// select wakes the body where it went dormant. Then the instruments this
// story is the first to drive live, scraped from the server: the produced
// Submits AW-SRV-010 could only count in its integration suite, the Event
// delivery AW-SRV-011 could only observe there, and AW-SRV-031's dedup of a
// produced Submit.
//
// Accounts and names are fresh per run: the stack's topics persist, a name
// is reserved forever, and a roster holds five.
func TestLive_M1Gate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	hc := httpClient(t)
	authc := authv1connect.NewAuthClient(hc, baseURL(), connect.WithGRPC())
	user, pass := operator(t)
	op, err := authc.Authenticate(ctx, connect.NewRequest(&authv1.AuthenticateRequest{Username: user, Password: pass}))
	if err != nil {
		t.Fatalf("Authenticate as the bootstrap operator: %v", err)
	}
	admin := adminv1connect.NewAdminClient(hc, baseURL(), connect.WithGRPC(), bearer(op.Msg.GetTokens().GetSessionToken()))
	login := func(suffix string) string {
		t.Helper()
		username := "smoke-" + suffix
		if _, err := admin.CreateAccount(ctx, connect.NewRequest(&adminv1.CreateAccountRequest{Username: username, Password: "smoke-password-1"})); err != nil {
			t.Fatalf("CreateAccount %s: %v", username, err)
		}
		tok, err := authc.Authenticate(ctx, connect.NewRequest(&authv1.AuthenticateRequest{Username: username, Password: "smoke-password-1"}))
		if err != nil {
			t.Fatalf("Authenticate as %s: %v", username, err)
		}
		return tok.Msg.GetTokens().GetSessionToken()
	}
	run := letters(6)
	tokA, tokB := login("a-"+run), login("b-"+run)

	// Streams outlive httpClient's unary timeout.
	sc := httpClient(t)
	sc.Timeout = 0
	game := gamev1connect.NewGameClient(sc, baseURL(), connect.WithGRPC())
	open := func(token string) string {
		t.Helper()
		resp, err := game.OpenSession(ctx, connect.NewRequest(&gamev1.OpenSessionRequest{ProtocolVersion: 1, AuthToken: token, ClientName: "stack-play/m1"}))
		if err != nil {
			t.Fatalf("OpenSession: %v", err)
		}
		return resp.Msg.GetSessionId()
	}
	sA, sB := open(tokA), open(tokB)
	before := scrape(t)
	nameA, nameB := "Smoke"+letters(5), "Smoke"+letters(5)
	create := func(session, name string) string {
		t.Helper()
		resp, err := game.CreateCharacter(ctx, connect.NewRequest(&gamev1.CreateCharacterRequest{SessionId: session, Name: name}))
		if err != nil {
			t.Fatalf("CreateCharacter %s: %v", name, err)
		}
		t.Logf("character_id=%s name=%s spawn=%s/%s (%d of %d)", resp.Msg.GetCharacter().GetCharacterId(), name,
			resp.Msg.GetCharacter().GetZoneId(), resp.Msg.GetCharacter().GetRoomId(), 1, resp.Msg.GetMaxPerAccount())
		return resp.Msg.GetCharacter().GetCharacterId()
	}
	chA, chB := create(sA, nameA), create(sB, nameB)
	if _, err := game.CreateCharacter(ctx, connect.NewRequest(&gamev1.CreateCharacterRequest{SessionId: sA, Name: strings.ToUpper(nameB)})); connect.CodeOf(err) != connect.CodeAlreadyExists {
		t.Fatalf("a name the other Account holds, in another case: %v", err)
	}

	streamA, streamB := newStream(ctx, t, game, sA), newStream(ctx, t, game, sB)
	sel := func(session, id string) {
		t.Helper()
		resp, err := game.SelectCharacter(ctx, connect.NewRequest(&gamev1.SelectCharacterRequest{SessionId: session, CharacterId: id}))
		if err != nil {
			t.Fatalf("SelectCharacter: %v", err)
		}
		t.Logf("BindCharacter at partition=%d offset=%d", resp.Msg.GetPartition(), resp.Msg.GetAcceptedOffset())
	}
	sel(sB, chB)
	if a := streamB.next(t, "character_arrived").GetCharacterArrived(); a.GetCharacterName() != nameB || a.GetRoomId() != "plaza" || a.GetFromDirection() != "" {
		t.Fatalf("B's own arrival: %v", a)
	}
	sel(sA, chA)
	if a := streamA.next(t, "character_arrived").GetCharacterArrived(); a.GetCharacterName() != nameA || a.GetFromDirection() != "" {
		t.Fatalf("A's own arrival: %v", a)
	}
	if a := streamB.next(t, "character_arrived").GetCharacterArrived(); a.GetCharacterName() != nameA {
		t.Fatalf("B's view of A arriving: %v", a)
	}

	submit := func(session, raw, ref string) *gamev1.SubmitResponse {
		t.Helper()
		resp, err := game.Submit(ctx, connect.NewRequest(&gamev1.SubmitRequest{SessionId: session, Raw: raw, ClientRef: ref}))
		if err != nil {
			t.Fatalf("Submit %q: %v", raw, err)
		}
		return resp.Msg
	}
	// AC-5 / play AC-1: the Room, Here: naming the other.
	submit(sA, "look", "look-1-"+run)
	if r := streamA.next(t, "room_described").GetRoomDescribed(); r.GetRoomId() != "plaza" || !hasName(r.GetOccupants(), nameB) {
		t.Fatalf("look: %v", r)
	}
	// AC-10 / play AC-2, AC-3: north through the broker; both streams.
	first := submit(sA, "north", "north-"+run)
	if l := streamA.next(t, "character_left").GetCharacterLeft(); l.GetToDirection() != "north" {
		t.Fatalf("own departure: %v", l)
	}
	if a := streamA.next(t, "character_arrived").GetCharacterArrived(); a.GetRoomId() != "hall" {
		t.Fatalf("own arrival: %v", a)
	}
	if l := streamB.next(t, "character_left").GetCharacterLeft(); l.GetCharacterName() != nameA || l.GetToDirection() != "north" {
		t.Fatalf("B's view of the move: %v", l)
	}
	// AW-SRV-031 on a produced Submit: the same client_ref again is the
	// same offset, and nothing moves again.
	again := submit(sA, "north", "north-"+run)
	if again.GetAcceptedOffset() != first.GetAcceptedOffset() || again.GetPartition() != first.GetPartition() {
		t.Fatalf("retry answered %v, first %v", again, first)
	}
	submit(sA, "look", "look-2-"+run)
	if r := streamA.next(t, "room_described").GetRoomDescribed(); r.GetRoomId() != "hall" {
		t.Fatalf("look in the hall: %v", r)
	}
	// play AC-4: a post-log rejection reads as prose.
	submit(sA, "west", "west-"+run)
	if rej := streamA.next(t, "command_rejected").GetCommandRejected(); rej.GetCode() != "no_such_exit" || rej.GetMessage() != "there is no exit west" {
		t.Fatalf("rejection: %v", rej)
	}
	submit(sA, "south", "south-"+run)
	streamA.next(t, "character_left")
	streamA.next(t, "character_arrived")
	streamB.next(t, "character_arrived")

	// AC-8: quit. B sees the departure with no direction; the roster
	// frees the Character in the plaza.
	if _, err := game.CloseSession(ctx, connect.NewRequest(&gamev1.CloseSessionRequest{SessionId: sA})); err != nil {
		t.Fatal(err)
	}
	if l := streamB.next(t, "character_left").GetCharacterLeft(); l.GetCharacterName() != nameA || l.GetToDirection() != "" {
		t.Fatalf("the quit as B sees it: %v", l)
	}
	submit(sB, "look", "look-b-"+run)
	if r := streamB.next(t, "room_described").GetRoomDescribed(); hasName(r.GetOccupants(), nameA) {
		t.Fatalf("a dormant body is listed: %v", r)
	}
	sA2 := open(tokA)
	deadline := time.Now().Add(10 * time.Second)
	for {
		list, err := game.ListCharacters(ctx, connect.NewRequest(&gamev1.ListCharactersRequest{SessionId: sA2}))
		if err == nil && len(list.Msg.GetCharacters()) == 1 && !list.Msg.GetCharacters()[0].GetLive() && list.Msg.GetCharacters()[0].GetRoomId() == "plaza" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the roster never freed %s in the plaza: %v %v", nameA, list, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	// AC-6: woken where it was.
	streamA2 := newStream(ctx, t, game, sA2)
	sel(sA2, chA)
	if a := streamA2.next(t, "character_arrived").GetCharacterArrived(); a.GetRoomId() != "plaza" || a.GetFromDirection() != "" {
		t.Fatalf("woken arrival: %v", a)
	}
	if a := streamB.next(t, "character_arrived").GetCharacterArrived(); a.GetCharacterName() != nameA {
		t.Fatalf("B's view of the waking: %v", a)
	}

	// The instruments, from the running server (AW-SRV-010, -011, -031,
	// -014 §8): what only a bound Character could drive.
	after := scrape(t)
	for _, series := range []string{
		`andara_ingress_submits_total{outcome="produced"}`,
		`andara_ingress_submits_total{outcome="deduplicated"}`,
		fmt.Sprintf(`andara_ingress_produced_total{partition="%d"}`, first.GetPartition()),
		`andara_ingress_produce_duration_seconds_count`,
		`andara_command_duration_seconds_count{phase="pre_log",verb="north"}`,
		`andara_command_duration_seconds_count{phase="post_log",verb="move"}`,
		`andara_command_duration_seconds_count{phase="post_log",verb="bind_character"}`,
		`andara_stream_events_sent_total{type="character_arrived"}`,
		`andara_stream_events_sent_total{type="room_described"}`,
		`andara_character_bindings_total{outcome="ok"}`,
		`andara_character_unbinds_total{outcome="ok",reason="quit"}`,
		`andara_character_creations_total{outcome="ok"}`,
	} {
		if !(value(after, series) > value(before, series)) {
			t.Errorf("%s did not move: %v -> %v", series, value(before, series), value(after, series))
		}
	}
	for _, series := range []string{`andara_characters_total{state="present"}`, `andara_sessions_bound`} {
		if value(after, series) < 2 {
			t.Errorf("%s = %v, want at least 2", series, value(after, series))
		}
	}
	t.Logf("produced=%v deduplicated=%v present=%v bound=%v",
		value(after, `andara_ingress_submits_total{outcome="produced"}`), value(after, `andara_ingress_submits_total{outcome="deduplicated"}`),
		value(after, `andara_characters_total{state="present"}`), value(after, `andara_sessions_bound`))
}

// eventStream reads a Subscribe stream on its own goroutine.
type eventStream struct {
	events chan *gamev1.EventEnvelope
	errs   chan error
}

func newStream(ctx context.Context, t *testing.T, game gamev1connect.GameClient, session string) *eventStream {
	t.Helper()
	stream, err := game.Subscribe(ctx, connect.NewRequest(&gamev1.SubscribeRequest{SessionId: session}))
	if err != nil {
		t.Fatal(err)
	}
	es := &eventStream{events: make(chan *gamev1.EventEnvelope, 256), errs: make(chan error, 1)}
	go func() {
		for stream.Receive() {
			es.events <- stream.Msg()
		}
		es.errs <- stream.Err()
	}()
	return es
}

func (es *eventStream) next(t *testing.T, typ string) *gamev1.EventEnvelope {
	t.Helper()
	deadline := time.After(15 * time.Second)
	for {
		select {
		case env := <-es.events:
			if eventType(env) == typ {
				return env
			}
		case err := <-es.errs:
			t.Fatalf("stream ended: %v", err)
		case <-deadline:
			t.Fatalf("no %s within 15s", typ)
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

// scrape reads the server's /metrics.
func scrape(t *testing.T) string {
	t.Helper()
	resp, err := http.Get("http://" + env("ANDARA_SMOKE_METRICS_ADDR", "localhost:8080") + "/metrics")
	if err != nil {
		t.Fatalf("scrape: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

// value is one series' sample, or -1 when absent.
func value(text, series string) float64 {
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, series+" ") {
			var v float64
			if _, err := fmt.Sscanf(strings.TrimPrefix(line, series+" "), "%g", &v); err == nil {
				return v
			}
		}
	}
	return -1
}

func letters(n int) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz"
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b)
}

func hasName(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
