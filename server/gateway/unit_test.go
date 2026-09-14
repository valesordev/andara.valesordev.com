// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package gateway

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	gamev1 "github.com/valesordev/andara/gen/go/andara/game/v1"
	"github.com/valesordev/andara/internal/testpki"
)

// Version negotiation across the boundary values of the range.
func TestNegotiate(t *testing.T) {
	cases := []struct {
		client, min, max uint32
		want             uint32
		ok               bool
	}{
		{1, 1, 1, 1, true},
		{0, 1, 1, 0, false}, // proto3 default: the client never set it
		{2, 1, 1, 0, false},
		{99, 1, 1, 0, false},
		{2, 2, 5, 2, true},
		{5, 2, 5, 5, true},
		{3, 2, 5, 3, true},
		{1, 2, 5, 0, false},
		{6, 2, 5, 0, false},
		{^uint32(0), 1, ^uint32(0), ^uint32(0), true},
	}
	for _, tc := range cases {
		got, err := Negotiate(tc.client, tc.min, tc.max)
		if tc.ok {
			if err != nil || got != tc.want {
				t.Errorf("Negotiate(%d, %d, %d) = %d, %v; want %d", tc.client, tc.min, tc.max, got, err, tc.want)
			}
			continue
		}
		if err == nil {
			t.Errorf("Negotiate(%d, %d, %d) accepted; want rejection", tc.client, tc.min, tc.max)
			continue
		}
		var ve *VersionError
		if !errors.As(err, &ve) || !errors.Is(err, ErrVersionUnsupported) {
			t.Errorf("Negotiate(%d, %d, %d) error %T is not a VersionError", tc.client, tc.min, tc.max, err)
			continue
		}
		if ve.Client != tc.client || ve.Min != tc.min || ve.Max != tc.max {
			t.Errorf("VersionError = %+v", ve)
		}
	}
}

// Every row of the error taxonomy maps to its gRPC code, and an unmapped
// error is INTERNAL rather than something more reassuring.
func TestCodeOf_EveryRow(t *testing.T) {
	rows := map[error]connect.Code{
		ErrVersionUnsupported:                    connect.CodeFailedPrecondition,
		&VersionError{Client: 9, Min: 1, Max: 1}: connect.CodeFailedPrecondition,
		ErrUnauthenticated:                       connect.CodeUnauthenticated,
		ErrPermissionDenied:                      connect.CodePermissionDenied,
		ErrMessageTooLarge:                       connect.CodeResourceExhausted,
		ErrDraining:                              connect.CodeUnavailable,
		errors.New("something else"):             connect.CodeInternal,
	}
	for err, want := range rows {
		if got := codeOf(err); got != want {
			t.Errorf("codeOf(%v) = %s, want %s", err, got, want)
		}
		if got := connect.CodeOf(connectError(err)); got != want {
			t.Errorf("connectError(%v) carries %s, want %s", err, got, want)
		}
	}
	// A *connect.Error passes through untouched.
	already := connect.NewError(connect.CodeNotFound, errors.New("x"))
	if connectError(already) != already {
		t.Error("connectError rewrapped a *connect.Error")
	}
	if connectError(nil) != nil {
		t.Error("connectError(nil) != nil")
	}
}

func TestVersionError_NamesBoth(t *testing.T) {
	ve := &VersionError{Client: 99, Min: 1, Max: 3}
	msg := ve.Error()
	for _, want := range []string{"99", "1..3"} {
		if !strings.Contains(msg, want) {
			t.Errorf("%q does not name %q", msg, want)
		}
	}
	ce := connectVersionError(ve)
	var cerr *connect.Error
	if !errors.As(ce, &cerr) || cerr.Code() != connect.CodeFailedPrecondition {
		t.Fatalf("connectVersionError = %v", ce)
	}
	if len(cerr.Details()) != 1 {
		t.Fatalf("details = %d, want 1", len(cerr.Details()))
	}
}

func TestBearerToken(t *testing.T) {
	for _, h := range []string{"", "Bearer", "Bearer ", "Basic abc", "bearer"} {
		if tok, err := bearerToken(h); err == nil {
			t.Errorf("bearerToken(%q) = %q, want error", h, tok)
		}
	}
	for _, h := range []string{"Bearer abc", "bearer abc", "BEARER abc "} {
		if tok, err := bearerToken(h); err != nil || tok != "abc" {
			t.Errorf("bearerToken(%q) = %q, %v", h, tok, err)
		}
	}
}

func TestNew_RequiresVerifier(t *testing.T) {
	pki := testpki.New(t)
	opts := defaultOptions(pki)
	opts.Verifier = nil
	if _, err := New(opts); err == nil {
		t.Fatal("New accepted a nil Verifier; there is no accept-anything mode after AW-SRV-008")
	}
}

type fakeRequest struct {
	connect.AnyRequest
	spec   connect.Spec
	header http.Header
}

func (f fakeRequest) Spec() connect.Spec  { return f.spec }
func (f fakeRequest) Header() http.Header { return f.header }
func (f fakeRequest) Peer() connect.Peer  { return connect.Peer{Addr: "test"} }
func (f fakeRequest) Any() any            { return nil }
func (f fakeRequest) HTTPMethod() string  { return http.MethodPost }

// AC-10 at the unit: the auth interceptor returns before next is called.
func TestAuthInterceptor_RejectsBeforeHandler(t *testing.T) {
	ic := &authInterceptor{verifier: acceptAnyVerifier{}}
	reached := false
	next := func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		reached = true
		return nil, nil
	}
	admin := connect.Spec{Procedure: "/andara.admin.v1.Admin/GetServerInfo"}

	_, err := ic.WrapUnary(next)(context.Background(), fakeRequest{spec: admin, header: http.Header{}})
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("no header: %v", err)
	}
	_, err = ic.WrapUnary(next)(context.Background(), fakeRequest{spec: admin, header: http.Header{"Authorization": {"Basic x"}}})
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("wrong scheme: %v", err)
	}
	if reached {
		t.Fatal("handler was reached by an unauthenticated Admin call")
	}

	_, err = ic.WrapUnary(next)(context.Background(), fakeRequest{spec: admin, header: http.Header{"Authorization": {"Bearer ok"}}})
	if err != nil || !reached {
		t.Errorf("authenticated call: err=%v reached=%v", err, reached)
	}

	// Game methods are not gated by the header; OpenSession carries its
	// token in the body and the rest are authenticated by Session ID.
	reached = false
	game := connect.Spec{Procedure: "/andara.game.v1.Game/OpenSession"}
	if _, err := ic.WrapUnary(next)(context.Background(), fakeRequest{spec: game, header: http.Header{}}); err != nil || !reached {
		t.Errorf("game call: err=%v reached=%v", err, reached)
	}
}

// A verifier that says "known but not permitted" maps to PERMISSION_DENIED,
// the taxonomy's third row; the interceptor does not flatten it.
type denyingVerifier struct{}

func (denyingVerifier) Verify(context.Context, string) (Principal, error) {
	return Principal{}, ErrPermissionDenied
}

func (denyingVerifier) ActAs(context.Context, Principal, string) (Principal, error) {
	return Principal{}, ErrPermissionDenied
}

func TestAuthInterceptor_PermissionDenied(t *testing.T) {
	ic := &authInterceptor{verifier: denyingVerifier{}}
	next := func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		t.Fatal("reached handler")
		return nil, nil
	}
	admin := connect.Spec{Procedure: "/andara.admin.v1.Admin/GetServerInfo"}
	_, err := ic.WrapUnary(next)(context.Background(), fakeRequest{spec: admin, header: http.Header{"Authorization": {"Bearer x"}}})
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("got %v", err)
	}
}

// AC-11 at the unit: a unary call with no deadline gets the maximum, and a
// deadline beyond the maximum is pulled in. One inside it is left alone.
func TestDeadlineInterceptor(t *testing.T) {
	const max = 250 * time.Millisecond
	ic := &deadlineInterceptor{max: max}
	var seen time.Time
	var ok bool
	next := func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		seen, ok = ctx.Deadline()
		return nil, nil
	}
	req := fakeRequest{spec: connect.Spec{Procedure: "/andara.game.v1.Game/Submit"}, header: http.Header{}}

	start := time.Now()
	_, _ = ic.WrapUnary(next)(context.Background(), req)
	if !ok {
		t.Fatal("no deadline applied")
	}
	if until := seen.Sub(start); until > max+50*time.Millisecond || until < max/2 {
		t.Errorf("applied deadline %s from now, want about %s", until, max)
	}

	far, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	_, _ = ic.WrapUnary(next)(far, req)
	if until := time.Until(seen); until > max+50*time.Millisecond {
		t.Errorf("hour-long deadline was not clamped: %s", until)
	}

	near, cancel2 := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel2()
	_, _ = ic.WrapUnary(next)(near, req)
	if until := time.Until(seen); until > 60*time.Millisecond {
		t.Errorf("near deadline was extended: %s", until)
	}
}

func TestJoinContexts_DetachesOnCancel(t *testing.T) {
	long, stopLong := context.WithCancel(context.Background())
	defer stopLong()
	ctx, cancel := joinContexts(context.Background(), long)
	cancel()
	if ctx.Err() == nil {
		t.Fatal("joined context not canceled")
	}
	// Canceling `long` afterwards must not panic or matter.
	stopLong()

	// The other direction: a done input ends the joined context.
	other, stopOther := context.WithCancel(context.Background())
	ctx2, cancel2 := joinContexts(context.Background(), other)
	defer cancel2()
	stopOther()
	select {
	case <-ctx2.Done():
	case <-time.After(time.Second):
		t.Fatal("joined context did not follow its input")
	}
}

func TestNew_RequiresTLS(t *testing.T) {
	pki := testpki.New(t)
	opts := defaultOptions(pki)
	opts.TLSCertFile = ""
	if _, err := New(opts); err == nil {
		t.Fatal("New accepted a configuration without a certificate")
	}
	opts = defaultOptions(pki)
	opts.TLSKeyFile = pki.CertFile // a certificate where a key should be
	if _, err := New(opts); err == nil {
		t.Fatal("New accepted a bad key pair")
	}
	opts = defaultOptions(pki)
	opts.ProtocolMin = 0
	if _, err := New(opts); err == nil {
		t.Fatal("New accepted protocol.min_version 0")
	}
}

func TestUnimplementedIngress(t *testing.T) {
	_, err := (UnimplementedIngress{}).Submit(context.Background(), &Session{}, &gamev1.SubmitRequest{})
	if connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Errorf("got %v", err)
	}
}
