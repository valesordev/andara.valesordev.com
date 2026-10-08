// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package main

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"connectrpc.com/connect"

	accountsv1 "github.com/valesordev/andara/gen/go/andara/accounts/v1"
	adminv1 "github.com/valesordev/andara/gen/go/andara/admin/v1"
	"github.com/valesordev/andara/gen/go/andara/admin/v1/adminv1connect"
	authv1 "github.com/valesordev/andara/gen/go/andara/auth/v1"
	"github.com/valesordev/andara/gen/go/andara/auth/v1/authv1connect"
	gamev1 "github.com/valesordev/andara/gen/go/andara/game/v1"
	"github.com/valesordev/andara/gen/go/andara/game/v1/gamev1connect"
	"github.com/valesordev/andara/internal/testpki"
)

// AW-SRV-032 over a real gateway, sim.source=memory: switching bodies shows
// the Room one departure ("switch") and one arrival, in that order (AC-6); a
// live Character cannot be deleted (AC-5); a deleted one keeps its name and
// its roster entry (AC-1, AC-2); and once character.delete_retention has
// passed the Gateway's sweep produces the purge, the sim removes the dormant
// body, and the dormant count falls while the roster's deleted count holds
// (AC-3). Retention is 2 s and the sweep runs every 500 ms.
func TestRun_DeleteSwitchAndPurge(t *testing.T) {
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
			"--character-delete-retention=2s", "--character-purge-sweep-interval=500ms",
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

	shared := &http.Client{Transport: &http.Transport{TLSClientConfig: pki.ClientTLS(), ForceAttemptHTTP2: true}}
	game := gamev1connect.NewGameClient(shared, "https://"+grpcAddr)
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
	open := func(token string) string {
		t.Helper()
		resp, err := game.OpenSession(ctx, connect.NewRequest(&gamev1.OpenSessionRequest{ProtocolVersion: 1, AuthToken: token, ClientName: "delete-test/0"}))
		if err != nil {
			t.Fatal(err)
		}
		return resp.Msg.GetSessionId()
	}
	create := func(session, name string) string {
		t.Helper()
		resp, err := game.CreateCharacter(ctx, connect.NewRequest(&gamev1.CreateCharacterRequest{SessionId: session, Name: name}))
		if err != nil {
			t.Fatal(err)
		}
		return resp.Msg.GetCharacter().GetCharacterId()
	}
	sel := func(session, character string) error {
		_, err := game.SelectCharacter(ctx, connect.NewRequest(&gamev1.SelectCharacterRequest{SessionId: session, CharacterId: character}))
		return err
	}
	del := func(session, character string) (*gamev1.DeleteCharacterResponse, error) {
		resp, err := game.DeleteCharacter(ctx, connect.NewRequest(&gamev1.DeleteCharacterRequest{SessionId: session, CharacterId: character}))
		if err != nil {
			return nil, err
		}
		return resp.Msg, nil
	}
	closeSession := func(session string) {
		t.Helper()
		if _, err := game.CloseSession(ctx, connect.NewRequest(&gamev1.CloseSessionRequest{SessionId: session})); err != nil {
			t.Fatal(err)
		}
	}

	// Brin stands in the plaza and watches.
	sB := open(alice)
	brin := create(sB, "Brin")
	streamB := subscribe(ctx, t, game, sB)
	if err := sel(sB, brin); err != nil {
		t.Fatal(err)
	}
	streamB.next(t, "character_arrived")

	// Brian's Aldric and Brenna: AC-6, a switch inside one Session.
	sA := open(brian)
	aldric, brenna := create(sA, "Aldric"), create(sA, "Brenna")
	if err := sel(sA, aldric); err != nil {
		t.Fatal(err)
	}
	if a := streamB.next(t, "character_arrived").GetCharacterArrived(); a.GetCharacterName() != "Aldric" {
		t.Fatalf("Brin's view of Aldric: %v", a)
	}
	if err := sel(sA, brenna); err != nil {
		t.Fatalf("the switch: %v", err)
	}
	if d := streamB.next(t, "character_despawned").GetCharacterDespawned(); d.GetReason() != "switch" || d.GetCharacterName() != "Aldric" || d.GetRoomId() != "plaza" {
		t.Fatalf("Brin's view of the switch out: %v", d)
	}
	if a := streamB.next(t, "character_arrived").GetCharacterArrived(); a.GetCharacterName() != "Brenna" || a.GetRoomId() != "plaza" {
		t.Fatalf("Brin's view of the switch in: %v", a)
	}

	// AC-5: Brenna is live. Aldric, dormant, can be deleted at once.
	if _, err := del(sA, brenna); connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "Brenna") {
		t.Fatalf("delete of the live Character: %v", err)
	}
	gone, err := del(sA, aldric)
	if err != nil {
		t.Fatalf("delete of the dormant Character: %v", err)
	}
	if c := gone.GetCharacter(); c.GetStatus() != accountsv1.CharacterStatus_CHARACTER_STATUS_DELETED || c.GetDeletedUnix() == 0 {
		t.Fatalf("deleted %v", c)
	}
	closeSession(sA)

	// AC-1, AC-2: the entry stays on the roster, and its name is taken.
	sA2 := open(brian)
	list, err := game.ListCharacters(ctx, connect.NewRequest(&gamev1.ListCharactersRequest{SessionId: sA2}))
	if err != nil {
		t.Fatal(err)
	}
	var deleted int
	for _, c := range list.Msg.GetCharacters() {
		if c.GetCharacterId() == aldric && c.GetStatus() == accountsv1.CharacterStatus_CHARACTER_STATUS_DELETED {
			deleted++
		}
	}
	if deleted != 1 || len(list.Msg.GetCharacters()) != 2 {
		t.Fatalf("roster %v", list.Msg.GetCharacters())
	}
	if _, err := game.CreateCharacter(ctx, connect.NewRequest(&gamev1.CreateCharacterRequest{SessionId: sA2, Name: "ALDRIC"})); connect.CodeOf(err) != connect.CodeAlreadyExists {
		t.Fatalf("create with a deleted Character's name: %v", err)
	}
	if err := sel(sA2, aldric); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("select of a deleted Character: %v", err)
	}

	// AC-3: after the retention the sweep produces the purge once and the sim
	// removes the dormant body. Two bodies were dormant (Aldric, and Brenna
	// since the quit); one remains. The roster still counts the deleted entry.
	waitUntil(t, func() bool {
		m := metrics(t, httpAddr)
		return strings.Contains(m, `andara_character_purges_total{outcome="ok"} 1`) &&
			strings.Contains(m, `andara_characters_total{state="dormant"} 1`) &&
			strings.Contains(m, `andara_roster_characters{status="deleted"} 1`)
	}, "the purge to remove Aldric's body and leave the roster entry")
	time.Sleep(1500 * time.Millisecond) // three more sweeps: still once
	m := metrics(t, httpAddr)
	if !strings.Contains(m, `andara_character_purges_total{outcome="ok"} 1`) || !strings.Contains(m, `andara_roster_characters{status="deleted"} 1`) {
		t.Fatalf("after more sweeps:\n%s", grepMetric(m, "andara_character_purges_total", "andara_roster_characters", "andara_characters_total"))
	}
	// And the name is still reserved after the purge.
	if _, err := game.CreateCharacter(ctx, connect.NewRequest(&gamev1.CreateCharacterRequest{SessionId: sA2, Name: "aldric"})); connect.CodeOf(err) != connect.CodeAlreadyExists {
		t.Fatalf("create with a purged Character's name: %v", err)
	}
}

func grepMetric(m string, prefixes ...string) string {
	var out []string
	for _, l := range strings.Split(m, "\n") {
		for _, p := range prefixes {
			if strings.HasPrefix(l, p) {
				out = append(out, l)
			}
		}
	}
	return strings.Join(out, "\n")
}
