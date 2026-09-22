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
	// the record may be in the log. DEADLINE_EXCEEDED. The client retries
	// with the same client_ref and gets the original outcome once the
	// record's fate is known (AW-SRV-031); the idempotent producer makes
	// only the ingress's own retries safe.
	ErrDeadline = errors.New("produce deadline exceeded; outcome unknown")
	// ErrDuplicateClientRef: the Session reused a client_ref inside the
	// idempotency window for a different Intent. INVALID_ARGUMENT; nothing
	// produced. A client bug, answered as one rather than with the first
	// Intent's outcome (AW-SRV-031).
	ErrDuplicateClientRef = errors.New("client_ref already used for a different command")
	// ErrNotWritten is an Unsettled record's fate when no produce request
	// left the process after it was enqueued: nothing carrying it can have
	// reached a broker. Never crosses the wire; the ingress reads it.
	ErrNotWritten = errors.New("record dropped before any produce request left the process")
	// ErrOutcomeUnknown is an Unsettled record's fate when a produce
	// request left the process after it was enqueued and the promise
	// failed anyway: the record may be in the log and the client that held
	// its idempotent retry is gone, so nothing will ever say. DEADLINE_EXCEEDED
	// with reason outcome_unknown — terminal for the client, unlike
	// produce_deadline, which it retries: the player is told the World may
	// or may not have taken the command, and looks (AW-SRV-031).
	ErrOutcomeUnknown = errors.New("outcome unknown: the command may be in the log and its fate will not be known")
)

// Unsettled is the error Produce returns when the caller's wait ended —
// the produce deadline, or the caller's own context — with the record
// still live in the client: ErrDeadline (or the context's error) on the
// surface, and underneath, a future for the record's fate. The promise
// still fires, so the fate is always known within the client's delivery
// timeout: landed, with Outcome's Accepted; not written, ErrNotWritten;
// or ErrDeadline again, when a request carrying it may have reached a
// broker and the client that held its retry is gone (AW-SRV-031).
type Unsettled struct {
	cause error
	done  chan struct{}
	acc   command.Accepted
	err   error
}

func newUnsettled(cause error) *Unsettled {
	return &Unsettled{cause: cause, done: make(chan struct{})}
}

func (u *Unsettled) Error() string { return u.cause.Error() }

// Unwrap is the surface error, so errors.Is(u, ErrDeadline) holds.
func (u *Unsettled) Unwrap() error { return u.cause }

// Settled is closed once the record's fate is known.
func (u *Unsettled) Settled() <-chan struct{} { return u.done }

// Outcome is the fate; valid after Settled is closed.
func (u *Unsettled) Outcome() (command.Accepted, error) { return u.acc, u.err }

func (u *Unsettled) settle(acc command.Accepted, err error) {
	u.acc, u.err = acc, err
	close(u.done)
}

// Reasons, the ErrorInfo.reason a client switches on.
const (
	ReasonRateLimited = "rate_limited"
	ReasonPendingFull = "pending_full"
	ReasonReadOnly    = "world_read_only"
	ReasonDeadline    = "produce_deadline"
	// ReasonDuplicateClientRef: the reused client_ref (AW-SRV-031).
	ReasonDuplicateClientRef = "duplicate_client_ref"
	// ReasonOutcomeUnknown: the settled-unknown fate, terminal (AW-SRV-031).
	ReasonOutcomeUnknown = "outcome_unknown"
	// ErrorDomain is ErrorInfo.domain for every ingress error.
	ErrorDomain = "andara.command"
)

// ReadOnlyMessage is what a player reads when the World is read-only: the
// one message every player eventually sees. Brian accepted the wording on
// 2026-09-19 (AW-SRV-010).
const ReadOnlyMessage = "The world is read-only for a moment: your command was not taken. Try it again shortly."

// readOnly is ErrUnavailable's face on the wire: the player's message,
// not the broker's error.
type readOnly struct{}

func (readOnly) Error() string { return ReadOnlyMessage }

// retryAfter is the RetryInfo on UNAVAILABLE: what a well-behaved client
// waits before trying again.
const retryAfter = time.Second

// WireError maps an ingress or pipeline error to the wire for a caller
// outside this package that produces through the same log — the roster's
// SelectCharacter (AW-SRV-014), whose produce failures are answered as
// Submit's are.
func WireError(err error) error { return connectError(err) }

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
	case errors.Is(err, ErrOutcomeUnknown):
		return withInfo(connect.NewError(connect.CodeDeadlineExceeded, err), ReasonOutcomeUnknown, nil)
	case errors.Is(err, ErrDuplicateClientRef):
		return withInfo(connect.NewError(connect.CodeInvalidArgument, err), ReasonDuplicateClientRef, nil)
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
	case errors.Is(err, ErrDuplicateClientRef):
		return OutcomeRejectedRef
	case errors.Is(err, ErrDeadline), errors.Is(err, ErrOutcomeUnknown), errors.Is(err, context.DeadlineExceeded):
		return OutcomeDeadline
	case errors.Is(err, context.Canceled):
		return OutcomeCanceled
	}
	return OutcomeInternal
}
