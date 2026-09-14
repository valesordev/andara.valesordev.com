// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package auth

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	accountsv1 "github.com/valesordev/andara/gen/go/andara/accounts/v1"
)

// The Admin operations. Each takes the acting Principal from ctx, requires
// operator, and writes exactly one audit record naming actor, acting-as,
// action, target, Session, and trace — on success and on refusal alike,
// because a refused privileged action is itself something to know about.
// The one exception is a caller with no Principal or without operator,
// which is not a privileged action and gets ErrPermissionDenied only.

// MaxInvitesPerIssue bounds IssueInvite.
const MaxInvitesPerIssue = 100

// operator returns the acting Principal if it holds operator.
func operator(ctx context.Context) (Principal, error) {
	p, ok := PrincipalFrom(ctx)
	if !ok {
		return Principal{}, ErrUnauthenticated
	}
	if !p.Has(RoleOperator) {
		return Principal{}, fmt.Errorf("%w: requires operator", ErrPermissionDenied)
	}
	return p, nil
}

// checkVersion enforces expected_record_version: 0 means unchecked.
func checkVersion(acc *accountsv1.Account, expected uint64) error {
	if expected != 0 && acc.GetRecordVersion() != expected {
		return fmt.Errorf("%w: record_version is %d, request named %d", ErrVersionConflict, acc.GetRecordVersion(), expected)
	}
	return nil
}

// CreateAccount creates a PASSWORD Account with the given roles ([player]
// when empty). The only path to an Account while registration is closed (AC-3).
func (s *Store) CreateAccount(ctx context.Context, username, password string, roles []Role) (string, error) {
	actor, err := operator(ctx)
	if err != nil {
		return "", err
	}
	username = NormalizeUsername(username)
	if !validUsername(username) {
		return "", fmt.Errorf("%w: username must be 3-32 characters of a-z, 0-9, _ or -, starting with a letter or digit", ErrInvalidArgument)
	}
	if err := validPassword(password); err != nil {
		return "", err
	}
	if len(roles) == 0 {
		roles = []Role{RolePlayer}
	}
	if slices.Contains(roles, RoleAgent) {
		return "", fmt.Errorf("%w: agent accounts are created with CreateAgentAccount", ErrInvalidArgument)
	}
	cred := hashCredential(accountsv1.CredentialKind_PASSWORD, password, s.opts.Argon2)

	s.wmu.Lock()
	defer s.wmu.Unlock()
	if _, taken := s.lookupUsername(username); taken {
		s.audit.Record(ctx, Entry{Actor: actor, Action: ActionCreateAccount, Target: "", Outcome: AuditConflict, Detail: "username taken"})
		return "", ErrUsernameTaken
	}
	acc := &accountsv1.Account{
		AccountId:   newAccountID(),
		Username:    username,
		Credential:  cred,
		Roles:       RolesToProto(roles),
		Status:      accountsv1.AccountStatus_ACTIVE,
		CreatedUnix: s.now().Unix(),
	}
	if err := s.commit(ctx, acc); err != nil {
		return "", err
	}
	s.audit.Record(ctx, Entry{Actor: actor, Action: ActionCreateAccount, Target: acc.AccountId, Outcome: AuditOK, Detail: "roles " + roleList(roles)})
	return acc.AccountId, nil
}

// ResetPassword replaces the credential of a PASSWORD Account and revokes
// every refresh token on it — a reset that left old refresh tokens live
// would not be one.
func (s *Store) ResetPassword(ctx context.Context, accountID, newPassword string, expectedVersion uint64) (uint64, error) {
	actor, err := operator(ctx)
	if err != nil {
		return 0, err
	}
	if err := validPassword(newPassword); err != nil {
		return 0, err
	}
	cred := hashCredential(accountsv1.CredentialKind_PASSWORD, newPassword, s.opts.Argon2)

	s.wmu.Lock()
	defer s.wmu.Unlock()
	acc, ok := s.clone(accountID)
	if !ok {
		s.audit.Record(ctx, Entry{Actor: actor, Action: ActionResetPassword, Target: accountID, Outcome: AuditDenied, Detail: "no such account"})
		return 0, ErrNotFound
	}
	if acc.GetCredential().GetKind() != accountsv1.CredentialKind_PASSWORD {
		s.audit.Record(ctx, Entry{Actor: actor, Action: ActionResetPassword, Target: accountID, Outcome: AuditDenied, Detail: "not a password account"})
		return 0, fmt.Errorf("%w: account does not use a password", ErrInvalidArgument)
	}
	if err := checkVersion(acc, expectedVersion); err != nil {
		s.audit.Record(ctx, Entry{Actor: actor, Action: ActionResetPassword, Target: accountID, Outcome: AuditConflict})
		return 0, err
	}
	acc.Credential = cred
	for _, r := range acc.RefreshTokens {
		r.Revoked = true
	}
	if err := s.commit(ctx, acc); err != nil {
		return 0, err
	}
	s.audit.Record(ctx, Entry{Actor: actor, Action: ActionResetPassword, Target: accountID, Outcome: AuditOK})
	return acc.RecordVersion, nil
}

// SetRoles replaces the role set. The agent role cannot be granted or
// removed this way: it is a property of how the Account was created.
func (s *Store) SetRoles(ctx context.Context, accountID string, roles []Role, expectedVersion uint64) (uint64, error) {
	actor, err := operator(ctx)
	if err != nil {
		return 0, err
	}
	if len(roles) == 0 {
		return 0, fmt.Errorf("%w: at least one role is required", ErrInvalidArgument)
	}

	s.wmu.Lock()
	defer s.wmu.Unlock()
	acc, ok := s.clone(accountID)
	if !ok {
		s.audit.Record(ctx, Entry{Actor: actor, Action: ActionSetRoles, Target: accountID, Outcome: AuditDenied, Detail: "no such account"})
		return 0, ErrNotFound
	}
	if err := checkVersion(acc, expectedVersion); err != nil {
		s.audit.Record(ctx, Entry{Actor: actor, Action: ActionSetRoles, Target: accountID, Outcome: AuditConflict})
		return 0, err
	}
	wasAgent := slices.Contains(RolesFromProto(acc.GetRoles()), RoleAgent)
	if wasAgent != slices.Contains(roles, RoleAgent) {
		s.audit.Record(ctx, Entry{Actor: actor, Action: ActionSetRoles, Target: accountID, Outcome: AuditDenied, Detail: "agent role is not assignable"})
		return 0, fmt.Errorf("%w: the agent role is set at creation and cannot be changed", ErrInvalidArgument)
	}
	acc.Roles = RolesToProto(roles)
	if err := s.commit(ctx, acc); err != nil {
		return 0, err
	}
	s.audit.Record(ctx, Entry{Actor: actor, Action: ActionSetRoles, Target: accountID, Outcome: AuditOK, Detail: "roles " + roleList(roles)})
	return acc.RecordVersion, nil
}

// SetAccountStatus sets ACTIVE or DISABLED. Disabling also revokes every
// refresh token, so re-enabling requires a fresh login.
func (s *Store) SetAccountStatus(ctx context.Context, accountID string, status accountsv1.AccountStatus, expectedVersion uint64) (uint64, error) {
	actor, err := operator(ctx)
	if err != nil {
		return 0, err
	}
	if status != accountsv1.AccountStatus_ACTIVE && status != accountsv1.AccountStatus_DISABLED {
		return 0, fmt.Errorf("%w: status must be ACTIVE or DISABLED", ErrInvalidArgument)
	}
	if actor.AccountID == accountID && status == accountsv1.AccountStatus_DISABLED {
		s.audit.Record(ctx, Entry{Actor: actor, Action: ActionSetAccountStatus, Target: accountID, Outcome: AuditDenied, Detail: "cannot disable self"})
		return 0, fmt.Errorf("%w: an operator cannot disable their own account", ErrPermissionDenied)
	}

	s.wmu.Lock()
	defer s.wmu.Unlock()
	acc, ok := s.clone(accountID)
	if !ok {
		s.audit.Record(ctx, Entry{Actor: actor, Action: ActionSetAccountStatus, Target: accountID, Outcome: AuditDenied, Detail: "no such account"})
		return 0, ErrNotFound
	}
	if err := checkVersion(acc, expectedVersion); err != nil {
		s.audit.Record(ctx, Entry{Actor: actor, Action: ActionSetAccountStatus, Target: accountID, Outcome: AuditConflict})
		return 0, err
	}
	acc.Status = status
	if status == accountsv1.AccountStatus_DISABLED {
		for _, r := range acc.RefreshTokens {
			r.Revoked = true
		}
	}
	if err := s.commit(ctx, acc); err != nil {
		return 0, err
	}
	s.audit.Record(ctx, Entry{Actor: actor, Action: ActionSetAccountStatus, Target: accountID, Outcome: AuditOK, Detail: strings.ToLower(status.String())})
	return acc.RecordVersion, nil
}

// IssueInvite mints count Invite Codes on the actor's own Account. The
// codes are returned once; only hashes are stored.
func (s *Store) IssueInvite(ctx context.Context, count int) ([]string, time.Time, error) {
	actor, err := operator(ctx)
	if err != nil {
		return nil, time.Time{}, err
	}
	if count < 1 || count > MaxInvitesPerIssue {
		return nil, time.Time{}, fmt.Errorf("%w: count must be 1..%d", ErrInvalidArgument, MaxInvitesPerIssue)
	}

	s.wmu.Lock()
	defer s.wmu.Unlock()
	acc, ok := s.clone(actor.AccountID)
	if !ok {
		return nil, time.Time{}, ErrUnauthenticated
	}
	now := s.now()
	expires := now.Add(s.opts.InviteTTL)
	codes := make([]string, 0, count)
	acc.Invites = pruneInvites(acc.Invites, now)
	for range count {
		code, hash := newInviteCode()
		codes = append(codes, code)
		acc.Invites = append(acc.Invites, &accountsv1.Invite{CodeHash: hash, IssuedUnix: now.Unix(), ExpiresUnix: expires.Unix()})
	}
	if err := s.commit(ctx, acc); err != nil {
		return nil, time.Time{}, err
	}
	s.audit.Record(ctx, Entry{Actor: actor, Action: ActionIssueInvite, Target: actor.AccountID, Outcome: AuditOK, Detail: fmt.Sprintf("%d issued, expire %s", count, expires.UTC().Format(time.RFC3339))})
	return codes, expires, nil
}

// pruneInvites drops invites that are expired and were never redeemed;
// redeemed ones stay as the record of who came in on whose code.
func pruneInvites(in []*accountsv1.Invite, now time.Time) []*accountsv1.Invite {
	out := in[:0]
	for _, i := range in {
		if i.GetRedeemed() || now.Unix() < i.GetExpiresUnix() {
			out = append(out, i)
		}
	}
	return out
}

// RevokeInvite marks a code unusable. Any operator may revoke any code —
// the audit record names both the actor and the issuer.
func (s *Store) RevokeInvite(ctx context.Context, code string) error {
	actor, err := operator(ctx)
	if err != nil {
		return err
	}
	hash := hashSecret(strings.ToLower(strings.TrimSpace(code)))

	s.wmu.Lock()
	defer s.wmu.Unlock()
	s.mu.RLock()
	issuerID, ok := s.invites[key32(hash)]
	s.mu.RUnlock()
	if !ok {
		s.audit.Record(ctx, Entry{Actor: actor, Action: ActionRevokeInvite, Target: "", Outcome: AuditDenied, Detail: "no such code"})
		return ErrNotFound
	}
	acc, _ := s.clone(issuerID)
	idx := slices.IndexFunc(acc.GetInvites(), func(i *accountsv1.Invite) bool { return bytes.Equal(i.GetCodeHash(), hash) })
	if idx < 0 || acc.Invites[idx].GetRedeemed() {
		s.audit.Record(ctx, Entry{Actor: actor, Action: ActionRevokeInvite, Target: issuerID, Outcome: AuditDenied, Detail: "already redeemed"})
		return fmt.Errorf("%w: code already redeemed", ErrInvalidArgument)
	}
	acc.Invites[idx].Revoked = true
	if err := s.commit(ctx, acc); err != nil {
		return err
	}
	s.audit.Record(ctx, Entry{Actor: actor, Action: ActionRevokeInvite, Target: issuerID, Outcome: AuditOK})
	return nil
}

// SetRegistrationMode writes the AuthConfig record and returns the previous
// mode.
func (s *Store) SetRegistrationMode(ctx context.Context, mode accountsv1.RegistrationMode) (accountsv1.RegistrationMode, error) {
	actor, err := operator(ctx)
	if err != nil {
		return 0, err
	}
	switch mode {
	case accountsv1.RegistrationMode_CLOSED, accountsv1.RegistrationMode_INVITE, accountsv1.RegistrationMode_OPEN:
	default:
		return 0, fmt.Errorf("%w: mode must be CLOSED, INVITE, or OPEN", ErrInvalidArgument)
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	prev := s.RegistrationMode()
	if err := s.commitMode(ctx, mode, actor.AccountID); err != nil {
		return 0, err
	}
	s.audit.Record(ctx, Entry{Actor: actor, Action: ActionSetRegistrationMode, Target: modeLabel(mode), Outcome: AuditOK, Detail: "was " + modeLabel(prev)})
	return prev, nil
}

// CreateAgentAccount creates an AGENT Account scoped to packID. For API_KEY
// the key is returned exactly once; for WORKLOAD_JWT there is no secret and
// workloadSubject is what the projected token's `sub` must equal.
func (s *Store) CreateAgentAccount(ctx context.Context, username, packID string, kind accountsv1.CredentialKind, workloadSubject string) (id, apiKey string, err error) {
	actor, err := operator(ctx)
	if err != nil {
		return "", "", err
	}
	username = NormalizeUsername(username)
	if !validUsername(username) {
		return "", "", fmt.Errorf("%w: username must be 3-32 characters of a-z, 0-9, _ or -, starting with a letter or digit", ErrInvalidArgument)
	}
	if strings.TrimSpace(packID) == "" {
		return "", "", fmt.Errorf("%w: pack_id is required", ErrInvalidArgument)
	}
	var cred *accountsv1.Credential
	switch kind {
	case accountsv1.CredentialKind_API_KEY:
		if workloadSubject != "" {
			return "", "", fmt.Errorf("%w: workload_subject is only for WORKLOAD_JWT", ErrInvalidArgument)
		}
		apiKey = newAPIKey()
		cred = hashCredential(kind, apiKey, s.opts.Argon2)
	case accountsv1.CredentialKind_WORKLOAD_JWT:
		if s.opts.Workload == nil {
			return "", "", fmt.Errorf("%w: WORKLOAD_JWT is disabled; set auth.k8s_issuer", ErrInvalidArgument)
		}
		if strings.TrimSpace(workloadSubject) == "" {
			return "", "", fmt.Errorf("%w: workload_subject is required for WORKLOAD_JWT", ErrInvalidArgument)
		}
		cred = &accountsv1.Credential{Kind: kind}
	default:
		return "", "", fmt.Errorf("%w: credential_kind must be API_KEY or WORKLOAD_JWT", ErrInvalidArgument)
	}

	s.wmu.Lock()
	defer s.wmu.Unlock()
	if _, taken := s.lookupUsername(username); taken {
		s.audit.Record(ctx, Entry{Actor: actor, Action: ActionCreateAgentAccount, Target: "", Outcome: AuditConflict, Detail: "username taken"})
		return "", "", ErrUsernameTaken
	}
	acc := &accountsv1.Account{
		AccountId:       newAccountID(),
		Username:        username,
		Credential:      cred,
		Roles:           []accountsv1.Role{accountsv1.Role_AGENT},
		Status:          accountsv1.AccountStatus_ACTIVE,
		CreatedUnix:     s.now().Unix(),
		AgentPackId:     strings.TrimSpace(packID),
		WorkloadSubject: strings.TrimSpace(workloadSubject),
	}
	if err := s.commit(ctx, acc); err != nil {
		return "", "", err
	}
	s.audit.Record(ctx, Entry{Actor: actor, Action: ActionCreateAgentAccount, Target: acc.AccountId, Outcome: AuditOK, Detail: "pack " + acc.AgentPackId + ", " + strings.ToLower(kind.String())})
	return acc.AccountId, apiKey, nil
}

func roleList(roles []Role) string {
	parts := make([]string, len(roles))
	for i, r := range roles {
		parts[i] = string(r)
	}
	return strings.Join(parts, ",")
}

// BootstrapActor is the actor named on the audit record Bootstrap writes:
// nobody had an Account yet, so nobody could have.
const BootstrapActor = "bootstrap"

// Bootstrap creates the first operator Account from auth.bootstrap_operator
// when the index holds no operator at all, and does nothing otherwise. It
// is how the first `Admin.CreateAccount` caller comes to exist; every later
// operator is created by one. Returns whether an Account was created.
func (s *Store) Bootstrap(ctx context.Context, username, password string) (bool, error) {
	username = NormalizeUsername(username)
	if !validUsername(username) {
		return false, fmt.Errorf("%w: bootstrap operator username must be 3-32 characters of a-z, 0-9, _ or -", ErrInvalidArgument)
	}
	if err := validPassword(password); err != nil {
		return false, fmt.Errorf("bootstrap operator: %w", err)
	}
	s.mu.RLock()
	haveOperator := false
	for _, a := range s.byID {
		if a.GetStatus() == accountsv1.AccountStatus_ACTIVE && slices.Contains(a.GetRoles(), accountsv1.Role_OPERATOR) {
			haveOperator = true
			break
		}
	}
	s.mu.RUnlock()
	if haveOperator {
		return false, nil
	}
	cred := hashCredential(accountsv1.CredentialKind_PASSWORD, password, s.opts.Argon2)

	s.wmu.Lock()
	defer s.wmu.Unlock()
	if _, taken := s.lookupUsername(username); taken {
		return false, fmt.Errorf("bootstrap operator: username %q exists without the operator role; grant it or choose another", username)
	}
	acc := &accountsv1.Account{
		AccountId:   newAccountID(),
		Username:    username,
		Credential:  cred,
		Roles:       []accountsv1.Role{accountsv1.Role_OPERATOR},
		Status:      accountsv1.AccountStatus_ACTIVE,
		CreatedUnix: s.now().Unix(),
	}
	if err := s.commit(ctx, acc); err != nil {
		return false, err
	}
	s.audit.Record(ctx, Entry{Actor: Principal{AccountID: BootstrapActor}, Action: ActionCreateAccount, Target: acc.AccountId, Outcome: AuditOK, Detail: "bootstrap operator"})
	return true, nil
}
