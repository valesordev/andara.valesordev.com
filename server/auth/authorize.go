// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package auth

import (
	"context"
	"fmt"
)

// VerbRoles maps a verb to the role it requires. A verb absent from the
// table needs only an authenticated Session. AW-SRV-003's verb table owns
// the verbs themselves; this is the column of it that authorize reads.
type VerbRoles map[string]Role

// Authorizer is the `authorize` stage's decision, replacing AW-SRV-003's
// stub permission model. It reads the Principal and the table, never World
// state, and audits every rejection so an unauthorized Command leaves a
// record without consuming a log offset (AC-6).
//
// The story sketch takes (sim.Command, Principal, *gateway.Session); this
// takes the verb and the Session ID instead, because the Gateway imports
// this package for its Verifier and the reverse import would be a cycle.
// The information is the same.
type Authorizer struct {
	Table VerbRoles
	Audit *Auditor
}

// Authorize returns nil if p may submit verb, else ErrNotAuthorized —
// audited with actor, verb, and Session.
func (a *Authorizer) Authorize(ctx context.Context, verb string, p Principal, sessionID string) error {
	required, gated := a.Table[verb]
	if !gated || p.Has(required) {
		return nil
	}
	ctx = WithSessionID(ctx, sessionID)
	a.Audit.Record(ctx, Entry{Actor: p, Action: ActionAuthorize, Target: verb, Outcome: AuditDenied, Detail: "requires " + string(required)})
	return fmt.Errorf("%w: %s requires %s", ErrNotAuthorized, verb, required)
}

// AuthorizeBind returns nil if p may bind an Entity whose Template comes
// from templatePackID. Only agent Principals are scoped (AC-11): an agent
// may drive its own pack's Entities and nothing else. A rejection is audited.
func (a *Authorizer) AuthorizeBind(ctx context.Context, p Principal, templatePackID string, sessionID string) error {
	if !p.Has(RoleAgent) || p.AgentPackID == templatePackID {
		return nil
	}
	ctx = WithSessionID(ctx, sessionID)
	a.Audit.Record(ctx, Entry{Actor: p, Action: ActionBind, Target: templatePackID, Outcome: AuditDenied, Detail: "agent scoped to " + p.AgentPackID})
	return fmt.Errorf("%w: agent is scoped to pack %s", ErrNotAuthorized, p.AgentPackID)
}
