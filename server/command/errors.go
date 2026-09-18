// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package command

import (
	"errors"

	"github.com/valesordev/andara/server/auth"
	"github.com/valesordev/andara/server/sim"
)

// Stage names a pipeline stage, pre-log or post-log. It is the `stage`
// label on andara_command_rejected_total, so the set is closed.
type Stage string

// The five stages. Emit never rejects; it is listed so the pipeline is the
// one CLAUDE.md §10 names.
const (
	StageParse     Stage = "parse"
	StageAuthorize Stage = "authorize"
	StageValidate  Stage = sim.StageValidate
	StageApply     Stage = sim.StageApply
	StageEmit      Stage = "emit"
)

// PreLog reports whether the stage runs before the produce.
func (s Stage) PreLog() bool { return s == StageParse || s == StageAuthorize }

// The pre-log rejection codes. Stable, snake_case, additive-only, the same
// taxonomy CommandRejected.code continues post-log (sim.RejectCodes).
const (
	// CodeUnknownVerb: the first token is not in the verb table, or is an
	// abbreviation two verbs claim.
	CodeUnknownVerb = "unknown_verb"
	// CodeMissingArgument: the verb binds an argument the Intent lacks.
	CodeMissingArgument = "missing_argument"
	// CodeInvalidArgument: an argument is present and not one of the values
	// its kind admits — `move frobnicate`. Rejected here rather than as
	// no_such_exit after the log, because the log is retained and the
	// story's reason for the split is to keep garbage out of it.
	CodeInvalidArgument = "invalid_argument"
	// CodeIntentTooLarge: the raw text exceeds command.max_intent_bytes.
	CodeIntentTooLarge = "intent_too_large"
	// CodeNotAuthorized: the Principal lacks the verb's role, or the
	// Session is bound to no Character (auth.ErrNotAuthorized).
	CodeNotAuthorized = "not_authorized"
)

// PreLogCodes is every pre-log code by stage, for metric pre-seeding.
var PreLogCodes = map[Stage][]string{
	StageParse:     {CodeUnknownVerb, CodeMissingArgument, CodeInvalidArgument, CodeIntentTooLarge},
	StageAuthorize: {CodeNotAuthorized},
}

// Error is a pre-log rejection: which stage, which code, and a detail the
// player may read. Nothing about it reached the log.
type Error struct {
	Stage  Stage
	Code   string
	Detail string
	// Arg names the argument for missing_argument and invalid_argument.
	Arg string
	err error
}

func (e *Error) Error() string { return string(e.Stage) + ": " + e.Code + ": " + e.Detail }

// Unwrap exposes the cause: auth.ErrNotAuthorized for not_authorized.
func (e *Error) Unwrap() error { return e.err }

// Is matches another *Error by code, so errors.Is(err, ErrUnknownVerb)
// reads the way the story writes it.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.Code == e.Code
}

// Sentinels, one per code.
var (
	ErrUnknownVerb     = &Error{Stage: StageParse, Code: CodeUnknownVerb}
	ErrMissingArgument = &Error{Stage: StageParse, Code: CodeMissingArgument}
	ErrInvalidArgument = &Error{Stage: StageParse, Code: CodeInvalidArgument}
	ErrIntentTooLarge  = &Error{Stage: StageParse, Code: CodeIntentTooLarge}
	ErrNotAuthorized   = &Error{Stage: StageAuthorize, Code: CodeNotAuthorized, err: auth.ErrNotAuthorized}
)

// AsError returns the *Error in err's chain, if any.
func AsError(err error) (*Error, bool) {
	var e *Error
	return e, errors.As(err, &e)
}
