// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package auth

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	auditv1 "github.com/valesordev/andara/gen/go/andara/audit/v1"
	"github.com/valesordev/andara/server/recordlog"
)

// AW-SRV-035: an Operator grants a Builder their packs.

func (f *fixture) builder(username string, roles ...Role) string {
	f.t.Helper()
	if len(roles) == 0 {
		roles = []Role{RoleBuilder}
	}
	id, err := f.store.CreateAccount(f.operatorCtx(), username, "correct horse battery", roles)
	if err != nil {
		f.t.Fatal(err)
	}
	return id
}

func (f *fixture) lastAudit(action string) *auditv1.AuditRecord {
	f.t.Helper()
	recs := f.auditRecords()
	for i := len(recs) - 1; i >= 0; i-- {
		if recs[i].GetAction() == action {
			return recs[i]
		}
	}
	f.t.Fatalf("no %s audit record", action)
	return nil
}

func (f *fixture) countAudit(action string) int {
	n := 0
	for _, r := range f.auditRecords() {
		if r.GetAction() == action {
			n++
		}
	}
	return n
}

func reasonOf(err error) string {
	var re *ReasonError
	if errors.As(err, &re) {
		return re.Reason
	}
	return ""
}

// AC-1, AC-3, AC-9: a grant is stored sorted and deduplicated, a second call
// replaces it, and each writes one audit record with the before and after
// sets.
func TestSetBuilderPacks_ReplacesSortsAndAudits(t *testing.T) {
	f := newFixture(t, nil)
	id := f.builder("alice")

	g, err := f.store.SetBuilderPacks(f.operatorCtx(), id, []string{"town", "docks", "town"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(g.Packs, []string{"docks", "town"}) || !g.BuilderRole || g.RecordVersion == 0 {
		t.Errorf("grant = %+v", g)
	}
	if got := f.store.BuilderPacks(id); !slices.Equal(got, []string{"docks", "town"}) {
		t.Errorf("stored = %v", got)
	}
	rec := f.lastAudit(ActionSetBuilderPacks)
	if rec.GetOutcome() != AuditOK || rec.GetTarget() != id || rec.GetActorAccountId() != "op" ||
		len(rec.GetBuilderPacksBefore()) != 0 || !slices.Equal(rec.GetBuilderPacksAfter(), []string{"docks", "town"}) {
		t.Errorf("audit = %v", rec)
	}

	g, err = f.store.SetBuilderPacks(f.operatorCtx(), id, []string{"town"}, 0)
	if err != nil || !slices.Equal(g.Packs, []string{"town"}) {
		t.Fatalf("replace = %+v, %v", g, err)
	}
	rec = f.lastAudit(ActionSetBuilderPacks)
	if !slices.Equal(rec.GetBuilderPacksBefore(), []string{"docks", "town"}) || !slices.Equal(rec.GetBuilderPacksAfter(), []string{"town"}) {
		t.Errorf("audit before/after = %v / %v", rec.GetBuilderPacksBefore(), rec.GetBuilderPacksAfter())
	}

	// AC-4: an empty set clears.
	if g, err = f.store.SetBuilderPacks(f.operatorCtx(), id, nil, 0); err != nil || len(g.Packs) != 0 || len(f.store.BuilderPacks(id)) != 0 {
		t.Errorf("clear = %+v, %v; stored %v", g, err, f.store.BuilderPacks(id))
	}
	if n := f.countAudit(ActionSetBuilderPacks); n != 3 {
		t.Errorf("%d audit records, want 3", n)
	}
}

// AC-5: without operator, PERMISSION_DENIED operator_only, nothing changes,
// and the refusal is audited.
func TestSetBuilderPacks_OperatorOnly(t *testing.T) {
	f := newFixture(t, nil)
	id := f.builder("alice")
	if _, err := f.store.SetBuilderPacks(f.operatorCtx(), id, []string{"town"}, 0); err != nil {
		t.Fatal(err)
	}
	before := f.accountRecords()

	ctx := WithPrincipal(context.Background(), Principal{AccountID: id, Roles: []Role{RoleBuilder}})
	_, err := f.store.SetBuilderPacks(ctx, id, []string{"town", "wilds"}, 0)
	if !errors.Is(err, ErrPermissionDenied) || reasonOf(err) != ReasonOperatorOnly {
		t.Fatalf("err = %v (reason %q)", err, reasonOf(err))
	}
	if len(f.accountRecords()) != len(before) || !slices.Equal(f.store.BuilderPacks(id), []string{"town"}) {
		t.Error("the Account changed")
	}
	rec := f.lastAudit(ActionSetBuilderPacks)
	if rec.GetOutcome() != AuditDenied || rec.GetActorAccountId() != id || len(rec.GetBuilderPacksAfter()) != 0 ||
		!slices.Equal(rec.GetBuilderPacksBefore(), []string{"town"}) {
		t.Errorf("audit = %v", rec)
	}
	if n := f.countAudit(ActionSetBuilderPacks); n != 2 {
		t.Errorf("%d audit records, want 2 (the grant, the refusal)", n)
	}
	if !strings.Contains(f.logs.String(), `"msg":"builder packs refused"`) || !strings.Contains(f.logs.String(), `"reason":"operator_only"`) {
		t.Errorf("no warn line naming the reason:\n%s", f.logs.String())
	}

	// No Principal at all is not a privileged action: no record.
	if _, err := f.store.SetBuilderPacks(context.Background(), id, nil, 0); !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("no principal: %v", err)
	}
	if n := f.countAudit(ActionSetBuilderPacks); n != 2 {
		t.Errorf("%d audit records after an unauthenticated call, want 2", n)
	}
}

// AC-6, AC-7, AC-11, and an unknown Account: each refused with its reason,
// the Account unchanged, the refusal audited.
func TestSetBuilderPacks_Refusals(t *testing.T) {
	f := newFixture(t, nil)
	id := f.builder("alice")
	g, err := f.store.SetBuilderPacks(f.operatorCtx(), id, []string{"town"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		account string
		packs   []string
		version uint64
		is      error
		reason  string
		outcome string
	}{
		{"andara.core", id, []string{"town", "andara.core"}, 0, ErrInvalidArgument, ReasonCoreNotGrantable, AuditInvalid},
		{"uppercase", id, []string{"Town"}, 0, ErrInvalidArgument, ReasonInvalidPackID, AuditInvalid},
		{"empty segment", id, []string{"town..docks"}, 0, ErrInvalidArgument, ReasonInvalidPackID, AuditInvalid},
		{"leading digit", id, []string{"9town"}, 0, ErrInvalidArgument, ReasonInvalidPackID, AuditInvalid},
		{"a comma", id, []string{"town,docks"}, 0, ErrInvalidArgument, ReasonInvalidPackID, AuditInvalid},
		{"empty", id, []string{""}, 0, ErrInvalidArgument, ReasonInvalidPackID, AuditInvalid},
		{"stale version", id, []string{"docks"}, g.RecordVersion - 1, ErrVersionConflict, ReasonRecordVersion, AuditConflict},
		{"no such account", "acct-nobody", []string{"docks"}, 0, ErrNotFound, ReasonAccountNotFound, AuditDenied},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := f.store.SetBuilderPacks(f.operatorCtx(), tc.account, tc.packs, tc.version)
			if !errors.Is(err, tc.is) || reasonOf(err) != tc.reason {
				t.Fatalf("err = %v (reason %q), want %v / %s", err, reasonOf(err), tc.is, tc.reason)
			}
			if got := f.store.BuilderPacks(id); !slices.Equal(got, []string{"town"}) {
				t.Errorf("stored = %v", got)
			}
			if rec := f.lastAudit(ActionSetBuilderPacks); rec.GetOutcome() != tc.outcome || len(rec.GetBuilderPacksAfter()) != 0 {
				t.Errorf("audit = %v", rec)
			}
		})
	}
	if _, err := f.store.SetBuilderPacks(f.operatorCtx(), id, []string{"andara.core"}, 0); err == nil ||
		!strings.Contains(err.Error(), "andara.core is published by the server and can't be granted") {
		t.Errorf("the core message: %v", err)
	}
	// A dotted pack ID, and the current version, are fine.
	if _, err := f.store.SetBuilderPacks(f.operatorCtx(), id, []string{"guild.north_hall"}, g.RecordVersion); err != nil {
		t.Errorf("a dotted pack id: %v", err)
	}
}

// AC-8: roles and packs are independent. A grant on an Account without
// builder is stored and reported inert, and survives the role coming and
// going.
func TestSetBuilderPacks_IndependentOfTheRole(t *testing.T) {
	f := newFixture(t, nil)
	id := f.builder("bob", RolePlayer)
	g, err := f.store.SetBuilderPacks(f.operatorCtx(), id, []string{"town"}, 0)
	if err != nil || g.BuilderRole || !slices.Equal(g.Packs, []string{"town"}) {
		t.Fatalf("grant = %+v, %v", g, err)
	}
	if _, err := f.store.SetRoles(f.operatorCtx(), id, []Role{RolePlayer, RoleBuilder}, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.SetRoles(f.operatorCtx(), id, []Role{RolePlayer}, 0); err != nil {
		t.Fatal(err)
	}
	if got := f.store.BuilderPacks(id); !slices.Equal(got, []string{"town"}) {
		t.Errorf("after the role came and went: %v", got)
	}
}

// AC-10: the grant is in the Account store, so a restart keeps it.
func TestSetBuilderPacks_SurvivesARestart(t *testing.T) {
	f := newFixture(t, nil)
	id := f.builder("alice")
	if _, err := f.store.SetBuilderPacks(f.operatorCtx(), id, []string{"town", "docks"}, 0); err != nil {
		t.Fatal(err)
	}
	f.reopen(nil)
	if got := f.store.BuilderPacks(id); !slices.Equal(got, []string{"docks", "town"}) {
		t.Errorf("after a restart: %v", got)
	}
}

// The Observability requirements: the accounts.write span with action and
// outcome, the privileged-action series pre-seeded and counted, and the
// info line.
func TestSetBuilderPacks_Instrumentation(t *testing.T) {
	f := newFixture(t, nil)
	if !strings.Contains(f.metricsText(), `andara_privileged_actions_total{action="set_builder_packs"} 0`) {
		t.Error("set_builder_packs is not pre-seeded at 0")
	}
	id := f.builder("alice")
	if _, err := f.store.SetBuilderPacks(f.operatorCtx(), id, []string{"town"}, 0); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.metricsText(), `andara_privileged_actions_total{action="set_builder_packs"} 1`) {
		t.Error("the grant is not counted")
	}
	var found bool
	for _, s := range f.spans.Ended() {
		if s.Name() != "accounts.write" {
			continue
		}
		attrs := map[string]string{}
		for _, kv := range s.Attributes() {
			attrs[string(kv.Key)] = kv.Value.AsString()
		}
		found = attrs["action"] == ActionSetBuilderPacks && attrs["outcome"] == AuditOK
	}
	if !found {
		t.Error("no accounts.write span with action=set_builder_packs outcome=ok")
	}
	logs := f.logs.String()
	for _, want := range []string{`"msg":"builder packs set"`, `"target_account_id":"` + id + `"`, `"after":["town"]`, `"actor_account_id":"op"`} {
		if !strings.Contains(logs, want) {
			t.Errorf("the info line lacks %s", want)
		}
	}
}

// stallingLog, once armed, holds the next Append until released and passes
// the rest through: an audit topic that goes slow for exactly one write.
type stallingLog struct {
	recordlog.Log
	armed   atomic.Bool
	entered chan struct{}
	release chan struct{}
}

func (l *stallingLog) Append(ctx context.Context, key string, value []byte) error {
	if l.armed.CompareAndSwap(true, false) {
		close(l.entered)
		<-l.release
	}
	return l.Log.Append(ctx, key, value)
}

// A refusal's audit write happens outside wmu (Codex on #266). With the audit
// topic stalled on a non-operator's refused call, an Operator's grant still
// goes through, well inside the audit timeout.
func TestSetBuilderPacks_ARefusalDoesNotHoldTheWriteLock(t *testing.T) {
	stall := &stallingLog{entered: make(chan struct{}), release: make(chan struct{})}
	f := newFixture(t, func(o *Options) {
		stall.Log = o.Audit
		o.Audit = stall
	})
	defer close(stall.release)
	id := f.builder("alice")
	stall.armed.Store(true)

	go func() {
		ctx := WithPrincipal(context.Background(), Principal{AccountID: id, Roles: []Role{RoleBuilder}})
		_, _ = f.store.SetBuilderPacks(ctx, id, []string{"town"}, 0)
	}()
	<-stall.entered

	done := make(chan error, 1)
	go func() {
		_, err := f.store.SetBuilderPacks(f.operatorCtx(), id, []string{"docks"}, 0)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(AuditWriteTimeout / 2):
		t.Fatal("an Operator's grant waited on a non-operator's refusal being audited")
	}
}
