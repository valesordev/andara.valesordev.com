// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package gateway

import (
	"context"
	"errors"
	"io"

	"connectrpc.com/connect"
	"google.golang.org/genproto/googleapis/rpc/errdetails"

	adminv1 "github.com/valesordev/andara/gen/go/andara/admin/v1"
	"github.com/valesordev/andara/server/content"
)

// AdminReadMaxBytes is the most one Admin message may be, whatever
// grpc.max_recv_bytes says: a HasBlobs at its 10,000-hash maximum is about
// 340 KB, and a PublishBlob chunk may be 1 MiB (AW-SRV-013;
// docs/feedback/AW-SRV-013-publish-path.md, For architecture 1).
// grpc.max_recv_bytes keeps guarding Game.
const AdminReadMaxBytes = 2 << 20

// ContentAdmin is the content publish half of Admin (AW-SRV-013).
// content.Admin implements it. The caller's Principal is on ctx.
type ContentAdmin interface {
	HasBlobs(context.Context, *adminv1.HasBlobsRequest) (*adminv1.HasBlobsResponse, error)
	PublishBlob(context.Context, content.BlobStream) (*adminv1.PublishBlobResponse, error)
	PublishVersion(context.Context, *adminv1.PublishVersionRequest) (*adminv1.PublishVersionResponse, error)
	ApproveVersion(context.Context, *adminv1.ApproveVersionRequest) (*adminv1.ApproveVersionResponse, error)
	ActivateVersion(context.Context, *adminv1.ActivateVersionRequest) (*adminv1.ActivateVersionResponse, error)
	ListVersions(context.Context, *adminv1.ListVersionsRequest) (*adminv1.ListVersionsResponse, error)
	GetVersion(context.Context, *adminv1.GetVersionRequest) (*adminv1.GetVersionResponse, error)
	GetBlob(context.Context, *adminv1.GetBlobRequest, func(*adminv1.GetBlobResponse) error) error
	ReloadContent(context.Context, *adminv1.ReloadContentRequest) (*adminv1.ReloadContentResponse, error)
}

var errNoContentAdmin = errors.New("content publishing is not configured on this server; it needs content.source=kafka")

// contentCall runs one unary ContentAdmin call.
func contentCall[Req, Resp any](ctx context.Context, a *adminService, req *connect.Request[Req], call func(ContentAdmin, context.Context, *Req) (*Resp, error)) (*connect.Response[Resp], error) {
	if a.s.opts.ContentAdmin == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errNoContentAdmin)
	}
	resp, err := call(a.s.opts.ContentAdmin, ctx, req.Msg)
	if err != nil {
		return nil, contentError(err)
	}
	return connect.NewResponse(resp), nil
}

func (a *adminService) HasBlobs(ctx context.Context, req *connect.Request[adminv1.HasBlobsRequest]) (*connect.Response[adminv1.HasBlobsResponse], error) {
	return contentCall(ctx, a, req, ContentAdmin.HasBlobs)
}

func (a *adminService) PublishVersion(ctx context.Context, req *connect.Request[adminv1.PublishVersionRequest]) (*connect.Response[adminv1.PublishVersionResponse], error) {
	return contentCall(ctx, a, req, ContentAdmin.PublishVersion)
}

func (a *adminService) ApproveVersion(ctx context.Context, req *connect.Request[adminv1.ApproveVersionRequest]) (*connect.Response[adminv1.ApproveVersionResponse], error) {
	return contentCall(ctx, a, req, ContentAdmin.ApproveVersion)
}

func (a *adminService) ActivateVersion(ctx context.Context, req *connect.Request[adminv1.ActivateVersionRequest]) (*connect.Response[adminv1.ActivateVersionResponse], error) {
	return contentCall(ctx, a, req, ContentAdmin.ActivateVersion)
}

func (a *adminService) ListVersions(ctx context.Context, req *connect.Request[adminv1.ListVersionsRequest]) (*connect.Response[adminv1.ListVersionsResponse], error) {
	return contentCall(ctx, a, req, ContentAdmin.ListVersions)
}

func (a *adminService) GetVersion(ctx context.Context, req *connect.Request[adminv1.GetVersionRequest]) (*connect.Response[adminv1.GetVersionResponse], error) {
	return contentCall(ctx, a, req, ContentAdmin.GetVersion)
}

func (a *adminService) ReloadContent(ctx context.Context, req *connect.Request[adminv1.ReloadContentRequest]) (*connect.Response[adminv1.ReloadContentResponse], error) {
	return contentCall(ctx, a, req, ContentAdmin.ReloadContent)
}

// blobStream adapts connect's client stream to content.BlobStream.
type blobStream struct {
	s *connect.ClientStream[adminv1.PublishBlobRequest]
}

func (b blobStream) Receive() (*adminv1.PublishBlobRequest, error) {
	if b.s.Receive() {
		return b.s.Msg(), nil
	}
	if err := b.s.Err(); err != nil {
		return nil, err
	}
	return nil, io.EOF
}

func (a *adminService) PublishBlob(ctx context.Context, stream *connect.ClientStream[adminv1.PublishBlobRequest]) (*connect.Response[adminv1.PublishBlobResponse], error) {
	if a.s.opts.ContentAdmin == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errNoContentAdmin)
	}
	resp, err := a.s.opts.ContentAdmin.PublishBlob(ctx, blobStream{stream})
	if err != nil {
		return nil, contentError(err)
	}
	return connect.NewResponse(resp), nil
}

func (a *adminService) GetBlob(ctx context.Context, req *connect.Request[adminv1.GetBlobRequest], stream *connect.ServerStream[adminv1.GetBlobResponse]) error {
	if a.s.opts.ContentAdmin == nil {
		return connect.NewError(connect.CodeUnimplemented, errNoContentAdmin)
	}
	return contentError(a.s.opts.ContentAdmin.GetBlob(ctx, req.Msg, stream.Send))
}

// contentError puts a publish-path refusal on the wire: its code, an
// ErrorInfo in the andara.content domain naming the reason, and the status
// detail it carries. Anything else takes the gateway's usual mapping.
func contentError(err error) error {
	if err == nil {
		return nil
	}
	var ae *content.AdminError
	if !errors.As(err, &ae) {
		return connectError(err)
	}
	ce := connect.NewError(contentCode(ae.Code), ae)
	if ae.Reason != "" {
		if d, derr := connect.NewErrorDetail(&errdetails.ErrorInfo{Domain: content.ErrorDomain, Reason: ae.Reason}); derr == nil {
			ce.AddDetail(d)
		}
	}
	if ae.Detail != nil {
		if d, derr := connect.NewErrorDetail(ae.Detail); derr == nil {
			ce.AddDetail(d)
		}
	}
	return ce
}

func contentCode(c content.Code) connect.Code {
	switch c {
	case content.CodeInvalidArgument:
		return connect.CodeInvalidArgument
	case content.CodePermissionDenied:
		return connect.CodePermissionDenied
	case content.CodeFailedPrecondition:
		return connect.CodeFailedPrecondition
	case content.CodeResourceExhausted:
		return connect.CodeResourceExhausted
	case content.CodeNotFound:
		return connect.CodeNotFound
	case content.CodeUnauthenticated:
		return connect.CodeUnauthenticated
	case content.CodeUnavailable:
		return connect.CodeUnavailable
	}
	return connect.CodeInternal
}
