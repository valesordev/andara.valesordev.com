// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package auth

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	accountsv1 "github.com/valesordev/andara/gen/go/andara/accounts/v1"
)

// AW-SRV-040: no audit record is written with wmu held.

const wlPassword = "correct horse battery"

// writeLockCase is one audited Account write. setup runs before the audit
// log is armed to stall and returns the call under test.
type writeLockCase struct {
	name    string
	action  string
	outcome string
	setup   func(t *testing.T, f *fixture) func() error
}

func writeLockCases() []writeLockCase {
	op := func(f *fixture) context.Context { return f.operatorCtx() }
	victim := func(t *testing.T, f *fixture) string {
		t.Helper()
		id, err := f.store.CreateAccount(op(f), "victim", wlPassword, nil)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	return []writeLockCase{
		{"CreateAccount", ActionCreateAccount, AuditOK, func(t *testing.T, f *fixture) func() error {
			return func() error { _, err := f.store.CreateAccount(op(f), "dave", wlPassword, nil); return err }
		}},
		{"CreateAccount/username taken", ActionCreateAccount, AuditConflict, func(t *testing.T, f *fixture) func() error {
			return func() error { _, err := f.store.CreateAccount(op(f), "carol", wlPassword, nil); return err }
		}},
		{"ResetPassword", ActionResetPassword, AuditOK, func(t *testing.T, f *fixture) func() error {
			id := victim(t, f)
			return func() error { _, err := f.store.ResetPassword(op(f), id, "another horse battery", 0); return err }
		}},
		{"ResetPassword/no such account", ActionResetPassword, AuditDenied, func(t *testing.T, f *fixture) func() error {
			return func() error { _, err := f.store.ResetPassword(op(f), "nobody", "another horse battery", 0); return err }
		}},
		{"ResetPassword/version conflict", ActionResetPassword, AuditConflict, func(t *testing.T, f *fixture) func() error {
			id := victim(t, f)
			return func() error { _, err := f.store.ResetPassword(op(f), id, "another horse battery", 99); return err }
		}},
		{"SetRoles", ActionSetRoles, AuditOK, func(t *testing.T, f *fixture) func() error {
			id := victim(t, f)
			return func() error { _, err := f.store.SetRoles(op(f), id, []Role{RolePlayer, RoleBuilder}, 0); return err }
		}},
		{"SetRoles/agent role", ActionSetRoles, AuditDenied, func(t *testing.T, f *fixture) func() error {
			id := victim(t, f)
			return func() error { _, err := f.store.SetRoles(op(f), id, []Role{RoleAgent}, 0); return err }
		}},
		{"SetAccountStatus", ActionSetAccountStatus, AuditOK, func(t *testing.T, f *fixture) func() error {
			id := victim(t, f)
			return func() error {
				_, err := f.store.SetAccountStatus(op(f), id, accountsv1.AccountStatus_DISABLED, 0)
				return err
			}
		}},
		{"SetAccountStatus/no such account", ActionSetAccountStatus, AuditDenied, func(t *testing.T, f *fixture) func() error {
			return func() error {
				_, err := f.store.SetAccountStatus(op(f), "nobody", accountsv1.AccountStatus_DISABLED, 0)
				return err
			}
		}},
		{"IssueInvite", ActionIssueInvite, AuditOK, func(t *testing.T, f *fixture) func() error {
			opCtx, _ := f.bootstrapOperator("opr", wlPassword)
			return func() error { _, _, err := f.store.IssueInvite(opCtx, 2); return err }
		}},
		{"RevokeInvite", ActionRevokeInvite, AuditOK, func(t *testing.T, f *fixture) func() error {
			opCtx, _ := f.bootstrapOperator("opr", wlPassword)
			codes, _, err := f.store.IssueInvite(opCtx, 1)
			if err != nil {
				t.Fatal(err)
			}
			return func() error { return f.store.RevokeInvite(opCtx, codes[0]) }
		}},
		{"RevokeInvite/no such code", ActionRevokeInvite, AuditDenied, func(t *testing.T, f *fixture) func() error {
			return func() error { return f.store.RevokeInvite(op(f), "nope") }
		}},
		{"SetRegistrationMode", ActionSetRegistrationMode, AuditOK, func(t *testing.T, f *fixture) func() error {
			return func() error {
				_, err := f.store.SetRegistrationMode(op(f), accountsv1.RegistrationMode_INVITE)
				return err
			}
		}},
		{"CreateAgentAccount", ActionCreateAgentAccount, AuditOK, func(t *testing.T, f *fixture) func() error {
			return func() error {
				_, _, err := f.store.CreateAgentAccount(op(f), "agent1", "town", accountsv1.CredentialKind_API_KEY, "")
				return err
			}
		}},
		{"CreateAgentAccount/username taken", ActionCreateAgentAccount, AuditConflict, func(t *testing.T, f *fixture) func() error {
			return func() error {
				_, _, err := f.store.CreateAgentAccount(op(f), "carol", "town", accountsv1.CredentialKind_API_KEY, "")
				return err
			}
		}},
		{"Bootstrap", ActionCreateAccount, AuditOK, func(t *testing.T, f *fixture) func() error {
			return func() error { _, err := f.store.Bootstrap(context.Background(), "first", wlPassword); return err }
		}},
		{"Register/invite redeemed", ActionRedeemInvite, AuditOK, func(t *testing.T, f *fixture) func() error {
			opCtx, _ := f.bootstrapOperator("opr", wlPassword)
			if _, err := f.store.SetRegistrationMode(opCtx, accountsv1.RegistrationMode_INVITE); err != nil {
				t.Fatal(err)
			}
			codes, _, err := f.store.IssueInvite(opCtx, 1)
			if err != nil {
				t.Fatal(err)
			}
			return func() error {
				_, err := f.store.Register(context.Background(), "newbie", wlPassword, codes[0], "peer")
				return err
			}
		}},
		{"Refresh/revoked token", ActionRefreshRevoked, AuditDenied, func(t *testing.T, f *fixture) func() error {
			pair := mustAuth(t, f, "carol", wlPassword)
			if _, err := f.store.Revoke(context.Background(), pair.RefreshToken, false, "peer"); err != nil {
				t.Fatal(err)
			}
			return func() error {
				_, err := f.store.Refresh(context.Background(), pair.RefreshToken, "peer")
				return err
			}
		}},
		{"Revoke", ActionRevokeRefresh, AuditOK, func(t *testing.T, f *fixture) func() error {
			pair := mustAuth(t, f, "carol", wlPassword)
			return func() error {
				_, err := f.store.Revoke(context.Background(), pair.RefreshToken, true, "peer")
				return err
			}
		}},
	}
}

// AC-1, AC-2: with the audit log stalled on one Account write, a Login
// completes well inside the audit timeout, and once the log is released
// the write has produced exactly one record, with the outcome it had.
func TestAccountWrites_AuditOutsideTheWriteLock(t *testing.T) {
	for _, tc := range writeLockCases() {
		t.Run(tc.name, func(t *testing.T) {
			stall := &stallingLog{entered: make(chan struct{}), release: make(chan struct{})}
			f := newFixture(t, func(o *Options) {
				stall.Log = o.Audit
				o.Audit = stall
			})
			defer close(stall.release)
			if _, err := f.store.CreateAccount(f.operatorCtx(), "carol", wlPassword, nil); err != nil {
				t.Fatal(err)
			}
			call := tc.setup(t, f)
			before := len(f.auditRecords())
			stall.armed.Store(true)

			callDone := make(chan struct{})
			go func() {
				defer close(callDone)
				_ = call()
			}()
			select {
			case <-stall.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("the write was never audited: the stalling audit log was not entered")
			}

			login := make(chan error, 1)
			go func() {
				_, err := f.store.Authenticate(context.Background(), "carol", wlPassword, "peer")
				login <- err
			}()
			select {
			case err := <-login:
				if err != nil {
					t.Fatalf("login: %v", err)
				}
			case <-time.After(AuditWriteTimeout / 2):
				t.Fatalf("a Login waited on %s being audited", tc.name)
			}

			stall.release <- struct{}{}
			select {
			case <-callDone:
			case <-time.After(5 * time.Second):
				t.Fatal("the write did not return after the audit log was released")
			}
			recs := f.auditRecords()
			if len(recs) != before+1 {
				t.Fatalf("%d audit records written, want exactly 1", len(recs)-before)
			}
			got := recs[len(recs)-1]
			if got.GetAction() != tc.action || got.GetOutcome() != tc.outcome {
				t.Errorf("audit record = %s/%s, want %s/%s", got.GetAction(), got.GetOutcome(), tc.action, tc.outcome)
			}
		})
	}
}

// AC-3: a refusal decided from the caller's roles alone never takes wmu.
// The test holds wmu itself, so a method that reached for it would block.
func TestAdminWrites_RoleRefusalNeverTakesTheWriteLock(t *testing.T) {
	f := newFixture(t, nil)
	id, err := f.store.CreateAccount(f.operatorCtx(), "alice", wlPassword, []Role{RoleBuilder})
	if err != nil {
		t.Fatal(err)
	}
	calls := map[string]func(ctx context.Context) error{
		"CreateAccount": func(ctx context.Context) error {
			_, err := f.store.CreateAccount(ctx, "dave", wlPassword, nil)
			return err
		},
		"ResetPassword": func(ctx context.Context) error {
			_, err := f.store.ResetPassword(ctx, id, "another horse battery", 0)
			return err
		},
		"SetRoles": func(ctx context.Context) error {
			_, err := f.store.SetRoles(ctx, id, []Role{RolePlayer}, 0)
			return err
		},
		"SetAccountStatus": func(ctx context.Context) error {
			_, err := f.store.SetAccountStatus(ctx, id, accountsv1.AccountStatus_DISABLED, 0)
			return err
		},
		"IssueInvite": func(ctx context.Context) error {
			_, _, err := f.store.IssueInvite(ctx, 1)
			return err
		},
		"RevokeInvite": func(ctx context.Context) error { return f.store.RevokeInvite(ctx, "code") },
		"SetRegistrationMode": func(ctx context.Context) error {
			_, err := f.store.SetRegistrationMode(ctx, accountsv1.RegistrationMode_OPEN)
			return err
		},
		"CreateAgentAccount": func(ctx context.Context) error {
			_, _, err := f.store.CreateAgentAccount(ctx, "agent1", "town", accountsv1.CredentialKind_API_KEY, "")
			return err
		},
		"SetBuilderPacks": func(ctx context.Context) error {
			_, err := f.store.SetBuilderPacks(ctx, id, []string{"town"}, 0)
			return err
		},
	}
	builder := WithPrincipal(context.Background(), Principal{AccountID: id, Roles: []Role{RoleBuilder}})

	f.store.wmu.Lock()
	defer f.store.wmu.Unlock()
	refuse := func(name string, ctx context.Context, call func(context.Context) error, want error) {
		t.Helper()
		done := make(chan error, 1)
		go func() { done <- call(ctx) }()
		select {
		case err := <-done:
			if !errors.Is(err, want) {
				t.Errorf("%s: err = %v, want %v", name, err, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s took wmu to refuse a caller without operator", name)
		}
	}
	for _, name := range sortedKeys(calls) {
		refuse(name+"/non-operator", builder, calls[name], ErrPermissionDenied)
		if name != "SetBuilderPacks" { // a Principal-less Pack Grant is refused the same way, below
			refuse(name+"/no principal", context.Background(), calls[name], ErrUnauthenticated)
		}
	}
	refuse("SetBuilderPacks/no principal", context.Background(), calls["SetBuilderPacks"], ErrUnauthenticated)

	// An operator disabling their own account is decided from the actor and
	// the argument alone.
	self := WithPrincipal(context.Background(), Principal{AccountID: "op", Roles: []Role{RoleOperator}})
	refuse("SetAccountStatus/self", self, func(ctx context.Context) error {
		_, err := f.store.SetAccountStatus(ctx, "op", accountsv1.AccountStatus_DISABLED, 0)
		return err
	}, ErrPermissionDenied)
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// AC-4: no function in server/auth reaches audit.Record while holding wmu.
func TestNoAuditRecordUnderWriteLock(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	src := map[string]string{}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		src[name] = string(b)
	}
	if len(src) == 0 {
		t.Fatal("no source files found")
	}
	violations, err := auditUnderLock(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range violations {
		t.Error(v)
	}
}

// The check itself catches what it exists to catch: a direct call, one
// through a helper, one inside a closure, and not a call after the lock's
// closure has returned (the shape of Store.writeLocked).
func TestAuditUnderLock_Detects(t *testing.T) {
	const head = "package p\n"
	cases := []struct {
		name string
		src  string
		want int
	}{
		{"direct", head + `func (s *S) A() { s.wmu.Lock(); defer s.wmu.Unlock(); s.audit.Record(1) }`, 1},
		{"helper", head + `func (s *S) A() { s.wmu.Lock(); defer s.wmu.Unlock(); s.refuse() }
func (s *S) refuse() { s.audit.Record(1) }`, 1},
		{"helper two deep", head + `func (s *S) A() { s.wmu.Lock(); defer s.wmu.Unlock(); s.b() }
func (s *S) b() { s.c() }
func (s *S) c() { a.Audit.Record(1) }`, 1},
		{"closure", head + `func (s *S) A() { s.wmu.Lock(); defer s.wmu.Unlock(); func() { s.audit.Record(1) }() }`, 1},
		{"locked literal", head + `func (s *S) A() { func() { s.wmu.Lock(); defer s.wmu.Unlock(); s.audit.Record(1) }() }`, 1},
		{"after the lock's closure", head + `func (s *S) A() {
	e := func() int { s.wmu.Lock(); defer s.wmu.Unlock(); return 1 }()
	s.audit.Record(e)
}`, 0},
		{"writeLocked callback", head + `func (s *S) A() { s.writeLocked(ctx, func() *Entry { s.audit.Record(1); return nil }) }`, 1},
		{"writeLocked callback via helper", head + `func (s *S) A() { s.writeLocked(ctx, func() *Entry { s.refuse(); return nil }) }
func (s *S) refuse() { s.audit.Record(1) }`, 1},
		{"writeLocked callback returning the entry", head + `func (s *S) A() {
	s.writeLocked(ctx, func() *Entry { return &Entry{} })
	s.audit.Record(1)
}`, 0},
		{"no lock", head + `func (s *S) A() { s.audit.Record(1) }`, 0},
		{"other Record", head + `func (s *S) A() { s.wmu.Lock(); defer s.wmu.Unlock(); s.log.Record(1) }`, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := auditUnderLock(map[string]string{"p.go": tc.src})
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != tc.want {
				t.Errorf("violations = %v, want %d", got, tc.want)
			}
		})
	}
}

// auditUnderLock parses src and returns one message per lock-holding region
// that reaches an audit Record call. A region is a function body that calls
// wmu.Lock itself (nested literals included), a function literal that does
// (its own body only), or a literal passed to Store.writeLocked. Reachability
// follows calls on the receiver or a bare name, by name, so two methods of one
// name are one node, and a function that unlocks before it records would be
// reported.
func auditUnderLock(src map[string]string) ([]string, error) {
	fset := token.NewFileSet()
	type fn struct {
		name  string
		pos   token.Pos
		calls map[string]bool // names called, nested literals included
		sink  bool            // calls audit.Record, nested literals included
	}
	decls := map[string][]*fn{}
	var regions []*fn

	// scan collects the names body calls and whether it records an audit
	// entry, nested literals included.
	scan := func(name string, body *ast.BlockStmt) *fn {
		f := &fn{name: name, pos: body.Pos(), calls: map[string]bool{}}
		ast.Inspect(body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch c := call.Fun.(type) {
			case *ast.SelectorExpr:
				// Only calls on a bare identifier (the receiver, a package)
				// are followed; s.opts.Workload.VerifySubject is an interface
				// call, and resolving it by name would find unrelated methods.
				if _, bare := c.X.(*ast.Ident); bare {
					f.calls[c.Sel.Name] = true
				}
				if c.Sel.Name == "Record" && endsIn(c.X, "audit", "Audit") {
					f.sink = true
				}
			case *ast.Ident:
				f.calls[c.Name] = true
			}
			return true
		})
		return f
	}

	var files []string
	for name := range src {
		files = append(files, name)
	}
	slices.Sort(files)
	for _, name := range files {
		file, err := parser.ParseFile(fset, name, src[name], 0)
		if err != nil {
			return nil, err
		}
		for _, d := range file.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			whole := scan(fd.Name.Name, fd.Body)
			decls[fd.Name.Name] = append(decls[fd.Name.Name], whole)
			if holdsLock(fd.Body, false) {
				regions = append(regions, whole)
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.FuncLit:
					if holdsLock(n.Body, false) {
						regions = append(regions, scan(fd.Name.Name+".func", n.Body))
					}
				case *ast.CallExpr:
					// Store.writeLocked takes wmu around the callback it is given.
					if c, ok := n.Fun.(*ast.SelectorExpr); ok && c.Sel.Name == "writeLocked" {
						for _, a := range n.Args {
							if lit, ok := a.(*ast.FuncLit); ok {
								regions = append(regions, scan(fd.Name.Name+".writeLocked callback", lit.Body))
							}
						}
					}
				}
				return true
			})
		}
	}

	var out []string
	for _, r := range regions {
		seen := map[string]bool{}
		var reach func(f *fn, path []string) []string
		reach = func(f *fn, path []string) []string {
			if f.sink {
				return path
			}
			for callee := range f.calls {
				if seen[callee] {
					continue
				}
				seen[callee] = true
				for _, d := range decls[callee] {
					if p := reach(d, append(slices.Clone(path), callee)); p != nil {
						return p
					}
				}
			}
			return nil
		}
		if r.sink {
			out = append(out, fmt.Sprintf("%s: %s calls audit.Record with wmu held", fset.Position(r.pos), r.name))
		} else if p := reach(r, nil); p != nil {
			out = append(out, fmt.Sprintf("%s: %s reaches audit.Record with wmu held, via %s", fset.Position(r.pos), r.name, strings.Join(p, " -> ")))
		}
	}
	return out, nil
}

// holdsLock reports whether body calls wmu.Lock. With lits false, calls in
// nested function literals don't count: those are regions of their own.
func holdsLock(body *ast.BlockStmt, lits bool) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncLit:
			return lits
		case *ast.CallExpr:
			if c, ok := n.Fun.(*ast.SelectorExpr); ok && c.Sel.Name == "Lock" && endsIn(c.X, "wmu") {
				found = true
			}
		}
		return true
	})
	return found
}

// endsIn reports whether e is a selector or identifier named one of names.
func endsIn(e ast.Expr, names ...string) bool {
	switch e := e.(type) {
	case *ast.SelectorExpr:
		return slices.Contains(names, e.Sel.Name)
	case *ast.Ident:
		return slices.Contains(names, e.Name)
	}
	return false
}
