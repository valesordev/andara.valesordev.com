// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package gateway

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	adminv1 "github.com/valesordev/andara/gen/go/andara/admin/v1"
	"github.com/valesordev/andara/gen/go/andara/admin/v1/adminv1connect"
	"github.com/valesordev/andara/server/auth"
)

type fakeSnapshotAdmin struct{ calls int }

func (f *fakeSnapshotAdmin) ListSnapshotRounds(context.Context, *adminv1.ListSnapshotRoundsRequest) (*adminv1.ListSnapshotRoundsResponse, error) {
	f.calls++
	return &adminv1.ListSnapshotRoundsResponse{Rounds: []*adminv1.SnapshotRound{{Tick: 7, Complete: true}}}, nil
}

type notFound struct{}

func (notFound) Error() string { return "no object" }
func (notFound) Status() (uint32, string, string, proto.Message) {
	return uint32(connect.CodeNotFound), "andara.snapshot", "round_not_found", nil
}

func (f *fakeSnapshotAdmin) VerifySnapshotRound(context.Context, *adminv1.VerifySnapshotRoundRequest) (*adminv1.VerifySnapshotRoundResponse, error) {
	f.calls++
	return nil, notFound{}
}

func snapshotClient(t *testing.T, fs *fakeSnapshotAdmin, roles ...auth.Role) adminv1connect.AdminClient {
	t.Helper()
	h := start(t, func(o *Options) {
		if fs != nil {
			o.SnapshotAdmin = fs
		}
		o.Verifier = &roleVerifier{roles: roles}
	})
	c, tr := h.httpClient()
	c.Transport = bearer{tr}
	return adminv1connect.NewAdminClient(c, h.baseURL())
}

// Both snapshot RPCs are OPERATOR only: a builder never reaches the seam.
func TestSnapshotAdmin_IsOperatorOnly(t *testing.T) {
	fs := &fakeSnapshotAdmin{}
	ctx := context.Background()
	builder := snapshotClient(t, fs, auth.RoleBuilder)
	if _, err := builder.ListSnapshotRounds(ctx, connect.NewRequest(&adminv1.ListSnapshotRoundsRequest{})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("list as builder: %v", err)
	}
	if _, err := builder.VerifySnapshotRound(ctx, connect.NewRequest(&adminv1.VerifySnapshotRoundRequest{Tick: 7})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("verify as builder: %v", err)
	}
	if fs.calls != 0 {
		t.Fatalf("the seam ran %d times for a non-operator", fs.calls)
	}

	op := snapshotClient(t, fs, auth.RoleOperator)
	resp, err := op.ListSnapshotRounds(ctx, connect.NewRequest(&adminv1.ListSnapshotRoundsRequest{}))
	if err != nil || len(resp.Msg.GetRounds()) != 1 || resp.Msg.GetRounds()[0].GetTick() != 7 {
		t.Fatalf("list as operator: %v %v", resp, err)
	}
	// A refusal that names its status crosses the wire with it and its reason.
	_, err = op.VerifySnapshotRound(ctx, connect.NewRequest(&adminv1.VerifySnapshotRoundRequest{Tick: 7}))
	ce, ei := errorInfo(t, err)
	if ce.Code() != connect.CodeNotFound || ei.GetReason() != "round_not_found" || ei.GetDomain() != "andara.snapshot" {
		t.Fatalf("verify: %v %v", ce.Code(), ei)
	}
}

func TestSnapshotAdmin_UnconfiguredIsUnimplemented(t *testing.T) {
	op := snapshotClient(t, nil, auth.RoleOperator)
	if _, err := op.ListSnapshotRounds(context.Background(), connect.NewRequest(&adminv1.ListSnapshotRoundsRequest{})); connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Fatalf("got %v, want UNIMPLEMENTED", err)
	}
}
