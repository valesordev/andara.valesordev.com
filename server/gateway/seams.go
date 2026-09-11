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
