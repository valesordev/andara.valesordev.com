// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package auth

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	adminv1 "github.com/valesordev/andara/gen/go/andara/admin/v1"
	authv1 "github.com/valesordev/andara/gen/go/andara/auth/v1"
	"github.com/valesordev/andara/gen/go/andara/auth/v1/authv1connect"
)

// Code maps a package error to its gRPC code (the story's error taxonomy).
// Anything outside the taxonomy is INTERNAL: an unmapped error is a bug.
func Code(err error) connect.Code {
	switch {
	case errors.Is(err, ErrUnauthenticated):
		return connect.CodeUnauthenticated
	case errors.Is(err, ErrPermissionDenied), errors.Is(err, ErrNotAuthorized):
		return connect.CodePermissionDenied
	case errors.Is(err, ErrRegistrationClosed):
		return connect.CodeFailedPrecondition
	case errors.Is(err, ErrUsernameTaken):
		return connect.CodeAlreadyExists
	case errors.Is(err, ErrRateLimited):
		return connect.CodeResourceExhausted
	case errors.Is(err, ErrVersionConflict):
		return connect.CodeAborted
	case errors.Is(err, ErrNotFound):
		return connect.CodeNotFound
	case errors.Is(err, ErrInvalidArgument):
		return connect.CodeInvalidArgument
	}
	return connect.CodeInternal
}

// connectError wraps err for the wire. Taxonomy errors carry their fixed
// message; anything else — a failed log write, say — is INTERNAL with a
// message that names no secret, because the wrapped detail is a broker
// error and never a credential.
func connectError(err error) error {
	if err == nil {
		return nil
	}
	var ce *connect.Error
	if errors.As(err, &ce) {
		return err
	}
	code := Code(err)
	if code == connect.CodeInternal {
		return connect.NewError(code, errors.New("internal error"))
	}
	return connect.NewError(code, err)
}

// Service serves andara.auth.v1.Auth over the Store.
type Service struct {
	authv1connect.UnimplementedAuthHandler
	store *Store
}

// NewService returns the Auth handler.
func NewService(s *Store) *Service { return &Service{store: s} }

// Register implements Auth.Register.
func (h *Service) Register(ctx context.Context, req *connect.Request[authv1.RegisterRequest]) (*connect.Response[authv1.RegisterResponse], error) {
	id, err := h.store.Register(ctx, req.Msg.GetUsername(), req.Msg.GetPassword(), req.Msg.GetInviteCode(), Peer(req.Peer().Addr))
	if err != nil {
		return nil, connectError(err)
	}
	return connect.NewResponse(&authv1.RegisterResponse{AccountId: id}), nil
}

// Authenticate implements Auth.Authenticate.
func (h *Service) Authenticate(ctx context.Context, req *connect.Request[authv1.AuthenticateRequest]) (*connect.Response[authv1.AuthenticateResponse], error) {
	pair, err := h.store.Authenticate(ctx, req.Msg.GetUsername(), req.Msg.GetPassword(), Peer(req.Peer().Addr))
	if err != nil {
		return nil, connectError(err)
	}
	return connect.NewResponse(&authv1.AuthenticateResponse{Tokens: pair.proto()}), nil
}

// Refresh implements Auth.Refresh.
func (h *Service) Refresh(ctx context.Context, req *connect.Request[authv1.RefreshRequest]) (*connect.Response[authv1.RefreshResponse], error) {
	pair, err := h.store.Refresh(ctx, req.Msg.GetRefreshToken(), Peer(req.Peer().Addr))
	if err != nil {
		return nil, connectError(err)
	}
	return connect.NewResponse(&authv1.RefreshResponse{Tokens: pair.proto()}), nil
}

// Revoke implements Auth.Revoke.
func (h *Service) Revoke(ctx context.Context, req *connect.Request[authv1.RevokeRequest]) (*connect.Response[authv1.RevokeResponse], error) {
	n, err := h.store.Revoke(ctx, req.Msg.GetRefreshToken(), req.Msg.GetAll(), Peer(req.Peer().Addr))
	if err != nil {
		return nil, connectError(err)
	}
	return connect.NewResponse(&authv1.RevokeResponse{Revoked: uint32(n)}), nil
}

func (p TokenPair) proto() *authv1.TokenPair {
	return &authv1.TokenPair{
		SessionToken:       p.SessionToken,
		SessionExpiresUnix: p.SessionExpires.Unix(),
		RefreshToken:       p.RefreshToken,
		RefreshExpiresUnix: p.RefreshExpires.Unix(),
	}
}

// Admin is the account-administration half of andara.admin.v1.Admin. The
// Gateway's Admin handler delegates these eight methods here after its auth
// interceptor has attached the caller's Principal to ctx.
type Admin struct {
	store *Store
}

// NewAdmin returns the account-administration handler.
func NewAdmin(s *Store) *Admin { return &Admin{store: s} }

// CreateAccount implements Admin.CreateAccount.
func (a *Admin) CreateAccount(ctx context.Context, req *adminv1.CreateAccountRequest) (*adminv1.CreateAccountResponse, error) {
	id, err := a.store.CreateAccount(ctx, req.GetUsername(), req.GetPassword(), RolesFromProto(req.GetRoles()))
	if err != nil {
		return nil, connectError(err)
	}
	return &adminv1.CreateAccountResponse{AccountId: id}, nil
}

// ResetPassword implements Admin.ResetPassword.
func (a *Admin) ResetPassword(ctx context.Context, req *adminv1.ResetPasswordRequest) (*adminv1.ResetPasswordResponse, error) {
	v, err := a.store.ResetPassword(ctx, req.GetAccountId(), req.GetNewPassword(), req.GetExpectedRecordVersion())
	if err != nil {
		return nil, connectError(err)
	}
	return &adminv1.ResetPasswordResponse{RecordVersion: v}, nil
}

// SetRoles implements Admin.SetRoles.
func (a *Admin) SetRoles(ctx context.Context, req *adminv1.SetRolesRequest) (*adminv1.SetRolesResponse, error) {
	v, err := a.store.SetRoles(ctx, req.GetAccountId(), RolesFromProto(req.GetRoles()), req.GetExpectedRecordVersion())
	if err != nil {
		return nil, connectError(err)
	}
	return &adminv1.SetRolesResponse{RecordVersion: v}, nil
}

// SetAccountStatus implements Admin.SetAccountStatus.
func (a *Admin) SetAccountStatus(ctx context.Context, req *adminv1.SetAccountStatusRequest) (*adminv1.SetAccountStatusResponse, error) {
	v, err := a.store.SetAccountStatus(ctx, req.GetAccountId(), req.GetStatus(), req.GetExpectedRecordVersion())
	if err != nil {
		return nil, connectError(err)
	}
	return &adminv1.SetAccountStatusResponse{RecordVersion: v}, nil
}

// IssueInvite implements Admin.IssueInvite.
func (a *Admin) IssueInvite(ctx context.Context, req *adminv1.IssueInviteRequest) (*adminv1.IssueInviteResponse, error) {
	codes, exp, err := a.store.IssueInvite(ctx, int(req.GetCount()))
	if err != nil {
		return nil, connectError(err)
	}
	return &adminv1.IssueInviteResponse{Codes: codes, ExpiresUnix: exp.Unix()}, nil
}

// RevokeInvite implements Admin.RevokeInvite.
func (a *Admin) RevokeInvite(ctx context.Context, req *adminv1.RevokeInviteRequest) (*adminv1.RevokeInviteResponse, error) {
	if err := a.store.RevokeInvite(ctx, req.GetCode()); err != nil {
		return nil, connectError(err)
	}
	return &adminv1.RevokeInviteResponse{}, nil
}

// SetRegistrationMode implements Admin.SetRegistrationMode.
func (a *Admin) SetRegistrationMode(ctx context.Context, req *adminv1.SetRegistrationModeRequest) (*adminv1.SetRegistrationModeResponse, error) {
	prev, err := a.store.SetRegistrationMode(ctx, req.GetMode())
	if err != nil {
		return nil, connectError(err)
	}
	return &adminv1.SetRegistrationModeResponse{Previous: prev}, nil
}

// CreateAgentAccount implements Admin.CreateAgentAccount.
func (a *Admin) CreateAgentAccount(ctx context.Context, req *adminv1.CreateAgentAccountRequest) (*adminv1.CreateAgentAccountResponse, error) {
	id, key, err := a.store.CreateAgentAccount(ctx, req.GetUsername(), req.GetPackId(), req.GetCredentialKind(), req.GetWorkloadSubject())
	if err != nil {
		return nil, connectError(err)
	}
	return &adminv1.CreateAgentAccountResponse{AccountId: id, ApiKey: key}, nil
}
