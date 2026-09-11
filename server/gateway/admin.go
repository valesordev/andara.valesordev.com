// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package gateway

import (
	"context"

	"connectrpc.com/connect"

	adminv1 "github.com/valesordev/andara/gen/go/andara/admin/v1"
	"github.com/valesordev/andara/gen/go/andara/admin/v1/adminv1connect"
)

// BuildInfo is what the binary knows about itself, stamped at link time
// (Makefile, Dockerfile.server). Content identity is empty until AW-SRV-012
// resolves the Active Pointer.
type BuildInfo struct {
	Version string
	Commit  string
}

type adminService struct {
	adminv1connect.UnimplementedAdminHandler
	s *Server
}

// GetServerInfo is reachable only through the auth interceptor; by the time
// it runs, the caller has a Principal.
func (a *adminService) GetServerInfo(_ context.Context, _ *connect.Request[adminv1.GetServerInfoRequest]) (*connect.Response[adminv1.GetServerInfoResponse], error) {
	return connect.NewResponse(&adminv1.GetServerInfoResponse{
		Version:            a.s.opts.Build.Version,
		Commit:             a.s.opts.Build.Commit,
		Environment:        a.s.opts.Environment,
		ProtocolMinVersion: a.s.opts.ProtocolMin,
		ProtocolMaxVersion: a.s.opts.ProtocolMax,
	}), nil
}
