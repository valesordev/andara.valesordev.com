// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

//go:build smoke

package smoke

import (
	"context"
	"errors"
	"net/http"
	"os"
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

// TestLive_EmptyStoreWaits is AW-SRV-042 AC-1 on a running stack whose server
// has content.source=kafka over an empty store and an empty World log: a
// fresh environment, before `make content-seed`. It runs only when
// ANDARA_SMOKE_EXPECT_EMPTY_STORE=1, since every other smoke test needs content
// in effect.
//   - /startedz is 200 and /readyz 503;
//   - OpenSession is refused UNAVAILABLE, andara.game / no_content_in_effect;
//   - Admin is served: ListVersions answers.
//
// The other half, the wait ending with no restart (AC-2), is `make
// content-seed` against the same stack, then this suite's other tests.
func TestLive_EmptyStoreWaits(t *testing.T) {
	if os.Getenv("ANDARA_SMOKE_EXPECT_EMPTY_STORE") != "1" {
		t.Skip("set ANDARA_SMOKE_EXPECT_EMPTY_STORE=1 on a fresh content.source=kafka stack")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	health := "http://" + env("ANDARA_SMOKE_METRICS_ADDR", "localhost:8080")
	for path, want := range map[string]int{"/startedz": http.StatusOK, "/readyz": http.StatusServiceUnavailable} {
		resp, err := http.Get(health + path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("%s = %d, want %d", path, resp.StatusCode, want)
		}
	}

	hc := httpClient(t)
	user, pass := operator(t)
	tok, err := authv1connect.NewAuthClient(hc, baseURL(), connect.WithGRPC()).Authenticate(ctx,
		connect.NewRequest(&authv1.AuthenticateRequest{Username: user, Password: pass}))
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	session := tok.Msg.GetTokens().GetSessionToken()

	_, err = gamev1connect.NewGameClient(hc, baseURL(), connect.WithGRPC()).OpenSession(ctx,
		connect.NewRequest(&gamev1.OpenSessionRequest{ProtocolVersion: 1, AuthToken: session, ClientName: "stack-smoke/empty-store"}))
	var ce *connect.Error
	if !errors.As(err, &ce) || ce.Code() != connect.CodeUnavailable || reasonOf(ce) != "no_content_in_effect" {
		t.Fatalf("OpenSession while waiting: %v", err)
	}

	admin := adminv1connect.NewAdminClient(hc, baseURL(), connect.WithGRPC(), bearer(session))
	if _, err := admin.ListVersions(ctx, connect.NewRequest(&adminv1.ListVersionsRequest{PackId: "andara.core"})); err != nil {
		t.Errorf("ListVersions while waiting: %v", err)
	}
}
