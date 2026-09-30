// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package command_test

import (
	"context"
	"errors"
	"testing"

	"github.com/valesordev/andara/server/auth"
	"github.com/valesordev/andara/server/command"
	"github.com/valesordev/andara/server/sim"
)

// AW-SRV-036: goto, before the log.

var builderPrincipal = auth.Principal{AccountID: "acct-bea", Roles: []auth.Role{auth.RolePlayer, auth.RoleBuilder}}

func (f *fixture) submitAs(t *testing.T, p auth.Principal, raw string) (command.Accepted, error) {
	t.Helper()
	return f.p.Submit(context.Background(), command.Intent{SessionID: "s-alice", Raw: raw, ClientRef: "ref"}, p)
}

// Both forms resolve to a Goto carrying Zone and Room. A bare Room is the
// actor's Zone, filled from the Binding before the log (AC-2).
func TestGoto_ParsesBothFormsAndFillsTheZone(t *testing.T) {
	f := newFixture(t, nil)
	for raw, want := range map[string][2]string{
		"goto docks/pier": {"docks", "pier"},
		"goto hall":       {"town", "hall"},
		"GOTO Docks/Pier": {"docks", "pier"},
	} {
		acc, err := f.submitAs(t, builderPrincipal, raw)
		if err != nil {
			t.Fatalf("%q: %v", raw, err)
		}
		recs := f.log.records[acc.Partition]
		rec := recs[len(recs)-1]
		g := rec.GetGoto()
		if acc.Verb != "goto" || rec.GetZoneId() != "town" || g.GetTargetZoneId() != want[0] || g.GetTargetRoomId() != want[1] {
			t.Errorf("%q: accepted %+v, record %v", raw, acc, rec)
		}
		if acc.Partition != sim.PartitionFor("town") {
			t.Errorf("%q: produced to partition %d, not the actor's Zone's", raw, acc.Partition)
		}
	}
}

// AC-5: no argument is missing_argument (arg target), and a malformed
// reference invalid_argument, both with the usage as detail, at parse, and
// nothing reaches the log.
func TestGoto_ParseRefusals(t *testing.T) {
	f := newFixture(t, nil)
	for raw, code := range map[string]string{
		"goto":           command.CodeMissingArgument,
		"goto /pier":     command.CodeInvalidArgument,
		"goto docks/":    command.CodeInvalidArgument,
		"goto a/b/c":     command.CodeInvalidArgument,
		"goto docks/p-r": command.CodeInvalidArgument,
		"goto 9docks/x":  command.CodeInvalidArgument,
		"goto dock$/x":   command.CodeInvalidArgument,
	} {
		_, err := f.submitAs(t, builderPrincipal, raw)
		e, ok := command.AsError(err)
		if !ok || e.Stage != command.StageParse || e.Code != code || e.Arg != "target" || e.Detail != "usage: goto <zone>/<room>" {
			t.Errorf("%q: %+v", raw, err)
		}
	}
	if f.log.calls != 0 {
		t.Fatalf("a refused goto reached the log: %d calls", f.log.calls)
	}
	// No abbreviation and no alias: a prefix of goto is not goto.
	for _, raw := range []string{"got docks/pier", "g docks/pier"} {
		if _, err := f.submitAs(t, builderPrincipal, raw); err == nil {
			t.Errorf("%q was accepted", raw)
		}
	}
	if f.log.calls != 0 {
		t.Fatal("a prefix of goto reached the log")
	}
}

// AC-3 and AC-9: goto needs builder. A player, and an Operator without
// builder, are refused not_authorized "you may not goto", audited, with no
// log offset consumed. An Operator acting as a Builder's Account holds that
// Account's roles, and goes through.
func TestGoto_NeedsBuilder(t *testing.T) {
	f := newFixture(t, nil)
	operator := auth.Principal{AccountID: "acct-op", Roles: []auth.Role{auth.RoleOperator}}
	for name, p := range map[string]auth.Principal{"player": player, "operator without builder": operator} {
		before := f.audit.Len()
		_, err := f.submitAs(t, p, "goto docks/pier")
		e, ok := command.AsError(err)
		if !ok || e.Stage != command.StageAuthorize || e.Code != command.CodeNotAuthorized || e.Detail != "you may not goto" || !errors.Is(err, auth.ErrNotAuthorized) {
			t.Errorf("%s: %+v", name, err)
		}
		if f.audit.Len() != before+1 {
			t.Errorf("%s: %d audit records written, want 1", name, f.audit.Len()-before)
		}
	}
	if f.log.calls != 0 {
		t.Fatalf("a refused goto consumed an offset: %d calls", f.log.calls)
	}
	actingAs := auth.Principal{AccountID: "acct-op", ActingAs: "acct-bea", Roles: []auth.Role{auth.RolePlayer, auth.RoleBuilder}}
	if _, err := f.submitAs(t, actingAs, "goto docks/pier"); err != nil {
		t.Fatalf("an Operator acting as a Builder: %v", err)
	}
	if got := f.rejected(command.StageAuthorize, command.CodeNotAuthorized); got != 2 {
		t.Errorf("rejected{authorize,not_authorized} = %v", got)
	}
}
