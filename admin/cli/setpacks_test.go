// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/valesordev/andara/server/auth"
)

// AW-SRV-035: `andara-cli account set-packs`, against a live gateway and the
// real Account store.

func (s *liveServer) account(t *testing.T, username string, roles ...auth.Role) string {
	t.Helper()
	op := auth.WithPrincipal(context.Background(), auth.Principal{AccountID: "op", Roles: []auth.Role{auth.RoleOperator}})
	id, err := s.store.CreateAccount(op, username, "builder-password", roles)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// jsonErrorCode is error.code from a --output json failure.
func jsonErrorCode(t *testing.T, stdout string) string {
	t.Helper()
	var env jsonErrorEnvelope
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("stdout is not the error envelope: %v\n%s", err, stdout)
	}
	return env.Error.Code
}

// AC-1, AC-3, AC-4, AC-8: grant, replace, clear, and a grant with no builder
// role, each printed as the stored set.
func TestAccountSetPacks_GrantReplaceClear(t *testing.T) {
	s := startServer(t)
	env := s.env(t)
	login(t, env)
	id := s.account(t, "alice", auth.RoleBuilder)

	res := runCLI(t, []string{"account", "set-packs", id, "--pack", "town", "--pack", "docks"}, env)
	if res.exit != ExitOK || res.stdout != id+": builder packs docks, town\n" {
		t.Fatalf("grant: exit=%d stdout=%q stderr=%q", res.exit, res.stdout, res.stderr)
	}
	res = runCLI(t, []string{"account", "set-packs", id, "--pack", "town"}, env)
	if res.exit != ExitOK || res.stdout != id+": builder packs town\n" {
		t.Errorf("replace: exit=%d stdout=%q", res.exit, res.stdout)
	}
	if got := s.store.BuilderPacks(id); len(got) != 1 || got[0] != "town" {
		t.Errorf("stored after replace: %v", got)
	}

	res = runCLI(t, []string{"account", "set-packs", id, "--pack", "town", "-o", "json"}, env)
	var out struct {
		AccountID     string   `json:"account_id"`
		BuilderPacks  []string `json:"builder_packs"`
		BuilderRole   bool     `json:"builder_role"`
		RecordVersion uint64   `json:"record_version"`
	}
	if err := json.Unmarshal([]byte(res.stdout), &out); err != nil || out.AccountID != id || len(out.BuilderPacks) != 1 || !out.BuilderRole || out.RecordVersion == 0 {
		t.Errorf("json: %s (%v)", res.stdout, err)
	}

	res = runCLI(t, []string{"account", "set-packs", id, "--clear"}, env)
	if res.exit != ExitOK || res.stdout != id+": builder packs none\n" || len(s.store.BuilderPacks(id)) != 0 {
		t.Errorf("clear: exit=%d stdout=%q", res.exit, res.stdout)
	}

	bob := s.account(t, "bob", auth.RolePlayer)
	res = runCLI(t, []string{"account", "set-packs", bob, "--pack", "town"}, env)
	if res.exit != ExitOK || res.stdout != bob+": builder packs town (inactive: no builder role)\n" {
		t.Errorf("no builder role: exit=%d stdout=%q", res.exit, res.stdout)
	}
}

// AC-5, AC-6, AC-7, AC-11: each server refusal is exit 1 with its reason as
// error.code, and the Account unchanged.
func TestAccountSetPacks_Refusals(t *testing.T) {
	s := startServer(t)
	env := s.env(t)
	login(t, env)
	id := s.account(t, "alice", auth.RoleBuilder)
	if res := runCLI(t, []string{"account", "set-packs", id, "--pack", "town"}, env); res.exit != ExitOK {
		t.Fatalf("grant: exit=%d stderr=%q", res.exit, res.stderr)
	}

	for _, tc := range []struct {
		name string
		args []string
		code string
		msg  string
	}{
		{"andara.core", []string{id, "--pack", "andara.core"}, "core_not_grantable", "andara.core is published by the server and can't be granted"},
		// The CLI doesn't pre-check a pack id: the server is the boundary.
		{"invalid id", []string{id, "--pack", "Town"}, "invalid_pack_id", `"Town" is not a pack id`},
		{"stale version", []string{id, "--pack", "docks", "--expected-version", "1"}, "record_version", "record changed"},
		{"no such account", []string{"acct-nobody", "--pack", "docks"}, "account_not_found", "no such account"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := runCLI(t, append([]string{"account", "set-packs"}, tc.args...), env)
			if res.exit != ExitFail || !strings.Contains(res.stderr, tc.msg) {
				t.Errorf("exit=%d stderr=%q", res.exit, res.stderr)
			}
			res = runCLI(t, append(append([]string{"account", "set-packs"}, tc.args...), "-o", "json"), env)
			if code := jsonErrorCode(t, res.stdout); code != tc.code {
				t.Errorf("error.code = %q, want %q", code, tc.code)
			}
			if got := s.store.BuilderPacks(id); len(got) != 1 || got[0] != "town" {
				t.Errorf("the Account changed: %v", got)
			}
		})
	}

	// AC-5: a Builder can't grant themselves a pack.
	benv := s.env(t)
	if res := runWithStdin(t, []string{"auth", "login", "--username", "alice", "--password-stdin"}, benv, "builder-password\n"); res.exit != ExitOK {
		t.Fatalf("login as alice: exit=%d stderr=%q", res.exit, res.stderr)
	}
	res := runCLI(t, []string{"account", "set-packs", id, "--pack", "town", "--pack", "wilds", "-o", "json"}, benv)
	if res.exit != ExitFail || jsonErrorCode(t, res.stdout) != "operator_only" {
		t.Errorf("as a Builder: exit=%d stdout=%q", res.exit, res.stdout)
	}
	if got := s.store.BuilderPacks(id); len(got) != 1 || got[0] != "town" {
		t.Errorf("the Account changed: %v", got)
	}
}

func TestAccountSetPacks_Usage(t *testing.T) {
	env := isolatedEnv(t, nil)
	for _, args := range [][]string{
		{"acct-x"},
		{"acct-x", "--pack", "town", "--clear"},
		{"--pack", "town"},
	} {
		if res := runCLI(t, append([]string{"account", "set-packs"}, args...), env); res.exit != ExitUsage {
			t.Errorf("%v: exit=%d, want %d", args, res.exit, ExitUsage)
		}
	}
}

// AC-2's CLI half (AW-SRV-035 §8, AW-CLI-003 §8 item 4): granted town and
// docks by `account set-packs`, a Builder's `content publish` of wilds
// exits 1 with error.code pack_not_held, and nothing is published. Their
// publish of town goes through.
func TestAccountSetPacks_PublishToAnUngrantedPackIsPackNotHeld(t *testing.T) {
	s := memoryStack(t)
	oper := s.identity(t, "oper", "operator-password")
	id, alice := s.builder(t, "alice") // builder, no packs
	mustRun(t, oper, "account", "set-packs", id, "--pack", "town", "--pack", "docks")

	wilds := filepath.Join(t.TempDir(), "wilds")
	if err := os.MkdirAll(wilds, 0o755); err != nil {
		t.Fatal(err)
	}
	writeAW(t, wilds, "pack.aw", "pack wilds requires andara.core@1\n")
	writeAW(t, wilds, "z.aw", "zone wilds \"Wilds\" {\n  fallback trail\n\n  room trail \"Trail\" {}\n}\n")

	res := runCLI(t, []string{"content", "publish", "--path", wilds, "-o", "json"}, alice)
	if res.exit != ExitFail || jsonErrorCode(t, res.stdout) != "pack_not_held" {
		t.Fatalf("publish wilds: exit=%d stdout=%q stderr=%q", res.exit, res.stdout, res.stderr)
	}
	res = runCLI(t, []string{"content", "publish", "--path", wilds}, alice)
	if res.exit != ExitFail || !strings.Contains(res.stderr, "does not hold pack wilds") {
		t.Errorf("human: exit=%d stderr=%q", res.exit, res.stderr)
	}
	if v := s.reg.Newest("wilds"); v != 0 {
		t.Errorf("wilds@%d was published", v)
	}
	if res := mustRun(t, alice, "content", "publish", "--path", devFixture); !strings.HasPrefix(res.stdout, "town@1 published") {
		t.Errorf("publish town: %q", res.stdout)
	}
}
