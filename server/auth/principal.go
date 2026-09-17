// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package auth

import (
	"context"
	"errors"
	"slices"
	"time"

	accountsv1 "github.com/valesordev/andara/gen/go/andara/accounts/v1"
)

// Role is an Account attribute checked in authorize (ADR-0006). The string
// form is the metric label and the glossary name; the proto enum is the wire
// and record form. Roles are a set, not a ladder: an operator who needs to
// build holds builder too.
type Role string

// The five roles ADR-0006 and the 2026-09-11 decision name.
const (
	RolePlayer     Role = "player"
	RoleBuilder    Role = "builder"
	RoleGameMaster Role = "game_master"
	RoleOperator   Role = "operator"
	RoleAgent      Role = "agent"
)

// AllRoles lists every Role, in enum order, for metric pre-seeding.
var AllRoles = []Role{RolePlayer, RoleBuilder, RoleGameMaster, RoleOperator, RoleAgent}

var roleFromProto = map[accountsv1.Role]Role{
	accountsv1.Role_PLAYER:      RolePlayer,
	accountsv1.Role_BUILDER:     RoleBuilder,
	accountsv1.Role_GAME_MASTER: RoleGameMaster,
	accountsv1.Role_OPERATOR:    RoleOperator,
	accountsv1.Role_AGENT:       RoleAgent,
}

var roleToProto = map[Role]accountsv1.Role{
	RolePlayer:     accountsv1.Role_PLAYER,
	RoleBuilder:    accountsv1.Role_BUILDER,
	RoleGameMaster: accountsv1.Role_GAME_MASTER,
	RoleOperator:   accountsv1.Role_OPERATOR,
	RoleAgent:      accountsv1.Role_AGENT,
}

// RolesFromProto converts and normalizes: unknown values dropped, sorted by
// enum value, deduplicated — the order the record stores.
func RolesFromProto(in []accountsv1.Role) []Role {
	protos := make([]accountsv1.Role, 0, len(in))
	for _, r := range in {
		if _, ok := roleFromProto[r]; ok && !slices.Contains(protos, r) {
			protos = append(protos, r)
		}
	}
	slices.Sort(protos)
	out := make([]Role, len(protos))
	for i, r := range protos {
		out[i] = roleFromProto[r]
	}
	return out
}

// RolesToProto is the inverse of RolesFromProto, sorted by enum value.
func RolesToProto(in []Role) []accountsv1.Role {
	out := make([]accountsv1.Role, 0, len(in))
	for _, r := range in {
		if p, ok := roleToProto[r]; ok && !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	slices.Sort(out)
	return out
}

// Principal is who a verified token speaks for, as the Gateway and the
// command pipeline see it. Roles, status, and the agent scope are read from
// the Account index when the token is verified, never from the token: a
// token is a bearer of identity (AW-SRV-008 AC-12).
type Principal struct {
	// AccountID is the Account that authenticated — who is really acting.
	AccountID string
	// Roles is the effective role set for the Session. When ActingAs is set
	// these are the acted-as Account's roles, so an operator sees what the
	// player sees; the audit record still names the operator.
	Roles []Role
	// ActingAs is the Account this Session acts as, or empty.
	ActingAs string
	// AgentPackID is the one Content Pack an agent Principal may drive.
	AgentPackID string
	// SessionExp is when the session token expires.
	SessionExp time.Time
}

// Has reports whether the Principal holds r.
func (p Principal) Has(r Role) bool { return slices.Contains(p.Roles, r) }

// EffectiveAccountID is the Account the Session operates as: ActingAs when
// set, else AccountID.
func (p Principal) EffectiveAccountID() string {
	if p.ActingAs != "" {
		return p.ActingAs
	}
	return p.AccountID
}

// Errors the package returns. The Gateway maps them to gRPC codes; the
// messages are fixed on purpose — one for every kind of failed
// authentication, one for every kind of bad invite — so nothing about the
// Account space can be learned from the wording.
var (
	// ErrUnauthenticated: bad username or password, bad or expired token,
	// disabled Account. UNAUTHENTICATED.
	ErrUnauthenticated = errors.New("authentication failed")
	// ErrPermissionDenied: invite invalid, role missing, act-as without
	// privilege, agent out of scope. PERMISSION_DENIED.
	ErrPermissionDenied = errors.New("permission denied")
	// ErrRegistrationClosed: Register while the mode is closed. FAILED_PRECONDITION.
	ErrRegistrationClosed = errors.New("registration is closed")
	// ErrUsernameTaken: ALREADY_EXISTS.
	ErrUsernameTaken = errors.New("username is taken")
	// ErrRateLimited: RESOURCE_EXHAUSTED.
	ErrRateLimited = errors.New("too many attempts; try again later")
	// ErrVersionConflict: an Admin write named a stale record_version. ABORTED.
	ErrVersionConflict = errors.New("record changed since it was read")
	// ErrNotFound: an Admin write named an Account that does not exist. NOT_FOUND.
	ErrNotFound = errors.New("no such account")
	// ErrInvalidArgument: a request the handler could not act on. INVALID_ARGUMENT.
	ErrInvalidArgument = errors.New("invalid argument")
	// ErrNotAuthorized is what Authorize returns: the verb's required role is
	// not held, or an agent reached outside its pack. PERMISSION_DENIED on
	// the wire (AW-SRV-003 names it as the pre-log rejection code).
	ErrNotAuthorized = errors.New("not authorized")
)

type principalKey struct{}

// WithPrincipal attaches p to ctx. The Gateway's auth interceptor does this
// for every authenticated Admin call; handlers read it back with
// PrincipalFrom.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// PrincipalFrom returns the Principal attached to ctx, if any.
func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}

type sessionKey struct{}

// WithSessionID attaches a Session correlation ID to ctx for audit records
// written on the Session's behalf.
func WithSessionID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, sessionKey{}, id)
}

// SessionIDFrom returns the Session ID attached to ctx, or "".
func SessionIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(sessionKey{}).(string)
	return id
}
