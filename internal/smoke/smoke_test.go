// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

//go:build smoke

// Package smoke exercises a *running* stack, which is what separates it from
// every other test in this module. `make test` runs against in-process servers
// and an in-process registry; that proves the handlers behave and the counters
// move, but not that a Session can be opened across a real network against a
// real certificate, nor that anything scrapes the result.
//
// AW-SRV-005's Definition of Done requires instrumentation "verified against a
// real backend, not just registered", and this is where that is verified.
// Build-tagged so `make test` never picks it up: it needs `make up` first.
// Run it with `make stack-smoke`.
package smoke

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"os"
	"testing"
	"time"

	"connectrpc.com/connect"
	authv1 "github.com/valesordev/andara/gen/go/andara/auth/v1"
	"github.com/valesordev/andara/gen/go/andara/auth/v1/authv1connect"
	gamev1 "github.com/valesordev/andara/gen/go/andara/game/v1"
	"github.com/valesordev/andara/gen/go/andara/game/v1/gamev1connect"
	"golang.org/x/net/http2"
)

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// client dials the running server the way any other client would: over TLS,
// trusting the local CA and nothing else. There is no insecure option here
// because the server offers no plaintext path (AC-3).
func client(t *testing.T) gamev1connect.GameClient {
	t.Helper()
	caFile := env("ANDARA_TLS_CA_FILE", ".local/tls/ca.pem")
	pem, err := os.ReadFile(caFile)
	if err != nil {
		t.Fatalf("read CA %s: %v (run `make up` first)", caFile, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		t.Fatalf("%s is not a PEM certificate", caFile)
	}
	hc := &http.Client{
		Timeout:   10 * time.Second,
		Transport: &http2.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}},
	}
	return gamev1connect.NewGameClient(hc, "https://"+env("ANDARA_SMOKE_ADDR", "localhost:8443"),
		connect.WithGRPC())
}

// AC-3 and AC-5 against the running server: the handshake succeeds against the
// local CA, and an in-range version comes back with a SessionID and the
// negotiated version. The token is a real one since AW-SRV-008; auth_test.go
// covers what a made-up one gets.
func TestLive_OpenSession(t *testing.T) {
	user, pass := operator(t)
	tokens, err := authv1connect.NewAuthClient(httpClient(t), baseURL(), connect.WithGRPC()).Authenticate(context.Background(),
		connect.NewRequest(&authv1.AuthenticateRequest{Username: user, Password: pass}))
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	resp, err := client(t).OpenSession(context.Background(),
		connect.NewRequest(&gamev1.OpenSessionRequest{
			ProtocolVersion: 1, AuthToken: tokens.Msg.GetTokens().GetSessionToken(), ClientName: "stack-smoke/0.1",
		}))
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	if resp.Msg.GetSessionId() == "" {
		t.Error("no session_id")
	}
	if got := resp.Msg.GetNegotiatedVersion(); got != 1 {
		t.Errorf("negotiated_version = %d, want 1", got)
	}
	t.Logf("session_id=%s negotiated=%d range=%d..%d", resp.Msg.GetSessionId(),
		resp.Msg.GetNegotiatedVersion(), resp.Msg.GetServerMinVersion(), resp.Msg.GetServerMaxVersion())
}

// AC-6 against the running server. This is the story's manual/operator test
// plan — the grpcurl invocation that expected FAILED_PRECONDITION naming both
// ranges — as something CI runs rather than something a human remembers to.
func TestLive_VersionOutsideRangeRejected(t *testing.T) {
	_, err := client(t).OpenSession(context.Background(),
		connect.NewRequest(&gamev1.OpenSessionRequest{
			ProtocolVersion: 99, AuthToken: "smoke-token", ClientName: "stack-smoke/0.1",
		}))
	if err == nil {
		t.Fatal("protocol version 99 was accepted")
	}
	if got := connect.CodeOf(err); got != connect.CodeFailedPrecondition {
		t.Errorf("code = %s, want failed_precondition", got)
	}
	// The error names both the client's version and the server's range, so an
	// operator reading it knows what to change without reading the source.
	for _, want := range []string{"99", "1..1"} {
		if !contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err.Error(), want)
		}
	}
	t.Logf("rejected: %v", err)
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
