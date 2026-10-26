// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package gateway

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	adminv1 "github.com/valesordev/andara/gen/go/andara/admin/v1"
	"github.com/valesordev/andara/gen/go/andara/admin/v1/adminv1connect"
	auditv1 "github.com/valesordev/andara/gen/go/andara/audit/v1"
	gamev1 "github.com/valesordev/andara/gen/go/andara/game/v1"
	"github.com/valesordev/andara/server/auth"
	"github.com/valesordev/andara/server/recordlog"
)

// AW-SRV-062: the act-as Ceiling through the Gateway, with the real
// auth.Store as the verifier, on both acting-as paths.

type ceilingHarness struct {
	*harness
	store  *auth.Store
	audit  *recordlog.Memory
	admin  adminv1connect.AdminClient
	fc     *fakeContent
	tokens map[string]string // username -> session token
	ids    map[string]string // username -> account_id
}

func startCeiling(t *testing.T) *ceilingHarness {
	t.Helper()
	kr, err := auth.ParseKeyring(strings.NewReader("k1: " + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{5}, 32)) + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	audit := recordlog.NewMemory()
	store, err := auth.Open(context.Background(), auth.Options{
		Accounts: recordlog.NewMemory(), Audit: audit, Keys: kr,
		Argon2:     auth.Argon2Params{MemoryKiB: 64, Time: 1, Threads: 1},
		SessionTTL: time.Hour, RefreshTTL: time.Hour, InviteTTL: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	c := &ceilingHarness{store: store, audit: audit, fc: &fakeContent{}, tokens: map[string]string{}, ids: map[string]string{}}
	op := auth.WithPrincipal(context.Background(), auth.Principal{AccountID: "op", Roles: []auth.Role{auth.RoleOperator}})
	for name, roles := range map[string][]auth.Role{
		"oper":     {auth.RoleOperator},
		"gmaster":  {auth.RoleGameMaster},
		"gmaster2": {auth.RoleGameMaster},
		"gmop":     {auth.RoleGameMaster, auth.RoleOperator},
		"player":   {auth.RolePlayer},
		"alice":    {auth.RoleBuilder},
	} {
		id, err := store.CreateAccount(op, name, "correct horse battery", roles)
		if err != nil {
			t.Fatal(err)
		}
		pair, err := store.Authenticate(context.Background(), name, "correct horse battery", "peer")
		if err != nil {
			t.Fatal(err)
		}
		c.ids[name], c.tokens[name] = id, pair.SessionToken
	}
	c.harness = start(t, func(o *Options) {
		o.Verifier = store
		o.ContentAdmin = c.fc
	})
	hc, _ := c.httpClient()
	c.admin = adminv1connect.NewAdminClient(hc, c.baseURL())
	return c
}

// adminAs makes an Admin call as user with andara-act-as naming target.
func (c *ceilingHarness) adminAs(user, target string) error {
	c.fc.caller = auth.Principal{}
	req := connect.NewRequest(&adminv1.HasBlobsRequest{})
	req.Header().Set("Authorization", "Bearer "+c.tokens[user])
	req.Header().Set(ActAsHeader, target)
	_, err := c.admin.HasBlobs(context.Background(), req)
	return err
}

// openAs opens a Session as user with act_as_account_id naming target.
func (c *ceilingHarness) openAs(user, target string) (*gamev1.OpenSessionResponse, error) {
	resp, err := c.harness.game().OpenSession(context.Background(), connect.NewRequest(&gamev1.OpenSessionRequest{
		ProtocolVersion: 1, AuthToken: c.tokens[user], ClientName: "ceiling-test/0", ActAsAccountId: target,
	}))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

func (c *ceilingHarness) lastActAs(t *testing.T) *auditv1.AuditRecord {
	t.Helper()
	recs := c.audit.Records()
	for i := len(recs) - 1; i >= 0; i-- {
		var rec auditv1.AuditRecord
		if err := proto.Unmarshal(recs[i].Value, &rec); err != nil {
			t.Fatal(err)
		}
		if rec.GetAction() == auth.ActionActAs {
			return &rec
		}
	}
	t.Fatal("no act_as audit record")
	return nil
}

const ceilingDetail = "target holds operator; actor does not"

// AC-1: a game master's andara-act-as naming an Operator fails
// PERMISSION_DENIED before the handler, is audited with the Ceiling's
// detail, and is counted denied.
func TestCeiling_AdminMetadataRefusesAGameMasterNamingAnOperator(t *testing.T) {
	c := startCeiling(t)
	before := actAsCount(c.srv.metrics, ActAsDenied)
	err := c.adminAs("gmaster", c.ids["oper"])
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("err = %v, want PERMISSION_DENIED", err)
	}
	if c.fc.caller.AccountID != "" {
		t.Fatalf("the handler ran as %+v", c.fc.caller)
	}
	rec := c.lastActAs(t)
	if rec.GetOutcome() != auth.AuditDenied || rec.GetDetail() != ceilingDetail || rec.GetTarget() != c.ids["oper"] || rec.GetActorAccountId() != c.ids["gmaster"] {
		t.Errorf("record = %v", rec)
	}
	if got := actAsCount(c.srv.metrics, ActAsDenied) - before; got != 1 {
		t.Errorf("act_as_total{denied} rose by %v, want 1", got)
	}
	if got := actAsCount(c.srv.metrics, ActAsUnknownAccount); got != 0 {
		t.Errorf("act_as_total{unknown_account} = %v, want 0", got)
	}
}

// AC-2: the same game master's OpenSession naming the Operator opens no
// Session, and the same record is written.
func TestCeiling_OpenSessionRefusesAGameMasterNamingAnOperator(t *testing.T) {
	c := startCeiling(t)
	_, err := c.openAs("gmaster", c.ids["oper"])
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("err = %v, want PERMISSION_DENIED", err)
	}
	if n := c.srv.sessions.count(); n != 0 {
		t.Fatalf("%d sessions open", n)
	}
	rec := c.lastActAs(t)
	if rec.GetOutcome() != auth.AuditDenied || rec.GetDetail() != ceilingDetail || rec.GetTarget() != c.ids["oper"] || rec.GetActorAccountId() != c.ids["gmaster"] {
		t.Errorf("record = %v", rec)
	}
}

// AC-3, AC-4, AC-5 on the wire: the cases the Ceiling leaves alone succeed
// on both paths.
func TestCeiling_OtherActingAsSucceedsOnBothPaths(t *testing.T) {
	c := startCeiling(t)
	for _, tc := range []struct{ actor, target string }{
		{"oper", "gmop"}, // an Operator as another Operator
		{"gmaster", "gmaster2"},
		{"gmaster", "player"},
		{"gmaster", "alice"},
		{"gmop", "oper"},
	} {
		target := c.ids[tc.target]
		if err := c.adminAs(tc.actor, target); err != nil {
			t.Errorf("%s as %s, Admin: %v", tc.actor, tc.target, err)
		} else if c.fc.caller.ActingAs != target || c.fc.caller.AccountID != c.ids[tc.actor] {
			t.Errorf("%s as %s, Admin: handler saw %+v", tc.actor, tc.target, c.fc.caller)
		}
		resp, err := c.openAs(tc.actor, target)
		if err != nil {
			t.Errorf("%s as %s, OpenSession: %v", tc.actor, tc.target, err)
			continue
		}
		if sess, ok := c.srv.sessions.get(resp.GetSessionId()); !ok || sess.Principal.ActingAs != target {
			t.Errorf("%s as %s, OpenSession: session %+v", tc.actor, tc.target, sess)
		}
	}
}

// AC-6: on each path, the Ceiling's refusal reaches the caller with the code
// and message of an actor holding neither Role and of an unknown target, so
// a game master can't tell an Operator's account_id from no account at all.
func TestCeiling_RefusalIsWireIdenticalToTheOtherTwo(t *testing.T) {
	c := startCeiling(t)
	cases := []struct{ name, actor, target string }{
		{"ceiling", "gmaster", c.ids["oper"]},
		{"neither role", "player", c.ids["alice"]},
		{"unknown target", "gmaster", "acct-nobody"},
	}
	wire := func(err error) string {
		var ce *connect.Error
		if err == nil || !errors.As(err, &ce) {
			t.Fatalf("not a connect error: %v", err)
		}
		return ce.Code().String() + "|" + ce.Message() + "|" + ce.Error()
	}
	for path, call := range map[string]func(actor, target string) error{
		"admin metadata": c.adminAs,
		"OpenSession": func(actor, target string) error {
			_, err := c.openAs(actor, target)
			return err
		},
	} {
		want := ""
		for _, tc := range cases {
			got := wire(call(tc.actor, tc.target))
			if want == "" {
				want = got
				if !strings.HasPrefix(got, "permission_denied|") {
					t.Fatalf("%s %s: %q", path, tc.name, got)
				}
				continue
			}
			if got != want {
				t.Errorf("%s: %s refusal %q, want %q (the ceiling's)", path, tc.name, got, want)
			}
		}
	}
}
