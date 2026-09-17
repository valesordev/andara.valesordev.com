// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package gateway

import (
	"context"
	"errors"

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

// AccountAdmin is the account-administration half of Admin (AW-SRV-008).
// auth.Admin implements it. By the time any of these is called the auth
// interceptor has attached the caller's Principal to ctx; the operator
// check and the audit record are the implementation's.
type AccountAdmin interface {
	CreateAccount(context.Context, *adminv1.CreateAccountRequest) (*adminv1.CreateAccountResponse, error)
	ResetPassword(context.Context, *adminv1.ResetPasswordRequest) (*adminv1.ResetPasswordResponse, error)
	SetRoles(context.Context, *adminv1.SetRolesRequest) (*adminv1.SetRolesResponse, error)
	SetAccountStatus(context.Context, *adminv1.SetAccountStatusRequest) (*adminv1.SetAccountStatusResponse, error)
	IssueInvite(context.Context, *adminv1.IssueInviteRequest) (*adminv1.IssueInviteResponse, error)
	RevokeInvite(context.Context, *adminv1.RevokeInviteRequest) (*adminv1.RevokeInviteResponse, error)
	SetRegistrationMode(context.Context, *adminv1.SetRegistrationModeRequest) (*adminv1.SetRegistrationModeResponse, error)
	CreateAgentAccount(context.Context, *adminv1.CreateAgentAccountRequest) (*adminv1.CreateAgentAccountResponse, error)
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

var errNoAccountAdmin = errors.New("account administration is not configured on this server")

// delegate runs one AccountAdmin call, or UNIMPLEMENTED when none is wired.
func delegate[Req, Resp any](ctx context.Context, a *adminService, req *connect.Request[Req], call func(AccountAdmin, context.Context, *Req) (*Resp, error)) (*connect.Response[Resp], error) {
	if a.s.opts.Accounts == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errNoAccountAdmin)
	}
	resp, err := call(a.s.opts.Accounts, ctx, req.Msg)
	if err != nil {
		return nil, connectError(err)
	}
	return connect.NewResponse(resp), nil
}

func (a *adminService) CreateAccount(ctx context.Context, req *connect.Request[adminv1.CreateAccountRequest]) (*connect.Response[adminv1.CreateAccountResponse], error) {
	return delegate(ctx, a, req, AccountAdmin.CreateAccount)
}

func (a *adminService) ResetPassword(ctx context.Context, req *connect.Request[adminv1.ResetPasswordRequest]) (*connect.Response[adminv1.ResetPasswordResponse], error) {
	return delegate(ctx, a, req, AccountAdmin.ResetPassword)
}

func (a *adminService) SetRoles(ctx context.Context, req *connect.Request[adminv1.SetRolesRequest]) (*connect.Response[adminv1.SetRolesResponse], error) {
	return delegate(ctx, a, req, AccountAdmin.SetRoles)
}

func (a *adminService) SetAccountStatus(ctx context.Context, req *connect.Request[adminv1.SetAccountStatusRequest]) (*connect.Response[adminv1.SetAccountStatusResponse], error) {
	return delegate(ctx, a, req, AccountAdmin.SetAccountStatus)
}

func (a *adminService) IssueInvite(ctx context.Context, req *connect.Request[adminv1.IssueInviteRequest]) (*connect.Response[adminv1.IssueInviteResponse], error) {
	return delegate(ctx, a, req, AccountAdmin.IssueInvite)
}

func (a *adminService) RevokeInvite(ctx context.Context, req *connect.Request[adminv1.RevokeInviteRequest]) (*connect.Response[adminv1.RevokeInviteResponse], error) {
	return delegate(ctx, a, req, AccountAdmin.RevokeInvite)
}

func (a *adminService) SetRegistrationMode(ctx context.Context, req *connect.Request[adminv1.SetRegistrationModeRequest]) (*connect.Response[adminv1.SetRegistrationModeResponse], error) {
	return delegate(ctx, a, req, AccountAdmin.SetRegistrationMode)
}

func (a *adminService) CreateAgentAccount(ctx context.Context, req *connect.Request[adminv1.CreateAgentAccountRequest]) (*connect.Response[adminv1.CreateAgentAccountResponse], error) {
	return delegate(ctx, a, req, AccountAdmin.CreateAgentAccount)
}
