// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package gateway

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	gamev1 "github.com/valesordev/andara/gen/go/andara/game/v1"
)

// Ingress is what Game.Submit plugs into. AW-SRV-010 implements parse,
// authorize, and the produce to andara.commands.v1 behind it. The gateway
// has already resolved the Session and bounded the request by the time
// this is called; the Intent is otherwise untouched.
type Ingress interface {
	Submit(ctx context.Context, s *Session, req *gamev1.SubmitRequest) (*gamev1.SubmitResponse, error)
}

// Egress is what Game.Subscribe plugs into. AW-SRV-011 implements
// perception-scoped delivery, buffering, resume, and heartbeats behind it.
//
// ctx is done when the client goes away, the Session ends, or the server
// drains — whichever comes first. An implementation returns when ctx is
// done; the gateway maps the reason to the typed error the client sees.
type Egress interface {
	Subscribe(ctx context.Context, s *Session, req *gamev1.SubscribeRequest, stream *connect.ServerStream[gamev1.EventEnvelope]) error
}

// SessionEnder is what an Egress may also implement: told, before the
// Session's context is canceled, that the Session is being closed for a
// reason the client did not choose, so its stream can end with a final
// frame rather than a bare cancellation — SubscriberDropped{reason=revoked}
// for AW-SRV-008 AC-12. The call is bounded by the implementation.
type SessionEnder interface {
	EndSession(sessionID, reason string)
}

// Roster is what the Character RPCs plug into (AW-SRV-014): the Account's
// Characters, and the binding of one to the Session. The gateway has
// resolved the Session by the time any of these is called; the one-live
// rule, the cap, and the name rule live behind the seam.
//
// ReleaseSession is the teardown: told, before the Session's context is
// canceled, that the Session is ending and how (AW-SRV-015), so the
// Character it drives leaves the World (EndQuit: an UnbindCharacter, the
// live flag released) or waits out its grace (EndLinkdead: a MarkLinkdead,
// the flag kept for the reconnect). It must not block on the log; the
// produce runs on its own context, and the returned channel is closed when
// it has finished — what CloseSession waits on before it answers.
type Roster interface {
	ListCharacters(ctx context.Context, s *Session) (*gamev1.ListCharactersResponse, error)
	CreateCharacter(ctx context.Context, s *Session, name string) (*gamev1.CreateCharacterResponse, error)
	SelectCharacter(ctx context.Context, s *Session, characterID string) (*gamev1.SelectCharacterResponse, error)
	ReleaseSession(s *Session, end SessionEnd) <-chan struct{}
}

// SessionEnd is how a Session's end treats the Character it drives
// (AW-SRV-015's teardown table).
type SessionEnd int

const (
	// EndQuit: CloseSession, or a revoked credential. The body leaves the
	// World now.
	EndQuit SessionEnd = iota
	// EndLinkdead: the stream was lost — a transport close, a keepalive
	// miss — or the server is draining. The body stays for the grace.
	EndLinkdead
)

// String names the end for logs.
func (e SessionEnd) String() string {
	if e == EndLinkdead {
		return "linkdead"
	}
	return "quit"
}

// closed is a channel already closed: a teardown with nothing to wait for.
var closed = func() <-chan struct{} { c := make(chan struct{}); close(c); return c }()

// UnimplementedRoster is the roster seam when none is wired: the RPCs are
// refused with UNIMPLEMENTED, and a Session's end frees nothing because
// nothing was bound.
type UnimplementedRoster struct{}

// ListCharacters returns UNIMPLEMENTED.
func (UnimplementedRoster) ListCharacters(context.Context, *Session) (*gamev1.ListCharactersResponse, error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errRosterPending)
}

// CreateCharacter returns UNIMPLEMENTED.
func (UnimplementedRoster) CreateCharacter(context.Context, *Session, string) (*gamev1.CreateCharacterResponse, error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errRosterPending)
}

// SelectCharacter returns UNIMPLEMENTED.
func (UnimplementedRoster) SelectCharacter(context.Context, *Session, string) (*gamev1.SelectCharacterResponse, error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errRosterPending)
}

// ReleaseSession does nothing.
func (UnimplementedRoster) ReleaseSession(*Session, SessionEnd) <-chan struct{} { return closed }

var errRosterPending = errors.New("no character roster is wired into this gateway")

// UnimplementedIngress is the Submit seam before AW-SRV-010: the RPC is
// accepted by the gateway, then refused with UNIMPLEMENTED so a client
// learns what is missing rather than what is broken.
type UnimplementedIngress struct{}

// Submit returns UNIMPLEMENTED.
func (UnimplementedIngress) Submit(context.Context, *Session, *gamev1.SubmitRequest) (*gamev1.SubmitResponse, error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errIngressPending)
}

// HoldingEgress is the Subscribe seam before AW-SRV-011. It delivers no
// Events — there is no source of them yet — and holds the stream open until
// the Session ends or the server drains, so that stream lifecycle and drain
// are exercised end to end today (AC-8) rather than the first time there
// is something to stream.
type HoldingEgress struct{}

// Subscribe blocks until ctx is done.
func (HoldingEgress) Subscribe(ctx context.Context, _ *Session, _ *gamev1.SubscribeRequest, _ *connect.ServerStream[gamev1.EventEnvelope]) error {
	<-ctx.Done()
	return nil
}

var errIngressPending = errors.New("command ingress is AW-SRV-010; this build accepts the RPC and does not produce")
