// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package gateway

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/genproto/googleapis/rpc/errdetails"

	adminv1 "github.com/valesordev/andara/gen/go/andara/admin/v1"
	"github.com/valesordev/andara/gen/go/andara/admin/v1/adminv1connect"
	contentv1 "github.com/valesordev/andara/gen/go/andara/content/v1"
	"github.com/valesordev/andara/server/auth"
	"github.com/valesordev/andara/server/content"
)

// fakeContent is the publish path with scripted answers: what it asserts is
// the wire, not the logic, which server/content's tests own.
type fakeContent struct {
	received []byte
	caller   auth.Principal
}

func (f *fakeContent) HasBlobs(ctx context.Context, req *adminv1.HasBlobsRequest) (*adminv1.HasBlobsResponse, error) {
	f.caller, _ = auth.PrincipalFrom(ctx)
	return &adminv1.HasBlobsResponse{Present: make([]bool, len(req.GetHashes()))}, nil
}

func (f *fakeContent) PublishBlob(_ context.Context, s content.BlobStream) (*adminv1.PublishBlobResponse, error) {
	for {
		m, err := s.Receive()
		if errors.Is(err, io.EOF) {
			return &adminv1.PublishBlobResponse{Deduplicated: true}, nil
		}
		if err != nil {
			return nil, err
		}
		f.received = append(f.received, m.GetData()...)
	}
}

func (f *fakeContent) PublishVersion(context.Context, *adminv1.PublishVersionRequest) (*adminv1.PublishVersionResponse, error) {
	return nil, &content.AdminError{Code: content.CodeInvalidArgument, Reason: content.ErrReasonValidation,
		Detail: &adminv1.PublishFindings{Findings: []*contentv1.Diagnostic{{File: "town.json", Code: "unknown_room", Severity: contentv1.Severity_ERROR}}},
		Err:    errors.New("town refused")}
}

func (f *fakeContent) ApproveVersion(context.Context, *adminv1.ApproveVersionRequest) (*adminv1.ApproveVersionResponse, error) {
	return nil, &content.AdminError{Code: content.CodePermissionDenied, Reason: content.ErrReasonSelfApproval, Err: errors.New("yours")}
}

func (f *fakeContent) ActivateVersion(context.Context, *adminv1.ActivateVersionRequest) (*adminv1.ActivateVersionResponse, error) {
	return nil, &content.AdminError{Code: content.CodeFailedPrecondition, Reason: content.RefusalZoneRemoved,
		Detail: &adminv1.ActivationRefusal{Reason: content.RefusalZoneRemoved, Subjects: []string{"docks"}}, Err: errors.New("docks")}
}

func (f *fakeContent) ListVersions(context.Context, *adminv1.ListVersionsRequest) (*adminv1.ListVersionsResponse, error) {
	return &adminv1.ListVersionsResponse{ActiveVersion: 7}, nil
}

func (f *fakeContent) GetVersion(context.Context, *adminv1.GetVersionRequest) (*adminv1.GetVersionResponse, error) {
	return nil, &content.AdminError{Code: content.CodeNotFound, Reason: content.ErrReasonNotFound, Err: errors.New("no")}
}

func (f *fakeContent) GetBlob(_ context.Context, _ *adminv1.GetBlobRequest, send func(*adminv1.GetBlobResponse) error) error {
	for range 3 {
		if err := send(&adminv1.GetBlobResponse{Data: bytes.Repeat([]byte("x"), content.BlobChunkBytes)}); err != nil {
			return err
		}
	}
	return nil
}

func (f *fakeContent) ReloadContent(context.Context, *adminv1.ReloadContentRequest) (*adminv1.ReloadContentResponse, error) {
	return &adminv1.ReloadContentResponse{}, nil
}

// bearer sets the Authorization header on every request, streams included.
type bearer struct{ next http.RoundTripper }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer test-token")
	return b.next.RoundTrip(r)
}

func contentClient(t *testing.T, fc *fakeContent) adminv1connect.AdminClient {
	t.Helper()
	h := start(t, func(o *Options) {
		o.ContentAdmin = fc
		o.Verifier = &roleVerifier{roles: []auth.Role{auth.RoleBuilder}}
	})
	c, tr := h.httpClient()
	c.Transport = bearer{tr}
	return adminv1connect.NewAdminClient(c, h.baseURL())
}

func errorInfo(t *testing.T, err error) (*connect.Error, *errdetails.ErrorInfo) {
	t.Helper()
	var ce *connect.Error
	if !errors.As(err, &ce) {
		t.Fatalf("not a connect error: %v", err)
	}
	for _, d := range ce.Details() {
		v, derr := d.Value()
		if ei, ok := v.(*errdetails.ErrorInfo); derr == nil && ok {
			return ce, ei
		}
	}
	t.Fatalf("%v carries no ErrorInfo", err)
	return nil, nil
}

func detail[T any](t *testing.T, ce *connect.Error) T {
	t.Helper()
	for _, d := range ce.Details() {
		if v, err := d.Value(); err == nil {
			if x, ok := v.(T); ok {
				return x
			}
		}
	}
	var zero T
	t.Fatalf("%v carries no %T", ce, zero)
	return zero
}

// Every publish-path refusal crosses the wire with its code, an ErrorInfo in
// the andara.content domain naming the contract's reason, and its detail.
func TestContentAdmin_RefusalsCrossTheWireWithReasonAndDetail(t *testing.T) {
	admin := contentClient(t, &fakeContent{})
	ctx := context.Background()

	_, err := admin.PublishVersion(ctx, connect.NewRequest(&adminv1.PublishVersionRequest{PackId: "town"}))
	ce, ei := errorInfo(t, err)
	if ce.Code() != connect.CodeInvalidArgument || ei.GetDomain() != content.ErrorDomain || ei.GetReason() != "validation" {
		t.Fatalf("PublishVersion: %v %v", ce.Code(), ei)
	}
	if pf := detail[*adminv1.PublishFindings](t, ce); pf.GetFindings()[0].GetCode() != "unknown_room" {
		t.Fatalf("findings %v", pf)
	}

	_, err = admin.ActivateVersion(ctx, connect.NewRequest(&adminv1.ActivateVersionRequest{PackId: "town", Version: 2}))
	ce, ei = errorInfo(t, err)
	if ce.Code() != connect.CodeFailedPrecondition || ei.GetReason() != "zone_removed" {
		t.Fatalf("ActivateVersion: %v %v", ce.Code(), ei)
	}
	if ar := detail[*adminv1.ActivationRefusal](t, ce); ar.GetSubjects()[0] != "docks" {
		t.Fatalf("refusal %v", ar)
	}

	for _, tc := range []struct {
		call   func() error
		code   connect.Code
		reason string
	}{
		{func() error {
			_, err := admin.ApproveVersion(ctx, connect.NewRequest(&adminv1.ApproveVersionRequest{}))
			return err
		}, connect.CodePermissionDenied, "self_approval"},
		{func() error {
			_, err := admin.GetVersion(ctx, connect.NewRequest(&adminv1.GetVersionRequest{}))
			return err
		}, connect.CodeNotFound, "not_found"},
	} {
		ce, ei := errorInfo(t, tc.call())
		if ce.Code() != tc.code || ei.GetReason() != tc.reason {
			t.Errorf("%v %v, want %v %s", ce.Code(), ei.GetReason(), tc.code, tc.reason)
		}
	}
}

// PublishBlob and GetBlob stream at the 1 MiB chunk the contract bounds, which
// the Admin handler reads whatever grpc.max_recv_bytes (64 KiB here) says;
// and a HasBlobs at its 10,000-hash maximum is read too.
func TestContentAdmin_StreamsAndLargeMessagesPassAdminsReadLimit(t *testing.T) {
	fc := &fakeContent{}
	admin := contentClient(t, fc)
	ctx := context.Background()

	stream := admin.PublishBlob(ctx)
	if err := stream.Send(&adminv1.PublishBlobRequest{Chunk: &adminv1.PublishBlobRequest_Header{Header: &adminv1.PublishBlobHeader{PackId: "town"}}}); err != nil {
		t.Fatal(err)
	}
	chunk := bytes.Repeat([]byte("y"), content.BlobChunkBytes)
	for range 3 {
		if err := stream.Send(&adminv1.PublishBlobRequest{Chunk: &adminv1.PublishBlobRequest_Data{Data: chunk}}); err != nil {
			t.Fatal(err)
		}
	}
	if resp, err := stream.CloseAndReceive(); err != nil || !resp.Msg.GetDeduplicated() {
		t.Fatalf("PublishBlob: %v %v", resp, err)
	}
	if len(fc.received) != 3*content.BlobChunkBytes {
		t.Fatalf("received %d bytes", len(fc.received))
	}

	hashes := make([][]byte, content.MaxHasBlobs)
	for i := range hashes {
		hashes[i] = bytes.Repeat([]byte{byte(i)}, 32)
	}
	if resp, err := admin.HasBlobs(ctx, connect.NewRequest(&adminv1.HasBlobsRequest{PackId: "town", Hashes: hashes})); err != nil || len(resp.Msg.GetPresent()) != content.MaxHasBlobs {
		t.Fatalf("HasBlobs at its maximum: %v", err)
	}
	if fc.caller.AccountID != "acct-test-token" {
		t.Errorf("the Principal on ctx is %+v", fc.caller)
	}

	gb, err := admin.GetBlob(ctx, connect.NewRequest(&adminv1.GetBlobRequest{PackId: "town"}))
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for gb.Receive() {
		n += len(gb.Msg().GetData())
	}
	if err := gb.Err(); err != nil || n != 3*content.BlobChunkBytes {
		t.Fatalf("GetBlob: %d bytes, %v", n, err)
	}
}

// A dir server has no store: every content RPC is UNIMPLEMENTED.
func TestContentAdmin_UnwiredIsUnimplemented(t *testing.T) {
	h := start(t, func(o *Options) { o.Verifier = &roleVerifier{roles: []auth.Role{auth.RoleOperator}} })
	c, tr := h.httpClient()
	c.Transport = bearer{tr}
	admin := adminv1connect.NewAdminClient(c, h.baseURL())
	if _, err := admin.ListVersions(context.Background(), connect.NewRequest(&adminv1.ListVersionsRequest{})); connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Fatalf("ListVersions: %v", err)
	}
}
