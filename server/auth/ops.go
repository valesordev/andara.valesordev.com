// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package auth

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	accountsv1 "github.com/valesordev/andara/gen/go/andara/accounts/v1"
)

// TokenPair is what Authenticate and Refresh return.
type TokenPair struct {
	SessionToken   string
	SessionExpires time.Time
	RefreshToken   string
	RefreshExpires time.Time
}

// Peer identifies the caller for rate limiting: the transport address, as
// the Gateway saw it.
type Peer string

// checkRate takes a token from the username's and the peer's buckets.
// Either empty is ErrRateLimited, counted and logged without the username.
func (s *Store) checkRate(ctx context.Context, username string, peer Peer) error {
	okUser := username == "" || s.limiter.allow("u:"+username)
	okPeer := peer == "" || s.limiter.allow("p:"+string(peer))
	if okUser && okPeer {
		return nil
	}
	s.metrics.Attempts.WithLabelValues(OutcomeRateLimited).Inc()
	s.log.LogAttrs(ctx, slog.LevelInfo, "authentication attempt",
		slog.String("outcome", OutcomeRateLimited),
		slog.String("session_id", SessionIDFrom(ctx)),
		slog.String("trace_id", traceID(ctx)),
	)
	return ErrRateLimited
}

// Register creates a PASSWORD Account under the Registration Mode in
// effect. Invite redemption and Account creation are one critical section
// (AC-4): exactly one of N concurrent presentations of a code wins, and the
// code is burned — durably — before the winner's Account is written or its
// response sent.
func (s *Store) Register(ctx context.Context, username, password, inviteCode string, peer Peer) (string, error) {
	username = NormalizeUsername(username)
	if err := s.checkRate(ctx, username, peer); err != nil {
		return "", err
	}
	if !validUsername(username) {
		return "", fmt.Errorf("%w: username must be 3-32 characters of a-z, 0-9, _ or -, starting with a letter or digit", ErrInvalidArgument)
	}
	if err := validPassword(password); err != nil {
		return "", err
	}

	mode := s.RegistrationMode()
	switch mode {
	case accountsv1.RegistrationMode_CLOSED:
		return "", ErrRegistrationClosed
	case accountsv1.RegistrationMode_INVITE:
		if inviteCode == "" {
			s.metrics.InviteRedemptions.WithLabelValues(RedemptionInvalid).Inc()
			return "", ErrPermissionDenied
		}
	case accountsv1.RegistrationMode_OPEN:
		inviteCode = ""
	default:
		return "", ErrRegistrationClosed
	}

	// The hash is computed outside the write lock: it is the expensive part
	// and depends on nothing another writer could change.
	cred := hashCredential(accountsv1.CredentialKind_PASSWORD, password, s.opts.Argon2)
	now := s.now()

	s.wmu.Lock()
	defer s.wmu.Unlock()

	if _, taken := s.lookupUsername(username); taken {
		return "", ErrUsernameTaken
	}
	// Re-read the mode under the write lock: an Operator may have closed
	// registration between the check above and now.
	if s.RegistrationMode() != mode {
		return "", ErrRegistrationClosed
	}

	id := newAccountID()
	var issuer *accountsv1.Account
	if inviteCode != "" {
		// Codes are issued lower-case; a player who types one in capitals
		// meant the same code, and RevokeInvite normalizes the same way.
		hash := hashSecret(strings.ToLower(strings.TrimSpace(inviteCode)))
		s.mu.RLock()
		issuerID, ok := s.invites[key32(hash)]
		s.mu.RUnlock()
		if !ok {
			s.metrics.InviteRedemptions.WithLabelValues(RedemptionInvalid).Inc()
			return "", ErrPermissionDenied
		}
		issuer, _ = s.clone(issuerID)
		idx := slices.IndexFunc(issuer.GetInvites(), func(i *accountsv1.Invite) bool { return bytes.Equal(i.GetCodeHash(), hash) })
		if idx < 0 {
			s.metrics.InviteRedemptions.WithLabelValues(RedemptionInvalid).Inc()
			return "", ErrPermissionDenied
		}
		inv := issuer.Invites[idx]
		switch {
		case inv.GetRedeemed():
			// Under wmu this is the losers of a race that has already been
			// decided, which is the only way a redeemed code is presented
			// from a caller that read it as unredeemed.
			s.metrics.InviteRedemptions.WithLabelValues(RedemptionRaceLost).Inc()
			return "", ErrPermissionDenied
		case inv.GetRevoked(), now.Unix() >= inv.GetExpiresUnix():
			s.metrics.InviteRedemptions.WithLabelValues(RedemptionInvalid).Inc()
			return "", ErrPermissionDenied
		}
		inv.Redeemed = true
		inv.RedeemedByAccountId = id
		// Burn first. A crash between this write and the next leaves a
		// burned code with no Account — an Operator re-issues one — which
		// is the stated failure mode, and better than the reverse.
		if err := s.commit(ctx, issuer); err != nil {
			return "", err
		}
		s.metrics.InviteRedemptions.WithLabelValues(RedemptionOK).Inc()
	}

	acc := &accountsv1.Account{
		AccountId:   id,
		Username:    username,
		Credential:  cred,
		Roles:       []accountsv1.Role{accountsv1.Role_PLAYER},
		Status:      accountsv1.AccountStatus_ACTIVE,
		CreatedUnix: now.Unix(),
	}
	if err := s.commit(ctx, acc); err != nil {
		return "", err
	}
	s.metrics.Registrations.WithLabelValues(modeLabel(mode)).Inc()
	s.log.LogAttrs(ctx, slog.LevelInfo, "account registered",
		slog.String("account_id", id),
		slog.String("registration_mode", modeLabel(mode)),
		slog.String("trace_id", traceID(ctx)),
	)
	if issuer != nil {
		s.audit.Record(ctx, Entry{
			Actor:   Principal{AccountID: issuer.GetAccountId()},
			Action:  ActionRedeemInvite,
			Target:  id,
			Outcome: AuditOK,
			Detail:  "redeemed by registration",
		})
	}
	return id, nil
}

// Authenticate verifies a username and secret — a password, an API key, or
// a workload JWT, according to the Account's credential kind — and issues a
// token pair. Every failure is ErrUnauthenticated with the one message, and
// an unknown username costs a full Argon2id verification (AC-2).
func (s *Store) Authenticate(ctx context.Context, username, secret string, peer Peer) (TokenPair, error) {
	username = NormalizeUsername(username)
	if err := s.checkRate(ctx, username, peer); err != nil {
		return TokenPair{}, err
	}
	if len(secret) > MaxSecretLen {
		return TokenPair{}, s.failAuth(ctx, OutcomeBadCredential)
	}

	acc, known := s.lookupUsername(username)
	cred := s.dummy
	if known {
		cred = acc.GetCredential()
	}

	var ok bool
	switch cred.GetKind() {
	case accountsv1.CredentialKind_WORKLOAD_JWT:
		ok = s.verifyWorkload(ctx, acc, secret)
	default:
		ok = s.verifyArgon(ctx, cred, secret)
	}
	if !known || !ok {
		return TokenPair{}, s.failAuth(ctx, OutcomeBadCredential)
	}
	if acc.GetStatus() != accountsv1.AccountStatus_ACTIVE {
		return TokenPair{}, s.failAuth(ctx, OutcomeDisabled)
	}

	s.wmu.Lock()
	defer s.wmu.Unlock()
	cur, _ := s.clone(acc.GetAccountId())
	if cur == nil || cur.GetStatus() != accountsv1.AccountStatus_ACTIVE {
		return TokenPair{}, s.failAuth(ctx, OutcomeDisabled)
	}
	// AC-13: a successful login under stale parameters rewrites the record
	// with the current ones — but only if the credential is still the one
	// we verified, so a concurrent ResetPassword is not undone.
	if cred.GetKind() != accountsv1.CredentialKind_WORKLOAD_JWT &&
		bytes.Equal(cur.GetCredential().GetHash(), cred.GetHash()) && needsRehash(cred, s.opts.Argon2) {
		cur.Credential = hashCredential(cred.GetKind(), secret, s.opts.Argon2)
	}
	pair, err := s.issue(ctx, cur)
	if err != nil {
		return TokenPair{}, err
	}
	s.metrics.Attempts.WithLabelValues(OutcomeOK).Inc()
	s.log.LogAttrs(ctx, slog.LevelInfo, "authentication attempt",
		slog.String("outcome", OutcomeOK),
		slog.String("account_id", cur.GetAccountId()),
		slog.String("session_id", SessionIDFrom(ctx)),
		slog.String("trace_id", traceID(ctx)),
	)
	return pair, nil
}

// verifyArgon runs the derivation under its span and histogram.
func (s *Store) verifyArgon(ctx context.Context, cred *accountsv1.Credential, secret string) bool {
	_, span := s.tracer.Start(ctx, "auth.verify_credential",
		trace.WithAttributes(attribute.Int64("argon2.memory_kib", int64(cred.GetParams().GetMemoryKib()))))
	start := s.now()
	ok := verifyCredential(cred, secret)
	s.metrics.VerifyDuration.Observe(s.now().Sub(start).Seconds())
	span.End()
	return ok
}

func (s *Store) verifyWorkload(ctx context.Context, acc *accountsv1.Account, token string) bool {
	if s.opts.Workload == nil || acc == nil || acc.GetWorkloadSubject() == "" {
		return false
	}
	sub, err := s.opts.Workload.VerifySubject(ctx, token)
	return err == nil && sub == acc.GetWorkloadSubject()
}

// failAuth counts and logs a failed authentication. Never the username: it
// may be a password typed in the wrong box.
func (s *Store) failAuth(ctx context.Context, outcome string) error {
	s.metrics.Attempts.WithLabelValues(outcome).Inc()
	s.log.LogAttrs(ctx, slog.LevelInfo, "authentication attempt",
		slog.String("outcome", outcome),
		slog.String("session_id", SessionIDFrom(ctx)),
		slog.String("trace_id", traceID(ctx)),
	)
	return ErrUnauthenticated
}

// issue signs a session token and mints a refresh token onto acc, then
// commits. Caller holds wmu and owns acc.
func (s *Store) issue(ctx context.Context, acc *accountsv1.Account) (TokenPair, error) {
	now := s.now()
	sessExp := now.Add(s.opts.SessionTTL)
	tok, err := s.keys.sign(tokenPayload{AccountID: acc.GetAccountId(), Exp: sessExp.Unix(), KeyID: s.keys.current().ID})
	if err != nil {
		return TokenPair{}, fmt.Errorf("auth: sign token: %w", err)
	}
	refresh, hash := newRefreshToken()
	refExp := now.Add(s.opts.RefreshTTL)
	acc.RefreshTokens = pruneRefresh(acc.RefreshTokens, now)
	acc.RefreshTokens = append(acc.RefreshTokens, &accountsv1.RefreshTokenRecord{
		Hash: hash, IssuedUnix: now.Unix(), ExpiresUnix: refExp.Unix(),
	})
	if err := s.commit(ctx, acc); err != nil {
		return TokenPair{}, err
	}
	return TokenPair{SessionToken: tok, SessionExpires: sessExp, RefreshToken: refresh, RefreshExpires: refExp}, nil
}

// pruneRefresh drops records that can never be presented again: expired
// ones, and revoked ones once they too have expired. A revoked-but-unexpired
// record stays so that presenting it is UNAUTHENTICATED-and-audited (AC-9)
// rather than merely unknown.
func pruneRefresh(in []*accountsv1.RefreshTokenRecord, now time.Time) []*accountsv1.RefreshTokenRecord {
	out := in[:0]
	for _, r := range in {
		if now.Unix() < r.GetExpiresUnix() {
			out = append(out, r)
		}
	}
	return out
}

// Refresh retires the presented refresh token and issues a new pair. A
// revoked token is refused and audited (AC-9); an unknown or expired one is
// refused. The Account must still be ACTIVE.
func (s *Store) Refresh(ctx context.Context, refreshToken string, peer Peer) (TokenPair, error) {
	if err := s.checkRate(ctx, "", peer); err != nil {
		return TokenPair{}, err
	}
	if refreshToken == "" || len(refreshToken) > MaxSecretLen {
		return TokenPair{}, s.failAuth(ctx, OutcomeBadCredential)
	}
	hash := hashSecret(refreshToken)

	s.wmu.Lock()
	defer s.wmu.Unlock()
	s.mu.RLock()
	id, ok := s.refresh[key32(hash)]
	s.mu.RUnlock()
	if !ok {
		return TokenPair{}, s.failAuth(ctx, OutcomeBadCredential)
	}
	acc, _ := s.clone(id)
	idx := slices.IndexFunc(acc.GetRefreshTokens(), func(r *accountsv1.RefreshTokenRecord) bool { return bytes.Equal(r.GetHash(), hash) })
	if idx < 0 {
		return TokenPair{}, s.failAuth(ctx, OutcomeBadCredential)
	}
	rec := acc.RefreshTokens[idx]
	now := s.now()
	switch {
	case rec.GetRevoked():
		s.audit.Record(ctx, Entry{Actor: Principal{AccountID: id}, Action: ActionRefreshRevoked, Target: id, Outcome: AuditDenied})
		return TokenPair{}, s.failAuth(ctx, OutcomeBadCredential)
	case now.Unix() >= rec.GetExpiresUnix():
		return TokenPair{}, s.failAuth(ctx, OutcomeBadCredential)
	}
	if acc.GetStatus() != accountsv1.AccountStatus_ACTIVE {
		return TokenPair{}, s.failAuth(ctx, OutcomeDisabled)
	}
	rec.Revoked = true
	pair, err := s.issue(ctx, acc)
	if err != nil {
		return TokenPair{}, err
	}
	s.metrics.Attempts.WithLabelValues(OutcomeOK).Inc()
	s.log.LogAttrs(ctx, slog.LevelInfo, "authentication attempt",
		slog.String("outcome", OutcomeOK),
		slog.String("account_id", id),
		slog.String("method", "refresh"),
		slog.String("session_id", SessionIDFrom(ctx)),
		slog.String("trace_id", traceID(ctx)),
	)
	return pair, nil
}

// Revoke retires one refresh token, or every refresh token on its Account.
// Returns how many were newly revoked. An unknown token is UNAUTHENTICATED,
// so the call cannot be used to probe the token space.
func (s *Store) Revoke(ctx context.Context, refreshToken string, all bool, peer Peer) (int, error) {
	if err := s.checkRate(ctx, "", peer); err != nil {
		return 0, err
	}
	if refreshToken == "" || len(refreshToken) > MaxSecretLen {
		return 0, ErrUnauthenticated
	}
	hash := hashSecret(refreshToken)

	s.wmu.Lock()
	defer s.wmu.Unlock()
	s.mu.RLock()
	id, ok := s.refresh[key32(hash)]
	s.mu.RUnlock()
	if !ok {
		return 0, ErrUnauthenticated
	}
	acc, _ := s.clone(id)
	n := 0
	for _, r := range acc.GetRefreshTokens() {
		if r.GetRevoked() {
			continue
		}
		if all || bytes.Equal(r.GetHash(), hash) {
			r.Revoked = true
			n++
		}
	}
	if n == 0 {
		return 0, nil
	}
	if err := s.commit(ctx, acc); err != nil {
		return 0, err
	}
	target := "one"
	if all {
		target = "all"
	}
	s.audit.Record(ctx, Entry{Actor: Principal{AccountID: id}, Action: ActionRevokeRefresh, Target: target, Outcome: AuditOK, Detail: fmt.Sprintf("%d revoked", n)})
	return n, nil
}

// --- verification ------------------------------------------------------------

// Verify is the Gateway's TokenVerifier: signature and expiry from the
// token, everything else from the index. A DISABLED or deleted Account's
// still-valid token is refused (AC-12).
func (s *Store) Verify(ctx context.Context, token string) (Principal, error) {
	_, span := s.tracer.Start(ctx, "session.authenticate")
	defer span.End()
	p, err := s.keys.verify(token, s.now())
	if err != nil {
		span.SetAttributes(attribute.String("auth.outcome", "bad_token"))
		return Principal{}, ErrUnauthenticated
	}
	acc, ok := s.lookupID(p.AccountID)
	if !ok || acc.GetStatus() != accountsv1.AccountStatus_ACTIVE {
		span.SetAttributes(attribute.String("auth.outcome", "not_active"))
		return Principal{}, ErrUnauthenticated
	}
	pr := principalFor(acc, time.Unix(p.Exp, 0))
	if p.ActingAs != "" {
		pr, err = s.ActAs(ctx, pr, p.ActingAs)
		if err != nil {
			span.SetAttributes(attribute.String("auth.outcome", "act_as_denied"))
			return Principal{}, ErrUnauthenticated
		}
	}
	span.SetAttributes(attribute.String("auth.outcome", "ok"))
	return pr, nil
}

func principalFor(acc *accountsv1.Account, exp time.Time) Principal {
	return Principal{
		AccountID:   acc.GetAccountId(),
		Roles:       RolesFromProto(acc.GetRoles()),
		AgentPackID: acc.GetAgentPackId(),
		SessionExp:  exp,
	}
}

// ActAs returns the Principal for a Session opened as target (AC-10). Only
// an operator or game master may; the result carries the actor's identity,
// the target's roles and scope, and is audited whether or not it succeeds.
func (s *Store) ActAs(ctx context.Context, p Principal, target string) (Principal, error) {
	if !p.Has(RoleOperator) && !p.Has(RoleGameMaster) {
		s.audit.Record(ctx, Entry{Actor: p, Action: ActionActAs, Target: target, Outcome: AuditDenied, Detail: "requires operator or game_master"})
		return Principal{}, ErrPermissionDenied
	}
	acc, ok := s.lookupID(target)
	if !ok || acc.GetStatus() != accountsv1.AccountStatus_ACTIVE {
		s.audit.Record(ctx, Entry{Actor: p, Action: ActionActAs, Target: target, Outcome: AuditDenied, Detail: "no such active account"})
		return Principal{}, ErrPermissionDenied
	}
	out := principalFor(acc, p.SessionExp)
	out.AccountID = p.AccountID
	out.ActingAs = target
	s.audit.Record(ctx, Entry{Actor: out, Action: ActionActAs, Target: target, Outcome: AuditOK})
	return out, nil
}

// Recheck re-reads the Accounts behind an open Session's Principal and
// returns nil if the Session may continue: the actor is ACTIVE, the
// acted-as Account (if any) is ACTIVE, and the effective role set is what
// the Session was opened with. The Gateway runs this every
// auth.recheck_interval and closes the Session on an error (AC-12).
func (s *Store) Recheck(p Principal) error {
	actor, ok := s.lookupID(p.AccountID)
	if !ok || actor.GetStatus() != accountsv1.AccountStatus_ACTIVE {
		return fmt.Errorf("%w: account revoked", ErrUnauthenticated)
	}
	effective := actor
	if p.ActingAs != "" {
		effective, ok = s.lookupID(p.ActingAs)
		if !ok || effective.GetStatus() != accountsv1.AccountStatus_ACTIVE {
			return fmt.Errorf("%w: acted-as account revoked", ErrUnauthenticated)
		}
	}
	if !slices.Equal(RolesFromProto(effective.GetRoles()), p.Roles) {
		return fmt.Errorf("%w: roles changed", ErrUnauthenticated)
	}
	return nil
}
