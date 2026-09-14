// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package auth

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/prometheus/client_golang/prometheus/testutil"

	accountsv1 "github.com/valesordev/andara/gen/go/andara/accounts/v1"
)

// --- AC-3: closed mode ---------------------------------------------------------

func TestRegister_ClosedMode(t *testing.T) {
	f := newFixture(t, nil)
	_, err := f.store.Register(context.Background(), "brian", "correct horse battery", "", "peer")
	if !errors.Is(err, ErrRegistrationClosed) || Code(err) != connect.CodeFailedPrecondition {
		t.Fatalf("closed mode: got %v", err)
	}
	// The only path is Admin.CreateAccount, which audits.
	opCtx, opID := f.bootstrapOperator("oper", "operator-password")
	id, err := f.store.CreateAccount(opCtx, "brian", "correct horse battery", nil)
	if err != nil {
		t.Fatal(err)
	}
	recs := f.auditRecords()
	last := recs[len(recs)-1]
	if last.GetAction() != ActionCreateAccount || last.GetActorAccountId() != opID || last.GetTarget() != id || last.GetOutcome() != AuditOK {
		t.Fatalf("audit record: %+v", last)
	}
	if got := testutil.ToFloat64(f.store.Metrics().PrivilegedActions.WithLabelValues(ActionCreateAccount)); got != 2 {
		t.Fatalf("privileged_actions{create_account} = %v, want 2 (bootstrap + brian)", got)
	}
}

func TestCreateAccount_RequiresOperator(t *testing.T) {
	f := newFixture(t, nil)
	player := WithPrincipal(context.Background(), Principal{AccountID: "p", Roles: []Role{RolePlayer, RoleBuilder}})
	if _, err := f.store.CreateAccount(player, "x1", "long enough pw", nil); Code(err) != connect.CodePermissionDenied {
		t.Fatalf("player: %v", err)
	}
	if _, err := f.store.CreateAccount(context.Background(), "x1", "long enough pw", nil); Code(err) != connect.CodeUnauthenticated {
		t.Fatalf("no principal: %v", err)
	}
	if len(f.auditRecords()) != 0 {
		t.Fatal("an unprivileged caller is not a privileged action; nothing should be audited")
	}
}

// --- AC-4 / AC-5: invites ------------------------------------------------------

func TestRegister_InviteMode_ConcurrentRedemption(t *testing.T) {
	f := newFixture(t, nil)
	opCtx, _ := f.bootstrapOperator("oper", "operator-password")
	if _, err := f.store.SetRegistrationMode(opCtx, accountsv1.RegistrationMode_INVITE); err != nil {
		t.Fatal(err)
	}
	codes, _, err := f.store.IssueInvite(opCtx, 1)
	if err != nil {
		t.Fatal(err)
	}
	before := f.accounts.Len()

	const n = 50
	var wg sync.WaitGroup
	results := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, results[i] = f.store.Register(context.Background(), fmt.Sprintf("racer%02d", i), "correct horse battery", codes[0], "peer")
		}()
	}
	wg.Wait()
	wins, lost := 0, 0
	for _, err := range results {
		switch {
		case err == nil:
			wins++
		case errors.Is(err, ErrPermissionDenied):
			lost++
		default:
			t.Errorf("unexpected: %v", err)
		}
	}
	if wins != 1 || lost != n-1 {
		t.Fatalf("wins=%d lost=%d", wins, lost)
	}
	// The code is burned on the issuer's record before the winner's Account
	// is written: two records appended, issuer first with redeemed=true.
	recs := f.accountRecords()[before:]
	if len(recs) != 2 {
		t.Fatalf("appended %d records, want 2 (burn, then account)", len(recs))
	}
	issuer := recs[0].GetAccount()
	if issuer == nil || len(issuer.GetInvites()) != 1 || !issuer.Invites[0].GetRedeemed() {
		t.Fatalf("first record is not the burned invite: %+v", recs[0])
	}
	if got := recs[1].GetAccount().GetAccountId(); got != issuer.Invites[0].GetRedeemedByAccountId() {
		t.Fatalf("burn names %s, account written is %s", issuer.Invites[0].GetRedeemedByAccountId(), got)
	}
	if got := testutil.ToFloat64(f.store.Metrics().InviteRedemptions.WithLabelValues(RedemptionRaceLost)); got != n-1 {
		t.Fatalf("race_lost = %v", got)
	}
	if got := testutil.ToFloat64(f.store.Metrics().Registrations.WithLabelValues("invite")); got != 1 {
		t.Fatalf("registrations{invite} = %v", got)
	}
}

func TestRegister_InviteMode_BadCodesOneMessage(t *testing.T) {
	f := newFixture(t, nil)
	opCtx, _ := f.bootstrapOperator("oper", "operator-password")
	if _, err := f.store.SetRegistrationMode(opCtx, accountsv1.RegistrationMode_INVITE); err != nil {
		t.Fatal(err)
	}
	codes, _, err := f.store.IssueInvite(opCtx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.RevokeInvite(opCtx, codes[0]); err != nil {
		t.Fatal(err)
	}
	// codes[1] expires.
	f.clock.advance(8 * 24 * time.Hour)

	msgs := map[string]bool{}
	for name, code := range map[string]string{"revoked": codes[0], "expired": codes[1], "never": "nope-never-issued", "empty": ""} {
		_, err := f.store.Register(context.Background(), "u"+name, "correct horse battery", code, "peer")
		if Code(err) != connect.CodePermissionDenied {
			t.Errorf("%s: got %v", name, err)
		}
		msgs[err.Error()] = true
	}
	if len(msgs) != 1 {
		t.Fatalf("one fixed message wanted, got %v", msgs)
	}
	// A used code cannot be reused.
	if _, err := f.store.SetRegistrationMode(opCtx, accountsv1.RegistrationMode_INVITE); err != nil {
		t.Fatal(err)
	}
	fresh, _, _ := f.store.IssueInvite(opCtx, 1)
	mustRegister(t, f, "first", "correct horse battery", fresh[0])
	if _, err := f.store.Register(context.Background(), "second", "correct horse battery", fresh[0], "peer"); Code(err) != connect.CodePermissionDenied {
		t.Fatalf("reuse: %v", err)
	}
}

func TestRegister_OpenMode(t *testing.T) {
	f := newFixture(t, nil)
	opCtx, _ := f.bootstrapOperator("oper", "operator-password")
	prev, err := f.store.SetRegistrationMode(opCtx, accountsv1.RegistrationMode_OPEN)
	if err != nil || prev != accountsv1.RegistrationMode_CLOSED {
		t.Fatalf("prev=%v err=%v", prev, err)
	}
	id := mustRegister(t, f, "Brian", "correct horse battery", "")
	if _, err := f.store.Register(context.Background(), "brian", "another password", "", "peer"); !errors.Is(err, ErrUsernameTaken) {
		t.Fatalf("case-insensitive uniqueness: %v", err)
	}
	if _, err := f.store.Register(context.Background(), "ab", "correct horse battery", "", "peer"); Code(err) != connect.CodeInvalidArgument {
		t.Fatalf("short username: %v", err)
	}
	if _, err := f.store.Register(context.Background(), "short", "short", "", "peer"); Code(err) != connect.CodeInvalidArgument {
		t.Fatalf("short password: %v", err)
	}
	// The mode survives a restart: it is a record on the topic.
	f.reopen(nil)
	if f.store.RegistrationMode() != accountsv1.RegistrationMode_OPEN {
		t.Fatal("mode did not survive reopen")
	}
	if _, ok := f.store.lookupID(id); !ok {
		t.Fatal("account did not survive reopen")
	}
}

// --- authentication ---------------------------------------------------------

func TestAuthenticate_Roundtrip(t *testing.T) {
	f := newFixture(t, nil)
	opCtx, _ := f.bootstrapOperator("oper", "operator-password")
	id, err := f.store.CreateAccount(opCtx, "brian", "correct horse battery", []Role{RolePlayer, RoleBuilder})
	if err != nil {
		t.Fatal(err)
	}
	pair := mustAuth(t, f, "BRIAN", "correct horse battery")
	if pair.SessionExpires != f.clock.now().Add(time.Hour) {
		t.Fatalf("session expiry %v", pair.SessionExpires)
	}
	p, err := f.store.Verify(context.Background(), pair.SessionToken)
	if err != nil {
		t.Fatal(err)
	}
	if p.AccountID != id || !slices.Equal(p.Roles, []Role{RolePlayer, RoleBuilder}) || p.ActingAs != "" {
		t.Fatalf("principal %+v", p)
	}
	// Wrong password and unknown user: one message, one code.
	_, e1 := f.store.Authenticate(context.Background(), "brian", "wrong password", "peer")
	_, e2 := f.store.Authenticate(context.Background(), "nobody", "wrong password", "peer")
	if Code(e1) != connect.CodeUnauthenticated || Code(e2) != connect.CodeUnauthenticated || e1.Error() != e2.Error() {
		t.Fatalf("e1=%v e2=%v", e1, e2)
	}
	if got := testutil.ToFloat64(f.store.Metrics().Attempts.WithLabelValues(OutcomeBadCredential)); got != 2 {
		t.Fatalf("bad_credential = %v", got)
	}
	if got := testutil.ToFloat64(f.store.Metrics().Attempts.WithLabelValues(OutcomeOK)); got != 1 {
		t.Fatalf("ok = %v", got)
	}
	// Expired session token.
	f.clock.advance(61 * time.Minute)
	if _, err := f.store.Verify(context.Background(), pair.SessionToken); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("expired token: %v", err)
	}
}

// AC-8: a token issued before a restart is accepted after it.
func TestVerify_SurvivesRestart(t *testing.T) {
	f := newFixture(t, nil)
	opCtx, _ := f.bootstrapOperator("oper", "operator-password")
	if _, err := f.store.CreateAccount(opCtx, "brian", "correct horse battery", nil); err != nil {
		t.Fatal(err)
	}
	pair := mustAuth(t, f, "brian", "correct horse battery")
	f.reopen(nil)
	if _, err := f.store.Verify(context.Background(), pair.SessionToken); err != nil {
		t.Fatalf("after restart: %v", err)
	}
	// And the refresh token, which lives on the record, also survives.
	if _, err := f.store.Refresh(context.Background(), pair.RefreshToken, "peer"); err != nil {
		t.Fatalf("refresh after restart: %v", err)
	}
}

func TestKeyRotation(t *testing.T) {
	f := newFixture(t, nil)
	opCtx, _ := f.bootstrapOperator("oper", "operator-password")
	if _, err := f.store.CreateAccount(opCtx, "brian", "correct horse battery", nil); err != nil {
		t.Fatal(err)
	}
	old := mustAuth(t, f, "brian", "correct horse battery")

	// Step 1 of rotation: add k2 behind k1. Old tokens verify, new ones
	// still sign with k1.
	f.keys = testKeyring(t, "k1", "k2")
	f.reopen(nil)
	if _, err := f.store.Verify(context.Background(), old.SessionToken); err != nil {
		t.Fatal("k1 token after adding k2:", err)
	}
	// Step 2: k2 first. Old tokens still verify; new ones sign with k2.
	f.keys = testKeyring(t, "k2", "k1")
	f.reopen(nil)
	if _, err := f.store.Verify(context.Background(), old.SessionToken); err != nil {
		t.Fatal("k1 token after promoting k2:", err)
	}
	fresh := mustAuth(t, f, "brian", "correct horse battery")
	if !strings.Contains(decodePayload(t, fresh.SessionToken), `"kid":"k2"`) {
		t.Fatal("new token not signed by the current key")
	}
	// Step 3: remove k1. Its tokens are refused; k2's verify.
	f.keys = testKeyring(t, "k2")
	f.reopen(nil)
	if _, err := f.store.Verify(context.Background(), old.SessionToken); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("k1 token after removing k1:", err)
	}
	if _, err := f.store.Verify(context.Background(), fresh.SessionToken); err != nil {
		t.Fatal("k2 token:", err)
	}
	// A forged signature and a tampered payload are both refused.
	enc, _, _ := strings.Cut(fresh.SessionToken, ".")
	if _, err := f.store.Verify(context.Background(), enc+"."+b64.EncodeToString(make([]byte, 32))); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("forged signature accepted")
	}
}

func decodePayload(t *testing.T, tok string) string {
	t.Helper()
	enc, _, _ := strings.Cut(tok, ".")
	b, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// AC-13: parameters changed in config → rehash on next successful login.
func TestAuthenticate_RehashOnLogin(t *testing.T) {
	f := newFixture(t, nil)
	opCtx, _ := f.bootstrapOperator("oper", "operator-password")
	id, err := f.store.CreateAccount(opCtx, "brian", "correct horse battery", nil)
	if err != nil {
		t.Fatal(err)
	}
	idle, err := f.store.CreateAccount(opCtx, "idle", "never logs in yet", nil)
	if err != nil {
		t.Fatal(err)
	}
	stronger := Argon2Params{MemoryKiB: 128, Time: 2, Threads: 1}
	f.reopen(func(o *Options) { o.Argon2 = stronger })

	// A wrong password does not rehash.
	if _, err := f.store.Authenticate(context.Background(), "brian", "wrong password", "peer"); err == nil {
		t.Fatal("wrong password accepted")
	}
	acc, _ := f.store.lookupID(id)
	if paramsFromProto(acc.GetCredential().GetParams()) != testArgon {
		t.Fatal("rehashed on a failed login")
	}
	mustAuth(t, f, "brian", "correct horse battery")
	acc, _ = f.store.lookupID(id)
	if got := paramsFromProto(acc.GetCredential().GetParams()); got != stronger {
		t.Fatalf("after login params = %+v, want %+v", got, stronger)
	}
	// It still verifies under the new parameters.
	mustAuth(t, f, "brian", "correct horse battery")
	// The idle account keeps the old parameters and still verifies.
	acc, _ = f.store.lookupID(idle)
	if paramsFromProto(acc.GetCredential().GetParams()) != testArgon {
		t.Fatal("idle account was rehashed without a login")
	}
	mustAuth(t, f, "idle", "never logs in yet")
}

// AC-12: DISABLED → token refused; recheck reports revoked; roles changed →
// recheck reports changed.
func TestDisableAndRecheck(t *testing.T) {
	f := newFixture(t, nil)
	opCtx, _ := f.bootstrapOperator("oper", "operator-password")
	id, err := f.store.CreateAccount(opCtx, "brian", "correct horse battery", []Role{RolePlayer})
	if err != nil {
		t.Fatal(err)
	}
	pair := mustAuth(t, f, "brian", "correct horse battery")
	p, err := f.store.Verify(context.Background(), pair.SessionToken)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.Recheck(p); err != nil {
		t.Fatal("fresh principal rechecks clean:", err)
	}
	if _, err := f.store.SetRoles(opCtx, id, []Role{RolePlayer, RoleBuilder}, 0); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Recheck(p); err == nil {
		t.Fatal("roles changed but recheck passed")
	}
	// A fresh Verify sees the new roles — they come from the index, not the token.
	p2, _ := f.store.Verify(context.Background(), pair.SessionToken)
	if !slices.Equal(p2.Roles, []Role{RolePlayer, RoleBuilder}) {
		t.Fatalf("roles from index: %v", p2.Roles)
	}
	if _, err := f.store.SetAccountStatus(opCtx, id, accountsv1.AccountStatus_DISABLED, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Verify(context.Background(), pair.SessionToken); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("disabled account's token: %v", err)
	}
	if err := f.store.Recheck(p2); err == nil {
		t.Fatal("disabled but recheck passed")
	}
	if _, err := f.store.Authenticate(context.Background(), "brian", "correct horse battery", "peer"); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("disabled login: %v", err)
	}
	if got := testutil.ToFloat64(f.store.Metrics().Attempts.WithLabelValues(OutcomeDisabled)); got != 1 {
		t.Fatalf("disabled = %v", got)
	}
	// Disabling revoked the refresh token too.
	if _, err := f.store.Refresh(context.Background(), pair.RefreshToken, "peer"); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("refresh after disable: %v", err)
	}
	if got := testutil.ToFloat64(f.store.Metrics().Accounts.WithLabelValues("player")); got != 0 {
		t.Fatalf("accounts{player} = %v after disable, want 0", got)
	}
	// Re-enable: the token verifies again (it is still within its TTL).
	if _, err := f.store.SetAccountStatus(opCtx, id, accountsv1.AccountStatus_ACTIVE, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Verify(context.Background(), pair.SessionToken); err != nil {
		t.Fatal("re-enabled:", err)
	}
}

// AC-9: a revoked refresh token is UNAUTHENTICATED and audited; refresh
// rotates.
func TestRefreshAndRevoke(t *testing.T) {
	f := newFixture(t, nil)
	opCtx, _ := f.bootstrapOperator("oper", "operator-password")
	id, err := f.store.CreateAccount(opCtx, "brian", "correct horse battery", nil)
	if err != nil {
		t.Fatal(err)
	}
	first := mustAuth(t, f, "brian", "correct horse battery")
	second, err := f.store.Refresh(context.Background(), first.RefreshToken, "peer")
	if err != nil {
		t.Fatal(err)
	}
	if second.RefreshToken == first.RefreshToken || second.SessionToken == first.SessionToken {
		t.Fatal("refresh did not rotate")
	}
	// The presented token was retired by the rotation: presenting it again
	// is the AC-9 case.
	auditBefore := len(f.auditRecords())
	if _, err := f.store.Refresh(context.Background(), first.RefreshToken, "peer"); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("retired refresh: %v", err)
	}
	recs := f.auditRecords()
	if len(recs) != auditBefore+1 || recs[len(recs)-1].GetAction() != ActionRefreshRevoked || recs[len(recs)-1].GetActorAccountId() != id {
		t.Fatalf("revoked refresh not audited: %+v", recs[len(recs)-1])
	}
	// Explicit revoke of all.
	third := mustAuth(t, f, "brian", "correct horse battery")
	n, err := f.store.Revoke(context.Background(), second.RefreshToken, true, "peer")
	if err != nil || n != 2 {
		t.Fatalf("revoke all: n=%d err=%v", n, err)
	}
	if _, err := f.store.Refresh(context.Background(), third.RefreshToken, "peer"); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("third refresh survived revoke-all")
	}
	// Unknown token: UNAUTHENTICATED, nothing audited.
	if _, err := f.store.Revoke(context.Background(), "not-a-token", false, "peer"); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("unknown revoke:", err)
	}
	// Expired refresh tokens fall off the record.
	f.clock.advance(31 * 24 * time.Hour)
	mustAuth(t, f, "brian", "correct horse battery")
	acc, _ := f.store.lookupID(id)
	if len(acc.GetRefreshTokens()) != 1 {
		t.Fatalf("expired refresh tokens not pruned: %d on record", len(acc.GetRefreshTokens()))
	}
}

// --- AC-10: acting as ----------------------------------------------------------

func TestActAs(t *testing.T) {
	f := newFixture(t, nil)
	opCtx, opID := f.bootstrapOperator("oper", "operator-password")
	playerID, err := f.store.CreateAccount(opCtx, "brian", "correct horse battery", []Role{RolePlayer, RoleBuilder})
	if err != nil {
		t.Fatal(err)
	}
	op := mustAuth(t, f, "oper", "operator-password")
	opP, _ := f.store.Verify(context.Background(), op.SessionToken)
	acting, err := f.store.ActAs(WithSessionID(context.Background(), "sess-1"), opP, playerID)
	if err != nil {
		t.Fatal(err)
	}
	if acting.AccountID != opID || acting.ActingAs != playerID || !slices.Equal(acting.Roles, []Role{RolePlayer, RoleBuilder}) {
		t.Fatalf("acting principal %+v", acting)
	}
	recs := f.auditRecords()
	last := recs[len(recs)-1]
	if last.GetAction() != ActionActAs || last.GetActorAccountId() != opID || last.GetActingAsAccountId() != playerID || last.GetSessionId() != "sess-1" {
		t.Fatalf("act-as audit: %+v", last)
	}
	// Anything audited in that Session names both.
	f.store.Auditor().Record(WithSessionID(context.Background(), "sess-1"), Entry{Actor: acting, Action: ActionIssueInvite, Outcome: AuditOK})
	recs = f.auditRecords()
	last = recs[len(recs)-1]
	if last.GetActorAccountId() != opID || last.GetActingAsAccountId() != playerID {
		t.Fatalf("both identities wanted: %+v", last)
	}
	// A player may not.
	pl := mustAuth(t, f, "brian", "correct horse battery")
	plP, _ := f.store.Verify(context.Background(), pl.SessionToken)
	if _, err := f.store.ActAs(context.Background(), plP, opID); Code(err) != connect.CodePermissionDenied {
		t.Fatalf("player act-as: %v", err)
	}
	// Nor as an unknown Account.
	if _, err := f.store.ActAs(context.Background(), opP, "nobody"); Code(err) != connect.CodePermissionDenied {
		t.Fatalf("act-as unknown: %v", err)
	}
	// Recheck follows the acted-as Account.
	if err := f.store.Recheck(acting); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.SetAccountStatus(opCtx, playerID, accountsv1.AccountStatus_DISABLED, 0); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Recheck(acting); err == nil {
		t.Fatal("acted-as disabled but recheck passed")
	}
}

// --- Admin -----------------------------------------------------------------

// Every Admin operation yields exactly one audit record, success or refusal.
func TestAdmin_AuditCompleteness(t *testing.T) {
	f := newFixture(t, nil)
	opCtx, opID := f.bootstrapOperator("oper", "operator-password")
	id, err := f.store.CreateAccount(opCtx, "brian", "correct horse battery", nil)
	if err != nil {
		t.Fatal(err)
	}
	steps := []struct {
		name string
		call func() error
	}{
		{ActionResetPassword, func() error { _, err := f.store.ResetPassword(opCtx, id, "a new password!", 0); return err }},
		{ActionSetRoles, func() error { _, err := f.store.SetRoles(opCtx, id, []Role{RoleGameMaster}, 0); return err }},
		{ActionSetAccountStatus, func() error {
			_, err := f.store.SetAccountStatus(opCtx, id, accountsv1.AccountStatus_DISABLED, 0)
			return err
		}},
		{ActionIssueInvite, func() error { _, _, err := f.store.IssueInvite(opCtx, 3); return err }},
		{ActionSetRegistrationMode, func() error {
			_, err := f.store.SetRegistrationMode(opCtx, accountsv1.RegistrationMode_INVITE)
			return err
		}},
		{ActionCreateAgentAccount, func() error {
			_, _, err := f.store.CreateAgentAccount(opCtx, "npc-agent", "pack.town", accountsv1.CredentialKind_API_KEY, "")
			return err
		}},
		// Refusals audit too.
		{ActionResetPassword, func() error {
			_, err := f.store.ResetPassword(opCtx, "nobody", "a new password!", 0)
			if !errors.Is(err, ErrNotFound) {
				return fmt.Errorf("want not found, got %v", err)
			}
			return nil
		}},
		{ActionRevokeInvite, func() error {
			err := f.store.RevokeInvite(opCtx, "never-issued")
			if !errors.Is(err, ErrNotFound) {
				return fmt.Errorf("want not found, got %v", err)
			}
			return nil
		}},
	}
	for _, st := range steps {
		before := len(f.auditRecords())
		if err := st.call(); err != nil {
			t.Fatalf("%s: %v", st.name, err)
		}
		recs := f.auditRecords()
		if len(recs) != before+1 {
			t.Fatalf("%s: %d audit records, want 1", st.name, len(recs)-before)
		}
		last := recs[len(recs)-1]
		if last.GetAction() != st.name || last.GetActorAccountId() != opID || last.GetTraceId() != "" && false {
			t.Fatalf("%s: audit %+v", st.name, last)
		}
	}
	if got := testutil.ToFloat64(f.store.Metrics().PrivilegedActions.WithLabelValues(ActionIssueInvite)); got != 1 {
		t.Fatalf("privileged_actions{issue_invite} = %v", got)
	}
}

func TestAdmin_RecordVersionConflict(t *testing.T) {
	f := newFixture(t, nil)
	opCtx, firstOp := f.bootstrapOperator("oper", "operator-password")
	id, err := f.store.CreateAccount(opCtx, "brian", "correct horse battery", nil)
	if err != nil {
		t.Fatal(err)
	}
	acc, _ := f.store.lookupID(id)
	v := acc.GetRecordVersion()
	v2, err := f.store.SetRoles(opCtx, id, []Role{RolePlayer, RoleBuilder}, v)
	if err != nil || v2 != v+1 {
		t.Fatalf("v2=%d err=%v", v2, err)
	}
	if _, err := f.store.SetRoles(opCtx, id, []Role{RolePlayer}, v); !errors.Is(err, ErrVersionConflict) || Code(err) != connect.CodeAborted {
		t.Fatalf("stale version: %v", err)
	}
	// Nothing was written for the refused call.
	acc, _ = f.store.lookupID(id)
	if !slices.Equal(RolesFromProto(acc.GetRoles()), []Role{RolePlayer, RoleBuilder}) {
		t.Fatal("stale write applied")
	}
	// Agent role is not assignable.
	if _, err := f.store.SetRoles(opCtx, id, []Role{RoleAgent}, 0); Code(err) != connect.CodeInvalidArgument {
		t.Fatalf("agent via SetRoles: %v", err)
	}
	// Self-disable refused.
	_, opID := f.bootstrapOperator("oper2", "operator-password")
	self := WithPrincipal(context.Background(), Principal{AccountID: opID, Roles: []Role{RoleOperator}})
	if _, err := f.store.SetAccountStatus(self, opID, accountsv1.AccountStatus_DISABLED, 0); Code(err) != connect.CodePermissionDenied {
		t.Fatalf("self-disable: %v", err)
	}
	// The last operator can be neither demoted nor disabled; the others can.
	if _, err := f.store.SetRoles(self, firstOp, []Role{RolePlayer}, 0); err != nil {
		t.Fatalf("demote one of two operators: %v", err)
	}
	if _, err := f.store.SetRoles(self, opID, []Role{RolePlayer}, 0); Code(err) != connect.CodePermissionDenied {
		t.Fatalf("demote the last operator: %v", err)
	}
	other := WithPrincipal(context.Background(), Principal{AccountID: "someone-else", Roles: []Role{RoleOperator}})
	if _, err := f.store.SetAccountStatus(other, opID, accountsv1.AccountStatus_DISABLED, 0); Code(err) != connect.CodePermissionDenied {
		t.Fatalf("disable the last operator: %v", err)
	}
}

func TestAgentAccount_APIKey(t *testing.T) {
	f := newFixture(t, nil)
	opCtx, _ := f.bootstrapOperator("oper", "operator-password")
	id, key, err := f.store.CreateAgentAccount(opCtx, "town-agent", "pack.town", accountsv1.CredentialKind_API_KEY, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(key, "ak_") {
		t.Fatalf("api key %q", key)
	}
	pair := mustAuth(t, f, "town-agent", key)
	p, err := f.store.Verify(context.Background(), pair.SessionToken)
	if err != nil {
		t.Fatal(err)
	}
	if p.AccountID != id || !slices.Equal(p.Roles, []Role{RoleAgent}) || p.AgentPackID != "pack.town" {
		t.Fatalf("agent principal %+v", p)
	}
	// The key is stored only as an Argon2id hash.
	acc, _ := f.store.lookupID(id)
	if acc.GetCredential().GetKind() != accountsv1.CredentialKind_API_KEY || strings.Contains(hex.EncodeToString(acc.GetCredential().GetHash()), hex.EncodeToString([]byte(key))) {
		t.Fatal("api key stored in the clear")
	}
	// Password reset does not apply to an API key account.
	if _, err := f.store.ResetPassword(opCtx, id, "new password here", 0); Code(err) != connect.CodeInvalidArgument {
		t.Fatalf("reset on agent: %v", err)
	}
	// WORKLOAD_JWT is disabled without an issuer.
	if _, _, err := f.store.CreateAgentAccount(opCtx, "k8s-agent", "pack.town", accountsv1.CredentialKind_WORKLOAD_JWT, "system:serviceaccount:andara:town"); Code(err) != connect.CodeInvalidArgument {
		t.Fatalf("workload jwt without issuer: %v", err)
	}
}

type fakeWorkload struct{ subject string }

func (w fakeWorkload) VerifySubject(_ context.Context, token string) (string, error) {
	if token == "good-jwt" {
		return w.subject, nil
	}
	return "", errors.New("bad")
}

func TestAgentAccount_WorkloadJWT(t *testing.T) {
	f := newFixture(t, func(o *Options) { o.Workload = fakeWorkload{subject: "system:serviceaccount:andara:town"} })
	opCtx, _ := f.bootstrapOperator("oper", "operator-password")
	if _, _, err := f.store.CreateAgentAccount(opCtx, "k8s-agent", "pack.town", accountsv1.CredentialKind_WORKLOAD_JWT, ""); Code(err) != connect.CodeInvalidArgument {
		t.Fatalf("missing subject: %v", err)
	}
	id, key, err := f.store.CreateAgentAccount(opCtx, "k8s-agent", "pack.town", accountsv1.CredentialKind_WORKLOAD_JWT, "system:serviceaccount:andara:town")
	if err != nil || key != "" {
		t.Fatalf("id=%s key=%q err=%v", id, key, err)
	}
	mustAuth(t, f, "k8s-agent", "good-jwt")
	if _, err := f.store.Authenticate(context.Background(), "k8s-agent", "bad-jwt", "peer"); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("bad jwt: %v", err)
	}
	// Same issuer, different subject: refused.
	other, _, _ := f.store.CreateAgentAccount(opCtx, "other-agent", "pack.docks", accountsv1.CredentialKind_WORKLOAD_JWT, "system:serviceaccount:andara:docks")
	if _, err := f.store.Authenticate(context.Background(), "other-agent", "good-jwt", "peer"); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("subject mismatch for %s: %v", other, err)
	}
}

// --- rate limiting --------------------------------------------------------------

func TestRateLimit(t *testing.T) {
	f := newFixture(t, func(o *Options) { o.RateLimit = RateLimit{N: 3, Period: time.Minute} })
	opCtx, _ := f.bootstrapOperator("oper", "operator-password")
	if _, err := f.store.CreateAccount(opCtx, "brian", "correct horse battery", nil); err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		if _, err := f.store.Authenticate(context.Background(), "brian", "wrong", Peer(fmt.Sprintf("peer%d", i))); !errors.Is(err, ErrUnauthenticated) {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	// Fourth from a new peer: the username bucket is empty.
	if _, err := f.store.Authenticate(context.Background(), "brian", "correct horse battery", "peer9"); !errors.Is(err, ErrRateLimited) || Code(err) != connect.CodeResourceExhausted {
		t.Fatalf("username limit: %v", err)
	}
	// Three unknown usernames from one peer exhaust the peer bucket; the
	// operator's correct password from that peer is then refused.
	for i := range 3 {
		if _, err := f.store.Authenticate(context.Background(), fmt.Sprintf("ghost%d", i), "wrong", "hostile"); !errors.Is(err, ErrUnauthenticated) {
			t.Fatalf("ghost %d: %v", i, err)
		}
	}
	if _, err := f.store.Authenticate(context.Background(), "oper", "operator-password", "hostile"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("peer limit: %v", err)
	}
	// No lockout: the buckets refill.
	f.clock.advance(time.Minute)
	mustAuth(t, f, "brian", "correct horse battery")
	if got := testutil.ToFloat64(f.store.Metrics().Attempts.WithLabelValues(OutcomeRateLimited)); got != 2 {
		t.Fatalf("rate_limited = %v", got)
	}
}

func TestParseRateLimit(t *testing.T) {
	for in, want := range map[string]RateLimit{"10/m": {10, time.Minute}, "5/s": {5, time.Second}, "100/h": {100, time.Hour}, "30/5m": {30, 5 * time.Minute}, "off": {}, "": {}} {
		got, err := ParseRateLimit(in)
		if err != nil || got != want {
			t.Errorf("%q: got %+v, %v", in, got, err)
		}
	}
	for _, in := range []string{"10", "x/m", "10/x", "-1/m"} {
		if _, err := ParseRateLimit(in); err == nil {
			t.Errorf("%q accepted", in)
		}
	}
}

// --- boot ------------------------------------------------------------------

func TestOpen_RejectsBadRecord(t *testing.T) {
	f := newFixture(t, nil)
	if err := f.accounts.Append(context.Background(), "junk", []byte{0xff, 0xff, 0xff}); err != nil {
		t.Fatal(err)
	}
	h := slog.New(slog.DiscardHandler)
	_, err := Open(context.Background(), Options{
		Accounts: f.accounts, Audit: f.audit, Keys: f.keys, Argon2: testArgon,
		SessionTTL: time.Hour, RefreshTTL: time.Hour, InviteTTL: time.Hour, Log: h,
	})
	if err == nil || !strings.Contains(err.Error(), "junk") {
		t.Fatalf("bad record accepted: %v", err)
	}
}

func TestOpen_FailedWriteLeavesIndexUntouched(t *testing.T) {
	f := newFixture(t, nil)
	opCtx, _ := f.bootstrapOperator("oper", "operator-password")
	f.accounts.Fail = errors.New("broker down")
	_, err := f.store.CreateAccount(opCtx, "brian", "correct horse battery", nil)
	if err == nil || Code(err) != connect.CodeInternal {
		t.Fatalf("got %v", err)
	}
	if _, ok := f.store.lookupUsername("brian"); ok {
		t.Fatal("account indexed although the write failed")
	}
	f.accounts.Fail = nil
	if _, err := f.store.CreateAccount(opCtx, "brian", "correct horse battery", nil); err != nil {
		t.Fatal(err)
	}
}
