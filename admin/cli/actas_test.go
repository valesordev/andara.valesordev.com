// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package cli

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	auditv1 "github.com/valesordev/andara/gen/go/andara/audit/v1"
	"github.com/valesordev/andara/server/auth"
	"github.com/valesordev/andara/server/recordlog"
)

// AW-SRV-039: `--as` on the content write commands.

// auditRecords reads the audit topic back.
func (s *contentStack) auditRecords(t *testing.T) []*auditv1.AuditRecord {
	t.Helper()
	var out []*auditv1.AuditRecord
	err := s.audit.Replay(context.Background(), func(r recordlog.Record) error {
		var rec auditv1.AuditRecord
		if err := proto.Unmarshal(r.Value, &rec); err == nil {
			out = append(out, &rec)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// actAsCount is andara_admin_act_as_total{outcome}, scraped from the server.
func (s *contentStack) actAsCount(t *testing.T, outcome string) float64 {
	t.Helper()
	mfs, err := s.liveServer.reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() != "andara_admin_act_as_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "outcome" && l.GetValue() == outcome {
					return m.GetCounter().GetValue()
				}
			}
		}
	}
	t.Fatal("andara_admin_act_as_total is not registered")
	return 0
}

// AC-7, AC-1, AC-2, AC-10: an Operator publishes as a Builder; the manifest
// names both of them, the history shows both, and a publish with no --as has
// the two equal.
func TestContentPublish_AsABuilder(t *testing.T) {
	s := memoryStack(t)
	oper := s.identity(t, "oper", "operator-password")
	aliceID, alice := s.builder(t, "alice", "town")

	// AC-2: no --as, publisher = author.
	mustRun(t, alice, "content", "publish", "--path", devFixture)
	if cv, _ := s.reg.Manifest("town", 1); cv.GetAuthor() != aliceID || cv.GetPublisher() != aliceID {
		t.Fatalf("town@1: author %q, publisher %q", cv.GetAuthor(), cv.GetPublisher())
	}
	mustRun(t, oper, "content", "publish", "--path", devFixture)
	operID := func() string { cv, _ := s.reg.Manifest("town", 2); return cv.GetAuthor() }()
	if cv, _ := s.reg.Manifest("town", 2); operID == "" || cv.GetPublisher() != operID {
		t.Fatalf("town@2: author %q, publisher %q", cv.GetAuthor(), cv.GetPublisher())
	}

	// AC-7: --as sends the metadata.
	res := mustRun(t, oper, "content", "publish", "--path", devFixture, "--as", aliceID)
	if res.stdout != "town@3 published (parent 2), awaiting approval\n" {
		t.Errorf("stdout = %q", res.stdout)
	}
	cv, _ := s.reg.Manifest("town", 3)
	if cv.GetAuthor() != aliceID || cv.GetPublisher() != operID {
		t.Fatalf("town@3: author %q, publisher %q; want %q, %q", cv.GetAuthor(), cv.GetPublisher(), aliceID, operID)
	}

	// AC-1: the audit record names both identities.
	var published bool
	for _, r := range s.auditRecords(t) {
		if r.GetAction() == "publish" && r.GetVersion() == 3 {
			published = true
			if r.GetActorAccountId() != operID || r.GetActingAsAccountId() != aliceID {
				t.Errorf("publish record: actor %q, acting as %q", r.GetActorAccountId(), r.GetActingAsAccountId())
			}
		}
	}
	if !published {
		t.Error("no publish audit record for town@3")
	}
	if got := s.actAsCount(t, "ok"); got < 1 {
		t.Errorf("andara_admin_act_as_total{ok} = %v after an acted-as publish", got)
	}

	// AC-10: history --output json carries the publisher on every entry.
	res = mustRun(t, oper, "content", "history", "town", "-o", "json")
	var hist struct {
		Versions []map[string]any `json:"versions"`
	}
	if err := json.Unmarshal([]byte(res.stdout), &hist); err != nil || len(hist.Versions) != 3 {
		t.Fatalf("history: %v %s", err, res.stdout)
	}
	for _, v := range hist.Versions {
		want := map[float64][2]string{1: {aliceID, aliceID}, 2: {operID, operID}, 3: {aliceID, operID}}[v["version"].(float64)]
		if v["author"] != want[0] || v["publisher"] != want[1] {
			t.Errorf("history v%v: author %v, publisher %v; want %v", v["version"], v["author"], v["publisher"], want)
		}
	}
}

// AC-3, AC-4 through the CLI: a Builder can't act as anyone, and an unknown
// Account isn't one to act as. Nothing is written either way.
func TestContentPublish_AsIsRefused(t *testing.T) {
	s := memoryStack(t)
	aliceID, alice := s.builder(t, "alice", "town")
	_, bob := s.builder(t, "bob", "town")
	oper := s.identity(t, "oper", "operator-password")

	res := runCLI(t, []string{"content", "publish", "--path", devFixture, "--as", aliceID}, bob)
	if res.exit != ExitFail || !strings.Contains(res.stderr, "permission denied") {
		t.Errorf("a Builder acting as a Builder: exit=%d stderr=%q", res.exit, res.stderr)
	}
	res = runCLI(t, []string{"content", "publish", "--path", devFixture, "--as", "acct-nobody"}, oper)
	if res.exit != ExitFail || !strings.Contains(res.stderr, "permission denied") {
		t.Errorf("acting as an unknown Account: exit=%d stderr=%q", res.exit, res.stderr)
	}
	if _, ok := s.reg.Manifest("town", 1); ok {
		t.Error("a refused act-as published a version")
	}
	if s.actAsCount(t, "denied") < 1 || s.actAsCount(t, "unknown_account") < 1 {
		t.Errorf("act_as_total denied=%v unknown_account=%v", s.actAsCount(t, "denied"), s.actAsCount(t, "unknown_account"))
	}
	_ = alice
}

// AC-8: an Operator approving what they published --as a Builder is asked
// first, and the server audits it as a self-approval.
func TestContentApprove_SelfApprovalOfAnActedAsPublish(t *testing.T) {
	s := memoryStack(t)
	oper := s.identity(t, "oper", "operator-password")
	aliceID, _ := s.builder(t, "alice", "town")
	mustRun(t, oper, "content", "publish", "--path", devFixture, "--as", aliceID)

	// author is alice, publisher is the Operator: the caller is the publisher.
	res := runTTY(t, []string{"content", "approve", "town", "1"}, oper, "n\n")
	if res.exit != ExitFail || !strings.Contains(res.stderr, "You published town@1. Approve it yourself as oper? [y/N]") {
		t.Fatalf("declined: exit=%d stderr=%q", res.exit, res.stderr)
	}
	if cv, _ := s.reg.Manifest("town", 1); cv.GetApprovedBy() != "" {
		t.Fatal("a declined prompt approved the version")
	}
	res = runTTY(t, []string{"content", "approve", "town", "1"}, oper, "y\n")
	if res.exit != ExitOK || res.stdout != "town@1 approved by oper (self-approval: you published it)\n" {
		t.Fatalf("approved: exit=%d stdout=%q stderr=%q", res.exit, res.stdout, res.stderr)
	}
	var self bool
	for _, r := range s.auditRecords(t) {
		if r.GetAction() == "approve" && r.GetVersion() == 1 && r.GetSelfApproval() {
			self = true
		}
	}
	if !self {
		t.Error("the server did not audit the approval as a self-approval")
	}

	// And with --as naming the author: the prompt compares the manifest with
	// the --as value too. The server then refuses, since acting as a Builder
	// leaves the Operator's own privileges behind.
	mustRun(t, oper, "content", "publish", "--path", devFixture, "--as", aliceID)
	res = runTTY(t, []string{"content", "approve", "town", "2", "--as", aliceID}, oper, "y\n")
	if !strings.Contains(res.stderr, "You published town@2.") || res.exit != ExitFail {
		t.Errorf("approve --as the author: exit=%d stderr=%q", res.exit, res.stderr)
	}
	if cv, _ := s.reg.Manifest("town", 2); cv.GetApprovedBy() != "" {
		t.Error("an Operator approved as the Builder who is the version's author")
	}
}

// AC-8: the prompt also compares the manifest with the --as value. An
// Operator approving --as the author of a version a different Operator
// published is neither its author nor its publisher, but is asked.
func TestContentApprove_PromptComparesTheAsValue(t *testing.T) {
	s := memoryStack(t)
	aliceID, _ := s.builder(t, "alice", "town")
	op := auth.WithPrincipal(context.Background(), auth.Principal{AccountID: "op", Roles: []auth.Role{auth.RoleOperator}})
	if _, err := s.store.CreateAccount(op, "oper2", "operator-two-pw", []auth.Role{auth.RoleOperator}); err != nil {
		t.Fatal(err)
	}
	oper2 := s.identity(t, "oper2", "operator-two-pw")
	oper := s.identity(t, "oper", "operator-password")
	mustRun(t, oper2, "content", "publish", "--path", devFixture, "--as", aliceID)

	res := runTTY(t, []string{"content", "approve", "town", "1", "--as", aliceID}, oper, "n\n")
	if res.exit != ExitFail || !strings.Contains(res.stderr, "You published town@1.") {
		t.Errorf("exit=%d stderr=%q", res.exit, res.stderr)
	}
	// Without --as the caller is neither: no prompt, so a plain approval.
	res = runTTY(t, []string{"content", "approve", "town", "1"}, oper, "")
	if res.exit != ExitOK || strings.Contains(res.stderr, "You published") {
		t.Errorf("no --as: exit=%d stdout=%q stderr=%q", res.exit, res.stdout, res.stderr)
	}
}

// --as is on the four write commands and nowhere else; activate and rollback
// send it too.
func TestContentWrites_AsOnActivateAndRollback(t *testing.T) {
	s := memoryStack(t)
	aliceID, _ := s.builder(t, "alice", "town")
	_, bob := s.builder(t, "bob", "town")
	oper := s.identity(t, "oper", "operator-password")

	mustRun(t, bob, "content", "publish", "--path", devFixture)
	mustRun(t, bob, "content", "publish", "--path", devFixture)
	mustRun(t, oper, "content", "approve", "town", "1", "--yes")
	mustRun(t, oper, "content", "approve", "town", "2", "--yes")

	before := s.actAsCount(t, "ok")
	mustRun(t, oper, "content", "activate", "town", "1", "--yes", "--as", aliceID)
	mustRun(t, oper, "content", "activate", "town", "2", "--yes", "--as", aliceID)
	mustRun(t, oper, "content", "rollback", "town", "--yes", "--as", aliceID)
	if got := s.actAsCount(t, "ok"); got <= before {
		t.Errorf("act_as_total{ok} stayed at %v: activate/rollback did not send the metadata", got)
	}

	for _, args := range [][]string{
		{"content", "history", "town", "--as", aliceID},
		{"content", "validate", "--path", devFixture, "--as", aliceID},
		{"auth", "whoami", "--as", aliceID},
	} {
		if res := runCLI(t, args, oper); res.exit != ExitUsage {
			t.Errorf("%v: exit=%d, want usage (--as is for the write commands)", args, res.exit)
		}
	}
}

// AC-9: no code path in the CLI reads the Session token's `act` claim. The
// token is read unverified, and only the server decides who a caller is.
func TestCLI_NeverReadsTheTokenActClaim(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.Ident:
				if n.Name == "TokenAccountID" || n.Name == "isCaller" {
					t.Errorf("%s: %s reads the token's act claim", fset.Position(n.Pos()), n.Name)
				}
			case *ast.BasicLit:
				if n.Kind == token.STRING && (n.Value == `"act"` || n.Value == "`act`") {
					t.Errorf("%s: the string %s names the token's act claim", fset.Position(n.Pos()), n.Value)
				}
			case *ast.Field:
				if n.Tag != nil && (strings.Contains(n.Tag.Value, `json:"act"`) || strings.Contains(n.Tag.Value, `json:"act,`)) {
					t.Errorf("%s: a struct tag %s decodes the token's act claim", fset.Position(n.Pos()), n.Tag.Value)
				}
			case *ast.SelectorExpr:
				if n.Sel.Name == "ActingAs" {
					t.Errorf("%s: %s reads the acting-as of a Principal or token", fset.Position(n.Pos()), n.Sel.Name)
				}
				if x, ok := n.X.(*ast.Ident); ok && x.Name == "auth" && strings.HasPrefix(n.Sel.Name, "Token") && n.Sel.Name != "TokenCaller" {
					t.Errorf("%s: auth.%s reads a token; only auth.TokenCaller is allowed, and it reads no act claim", fset.Position(n.Pos()), n.Sel.Name)
				}
			}
			return true
		})
	}
}
