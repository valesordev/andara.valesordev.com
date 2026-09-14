// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package gateway

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/prometheus/client_golang/prometheus/testutil"

	adminv1 "github.com/valesordev/andara/gen/go/andara/admin/v1"
	"github.com/valesordev/andara/gen/go/andara/admin/v1/adminv1connect"
	gamev1 "github.com/valesordev/andara/gen/go/andara/game/v1"
	"github.com/valesordev/andara/internal/testpki"
	"github.com/valesordev/andara/server/auth"
)

// roleVerifier is a verifier with a fixed role set and a real act-as rule,
// so the Gateway's AC-10 and AC-12 behavior can be tested without a Store.
type roleVerifier struct {
	mu       sync.Mutex
	roles    []auth.Role
	revoked  bool
	actAsLog []string
}

func (v *roleVerifier) Verify(_ context.Context, token string) (Principal, error) {
	if token == "" {
		return Principal{}, ErrUnauthenticated
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	return Principal{AccountID: "acct-" + token, Roles: v.roles}, nil
}

func (v *roleVerifier) ActAs(_ context.Context, p Principal, target string) (Principal, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.actAsLog = append(v.actAsLog, p.AccountID+"->"+target)
	if !p.Has(auth.RoleOperator) && !p.Has(auth.RoleGameMaster) {
		return Principal{}, auth.ErrPermissionDenied
	}
	p.ActingAs = target
	return p, nil
}

func (v *roleVerifier) Recheck(Principal) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.revoked {
		return errors.New("account revoked")
	}
	return nil
}

// AC-10 at the Gateway: an operator's act_as_account_id is honored and the
// Session carries both identities; a player's is PERMISSION_DENIED.
func TestOpenSession_ActAs(t *testing.T) {
	v := &roleVerifier{roles: []auth.Role{auth.RoleOperator}}
	h := start(t, func(o *Options) { o.Verifier = v })
	client := h.game()

	resp, err := client.OpenSession(context.Background(), connect.NewRequest(&gamev1.OpenSessionRequest{
		ProtocolVersion: 1, AuthToken: "op", ClientName: "t", ActAsAccountId: "acct-player",
	}))
	if err != nil {
		t.Fatal(err)
	}
	sess, ok := h.srv.sessions.get(resp.Msg.GetSessionId())
	if !ok {
		t.Fatal("session not stored")
	}
	if sess.Principal.AccountID != "acct-op" || sess.Principal.ActingAs != "acct-player" {
		t.Fatalf("principal %+v", sess.Principal)
	}

	v.mu.Lock()
	v.roles = []auth.Role{auth.RolePlayer}
	v.mu.Unlock()
	_, err = client.OpenSession(context.Background(), connect.NewRequest(&gamev1.OpenSessionRequest{
		ProtocolVersion: 1, AuthToken: "pl", ClientName: "t", ActAsAccountId: "acct-op",
	}))
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("player act-as: %v", err)
	}
	if got := testutil.ToFloat64(h.srv.metrics.SessionsTotal.WithLabelValues(OutcomeRejectedAuth)); got != 1 {
		t.Fatalf("rejected_auth = %v", got)
	}
	// Without the field nothing is asked of ActAs.
	if _, err := client.OpenSession(context.Background(), connect.NewRequest(&gamev1.OpenSessionRequest{
		ProtocolVersion: 1, AuthToken: "pl", ClientName: "t",
	})); err != nil {
		t.Fatal(err)
	}
	if len(v.actAsLog) != 2 {
		t.Fatalf("act-as consulted %d times, want 2: %v", len(v.actAsLog), v.actAsLog)
	}
}

// AC-12 at the Gateway: the recheck loop closes every Session whose
// Principal no longer rechecks clean, within the interval, with outcome
// "revoked".
func TestRecheck_ClosesRevokedSessions(t *testing.T) {
	v := &roleVerifier{roles: []auth.Role{auth.RolePlayer}}
	h := start(t, func(o *Options) {
		o.Verifier = v
		o.Rechecker = v
		o.RecheckInterval = 20 * time.Millisecond
	})
	client := h.game()
	a := h.open(t, client)
	b := h.open(t, client)
	if h.srv.SessionCount() != 2 {
		t.Fatalf("sessions = %d", h.srv.SessionCount())
	}
	// A clean recheck closes nothing.
	time.Sleep(60 * time.Millisecond)
	if h.srv.SessionCount() != 2 {
		t.Fatal("clean recheck closed a session")
	}
	v.mu.Lock()
	v.revoked = true
	v.mu.Unlock()
	waitFor(t, 2*time.Second, func() bool { return h.srv.SessionCount() == 0 }, "revoked sessions to close")
	for _, id := range []string{a.GetSessionId(), b.GetSessionId()} {
		if _, err := client.Submit(context.Background(), connect.NewRequest(&gamev1.SubmitRequest{SessionId: id, Raw: "look"})); connect.CodeOf(err) != connect.CodeUnauthenticated {
			t.Fatalf("closed session still resolves: %v", err)
		}
	}
	if got := testutil.ToFloat64(h.srv.metrics.SessionsTotal.WithLabelValues(OutcomeRevoked)); got != 2 {
		t.Fatalf("sessions_total{revoked} = %v", got)
	}
	line := findLog(t, h.logs, "session closed")
	if line["outcome"] != OutcomeRevoked {
		t.Fatalf("close line: %v", line)
	}
}

func TestNew_RecheckerNeedsInterval(t *testing.T) {
	pki := testpki.New(t)
	opts := defaultOptions(pki)
	opts.Rechecker = &roleVerifier{}
	if _, err := New(opts); err == nil {
		t.Fatal("Rechecker without interval accepted")
	}
}

// fakeAccounts records which AccountAdmin method was reached and with what
// Principal, so the delegation and the interceptor's Principal attachment
// are tested without a Store.
type fakeAccounts struct {
	mu   sync.Mutex
	seen []string
}

func (f *fakeAccounts) note(ctx context.Context, m string) {
	p, _ := PrincipalFrom(ctx)
	f.mu.Lock()
	f.seen = append(f.seen, m+":"+p.AccountID)
	f.mu.Unlock()
}

func (f *fakeAccounts) CreateAccount(ctx context.Context, _ *adminv1.CreateAccountRequest) (*adminv1.CreateAccountResponse, error) {
	f.note(ctx, "CreateAccount")
	return &adminv1.CreateAccountResponse{AccountId: "new"}, nil
}
func (f *fakeAccounts) ResetPassword(ctx context.Context, _ *adminv1.ResetPasswordRequest) (*adminv1.ResetPasswordResponse, error) {
	f.note(ctx, "ResetPassword")
	return nil, auth.ErrVersionConflict
}
func (f *fakeAccounts) SetRoles(ctx context.Context, _ *adminv1.SetRolesRequest) (*adminv1.SetRolesResponse, error) {
	f.note(ctx, "SetRoles")
	return &adminv1.SetRolesResponse{}, nil
}
func (f *fakeAccounts) SetAccountStatus(ctx context.Context, _ *adminv1.SetAccountStatusRequest) (*adminv1.SetAccountStatusResponse, error) {
	f.note(ctx, "SetAccountStatus")
	return &adminv1.SetAccountStatusResponse{}, nil
}
func (f *fakeAccounts) IssueInvite(ctx context.Context, _ *adminv1.IssueInviteRequest) (*adminv1.IssueInviteResponse, error) {
	f.note(ctx, "IssueInvite")
	return &adminv1.IssueInviteResponse{}, nil
}
func (f *fakeAccounts) RevokeInvite(ctx context.Context, _ *adminv1.RevokeInviteRequest) (*adminv1.RevokeInviteResponse, error) {
	f.note(ctx, "RevokeInvite")
	return &adminv1.RevokeInviteResponse{}, nil
}
func (f *fakeAccounts) SetRegistrationMode(ctx context.Context, _ *adminv1.SetRegistrationModeRequest) (*adminv1.SetRegistrationModeResponse, error) {
	f.note(ctx, "SetRegistrationMode")
	return &adminv1.SetRegistrationModeResponse{}, nil
}
func (f *fakeAccounts) CreateAgentAccount(ctx context.Context, _ *adminv1.CreateAgentAccountRequest) (*adminv1.CreateAgentAccountResponse, error) {
	f.note(ctx, "CreateAgentAccount")
	return &adminv1.CreateAgentAccountResponse{}, nil
}

func TestAdmin_AccountMethodsDelegate(t *testing.T) {
	fa := &fakeAccounts{}
	h := start(t, func(o *Options) { o.Accounts = fa })
	c, _ := h.httpClient()
	bearer := connect.WithInterceptors(connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			req.Header().Set("Authorization", "Bearer test-token")
			return next(ctx, req)
		}
	}))
	admin := adminv1connect.NewAdminClient(c, h.baseURL(), bearer)
	ctx := context.Background()
	if resp, err := admin.CreateAccount(ctx, connect.NewRequest(&adminv1.CreateAccountRequest{})); err != nil || resp.Msg.GetAccountId() != "new" {
		t.Fatalf("CreateAccount: %v %v", resp, err)
	}
	// An auth error crosses the wire with auth's code.
	if _, err := admin.ResetPassword(ctx, connect.NewRequest(&adminv1.ResetPasswordRequest{})); connect.CodeOf(err) != connect.CodeAborted {
		t.Fatalf("ResetPassword: %v", err)
	}
	for _, call := range []func() error{
		func() error {
			_, err := admin.SetRoles(ctx, connect.NewRequest(&adminv1.SetRolesRequest{}))
			return err
		},
		func() error {
			_, err := admin.SetAccountStatus(ctx, connect.NewRequest(&adminv1.SetAccountStatusRequest{}))
			return err
		},
		func() error {
			_, err := admin.IssueInvite(ctx, connect.NewRequest(&adminv1.IssueInviteRequest{}))
			return err
		},
		func() error {
			_, err := admin.RevokeInvite(ctx, connect.NewRequest(&adminv1.RevokeInviteRequest{}))
			return err
		},
		func() error {
			_, err := admin.SetRegistrationMode(ctx, connect.NewRequest(&adminv1.SetRegistrationModeRequest{}))
			return err
		},
		func() error {
			_, err := admin.CreateAgentAccount(ctx, connect.NewRequest(&adminv1.CreateAgentAccountRequest{}))
			return err
		},
	} {
		if err := call(); err != nil {
			t.Fatal(err)
		}
	}
	fa.mu.Lock()
	defer fa.mu.Unlock()
	if len(fa.seen) != 8 {
		t.Fatalf("saw %v", fa.seen)
	}
	for _, s := range fa.seen {
		if s[len(s)-len(":stub"):] != ":stub" {
			t.Fatalf("Principal not attached: %s", s)
		}
	}
	// Without a bearer token none of them is reachable.
	plain := adminv1connect.NewAdminClient(c, h.baseURL())
	if _, err := plain.CreateAccount(ctx, connect.NewRequest(&adminv1.CreateAccountRequest{})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("no bearer: %v", err)
	}
	// And without an AccountAdmin they are UNIMPLEMENTED, not a crash.
	h2 := start(t, nil)
	c2, _ := h2.httpClient()
	admin2 := adminv1connect.NewAdminClient(c2, h2.baseURL(), bearer)
	if _, err := admin2.CreateAccount(ctx, connect.NewRequest(&adminv1.CreateAccountRequest{})); connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Fatalf("no AccountAdmin: %v", err)
	}
}
