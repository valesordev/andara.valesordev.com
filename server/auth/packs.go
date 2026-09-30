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
func (s *Store) SetBuilderPacks(ctx context.Context, accountID string, packs []string, expectedVersion uint64) (PackGrant, error) {
	actor, ok := PrincipalFrom(ctx)
	if !ok {
		return PackGrant{}, ErrUnauthenticated
	}

	s.wmu.Lock()
	defer s.wmu.Unlock()
	acc, found := s.clone(accountID)
	var before []string
	if found {
		before = slices.Clone(acc.GetBuilderPacks())
	}
	refuse := func(outcome, reason string, err error) (PackGrant, error) {
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

	if !actor.Has(RoleOperator) {
		return refuse(AuditDenied, ReasonOperatorOnly, fmt.Errorf("%w: requires operator", ErrPermissionDenied))
	}
	if !found {
		return refuse(AuditDenied, ReasonAccountNotFound, ErrNotFound)
	}
	after, err := normalizePacks(packs)
	if err != nil {
		var re *ReasonError
		errors.As(err, &re)
		return refuse(AuditInvalid, re.Reason, re.Err)
	}
	if err := checkVersion(acc, expectedVersion); err != nil {
		return refuse(AuditConflict, ReasonRecordVersion, err)
	}

	acc.BuilderPacks = after
	wctx, span := s.tracer.Start(ctx, spanAccountsWrite)
	span.SetAttributes(attribute.String("action", ActionSetBuilderPacks))
	if err := s.commit(wctx, acc); err != nil {
		span.SetAttributes(attribute.String("outcome", "error"))
		span.SetStatus(codes.Error, err.Error())
		span.End()
		return PackGrant{}, err
	}
	span.SetAttributes(attribute.String("outcome", AuditOK))
	span.End()

	s.audit.Record(ctx, Entry{Actor: actor, Action: ActionSetBuilderPacks, Target: accountID, Outcome: AuditOK,
		Detail: "builder packs " + packList(after), Packs: &PackAudit{Before: before, After: after}})
	s.log.LogAttrs(ctx, slog.LevelInfo, "builder packs set",
		slog.String("actor_account_id", actor.AccountID),
		slog.String("acting_as_account_id", actor.ActingAs),
		slog.String("target_account_id", accountID),
		slog.Any("before", before),
		slog.Any("after", after),
		slog.String("session_id", SessionIDFrom(ctx)),
		slog.String("trace_id", traceID(ctx)),
	)
	return PackGrant{
		Packs:         slices.Clone(after),
		RecordVersion: acc.GetRecordVersion(),
		BuilderRole:   slices.Contains(RolesFromProto(acc.GetRoles()), RoleBuilder),
	}, nil
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
