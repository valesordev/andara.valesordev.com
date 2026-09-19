// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package gateway

import (
	"context"
	"errors"
	"strings"
	"time"

	"connectrpc.com/connect"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// The interceptor chain, outermost first:
//
//	metrics → trace → draining → deadline → auth → handler
//
// Metrics is outermost so that every rejection below it is counted with the
// code it returned. Trace is next so that a rejection is a span too. Draining
// precedes auth because a draining server should not spend a verifier call
// on a request it will refuse. Deadline precedes auth for the same reason a
// verifier that reaches the network must be bounded.
func (s *Server) interceptors() connect.HandlerOption {
	return connect.WithInterceptors(
		&metricsInterceptor{m: s.metrics},
		&traceInterceptor{tracer: s.tracer, trust: s.opts.TrustInboundTraceparent},
		&drainInterceptor{s: s},
		&deadlineInterceptor{max: s.opts.MaxRequestTimeout},
		&authInterceptor{verifier: s.opts.Verifier},
	)
}

func methodLabel(spec connect.Spec) string {
	return strings.TrimPrefix(spec.Procedure, "/")
}

func codeLabel(err error) string {
	if err == nil {
		return "ok"
	}
	return connect.CodeOf(err).String()
}

// --- metrics ---------------------------------------------------------------

type metricsInterceptor struct{ m *Metrics }

func (i *metricsInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		start := time.Now()
		resp, err := next(ctx, req)
		i.observe(req.Spec(), start, err)
		return resp, err
	}
}

func (i *metricsInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (i *metricsInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		start := time.Now()
		err := next(ctx, conn)
		i.observe(conn.Spec(), start, err)
		return err
	}
}

func (i *metricsInterceptor) observe(spec connect.Spec, start time.Time, err error) {
	method := methodLabel(spec)
	i.m.RequestsTotal.WithLabelValues(method, codeLabel(err)).Inc()
	i.m.RequestDuration.WithLabelValues(method).Observe(time.Since(start).Seconds())
}

// --- trace -----------------------------------------------------------------

// traceInterceptor starts the server's span for each RPC. With trust, the
// incoming W3C traceparent is its parent, so andara-cli's cli.command span
// (AW-CLI-001) is the root and this RPC is under it — and the client's
// sampled flag is the decision. Without it the RPC span is a new root
// that links to the client's context: correlated, but sampled here.
type traceInterceptor struct {
	tracer trace.Tracer
	trust  bool
}

var propagator = propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{})

func (i *traceInterceptor) start(ctx context.Context, spec connect.Spec, header propagation.TextMapCarrier) (context.Context, trace.Span) {
	method := methodLabel(spec)
	service, rpc, _ := strings.Cut(method, "/")
	opts := []trace.SpanStartOption{
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("rpc.system", "connect_rpc"),
			attribute.String("rpc.service", service),
			attribute.String("rpc.method", rpc),
		),
	}
	inbound := propagator.Extract(context.Background(), header)
	if i.trust {
		ctx = propagator.Extract(ctx, header)
	} else if sc := trace.SpanContextFromContext(inbound); sc.IsValid() {
		opts = append(opts, trace.WithLinks(trace.Link{SpanContext: sc}))
	}
	return i.tracer.Start(ctx, method, opts...)
}

func end(span trace.Span, err error) {
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		span.SetAttributes(attribute.String("rpc.grpc.status_code", connect.CodeOf(err).String()))
	} else {
		span.SetAttributes(attribute.String("rpc.grpc.status_code", "ok"))
	}
	span.End()
}

func (i *traceInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		ctx, span := i.start(ctx, req.Spec(), propagation.HeaderCarrier(req.Header()))
		resp, err := next(ctx, req)
		end(span, err)
		return resp, err
	}
}

func (i *traceInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (i *traceInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		ctx, span := i.start(ctx, conn.Spec(), propagation.HeaderCarrier(conn.RequestHeader()))
		err := next(ctx, conn)
		end(span, err)
		return err
	}
}

// --- draining --------------------------------------------------------------

// drainInterceptor refuses new RPCs once Shutdown has begun. UNAVAILABLE is
// the retryable code, so a well-behaved client moves to another replica
// rather than reporting an error to the player.
type drainInterceptor struct{ s *Server }

func (i *drainInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if i.s.draining.Load() {
			return nil, connectError(ErrDraining)
		}
		return next(ctx, req)
	}
}

func (i *drainInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (i *drainInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		if i.s.draining.Load() {
			return connectError(ErrDraining)
		}
		return next(ctx, conn)
	}
}

// --- deadline --------------------------------------------------------------

// deadlineInterceptor bounds every unary RPC by grpc.max_request_timeout: a
// request with no deadline gets one, and a deadline further out than the
// maximum is pulled in (AC-11). Streams are not bounded here — a Subscribe
// stream is meant to live for the Session, and its bound is the Session's
// lifetime and drain, not a wall clock.
type deadlineInterceptor struct{ max time.Duration }

func (i *deadlineInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if i.max <= 0 {
			return next(ctx, req)
		}
		if dl, ok := ctx.Deadline(); !ok || time.Until(dl) > i.max {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, i.max)
			defer cancel()
		}
		return next(ctx, req)
	}
}

func (i *deadlineInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (i *deadlineInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

// --- auth ------------------------------------------------------------------

const adminPrefix = "/andara.admin.v1.Admin/"

// authInterceptor rejects an unauthenticated call to any Admin method before
// it reaches a handler (AC-10). Admin request messages carry no token field,
// so the credential travels as `Authorization: Bearer <token>` metadata.
// Game methods are not gated here: OpenSession carries its token in the
// request body and the others are authenticated by Session ID in the handler.
type authInterceptor struct{ verifier TokenVerifier }

func (i *authInterceptor) authenticate(ctx context.Context, spec connect.Spec, authorization string) (context.Context, error) {
	if !strings.HasPrefix(spec.Procedure, adminPrefix) {
		return ctx, nil
	}
	tok, err := bearerToken(authorization)
	if err != nil {
		return ctx, connectError(ErrUnauthenticated)
	}
	p, err := i.verifier.Verify(ctx, tok)
	if err != nil {
		if errors.Is(err, ErrPermissionDenied) {
			return ctx, connectError(ErrPermissionDenied)
		}
		return ctx, connectError(ErrUnauthenticated)
	}
	return withPrincipal(ctx, p), nil
}

func (i *authInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		ctx, err := i.authenticate(ctx, req.Spec(), req.Header().Get("Authorization"))
		if err != nil {
			return nil, err
		}
		return next(ctx, req)
	}
}

func (i *authInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (i *authInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		ctx, err := i.authenticate(ctx, conn.Spec(), conn.RequestHeader().Get("Authorization"))
		if err != nil {
			return err
		}
		return next(ctx, conn)
	}
}
