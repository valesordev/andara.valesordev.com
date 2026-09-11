// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package gateway

import (
	"errors"
	"fmt"

	"connectrpc.com/connect"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
)

// The error taxonomy from AW-SRV-005's interface contract, as sentinel errors
// so that a test can assert the mapping row by row. Every one crosses the
// wire as the gRPC code in the table, via connectError, so generated clients
// behave correctly without special-casing.
var (
	// ErrVersionUnsupported: the client's Protocol version is outside the
	// server's range. FAILED_PRECONDITION, detail names both.
	ErrVersionUnsupported = errors.New("protocol version unsupported")
	// ErrUnauthenticated: missing or invalid token, or an unknown Session.
	// UNAUTHENTICATED.
	ErrUnauthenticated = errors.New("unauthenticated")
	// ErrPermissionDenied: authenticated but not permitted. PERMISSION_DENIED.
	// Nothing raises it until AW-SRV-008 gives a Principal permissions; it is
	// in the taxonomy so the mapping is settled before it is needed.
	ErrPermissionDenied = errors.New("permission denied")
	// ErrMessageTooLarge: a message over grpc.max_recv_bytes. RESOURCE_EXHAUSTED.
	// The Connect runtime raises its own error for this before any handler
	// runs; this sentinel exists so the mapping is testable in one place.
	ErrMessageTooLarge = errors.New("message too large")
	// ErrDraining: the server is shutting down. UNAVAILABLE, retryable.
	ErrDraining = errors.New("server draining")
)

// codeOf maps a taxonomy error to its gRPC code. Anything outside the
// taxonomy is INTERNAL: an unmapped error is a bug, and reporting it as
// anything more specific would hide that.
func codeOf(err error) connect.Code {
	switch {
	case errors.Is(err, ErrVersionUnsupported):
		return connect.CodeFailedPrecondition
	case errors.Is(err, ErrUnauthenticated):
		return connect.CodeUnauthenticated
	case errors.Is(err, ErrPermissionDenied):
		return connect.CodePermissionDenied
	case errors.Is(err, ErrMessageTooLarge):
		return connect.CodeResourceExhausted
	case errors.Is(err, ErrDraining):
		return connect.CodeUnavailable
	}
	return connect.CodeInternal
}

// connectError wraps a taxonomy error for the wire. A *connect.Error passes
// through unchanged so a handler that already chose a code is not
// second-guessed.
func connectError(err error) error {
	if err == nil {
		return nil
	}
	var ce *connect.Error
	if errors.As(err, &ce) {
		return err
	}
	return connect.NewError(codeOf(err), err)
}

// VersionError is ErrVersionUnsupported carrying the numbers a client needs
// to report what would have worked without a second round trip.
type VersionError struct {
	Client, Min, Max uint32
}

func (e *VersionError) Error() string {
	return fmt.Sprintf("protocol version %d is outside the supported range %d..%d", e.Client, e.Min, e.Max)
}

// Unwrap makes errors.Is(err, ErrVersionUnsupported) true.
func (e *VersionError) Unwrap() error { return ErrVersionUnsupported }

// connectVersionError builds the FAILED_PRECONDITION the contract requires:
// the message names both ranges, and a PreconditionFailure detail carries
// them in the standard rich-error form generated clients in every language
// can decode.
func connectVersionError(e *VersionError) error {
	ce := connect.NewError(connect.CodeFailedPrecondition, e)
	detail, err := connect.NewErrorDetail(&errdetails.PreconditionFailure{
		Violations: []*errdetails.PreconditionFailure_Violation{{
			Type:        "PROTOCOL_VERSION",
			Subject:     fmt.Sprintf("client=%d", e.Client),
			Description: fmt.Sprintf("server supports %d..%d", e.Min, e.Max),
		}},
	})
	if err == nil {
		ce.AddDetail(detail)
	}
	return ce
}
