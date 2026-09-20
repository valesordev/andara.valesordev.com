// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package egress

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	"google.golang.org/genproto/googleapis/rpc/errdetails"

	"github.com/valesordev/andara/server/events"
)

// The stream error taxonomy (AW-SRV-011). Each crosses the wire as a
// gRPC code carrying an ErrorInfo whose reason is the typed code, so a
// client switches on the reason rather than the message. A stream that
// ends for one of these is one the client must reopen — with
// last_event_id, so the server can say whether it may resume.
var (
	// ErrBufferFull: the client trailed by more than egress.buffer and the
	// stream was ended rather than the Session's memory growing or an
	// Event being skipped. RESOURCE_EXHAUSTED. A client that reads too
	// slowly for the Room it is in sees this repeatedly.
	ErrBufferFull = errors.New("event stream buffer full; resubscribe")
	// ErrAlreadySubscribed: the Session has an open stream. One per
	// Session; a client that wants a new one ends the old one first.
	// FAILED_PRECONDITION.
	ErrAlreadySubscribed = errors.New("session already has an event stream")
	// ErrTooManySubscribers: events.max_subscribers reached.
	// RESOURCE_EXHAUSTED.
	ErrTooManySubscribers = errors.New("too many event subscriptions")
	// ErrNotPrivileged: world visibility asked for without game_master or
	// operator. PERMISSION_DENIED.
	ErrNotPrivileged = errors.New("world visibility requires game_master or operator")
	// ErrDraining: the fan-out has shut down. UNAVAILABLE.
	ErrDraining = errors.New("event stream closed: server draining")
)

// Reasons, the ErrorInfo.reason a client switches on.
const (
	ReasonAlreadySubscribed  = "already_subscribed"
	ReasonTooManySubscribers = "too_many_subscribers"
	ReasonNotPrivileged      = "world_visibility"
	// ErrorDomain is ErrorInfo.domain for every stream error.
	ErrorDomain = "andara.stream"
)

// connectError maps a stream error to the wire. A *connect.Error passes
// through. Anything outside the taxonomy is INTERNAL: an unmapped error
// is a bug.
func connectError(err error) error {
	if err == nil {
		return nil
	}
	var ce *connect.Error
	if errors.As(err, &ce) {
		return err
	}
	switch {
	case errors.Is(err, ErrBufferFull):
		return withInfo(connect.NewError(connect.CodeResourceExhausted, err), ReasonBufferFull)
	case errors.Is(err, ErrAlreadySubscribed):
		return withInfo(connect.NewError(connect.CodeFailedPrecondition, err), ReasonAlreadySubscribed)
	case errors.Is(err, ErrTooManySubscribers), errors.Is(err, events.ErrTooManySubscribers):
		return withInfo(connect.NewError(connect.CodeResourceExhausted, ErrTooManySubscribers), ReasonTooManySubscribers)
	case errors.Is(err, ErrNotPrivileged), errors.Is(err, events.ErrNotPrivileged):
		return withInfo(connect.NewError(connect.CodePermissionDenied, ErrNotPrivileged), ReasonNotPrivileged)
	case errors.Is(err, ErrDraining), errors.Is(err, events.ErrClosed):
		return withInfo(connect.NewError(connect.CodeUnavailable, ErrDraining), ReasonDraining)
	case errors.Is(err, context.Canceled):
		return connect.NewError(connect.CodeCanceled, err)
	case errors.Is(err, context.DeadlineExceeded):
		return connect.NewError(connect.CodeDeadlineExceeded, err)
	}
	return connect.NewError(connect.CodeInternal, err)
}

func withInfo(ce *connect.Error, reason string) *connect.Error {
	if d, err := connect.NewErrorDetail(&errdetails.ErrorInfo{Reason: reason, Domain: ErrorDomain}); err == nil {
		ce.AddDetail(d)
	}
	return ce
}
