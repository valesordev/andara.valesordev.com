// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package ingress

import (
	"context"
	"errors"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/valesordev/andara/server/command"
)

// The ingress error taxonomy (AW-SRV-010). Each crosses the wire as the
// gRPC code in the story's table, carrying an ErrorInfo whose reason is
// the typed code, so a client reads the code rather than the message.
var (
	// ErrRateLimited: the Session exceeded ingress.rate_limit.
	// RESOURCE_EXHAUSTED; nothing produced.
	ErrRateLimited = errors.New("rate limited")
	// ErrPendingFull: the Session has ingress.max_pending Submits in
	// flight, or the process's produce buffer is full. RESOURCE_EXHAUSTED.
	ErrPendingFull = errors.New("too many commands pending")
	// ErrUnavailable: the Command log is unreachable; the World is
	// read-only. UNAVAILABLE, retryable; nothing produced.
	ErrUnavailable = errors.New("command log unreachable")
	// ErrDeadline: the produce deadline passed with the outcome unknown —
	// the record may be in the log. DEADLINE_EXCEEDED. The client should
	// retry; the idempotent producer makes the ingress's own retries safe,
	// and a client retry after this is a new Command.
	ErrDeadline = errors.New("produce deadline exceeded; outcome unknown")
)

// Reasons, the ErrorInfo.reason a client switches on.
const (
	ReasonRateLimited = "rate_limited"
	ReasonPendingFull = "pending_full"
	ReasonReadOnly    = "world_read_only"
	ReasonDeadline    = "produce_deadline"
	// ErrorDomain is ErrorInfo.domain for every ingress error.
	ErrorDomain = "andara.command"
)

// ReadOnlyMessage is what a player reads when the World is read-only.
// PLACEHOLDER: the wording is Brian's call (AW-SRV-010 open question) and
// this is the one message every player eventually sees.
const ReadOnlyMessage = "The world is read-only for a moment: your command was not taken. Try it again shortly."

// readOnly is ErrUnavailable's face on the wire: the player's message,
// not the broker's error.
type readOnly struct{}

func (readOnly) Error() string { return ReadOnlyMessage }

// retryAfter is the RetryInfo on UNAVAILABLE: what a well-behaved client
// waits before trying again.
const retryAfter = time.Second

// connectError maps an ingress or pipeline error to the wire. A
// *connect.Error passes through. Anything outside the taxonomy is
// INTERNAL: an unmapped error is a bug.
func connectError(err error) error {
	if err == nil {
		return nil
	}
	var ce *connect.Error
	if errors.As(err, &ce) {
		return err
	}
	if e, ok := command.AsError(err); ok {
		return pipelineError(e)
	}
	switch {
	case errors.Is(err, ErrRateLimited):
		return withInfo(connect.NewError(connect.CodeResourceExhausted, err), ReasonRateLimited, nil)
	case errors.Is(err, ErrPendingFull):
		return withInfo(connect.NewError(connect.CodeResourceExhausted, err), ReasonPendingFull, nil)
	case errors.Is(err, ErrUnavailable):
		ce := withInfo(connect.NewError(connect.CodeUnavailable, readOnly{}), ReasonReadOnly, nil)
		if d, derr := connect.NewErrorDetail(&errdetails.RetryInfo{RetryDelay: durationpb.New(retryAfter)}); derr == nil {
			ce.AddDetail(d)
		}
		return ce
	case errors.Is(err, ErrDeadline):
		return withInfo(connect.NewError(connect.CodeDeadlineExceeded, err), ReasonDeadline, nil)
	case errors.Is(err, context.DeadlineExceeded):
		return connect.NewError(connect.CodeDeadlineExceeded, err)
	case errors.Is(err, context.Canceled):
		return connect.NewError(connect.CodeCanceled, err)
	}
	return connect.NewError(connect.CodeInternal, err)
}

// pipelineError maps a pre-log rejection: parse → INVALID_ARGUMENT,
// authorize → PERMISSION_DENIED, except in_transit, which is UNAVAILABLE —
// the Character will arrive or be restored, and the player should retry.
func pipelineError(e *command.Error) *connect.Error {
	code := connect.CodeInternal
	switch {
	case e.Stage == command.StageParse:
		code = connect.CodeInvalidArgument
	case e.Code == command.CodeInTransit:
		code = connect.CodeUnavailable
	case e.Stage == command.StageAuthorize:
		code = connect.CodePermissionDenied
	}
	meta := map[string]string{"stage": string(e.Stage), "pre_log": "true"}
	if e.Arg != "" {
		meta["arg"] = e.Arg
	}
	return withInfo(connect.NewError(code, errors.New(e.Detail)), e.Code, meta)
}

func withInfo(ce *connect.Error, reason string, meta map[string]string) *connect.Error {
	if d, err := connect.NewErrorDetail(&errdetails.ErrorInfo{Reason: reason, Domain: ErrorDomain, Metadata: meta}); err == nil {
		ce.AddDetail(d)
	}
	return ce
}

// outcomeOf names the metric outcome for an error, or produced for nil.
func outcomeOf(err error) string {
	if err == nil {
		return OutcomeProduced
	}
	if e, ok := command.AsError(err); ok {
		switch {
		case e.Stage == command.StageParse:
			return OutcomeRejectedParse
		case e.Code == command.CodeInTransit:
			return OutcomeInTransit
		default:
			return OutcomeRejectedAuthz
		}
	}
	switch {
	case errors.Is(err, ErrRateLimited):
		return OutcomeRateLimited
	case errors.Is(err, ErrPendingFull):
		return OutcomePendingFull
	case errors.Is(err, ErrUnavailable):
		return OutcomeUnavailable
	case errors.Is(err, ErrDeadline), errors.Is(err, context.DeadlineExceeded):
		return OutcomeDeadline
	case errors.Is(err, context.Canceled):
		return OutcomeCanceled
	}
	return OutcomeInternal
}
