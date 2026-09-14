// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package auth

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	accountsv1 "github.com/valesordev/andara/gen/go/andara/accounts/v1"
)

// AC-1: no credential material, token, or hash appears in any log line,
// metric label, span attribute, or error message. Every secret the flows
// below produce is planted, then every telemetry surface is scanned.
func TestNoSecretLeaks(t *testing.T) {
	f := newFixture(t, func(o *Options) { o.RateLimit = RateLimit{N: 2, Period: time.Minute} })
	opCtx, opID := f.bootstrapOperator("oper", "operator-password-secret")
	const password = "correct-horse-battery-staple"
	const wrong = "wrong-password-that-must-not-leak"

	var secrets []string
	plant := func(name, s string) {
		t.Helper()
		if s == "" {
			t.Fatalf("planted secret %s is empty", name)
		}
		secrets = append(secrets, s)
	}
	plant("operator password", "operator-password-secret")
	plant("password", password)
	plant("wrong password", wrong)

	// Success and failure across every operation.
	id, err := f.store.CreateAccount(opCtx, "brian", password, nil)
	if err != nil {
		t.Fatal(err)
	}
	pair := mustAuth(t, f, "brian", password)
	plant("session token", pair.SessionToken)
	plant("refresh token", pair.RefreshToken)
	var errs []error
	_, e := f.store.Authenticate(context.Background(), "brian", wrong, "peer")
	errs = append(errs, e)
	_, e = f.store.Authenticate(context.Background(), "brian", wrong, "peer") // rate limited now
	errs = append(errs, e)
	_, e = f.store.Verify(context.Background(), pair.SessionToken+"x")
	errs = append(errs, e)
	rotated, err := f.store.Refresh(context.Background(), pair.RefreshToken, "peer2")
	if err != nil {
		t.Fatal(err)
	}
	plant("rotated session token", rotated.SessionToken)
	plant("rotated refresh token", rotated.RefreshToken)
	_, e = f.store.Refresh(context.Background(), pair.RefreshToken, "peer3") // revoked → audited
	errs = append(errs, e)
	if _, err := f.store.SetRegistrationMode(opCtx, accountsv1.RegistrationMode_INVITE); err != nil {
		t.Fatal(err)
	}
	codes, _, err := f.store.IssueInvite(opCtx, 2)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range codes {
		plant("invite code", c)
	}
	if err := f.store.RevokeInvite(opCtx, codes[1]); err != nil {
		t.Fatal(err)
	}
	_, e = f.store.Register(context.Background(), "newbie", password, codes[1], "peer4")
	errs = append(errs, e)
	_, e = f.store.Register(context.Background(), "newbie", "short", codes[0], "peer4")
	errs = append(errs, e)
	f.clock.advance(time.Minute) // the newbie bucket refills
	mustRegister(t, f, "newbie", password, codes[0])
	_, key, err := f.store.CreateAgentAccount(opCtx, "town-agent", "pack.town", accountsv1.CredentialKind_API_KEY, "")
	if err != nil {
		t.Fatal(err)
	}
	plant("api key", key)
	mustAuth(t, f, "town-agent", key)
	if _, err := f.store.ResetPassword(opCtx, id, "a-brand-new-password", 0); err != nil {
		t.Fatal(err)
	}
	plant("new password", "a-brand-new-password")
	if _, e := f.store.Revoke(context.Background(), rotated.RefreshToken, true, "peer5"); e != nil {
		t.Fatal(e)
	}
	ath := &Authorizer{Table: VerbRoles{"@build": RoleBuilder}, Audit: f.store.Auditor()}
	errs = append(errs, ath.Authorize(context.Background(), "@build", Principal{AccountID: id, Roles: []Role{RolePlayer}}, "sess"))

	// Hashes and salts from the records, in every encoding a log line could
	// plausibly render them in.
	for _, rec := range f.accountRecords() {
		acc := rec.GetAccount()
		if acc == nil {
			continue
		}
		for name, b := range map[string][]byte{"hash": acc.GetCredential().GetHash(), "salt": acc.GetCredential().GetSalt()} {
			if len(b) == 0 {
				continue
			}
			plant(name+" hex", hex.EncodeToString(b))
			plant(name+" b64", base64.StdEncoding.EncodeToString(b))
			plant(name+" b64url", base64.RawURLEncoding.EncodeToString(b))
		}
		for _, rt := range acc.GetRefreshTokens() {
			plant("refresh hash hex", hex.EncodeToString(rt.GetHash()))
			plant("refresh hash b64", base64.StdEncoding.EncodeToString(rt.GetHash()))
		}
		for _, inv := range acc.GetInvites() {
			plant("invite hash hex", hex.EncodeToString(inv.GetCodeHash()))
			plant("invite hash b64", base64.StdEncoding.EncodeToString(inv.GetCodeHash()))
		}
	}
	if len(secrets) < 20 {
		t.Fatalf("only %d secrets planted; the flows above should have produced more", len(secrets))
	}

	// The username must not appear on a failure line either (it may be a
	// password typed in the wrong box). It legitimately appears nowhere in
	// telemetry at all today, so scan for it outright.
	surfaces := map[string]string{
		"logs":    f.logs.String(),
		"metrics": f.metricsText(),
		"spans":   spanText(f),
		"errors":  errorText(errs),
		"audit":   auditText(f),
	}
	for surface, text := range surfaces {
		for _, s := range secrets {
			if strings.Contains(text, s) {
				t.Errorf("%s contains a planted secret (%d bytes, starts %q)", surface, len(s), s[:4])
			}
		}
		if strings.Contains(text, "brian") || strings.Contains(text, "newbie") {
			t.Errorf("%s contains a username", surface)
		}
	}
	// Sanity: the surfaces are not trivially empty, and the success line
	// names the account by id.
	if !strings.Contains(surfaces["logs"], `"account_id":"`+id+`"`) {
		t.Error("success log line should carry account_id")
	}
	if !strings.Contains(surfaces["logs"], `"actor_account_id":"`+opID+`"`) {
		t.Error("privileged action log line should carry actor_account_id")
	}
	if !strings.Contains(surfaces["spans"], "auth.verify_credential") || !strings.Contains(surfaces["spans"], "argon2.memory_kib") {
		t.Error("verify_credential span with argon2.memory_kib expected")
	}
}

func spanText(f *fixture) string {
	var sb strings.Builder
	for _, s := range f.spans.Ended() {
		sb.WriteString(s.Name())
		sb.WriteString(" ")
		for _, kv := range s.Attributes() {
			sb.WriteString(string(kv.Key) + "=" + kv.Value.String() + " ")
		}
		for _, ev := range s.Events() {
			sb.WriteString(ev.Name + " ")
			for _, kv := range ev.Attributes {
				sb.WriteString(kv.Value.String() + " ")
			}
		}
		sb.WriteString(s.Status().Description)
		sb.WriteString("\n")
	}
	return sb.String()
}

func errorText(errs []error) string {
	var sb strings.Builder
	for _, e := range errs {
		if e != nil {
			sb.WriteString(e.Error())
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

func auditText(f *fixture) string {
	var sb strings.Builder
	for _, r := range f.auditRecords() {
		sb.WriteString(r.String())
		sb.WriteString("\n")
	}
	return sb.String()
}

// AC-2: unknown Account and wrong credential cost the same. Median response
// times over 1,000 attempts each differ by less than 10%.
func TestAuthenticate_ConstantCostOnFailure(t *testing.T) {
	if testing.Short() {
		t.Skip("timing test")
	}
	// Real time, not the fixture clock: this measures the derivation.
	f := newFixture(t, func(o *Options) {
		o.Argon2 = Argon2Params{MemoryKiB: 256, Time: 1, Threads: 1}
		o.Now = time.Now
	})
	opCtx, _ := f.bootstrapOperator("oper", "operator-password")
	if _, err := f.store.CreateAccount(opCtx, "brian", "correct horse battery", nil); err != nil {
		t.Fatal(err)
	}
	const n = 1000
	measure := func(username string) time.Duration {
		ds := make([]time.Duration, n)
		for i := range n {
			start := time.Now()
			_, err := f.store.Authenticate(context.Background(), username, "wrong password", "peer")
			ds[i] = time.Since(start)
			if !errors.Is(err, ErrUnauthenticated) {
				t.Fatalf("%s: %v", username, err)
			}
		}
		sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
		return ds[n/2]
	}
	// Interleave so drift affects both equally.
	known, unknown := measure("brian"), measure("nobody")
	known2, unknown2 := measure("brian"), measure("nobody")
	known = (known + known2) / 2
	unknown = (unknown + unknown2) / 2
	diff := float64(known-unknown) / float64(known)
	if diff < 0 {
		diff = -diff
	}
	t.Logf("median known=%v unknown=%v diff=%.1f%%", known, unknown, diff*100)
	if diff >= 0.10 {
		t.Fatalf("medians differ by %.1f%%: known=%v unknown=%v", diff*100, known, unknown)
	}
}

// --- keyring file ------------------------------------------------------------

func TestLoadKeyring(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "keys")
	k := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))
	content := "# comment\n\ncurrent: " + k + "\nold: " + base64.StdEncoding.EncodeToString([]byte(strings.Repeat("o", 40))) + "\n"
	if err := os.WriteFile(good, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	kr, err := LoadKeyring(good)
	if err != nil {
		t.Fatal(err)
	}
	if ids := kr.KeyIDs(); fmt.Sprint(ids) != "[current old]" {
		t.Fatalf("ids %v", ids)
	}
	if kr.current().ID != "current" {
		t.Fatal("first key is not current")
	}

	loose := filepath.Join(dir, "loose")
	_ = os.WriteFile(loose, []byte(content), 0o644)
	if _, err := LoadKeyring(loose); err == nil || !strings.Contains(err.Error(), "mode") {
		t.Fatalf("0644 key file accepted: %v", err)
	}
	if _, err := LoadKeyring(""); err == nil {
		t.Fatal("empty path accepted")
	}
	if _, err := LoadKeyring(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("missing file accepted")
	}
	for name, bad := range map[string]string{
		"short":     "k: " + base64.StdEncoding.EncodeToString([]byte("short")),
		"not b64":   "k: not*base64",
		"no colon":  "k " + k,
		"dup":       "k: " + k + "\nk: " + k,
		"empty":     "\n# nothing\n",
		"dotted id": "a.b: " + k,
	} {
		if _, err := ParseKeyring(strings.NewReader(bad)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

// --- AC-6, AC-11: authorize -----------------------------------------------------

func TestAuthorize(t *testing.T) {
	f := newFixture(t, nil)
	a := &Authorizer{Table: VerbRoles{"@dig": RoleBuilder, "@shutdown": RoleOperator, "@goto": RoleGameMaster}, Audit: f.store.Auditor()}
	player := Principal{AccountID: "p1", Roles: []Role{RolePlayer}}
	builder := Principal{AccountID: "b1", Roles: []Role{RolePlayer, RoleBuilder}}
	operator := Principal{AccountID: "o1", Roles: []Role{RoleOperator}}

	// Every role × gated verb.
	cases := []struct {
		p    Principal
		verb string
		ok   bool
	}{
		{player, "look", true}, {player, "@dig", false}, {player, "@shutdown", false}, {player, "@goto", false},
		{builder, "look", true}, {builder, "@dig", true}, {builder, "@shutdown", false},
		{operator, "@shutdown", true}, {operator, "@dig", false}, // roles are a set, not a ladder
	}
	before := len(f.auditRecords())
	denied := 0
	for _, c := range cases {
		err := a.Authorize(context.Background(), c.verb, c.p, "sess-"+c.p.AccountID)
		if (err == nil) != c.ok {
			t.Errorf("%s %s: ok=%v err=%v", c.p.AccountID, c.verb, c.ok, err)
		}
		if err != nil {
			if !errors.Is(err, ErrNotAuthorized) {
				t.Errorf("%s %s: want ErrNotAuthorized, got %v", c.p.AccountID, c.verb, err)
			}
			denied++
		}
	}
	recs := f.auditRecords()[before:]
	if len(recs) != denied {
		t.Fatalf("%d rejections, %d audit records", denied, len(recs))
	}
	// AC-6's record names actor, verb, and Session.
	first := recs[0]
	if first.GetActorAccountId() != "p1" || first.GetTarget() != "@dig" || first.GetSessionId() != "sess-p1" || first.GetAction() != ActionAuthorize {
		t.Fatalf("audit record: %+v", first)
	}

	// AC-11: an agent may bind only its own pack's Templates.
	agent := Principal{AccountID: "a1", Roles: []Role{RoleAgent}, AgentPackID: "pack.town"}
	if err := a.AuthorizeBind(context.Background(), agent, "pack.town", "s"); err != nil {
		t.Fatal(err)
	}
	before = len(f.auditRecords())
	if err := a.AuthorizeBind(context.Background(), agent, "pack.docks", "s"); !errors.Is(err, ErrNotAuthorized) {
		t.Fatalf("out of scope: %v", err)
	}
	if recs := f.auditRecords()[before:]; len(recs) != 1 || recs[0].GetAction() != ActionBind || recs[0].GetTarget() != "pack.docks" {
		t.Fatalf("bind audit: %+v", recs)
	}
	// A player is not scoped.
	if err := a.AuthorizeBind(context.Background(), player, "pack.docks", "s"); err != nil {
		t.Fatal(err)
	}
}
