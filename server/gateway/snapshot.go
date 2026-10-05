// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package gateway

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"

	adminv1 "github.com/valesordev/andara/gen/go/andara/admin/v1"
	"github.com/valesordev/andara/server/auth"
)

// SnapshotAdmin is the Snapshot Round half of Admin (AW-SRV-007). Both
// methods read the store the server is configured with and never touch the
// live Engine. A refusal that names its status (the NOT_FOUND of a tick with
// no object, the FAILED_PRECONDITION of an incomplete round) implements
// statusError; the caller's Principal is on ctx and the operator check is the
// gateway's.
type SnapshotAdmin interface {
	ListSnapshotRounds(context.Context, *adminv1.ListSnapshotRoundsRequest) (*adminv1.ListSnapshotRoundsResponse, error)
	VerifySnapshotRound(context.Context, *adminv1.VerifySnapshotRoundRequest) (*adminv1.VerifySnapshotRoundResponse, error)
}

var errNoSnapshotAdmin = errors.New("snapshot rounds are not available on this server")

// snapshotCall runs one SnapshotAdmin call for an operator.
func snapshotCall[Req, Resp any](ctx context.Context, a *adminService, req *connect.Request[Req], call func(SnapshotAdmin, context.Context, *Req) (*Resp, error)) (*connect.Response[Resp], error) {
	if a.s.opts.SnapshotAdmin == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errNoSnapshotAdmin)
	}
	p, ok := auth.PrincipalFrom(ctx)
	if !ok {
		return nil, connectError(auth.ErrUnauthenticated)
	}
	if !p.Has(auth.RoleOperator) {
		return nil, connectError(fmt.Errorf("%w: requires operator", auth.ErrPermissionDenied))
	}
	resp, err := call(a.s.opts.SnapshotAdmin, ctx, req.Msg)
	if err != nil {
		return nil, contentError(err)
	}
	return connect.NewResponse(resp), nil
}

func (a *adminService) ListSnapshotRounds(ctx context.Context, req *connect.Request[adminv1.ListSnapshotRoundsRequest]) (*connect.Response[adminv1.ListSnapshotRoundsResponse], error) {
	return snapshotCall(ctx, a, req, SnapshotAdmin.ListSnapshotRounds)
}

func (a *adminService) VerifySnapshotRound(ctx context.Context, req *connect.Request[adminv1.VerifySnapshotRoundRequest]) (*connect.Response[adminv1.VerifySnapshotRoundResponse], error) {
	return snapshotCall(ctx, a, req, SnapshotAdmin.VerifySnapshotRound)
}
