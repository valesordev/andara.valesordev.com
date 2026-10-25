// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package auth

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"

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

// AW-SRV-062: the Ceiling. Only an operator may act as an operator; a game
// master naming one is refused after the target lookup, and audited.

const ceilingDetail = "target holds operator; actor does not"

// privilegedActions reads andara_privileged_actions_total{action} from the
// fixture's registry.
func (f *fixture) privilegedActions(action string) float64 {
	f.t.Helper()
	mfs, err := f.reg.Gather()
	if err != nil {
		f.t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() != "andara_privileged_actions_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "action" && l.GetValue() == action {
					return m.GetCounter().GetValue()
				}
			}
		}
	}
	return 0
}

// lastLog is the last JSON log line whose msg is msg.
func (f *fixture) lastLog(msg string) map[string]any {
	f.t.Helper()
	var last map[string]any
	for l := range strings.SplitSeq(f.logs.String(), "\n") {
		var m map[string]any
		if json.Unmarshal([]byte(l), &m) == nil && m["msg"] == msg {
			last = m
		}
	}
	if last == nil {
		f.t.Fatalf("no %q line:\n%s", msg, f.logs.String())
	}
	return last
}

// AC-1 and AC-2 (store half), AC-8: a game master naming an Operator is
// refused with ErrPermissionDenied, not the unknown-account error, and the
// refusal is audited with the Ceiling's detail, counted, and logged under the
// caller's trace. An Admin call names its method and OpenSession doesn't;
// the Ceiling is the same check on both.
func TestActAs_CeilingRefusesAGameMasterNamingAnOperator(t *testing.T) {
	f := newFixture(t, nil)
	opID := f.builder("oper", RoleOperator)
	gm := Principal{AccountID: "acct-gm", Roles: []Role{RoleGameMaster}}
	tp := sdktrace.NewTracerProvider()
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	for name, ctx := range map[string]context.Context{
		"admin metadata": WithActAsMethod(context.Background(), "andara.admin.v1.Admin/ActivateVersion"),
		"OpenSession":    WithSessionID(context.Background(), "sess-gm"),
	} {
		t.Run(name, func(t *testing.T) {
			ctx, span := tp.Tracer("test").Start(ctx, name)
			defer span.End()
			before, audits := f.privilegedActions(ActionActAs), len(f.auditRecords())

			got, err := f.store.ActAs(ctx, gm, opID)
			if !errors.Is(err, ErrPermissionDenied) || errors.Is(err, ErrActAsNoAccount) {
				t.Fatalf("err = %v, want ErrPermissionDenied and not ErrActAsNoAccount", err)
			}
			if got.AccountID != "" || got.ActingAs != "" || len(got.Roles) != 0 {
				t.Errorf("a refused act-as returned a Principal: %+v", got)
			}
			if n := len(f.auditRecords()) - audits; n != 1 {
				t.Fatalf("%d audit records, want 1", n)
			}
			rec := f.lastAudit(ActionActAs)
			if rec.GetOutcome() != AuditDenied || rec.GetDetail() != ceilingDetail || rec.GetTarget() != opID ||
				rec.GetActorAccountId() != "acct-gm" || rec.GetActingAsAccountId() != "" {
				t.Errorf("record = %v", rec)
			}
			if got := f.privilegedActions(ActionActAs) - before; got != 1 {
				t.Errorf("privileged_actions_total{act_as} rose by %v, want 1", got)
			}
			line := f.lastLog("privileged action")
			if line["action"] != ActionActAs || line["outcome"] != AuditDenied || line["target"] != opID ||
				line["actor_account_id"] != "acct-gm" || line["trace_id"] == "" || line["trace_id"] != span.SpanContext().TraceID().String() {
				t.Errorf("log line = %v", line)
			}
		})
	}
}

// AC-3, AC-4, AC-5: everything the Ceiling doesn't cover acts as before.
func TestActAs_CeilingLeavesOtherTargetsAlone(t *testing.T) {
	f := newFixture(t, nil)
	targets := map[string]string{
		"operator":    f.builder("oper2", RoleOperator),
		"game_master": f.builder("gm2", RoleGameMaster),
		"player":      f.builder("pla", RolePlayer),
		"builder":     f.builder("bui", RoleBuilder),
	}
	operator := Principal{AccountID: "acct-op", Roles: []Role{RoleOperator}}
	gm := Principal{AccountID: "acct-gm", Roles: []Role{RoleGameMaster}}
	both := Principal{AccountID: "acct-both", Roles: []Role{RoleGameMaster, RoleOperator}}
	for _, tc := range []struct {
		name   string
		actor  Principal
		target string
	}{
		{"an operator as an operator", operator, "operator"},
		{"a game master as a game master", gm, "game_master"},
		{"a game master as a player", gm, "player"},
		{"a game master as a builder", gm, "builder"},
		{"game master and operator as an operator", both, "operator"},
	} {
		for path, ctx := range map[string]context.Context{
			"admin metadata": WithActAsMethod(context.Background(), "andara.admin.v1.Admin/PublishVersion"),
			"OpenSession":    context.Background(),
		} {
			t.Run(tc.name+" via "+path, func(t *testing.T) {
				id := targets[tc.target]
				got, err := f.store.ActAs(ctx, tc.actor, id)
				if err != nil {
					t.Fatalf("err = %v", err)
				}
				if got.AccountID != tc.actor.AccountID || got.ActingAs != id {
					t.Errorf("principal = %+v", got)
				}
				if rec := f.lastAudit(ActionActAs); rec.GetOutcome() != AuditOK || rec.GetActingAsAccountId() != id {
					t.Errorf("record = %v", rec)
				}
			})
		}
	}
}

// The contract's order of checks: an actor without either Role is refused
// on the role check even when the target is an Operator, and an unknown
// target is unknown_account, never the Ceiling.
func TestActAs_CeilingRunsLast(t *testing.T) {
	f := newFixture(t, nil)
	opID := f.builder("oper", RoleOperator)
	player := Principal{AccountID: "acct-pl", Roles: []Role{RolePlayer}}
	gm := Principal{AccountID: "acct-gm", Roles: []Role{RoleGameMaster}}

	if _, err := f.store.ActAs(context.Background(), player, opID); !errors.Is(err, ErrPermissionDenied) || errors.Is(err, ErrActAsNoAccount) {
		t.Fatalf("player: err = %v", err)
	}
	if d := f.lastAudit(ActionActAs).GetDetail(); d != "requires operator or game_master" {
		t.Errorf("player: detail = %q", d)
	}
	if _, err := f.store.ActAs(context.Background(), gm, "acct-nobody"); !errors.Is(err, ErrActAsNoAccount) {
		t.Fatalf("unknown: err = %v", err)
	}
	if d := f.lastAudit(ActionActAs).GetDetail(); d != "no such active account" {
		t.Errorf("unknown: detail = %q", d)
	}
}
