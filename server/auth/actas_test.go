// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	accountsv1 "github.com/valesordev/andara/gen/go/andara/accounts/v1"
)

// AW-SRV-039: acting-as on an Admin call is audited as OpenSession's is,
// and an unknown or inactive target is told apart from a role refusal.

func TestActAs_AuditRecords(t *testing.T) {
	f := newFixture(t, nil)
	builderID := f.builder("alice")
	disabledID := f.builder("dormant")
	if _, err := f.store.SetAccountStatus(f.operatorCtx(), disabledID, accountsv1.AccountStatus_DISABLED, 0); err != nil {
		t.Fatal(err)
	}
	operator := Principal{AccountID: "op", Roles: []Role{RoleOperator}}
	player := Principal{AccountID: "pl", Roles: []Role{RolePlayer}}
	const method = "andara.admin.v1.Admin/PublishVersion"

	t.Run("ok names both identities and the method", func(t *testing.T) {
		got, err := f.store.ActAs(WithActAsMethod(context.Background(), method), operator, builderID)
		if err != nil || got.AccountID != "op" || got.ActingAs != builderID || !got.Has(RoleBuilder) || got.Has(RoleOperator) {
			t.Fatalf("principal %+v, err %v", got, err)
		}
		rec := f.lastAudit(ActionActAs)
		if rec.GetActorAccountId() != "op" || rec.GetActingAsAccountId() != builderID || rec.GetOutcome() != AuditOK || rec.GetDetail() != "method "+method {
			t.Errorf("record = %v", rec)
		}
	})
	t.Run("a Session's act-as names no method", func(t *testing.T) {
		if _, err := f.store.ActAs(context.Background(), operator, builderID); err != nil {
			t.Fatal(err)
		}
		if d := f.lastAudit(ActionActAs).GetDetail(); d != "" {
			t.Errorf("detail = %q", d)
		}
	})
	t.Run("a caller without operator or game_master", func(t *testing.T) {
		_, err := f.store.ActAs(WithActAsMethod(context.Background(), method), player, builderID)
		if !errors.Is(err, ErrPermissionDenied) || errors.Is(err, ErrActAsNoAccount) {
			t.Fatalf("err = %v", err)
		}
		rec := f.lastAudit(ActionActAs)
		if rec.GetOutcome() != AuditDenied || rec.GetDetail() != "requires operator or game_master" || rec.GetActorAccountId() != "pl" {
			t.Errorf("record = %v", rec)
		}
	})
	for name, target := range map[string]string{"unknown": "acct-nobody", "disabled": disabledID} {
		t.Run(name+" target", func(t *testing.T) {
			_, err := f.store.ActAs(WithActAsMethod(context.Background(), method), operator, target)
			if !errors.Is(err, ErrPermissionDenied) || !errors.Is(err, ErrActAsNoAccount) {
				t.Fatalf("err = %v", err)
			}
			rec := f.lastAudit(ActionActAs)
			if rec.GetOutcome() != AuditDenied || rec.GetDetail() != "no such active account" || rec.GetTarget() != target {
				t.Errorf("record = %v", rec)
			}
		})
	}
}

// TokenCaller names the Account a token is for and reads no other claim: a
// token carrying `act` still names its own Account, so a client can't be led
// to treat the target as itself.
func TestTokenCaller_IgnoresTheActClaim(t *testing.T) {
	f := newFixture(t, nil)
	tok, err := f.keys.sign(tokenPayload{AccountID: "acct-op", ActingAs: "acct-builder", Exp: f.clock.now().Add(time.Hour).Unix(), KeyID: f.keys.current().ID})
	if err != nil {
		t.Fatal(err)
	}
	if id, ok := TokenCaller(tok); !ok || id != "acct-op" {
		t.Errorf("TokenCaller = %q, %v", id, ok)
	}
	for _, bad := range []string{"", "nodot", "!!!.x"} {
		if id, ok := TokenCaller(bad); ok || id != "" {
			t.Errorf("TokenCaller(%q) = %q, %v", bad, id, ok)
		}
	}
}
