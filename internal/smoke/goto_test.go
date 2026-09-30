// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

//go:build smoke

package smoke

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/genproto/googleapis/rpc/errdetails"

	accountsv1 "github.com/valesordev/andara/gen/go/andara/accounts/v1"
	adminv1 "github.com/valesordev/andara/gen/go/andara/admin/v1"
	"github.com/valesordev/andara/gen/go/andara/admin/v1/adminv1connect"
	authv1 "github.com/valesordev/andara/gen/go/andara/auth/v1"
	"github.com/valesordev/andara/gen/go/andara/auth/v1/authv1connect"
	gamev1 "github.com/valesordev/andara/gen/go/andara/game/v1"
	"github.com/valesordev/andara/gen/go/andara/game/v1/gamev1connect"
	"github.com/valesordev/andara/internal/eventually"
)

// TestLive_Goto is AW-SRV-036 against the running stack: the Gateway, the
// Command through Redpanda, the ticks of two Zones, and the roster.
//   - A Builder jumps from the plaza to the pier: a bystander in each sees
//     them go and come, they're shown The Pier, and the roster keeps them
//     there after quit (AC-1).
//   - Woken there, a goto within the docks stays on the docks' Partition
//     (AC-2).
//   - A player without builder is refused at authorize (AC-3).
func TestLive_Goto(t *testing.T) {
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
	run := letters(6)
	login := func(suffix string, roles ...accountsv1.Role) string {
		t.Helper()
		username := "goto-" + suffix + "-" + run
		if _, err := admin.CreateAccount(ctx, connect.NewRequest(&adminv1.CreateAccountRequest{Username: username, Password: "smoke-password-1", Roles: roles})); err != nil {
			t.Fatalf("CreateAccount %s: %v", username, err)
		}
		tok, err := authc.Authenticate(ctx, connect.NewRequest(&authv1.AuthenticateRequest{Username: username, Password: "smoke-password-1"}))
		if err != nil {
			t.Fatalf("Authenticate as %s: %v", username, err)
		}
		return tok.Msg.GetTokens().GetSessionToken()
	}
	tokA := login("builder", accountsv1.Role_PLAYER, accountsv1.Role_BUILDER)
	tokB, tokC := login("plaza"), login("pier")

	sc := httpClient(t)
	sc.Timeout = 0
	game := gamev1connect.NewGameClient(sc, baseURL(), connect.WithGRPC())
	open := func(token string) string {
		t.Helper()
		resp, err := game.OpenSession(ctx, connect.NewRequest(&gamev1.OpenSessionRequest{ProtocolVersion: 1, AuthToken: token, ClientName: "stack-smoke/goto"}))
		if err != nil {
			t.Fatalf("OpenSession: %v", err)
		}
		return resp.Msg.GetSessionId()
	}
	submit := func(session, raw, ref string) (*gamev1.SubmitResponse, error) {
		t.Helper()
		resp, err := game.Submit(ctx, connect.NewRequest(&gamev1.SubmitRequest{SessionId: session, Raw: raw, ClientRef: ref}))
		if err != nil {
			return nil, err
		}
		return resp.Msg, nil
	}
	must := func(session, raw, ref string) *gamev1.SubmitResponse {
		t.Helper()
		resp, err := submit(session, raw, ref)
		if err != nil {
			t.Fatalf("Submit %q: %v", raw, err)
		}
		return resp
	}
	// enter makes a Character, selects it, and brings it to the plaza from
	// wherever this environment spawns (as TestLive_M1Gate does).
	enter := func(who, token string) (session, id, name string, stream *eventStream) {
		t.Helper()
		session = open(token)
		name = "Goto" + letters(5)
		resp, err := game.CreateCharacter(ctx, connect.NewRequest(&gamev1.CreateCharacterRequest{SessionId: session, Name: name}))
		if err != nil {
			t.Fatalf("CreateCharacter %s: %v", name, err)
		}
		id = resp.Msg.GetCharacter().GetCharacterId()
		stream = newStream(ctx, t, game, session)
		if _, err := game.SelectCharacter(ctx, connect.NewRequest(&gamev1.SelectCharacterRequest{SessionId: session, CharacterId: id})); err != nil {
			t.Fatalf("SelectCharacter: %v", err)
		}
		a := stream.next(t, "character_arrived").GetCharacterArrived()
		if a.GetZoneId() == "purgatory" && a.GetRoomId() == "start" {
			must(session, "out", "out-"+who+"-"+run)
			a = stream.next(t, "character_arrived").GetCharacterArrived()
		}
		if a.GetCharacterName() != name || a.GetRoomId() != "plaza" {
			t.Fatalf("%s did not reach the plaza: %v", who, a)
		}
		return session, id, name, stream
	}

	// C walks to the pier and waits there; B waits in the plaza; A arrives
	// in the plaza last, so both bystanders are in place.
	sC, _, _, streamC := enter("C", tokC)
	must(sC, "south", "south-c-"+run)
	if a := streamC.next(t, "character_arrived").GetCharacterArrived(); a.GetRoomId() != "pier" {
		t.Fatalf("C's walk to the pier: %v", a)
	}
	sB, _, _, streamB := enter("B", tokB)
	sA, chA, nameA, streamA := enter("A", tokA)
	if a := streamB.next(t, "character_arrived").GetCharacterArrived(); a.GetCharacterName() != nameA {
		t.Fatalf("B's view of A arriving: %v", a)
	}

	// AC-1.
	must(sA, "goto docks/pier", "goto-pier-"+run)
	if l := streamB.next(t, "character_left").GetCharacterLeft(); l.GetCharacterName() != nameA || l.GetToDirection() != "" {
		t.Fatalf("B's view of the jump: %v (renders \"<name> leaves.\" only with no direction)", l)
	}
	if a := streamC.next(t, "character_arrived").GetCharacterArrived(); a.GetCharacterName() != nameA || a.GetFromDirection() != "" || a.GetRoomId() != "pier" {
		t.Fatalf("C's view of the landing: %v", a)
	}
	if a := streamA.next(t, "character_arrived").GetCharacterArrived(); a.GetRoomId() != "pier" {
		t.Fatalf("A's own landing: %v", a)
	}
	if r := streamA.next(t, "room_described").GetRoomDescribed(); r.GetTitle() != "The Pier" {
		t.Fatalf("A is shown %v", r)
	}
	if _, err := game.CloseSession(ctx, connect.NewRequest(&gamev1.CloseSessionRequest{SessionId: sA})); err != nil {
		t.Fatal(err)
	}
	sA2 := open(tokA)
	eventually.Observed(t, 10*time.Second, "the roster to keep "+nameA+" at docks/pier", func() (bool, string) {
		list, err := game.ListCharacters(ctx, connect.NewRequest(&gamev1.ListCharactersRequest{SessionId: sA2}))
		if err != nil {
			return false, err.Error()
		}
		cs := list.Msg.GetCharacters()
		return len(cs) == 1 && !cs[0].GetLive() && cs[0].GetZoneId() == "docks" && cs[0].GetRoomId() == "pier", list.Msg.String()
	})

	// AC-2: woken at the pier, a goto within the docks is on the docks'
	// Partition, as the walk C took there was.
	streamA2 := newStream(ctx, t, game, sA2)
	if _, err := game.SelectCharacter(ctx, connect.NewRequest(&gamev1.SelectCharacterRequest{SessionId: sA2, CharacterId: chA})); err != nil {
		t.Fatal(err)
	}
	if a := streamA2.next(t, "character_arrived").GetCharacterArrived(); a.GetRoomId() != "pier" {
		t.Fatalf("A woke at %v", a)
	}
	docks := must(sC, "look", "look-c-"+run).GetPartition()
	if got := must(sA2, "goto warehouse", "goto-warehouse-"+run).GetPartition(); got != docks {
		t.Fatalf("goto warehouse went to partition %d; the docks' is %d", got, docks)
	}
	if a := streamA2.next(t, "character_arrived").GetCharacterArrived(); a.GetZoneId() != "docks" || a.GetRoomId() != "warehouse" {
		t.Fatalf("A's jump within the docks: %v", a)
	}

	// AC-3: B holds no builder.
	_, err = submit(sB, "goto docks/pier", "goto-b-"+run)
	var ce *connect.Error
	if !errors.As(err, &ce) || ce.Code() != connect.CodePermissionDenied || ce.Message() != "you may not goto" || reasonOf(ce) != "not_authorized" {
		t.Fatalf("a player's goto: %v", err)
	}
}

func reasonOf(ce *connect.Error) string {
	for _, d := range ce.Details() {
		if v, err := d.Value(); err == nil {
			if info, ok := v.(*errdetails.ErrorInfo); ok {
				return info.GetReason()
			}
		}
	}
	return ""
}
