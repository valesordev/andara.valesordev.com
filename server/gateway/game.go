// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package gateway

import (
	"context"
	"errors"
	"log/slog"

	"connectrpc.com/connect"

	gamev1 "github.com/valesordev/andara/gen/go/andara/game/v1"
	"github.com/valesordev/andara/gen/go/andara/game/v1/gamev1connect"
	"github.com/valesordev/andara/server/auth"
)

// gameService implements andara.game.v1.Game from the generated interface.
// Only Session establishment and teardown have behavior here; Submit and
// Subscribe resolve the Session, bound the call, and hand off to the seams.
type gameService struct {
	gamev1connect.UnimplementedGameHandler
	s *Server
}

func (g *gameService) OpenSession(ctx context.Context, req *connect.Request[gamev1.OpenSessionRequest]) (*connect.Response[gamev1.OpenSessionResponse], error) {
	msg := req.Msg
	minV, maxV := g.s.opts.ProtocolMin, g.s.opts.ProtocolMax

	// Version before token: a client on the wrong Protocol is told so even
	// when its credential is also wrong, because the version is what it can
	// fix on its own.
	negotiated, err := Negotiate(msg.GetProtocolVersion(), minV, maxV)
	if err != nil {
		var ve *VersionError
		if errors.As(err, &ve) {
			g.s.metrics.SessionsTotal.WithLabelValues(OutcomeRejectedVersion).Inc()
			g.s.log.LogAttrs(ctx, slog.LevelWarn, "session rejected: protocol version outside supported range",
				slog.Uint64("client_version", uint64(ve.Client)),
				slog.Uint64("server_min_version", uint64(ve.Min)),
				slog.Uint64("server_max_version", uint64(ve.Max)),
				slog.String("client_name", msg.GetClientName()),
				slog.String("remote_addr", req.Peer().Addr),
				slog.String("trace_id", traceID(ctx)),
			)
			return nil, connectVersionError(ve)
		}
		return nil, connectError(err)
	}

	// The token is passed to the verifier and nowhere else. It is not on the
	// Session, not in a span, and not in a log line at any level.
	principal, err := g.s.opts.Verifier.Verify(ctx, msg.GetAuthToken())
	if err != nil {
		g.s.metrics.SessionsTotal.WithLabelValues(OutcomeRejectedAuth).Inc()
		g.s.log.LogAttrs(ctx, slog.LevelInfo, "session rejected: token not accepted",
			slog.String("client_name", msg.GetClientName()),
			slog.String("remote_addr", req.Peer().Addr),
			slog.String("trace_id", traceID(ctx)),
		)
		return nil, connectError(ErrUnauthenticated)
	}
	// Acting as another Account (AW-SRV-008 AC-10) is a privilege check on
	// the verified Principal; the verifier audits it either way.
	if target := msg.GetActAsAccountId(); target != "" {
		principal, err = g.s.opts.Verifier.ActAs(ctx, principal, target)
		if err != nil {
			g.s.metrics.SessionsTotal.WithLabelValues(OutcomeRejectedAuth).Inc()
			g.s.log.LogAttrs(ctx, slog.LevelInfo, "session rejected: act-as refused",
				slog.String("client_name", msg.GetClientName()),
				slog.String("remote_addr", req.Peer().Addr),
				slog.String("trace_id", traceID(ctx)),
			)
			return nil, connectError(ErrPermissionDenied)
		}
	}

	sess, err := g.s.sessions.open(ctx, connIDFrom(ctx), msg.GetClientName(), negotiated, req.Peer().Addr, principal)
	if err != nil {
		// The connection is already gone; the code is for the record.
		return nil, connect.NewError(connect.CodeCanceled, err)
	}
	return connect.NewResponse(&gamev1.OpenSessionResponse{
		SessionId:         sess.ID,
		NegotiatedVersion: negotiated,
		ServerMinVersion:  minV,
		ServerMaxVersion:  maxV,
	}), nil
}

// resolve turns a session_id into a Session or the UNAUTHENTICATED the
// taxonomy prescribes for an invalid credential — which is what an unknown
// Session ID is.
func (g *gameService) resolve(id string) (*Session, error) {
	sess, ok := g.s.sessions.get(id)
	if !ok {
		return nil, connectError(ErrUnauthenticated)
	}
	return sess, nil
}

func (g *gameService) Submit(ctx context.Context, req *connect.Request[gamev1.SubmitRequest]) (*connect.Response[gamev1.SubmitResponse], error) {
	sess, err := g.resolve(req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	ctx, cancel := joinContexts(ctx, sess.Context(), g.s.drainCtx)
	defer cancel()
	ctx = auth.WithSessionID(ctx, sess.ID)
	resp, err := g.s.opts.Ingress.Submit(ctx, sess, req.Msg)
	if err != nil {
		return nil, g.s.mapSeamError(ctx, sess, err)
	}
	return connect.NewResponse(resp), nil
}

func (g *gameService) Subscribe(ctx context.Context, req *connect.Request[gamev1.SubscribeRequest], stream *connect.ServerStream[gamev1.EventEnvelope]) error {
	sess, err := g.resolve(req.Msg.GetSessionId())
	if err != nil {
		return err
	}
	ctx, cancel := joinContexts(ctx, sess.Context(), g.s.drainCtx)
	defer cancel()
	// Flush the response headers now, before there is anything to send.
	// Connect writes them with the first message, and a client's Subscribe
	// call does not return until they arrive — so a quiet world would look
	// like a stream that never opened. A nil send is Connect's way to say
	// "headers only".
	if err := stream.Send(nil); err != nil {
		return err
	}
	err = g.s.opts.Egress.Subscribe(ctx, sess, req.Msg, stream)
	return g.s.mapSeamError(ctx, sess, err)
}

func (g *gameService) CloseSession(ctx context.Context, req *connect.Request[gamev1.CloseSessionRequest]) (*connect.Response[gamev1.CloseSessionResponse], error) {
	sess, err := g.resolve(req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	g.s.sessions.close(ctx, sess, OutcomeClosed, "closed by client")
	return connect.NewResponse(&gamev1.CloseSessionResponse{}), nil
}

// The roster RPCs (AW-SRV-014): resolve the Session, bound the call, hand
// off. The seam chooses every code; the gateway adds only what it knows —
// drain, and a Session that ended under the call.

func (g *gameService) ListCharacters(ctx context.Context, req *connect.Request[gamev1.ListCharactersRequest]) (*connect.Response[gamev1.ListCharactersResponse], error) {
	sess, err := g.resolve(req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	ctx, cancel := joinContexts(ctx, sess.Context(), g.s.drainCtx)
	defer cancel()
	resp, err := g.s.opts.Roster.ListCharacters(auth.WithSessionID(ctx, sess.ID), sess)
	if err != nil {
		return nil, g.s.mapSeamError(ctx, sess, err)
	}
	return connect.NewResponse(resp), nil
}

func (g *gameService) CreateCharacter(ctx context.Context, req *connect.Request[gamev1.CreateCharacterRequest]) (*connect.Response[gamev1.CreateCharacterResponse], error) {
	sess, err := g.resolve(req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	ctx, cancel := joinContexts(ctx, sess.Context(), g.s.drainCtx)
	defer cancel()
	resp, err := g.s.opts.Roster.CreateCharacter(auth.WithSessionID(ctx, sess.ID), sess, req.Msg.GetName())
	if err != nil {
		return nil, g.s.mapSeamError(ctx, sess, err)
	}
	return connect.NewResponse(resp), nil
}

func (g *gameService) SelectCharacter(ctx context.Context, req *connect.Request[gamev1.SelectCharacterRequest]) (*connect.Response[gamev1.SelectCharacterResponse], error) {
	sess, err := g.resolve(req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	ctx, cancel := joinContexts(ctx, sess.Context(), g.s.drainCtx)
	defer cancel()
	resp, err := g.s.opts.Roster.SelectCharacter(auth.WithSessionID(ctx, sess.ID), sess, req.Msg.GetCharacterId())
	if err != nil {
		return nil, g.s.mapSeamError(ctx, sess, err)
	}
	return connect.NewResponse(resp), nil
}

// mapSeamError names why a seam call ended when the cause was the gateway
// rather than the seam: drain and Session teardown are typed here so that
// Ingress and Egress implementations do not each invent a code for them.
// A code the seam chose itself, other than a cancellation, stands even
// when the Session has since closed: a stream ended revoked, or a Submit
// refused, is what happened, and the teardown that follows is not.
func (s *Server) mapSeamError(ctx context.Context, sess *Session, err error) error {
	var ce *connect.Error
	typed := errors.As(err, &ce) && ce.Code() != connect.CodeCanceled
	switch {
	case s.drainCtx.Err() != nil:
		return connectError(ErrDraining)
	case typed:
		return err
	case sess.Context().Err() != nil:
		return connect.NewError(connect.CodeCanceled, errSessionClosed)
	case err == nil:
		return nil
	case errors.Is(err, context.Canceled) && ctx.Err() != nil:
		// The client went away; there is nobody to send a code to.
		return connect.NewError(connect.CodeCanceled, err)
	}
	return connectError(err)
}

var errSessionClosed = errors.New("session closed")

// joinContexts derives a context that is done when any of its inputs is.
// The first is the parent, so values and the deadline carry through. The
// returned cancel also detaches from the other contexts: drainCtx lives for
// the process, and a registration left on it per RPC would be the leak AC-7
// forbids.
func joinContexts(parent context.Context, others ...context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	stops := make([]func() bool, 0, len(others))
	for _, o := range others {
		stops = append(stops, context.AfterFunc(o, cancel))
	}
	return ctx, func() {
		cancel()
		for _, stop := range stops {
			stop()
		}
	}
}
