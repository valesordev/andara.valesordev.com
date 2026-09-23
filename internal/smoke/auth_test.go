// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

//go:build smoke

package smoke

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/twmb/franz-go/pkg/kgo"
	"golang.org/x/net/http2"
	"google.golang.org/protobuf/proto"

	adminv1 "github.com/valesordev/andara/gen/go/andara/admin/v1"
	"github.com/valesordev/andara/gen/go/andara/admin/v1/adminv1connect"
	auditv1 "github.com/valesordev/andara/gen/go/andara/audit/v1"
	authv1 "github.com/valesordev/andara/gen/go/andara/auth/v1"
	"github.com/valesordev/andara/gen/go/andara/auth/v1/authv1connect"
	gamev1 "github.com/valesordev/andara/gen/go/andara/game/v1"
	"github.com/valesordev/andara/gen/go/andara/game/v1/gamev1connect"
	"github.com/valesordev/andara/internal/eventually"
)

// httpClient trusts the local CA and nothing else.
func httpClient(t *testing.T) *http.Client {
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
	return &http.Client{
		Timeout:   10 * time.Second,
		Transport: &http2.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}},
	}
}

func baseURL() string { return "https://" + env("ANDARA_SMOKE_ADDR", "localhost:8443") }

// operator returns the bootstrap operator's username and password, which
// `make up` set through ANDARA_AUTH_BOOTSTRAP_OPERATOR.
func operator(t *testing.T) (string, string) {
	t.Helper()
	u, p, ok := strings.Cut(env("ANDARA_SMOKE_OPERATOR", "operator:andara-local"), ":")
	if !ok {
		t.Fatal("ANDARA_SMOKE_OPERATOR must be username:password")
	}
	return u, p
}

func bearer(token string) connect.ClientOption {
	return connect.WithInterceptors(connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			req.Header().Set("Authorization", "Bearer "+token)
			return next(ctx, req)
		}
	}))
}

// AW-SRV-008 against the running stack: the bootstrap operator authenticates,
// its session token opens a Session, a made-up token does not, registration
// is closed, Admin.CreateAccount works with the bearer token and its audit
// record lands on andara.audit.v1 — read back from the broker, which is the
// "verified against a real backend" the Definition of Done asks for.
func TestLive_AccountsAndAudit(t *testing.T) {
	ctx := context.Background()
	hc := httpClient(t)
	authc := authv1connect.NewAuthClient(hc, baseURL(), connect.WithGRPC())
	game := gamev1connect.NewGameClient(hc, baseURL(), connect.WithGRPC())
	user, pass := operator(t)

	tokens, err := authc.Authenticate(ctx, connect.NewRequest(&authv1.AuthenticateRequest{Username: user, Password: pass}))
	if err != nil {
		t.Fatalf("Authenticate as the bootstrap operator: %v", err)
	}
	session := tokens.Msg.GetTokens().GetSessionToken()

	resp, err := game.OpenSession(ctx, connect.NewRequest(&gamev1.OpenSessionRequest{
		ProtocolVersion: 1, AuthToken: session, ClientName: "stack-smoke/0.1",
	}))
	if err != nil {
		t.Fatalf("OpenSession with a real token: %v", err)
	}
	t.Logf("session_id=%s", resp.Msg.GetSessionId())

	if _, err := game.OpenSession(ctx, connect.NewRequest(&gamev1.OpenSessionRequest{
		ProtocolVersion: 1, AuthToken: "smoke-token", ClientName: "stack-smoke/0.1",
	})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("made-up token: %v", err)
	}

	if _, err := authc.Register(ctx, connect.NewRequest(&authv1.RegisterRequest{Username: "smoke-newbie", Password: "smoke-password-1"})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("Register in closed mode: %v", err)
	}

	admin := adminv1connect.NewAdminClient(hc, baseURL(), connect.WithGRPC(), bearer(session))
	username := fmt.Sprintf("smoke-%d", time.Now().UnixNano()%1_000_000)
	created, err := admin.CreateAccount(ctx, connect.NewRequest(&adminv1.CreateAccountRequest{Username: username, Password: "smoke-password-1"}))
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if _, err := authc.Authenticate(ctx, connect.NewRequest(&authv1.AuthenticateRequest{Username: username, Password: "smoke-password-1"})); err != nil {
		t.Fatalf("Authenticate as the new account: %v", err)
	}
	// The unauthenticated path to Admin is still closed.
	plain := adminv1connect.NewAdminClient(hc, baseURL(), connect.WithGRPC())
	if _, err := plain.CreateAccount(ctx, connect.NewRequest(&adminv1.CreateAccountRequest{Username: "x", Password: "y"})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("CreateAccount without a token: %v", err)
	}

	// Now the broker: the audit record for that CreateAccount is on the topic.
	cl, err := kgo.NewClient(
		kgo.SeedBrokers(env("ANDARA_KAFKA_BROKERS", "localhost:9092")),
		kgo.ConsumeTopics("andara.audit.v1"),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	polled := 0
	eventually.Observed(t, 20*time.Second, "the create_account audit record on andara.audit.v1", func() (bool, string) {
		pctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		fetches := cl.PollFetches(pctx)
		cancel()
		found := false
		fetches.EachRecord(func(r *kgo.Record) {
			polled++
			var rec auditv1.AuditRecord
			if proto.Unmarshal(r.Value, &rec) == nil && rec.GetAction() == "create_account" && rec.GetTarget() == created.Msg.GetAccountId() {
				found = true
				if rec.GetActorAccountId() == "" || rec.GetTraceId() == "" || rec.GetOutcome() != "ok" {
					t.Errorf("audit record incomplete: %v", &rec)
				}
				t.Logf("audit: actor=%s action=%s target=%s trace=%s", rec.GetActorAccountId(), rec.GetAction(), rec.GetTarget(), rec.GetTraceId())
			}
		})
		return found, fmt.Sprintf("%d records read, none for account %s", polled, created.Msg.GetAccountId())
	})
}
