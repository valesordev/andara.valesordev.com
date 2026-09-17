// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package cli

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"connectrpc.com/connect"
	"go.opentelemetry.io/otel/propagation"
	"golang.org/x/net/http2"

	"github.com/valesordev/andara/gen/go/andara/admin/v1/adminv1connect"
	"github.com/valesordev/andara/gen/go/andara/auth/v1/authv1connect"
)

// The Protocol client for the commands that connect (AW-SRV-008's account
// and auth commands). Connect's generated Go client over HTTP/2 with TLS,
// trusting --tls-ca or the system store and nothing else; there is no
// insecure path here because the server has none (AW-CLI-001 AC-10).
//
// AW-CLI-004 was to choose the client library. The choice is made here
// because these commands ship first: it is the same generated Connect code
// the server and the smoke tests already use, which leaves nothing to
// choose.

// error.code values for the connected commands. Additive-only.
const (
	CodeConnect          = "connect_failed"
	CodeUnauthenticated  = "unauthenticated"
	CodePermissionDenied = "permission_denied"
	CodeServerError      = "server_error"
	CodeNotLoggedIn      = "not_logged_in"
	CodeTimeout          = "timeout"
)

var propagator = propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{})

// httpClient builds the TLS transport for the configured server.
func (rt *runtime) httpClient() (*http.Client, error) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if ca := rt.settings.TLSCA; ca != "" {
		pem, err := os.ReadFile(ca)
		if err != nil {
			return nil, &AppError{Exit: ExitUsage, Code: CodeInvalidConfig, Message: fmt.Sprintf("cannot read CA bundle %s", ca), Detail: map[string]any{"path": ca}}
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, &AppError{Exit: ExitUsage, Code: CodeInvalidConfig, Message: fmt.Sprintf("%s holds no PEM certificate", ca), Detail: map[string]any{"path": ca}}
		}
		cfg.RootCAs = pool
	}
	return &http.Client{
		Timeout:   rt.settings.Timeout,
		Transport: &http2.Transport{TLSClientConfig: cfg},
	}, nil
}

// clientOptions propagates the cli.command trace and, when a bearer token
// is given, sends it on every request.
func (rt *runtime) clientOptions(bearer string) []connect.ClientOption {
	return []connect.ClientOption{
		connect.WithGRPC(),
		connect.WithInterceptors(connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
			return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
				propagator.Inject(ctx, propagation.HeaderCarrier(req.Header()))
				if bearer != "" {
					req.Header().Set("Authorization", "Bearer "+bearer)
				}
				return next(ctx, req)
			}
		})),
	}
}

func (rt *runtime) baseURL() string { return "https://" + rt.settings.ServerAddress }

// authClient is the unauthenticated Auth service.
func (rt *runtime) authClient() (authv1connect.AuthClient, error) {
	hc, err := rt.httpClient()
	if err != nil {
		return nil, err
	}
	return authv1connect.NewAuthClient(hc, rt.baseURL(), rt.clientOptions("")...), nil
}

// adminClient is Admin with the stored session token as bearer. A missing
// credential is a usage error: nothing was attempted.
func (rt *runtime) adminClient() (adminv1connect.AdminClient, error) {
	cred, err := rt.loadCredential()
	if err != nil {
		return nil, err
	}
	if cred == nil {
		return nil, &AppError{Exit: ExitUsage, Code: CodeNotLoggedIn,
			Message: fmt.Sprintf("no credential for %s; run `andara-cli auth login`", rt.settings.ServerAddress),
			Detail:  map[string]any{"server": rt.settings.ServerAddress}}
	}
	hc, err := rt.httpClient()
	if err != nil {
		return nil, err
	}
	return adminv1connect.NewAdminClient(hc, rt.baseURL(), rt.clientOptions(cred.SessionToken)...), nil
}

// callCtx bounds one RPC by --timeout and carries the command span.
func (rt *runtime) callCtx() (context.Context, context.CancelFunc) {
	ctx := rt.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(ctx, rt.settings.Timeout)
}

// rpcError maps a failed RPC onto the exit-code contract: the server not
// reached is 3, a timeout is 4, a refused or failed operation is 1. The
// server's message is passed through; it never carries a credential.
func rpcError(err error) error {
	var ae *AppError
	if errors.As(err, &ae) {
		return err
	}
	if errors.Is(err, context.DeadlineExceeded) || connect.CodeOf(err) == connect.CodeDeadlineExceeded {
		return &AppError{Exit: ExitTimeout, Code: CodeTimeout, Message: "the server did not answer within --timeout", Detail: map[string]any{}}
	}
	var ce *connect.Error
	if errors.As(err, &ce) {
		detail := map[string]any{"grpc_code": ce.Code().String()}
		switch ce.Code() {
		case connect.CodeUnauthenticated:
			return &AppError{Exit: ExitConnect, Code: CodeUnauthenticated, Message: ce.Message(), Detail: detail}
		case connect.CodePermissionDenied:
			return &AppError{Exit: ExitFail, Code: CodePermissionDenied, Message: ce.Message(), Detail: detail}
		case connect.CodeUnavailable:
			return &AppError{Exit: ExitConnect, Code: CodeConnect, Message: ce.Message(), Detail: detail}
		}
		return &AppError{Exit: ExitFail, Code: CodeServerError, Message: ce.Message(), Detail: detail}
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return &AppError{Exit: ExitTimeout, Code: CodeTimeout, Message: "the server did not answer within --timeout", Detail: map[string]any{}}
	}
	return &AppError{Exit: ExitConnect, Code: CodeConnect, Message: "cannot reach the server: " + firstLine(err.Error()), Detail: map[string]any{}}
}

func unixTime(sec int64) string { return time.Unix(sec, 0).UTC().Format(time.RFC3339) }
