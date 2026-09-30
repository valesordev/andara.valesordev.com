// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

// Pack Grants (AW-SRV-035): the Content Packs a Builder may publish to,
// Account.builder_packs, which AW-SRV-013's publish path reads. An Operator
// replaces the whole set, as SetRoles replaces roles. Roles and packs are
// independent: a grant on an Account without builder is stored and inert, so
// revoking the role doesn't lose it.

// CorePack is the pack the server publishes itself. No Account holds a grant
// to it, Operators included (ADR-0004, 2026-09-28).
const CorePack = "andara.core"

// AccountsDomain is ErrorInfo.domain on an Account refusal that carries a
// reason.
const AccountsDomain = "andara.accounts"

// The ErrorInfo reasons SetBuilderPacks refuses with (AW-SRV-035).
const (
	ReasonOperatorOnly     = "operator_only"
	ReasonAccountNotFound  = "account_not_found"
	ReasonInvalidPackID    = "invalid_pack_id"
	ReasonCoreNotGrantable = "core_not_grantable"
	ReasonRecordVersion    = "record_version"
)

// spanAccountsWrite is the span around an Account store write (CLAUDE.md §7:
// persistence writes are trace-worthy).
const spanAccountsWrite = "accounts.write"

// ReasonError is a taxonomy error with the ErrorInfo reason a client
// switches on. errors.Is still sees the taxonomy error, so its gRPC code is
// Code's.
type ReasonError struct {
	Reason string
	Err    error
}

func (e *ReasonError) Error() string { return e.Err.Error() }
func (e *ReasonError) Unwrap() error { return e.Err }

func reasoned(reason string, err error) error { return &ReasonError{Reason: reason, Err: err} }

// packID is Content Language semantics.md §1's pack identifier: LOWER_ID
// segments joined by dots (grammar.ebnf pack_ref).
var packID = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)*$`)

// PackGrant is the result of SetBuilderPacks.
type PackGrant struct {
	Packs         []string // as stored: sorted, deduplicated
	RecordVersion uint64
	BuilderRole   bool // false: the grant is stored and inert
}

// SetBuilderPacks replaces an Account's builder_packs. Operator only. Every
// call with a Principal writes one audit record, a refusal for want of
// operator included: granting publish rights is the privileged action this
// exists to account for (AC-5).
//
// No audit record is written under wmu. A write waits up to the audit
// timeout when the broker is slow, and every Account writer (login and
// refresh included) queues on wmu. A non-operator never takes the lock at
// all, so repeated refused calls can't hold the store's writers behind a
// slow audit topic.
func (s *Store) SetBuilderPacks(ctx context.Context, accountID string, packs []string, expectedVersion uint64) (PackGrant, error) {
	actor, ok := PrincipalFrom(ctx)
	if !ok {
		return PackGrant{}, ErrUnauthenticated
	}
	if !actor.Has(RoleOperator) {
		var before []string
		if acc, found := s.lookupID(accountID); found {
			before = slices.Clone(acc.GetBuilderPacks())
		}
		return s.refusePacks(ctx, actor, accountID, before, AuditDenied, ReasonOperatorOnly,
			fmt.Errorf("%w: requires operator", ErrPermissionDenied))
	}

	g, before, outcome, reason, err := s.setBuilderPacks(ctx, accountID, packs, expectedVersion)
	switch {
	case reason != "":
		return s.refusePacks(ctx, actor, accountID, before, outcome, reason, err)
	case err != nil:
		return PackGrant{}, err // the store write failed; nothing was granted
	}
	s.audit.Record(ctx, Entry{Actor: actor, Action: ActionSetBuilderPacks, Target: accountID, Outcome: AuditOK,
		Detail: "builder packs " + packList(g.Packs), Packs: &PackAudit{Before: before, After: g.Packs}})
	s.log.LogAttrs(ctx, slog.LevelInfo, "builder packs set",
		slog.String("actor_account_id", actor.AccountID),
		slog.String("acting_as_account_id", actor.ActingAs),
		slog.String("target_account_id", accountID),
		slog.Any("before", before),
		slog.Any("after", g.Packs),
		slog.String("session_id", SessionIDFrom(ctx)),
		slog.String("trace_id", traceID(ctx)),
	)
	return g, nil
}

// setBuilderPacks is the decision and the write, under wmu. A refusal comes
// back with its audit outcome and reason for the caller to record once the
// lock is released.
func (s *Store) setBuilderPacks(ctx context.Context, accountID string, packs []string, expectedVersion uint64) (g PackGrant, before []string, outcome, reason string, err error) {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	acc, found := s.clone(accountID)
	if !found {
		return PackGrant{}, nil, AuditDenied, ReasonAccountNotFound, ErrNotFound
	}
	before = slices.Clone(acc.GetBuilderPacks())
	after, err := normalizePacks(packs)
	if err != nil {
		var re *ReasonError
		errors.As(err, &re)
		return PackGrant{}, before, AuditInvalid, re.Reason, re.Err
	}
	if err := checkVersion(acc, expectedVersion); err != nil {
		return PackGrant{}, before, AuditConflict, ReasonRecordVersion, err
	}

	acc.BuilderPacks = after
	wctx, span := s.tracer.Start(ctx, spanAccountsWrite)
	defer span.End()
	span.SetAttributes(attribute.String("action", ActionSetBuilderPacks))
	if err := s.commit(wctx, acc); err != nil {
		span.SetAttributes(attribute.String("outcome", "error"))
		span.SetStatus(codes.Error, err.Error())
		return PackGrant{}, before, "", "", err
	}
	span.SetAttributes(attribute.String("outcome", AuditOK))
	return PackGrant{
		Packs:         slices.Clone(after),
		RecordVersion: acc.GetRecordVersion(),
		BuilderRole:   slices.Contains(RolesFromProto(acc.GetRoles()), RoleBuilder),
	}, before, AuditOK, "", nil
}

// refusePacks audits and logs a refused grant, and returns its error. Called
// without wmu held.
func (s *Store) refusePacks(ctx context.Context, actor Principal, accountID string, before []string, outcome, reason string, err error) (PackGrant, error) {
	s.audit.Record(ctx, Entry{Actor: actor, Action: ActionSetBuilderPacks, Target: accountID, Outcome: outcome,
		Detail: err.Error(), Packs: &PackAudit{Before: before}})
	s.log.LogAttrs(ctx, slog.LevelWarn, "builder packs refused",
		slog.String("actor_account_id", actor.AccountID),
		slog.String("acting_as_account_id", actor.ActingAs),
		slog.String("target_account_id", accountID),
		slog.Any("before", before),
		slog.String("reason", reason),
		slog.String("session_id", SessionIDFrom(ctx)),
		slog.String("trace_id", traceID(ctx)),
	)
	return PackGrant{}, reasoned(reason, err)
}

// normalizePacks validates a requested set and returns it sorted and
// deduplicated, or the first pack ID refused, in request order.
func normalizePacks(packs []string) ([]string, error) {
	out := make([]string, 0, len(packs))
	for _, p := range packs {
		switch {
		case p == CorePack:
			return nil, reasoned(ReasonCoreNotGrantable,
				fmt.Errorf("%w: %s is published by the server and can't be granted", ErrInvalidArgument, CorePack))
		case !packID.MatchString(p):
			return nil, reasoned(ReasonInvalidPackID,
				fmt.Errorf("%w: %q is not a pack id; a pack id is lowercase segments joined by dots, each a letter then letters, digits or _", ErrInvalidArgument, p))
		}
		if !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	slices.Sort(out)
	return out, nil
}

func packList(packs []string) string {
	if len(packs) == 0 {
		return "none"
	}
	return strings.Join(packs, ", ")
}
