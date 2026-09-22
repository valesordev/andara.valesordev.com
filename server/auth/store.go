// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
	"google.golang.org/protobuf/proto"

	accountsv1 "github.com/valesordev/andara/gen/go/andara/accounts/v1"
	"github.com/valesordev/andara/server/recordlog"
)

// ConfigKey is the one non-Account key on andara.accounts.v1.
const ConfigKey = "config/registration"

// Options configures a Store.
type Options struct {
	Accounts recordlog.Log // andara.accounts.v1
	Audit    recordlog.Log // andara.audit.v1
	Keys     *Keyring

	Argon2     Argon2Params
	SessionTTL time.Duration
	RefreshTTL time.Duration
	InviteTTL  time.Duration
	RateLimit  RateLimit

	// Workload verifies WORKLOAD_JWT credentials. Nil disables the kind.
	Workload WorkloadVerifier

	// Characters is the roster's cap and name rule (AW-SRV-014).
	Characters CharacterOptions

	Log      *slog.Logger
	Tracer   trace.Tracer
	Registry prometheus.Registerer

	// Now is the clock. Tests set it; nil means time.Now.
	Now func() time.Time
}

// WorkloadVerifier checks a projected service-account token and returns
// its subject. AW-SRV-008 ships the API_KEY kind for `make up`; the JWT
// kind is enabled by auth.k8s_issuer and verified here.
type WorkloadVerifier interface {
	VerifySubject(ctx context.Context, token string) (subject string, err error)
}

// Store is the Account index and every operation on it. The server is the
// single writer of andara.accounts.v1 (ADR-0001): writes are serialized
// behind wmu and made durable before the index changes, so a response is
// never sent about a record that could be lost. Reads take mu.
type Store struct {
	opts    Options
	keys    *Keyring
	now     func() time.Time
	log     *slog.Logger
	tracer  trace.Tracer
	metrics *Metrics
	audit   *Auditor
	limiter *Limiter
	roster  *roster

	// wmu serializes writers end to end: read, decide, append, swap. AC-4's
	// atomicity is this lock plus acks=all before the response.
	wmu sync.Mutex
	// mu guards the index. Indexed Accounts are never mutated in place — a
	// writer clones, mutates the clone, appends, and swaps — so a reader
	// that copied a pointer under mu may keep reading it after unlocking.
	mu         sync.RWMutex
	byID       map[string]*accountsv1.Account
	byUsername map[string]string
	invites    map[[32]byte]string // code hash -> issuer account_id
	refresh    map[[32]byte]string // token hash -> account_id
	mode       accountsv1.RegistrationMode

	// dummy is a credential hashed with the configured parameters, verified
	// against when the username is unknown so the two failure paths cost
	// the same (AC-2).
	dummy *accountsv1.Credential
}

// Open rebuilds the index from the accounts log and returns a Store ready
// to serve. A record that does not decode fails the boot: an Account index
// that silently skipped a record would accept a username it should refuse.
func Open(ctx context.Context, o Options) (*Store, error) {
	if o.Accounts == nil || o.Audit == nil {
		return nil, errors.New("auth: accounts and audit logs are required")
	}
	if o.Keys == nil {
		return nil, errors.New("auth: a keyring is required")
	}
	if err := o.Argon2.Validate(); err != nil {
		return nil, err
	}
	if o.SessionTTL <= 0 || o.RefreshTTL <= 0 || o.InviteTTL <= 0 {
		return nil, errors.New("auth: session_ttl, refresh_ttl, and invite_ttl must be positive")
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Tracer == nil {
		o.Tracer = noop.NewTracerProvider().Tracer("andara-server")
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	ros, err := newRoster(o.Characters)
	if err != nil {
		return nil, err
	}
	m := NewMetrics(o.Registry)
	s := &Store{
		opts:       o,
		keys:       o.Keys,
		now:        o.Now,
		log:        o.Log,
		tracer:     o.Tracer,
		metrics:    m,
		limiter:    newLimiter(o.RateLimit, o.Now),
		byID:       map[string]*accountsv1.Account{},
		byUsername: map[string]string{},
		invites:    map[[32]byte]string{},
		refresh:    map[[32]byte]string{},
		mode:       accountsv1.RegistrationMode_CLOSED,
		dummy:      hashCredential(accountsv1.CredentialKind_PASSWORD, "dummy", o.Argon2),
		roster:     ros,
	}
	s.audit = &Auditor{log: o.Audit, slog: o.Log, metrics: m, now: o.Now}

	start := o.Now()
	n := 0
	err = o.Accounts.Replay(ctx, func(r recordlog.Record) error {
		n++
		var rec accountsv1.AccountRecord
		if err := proto.Unmarshal(r.Value, &rec); err != nil {
			return fmt.Errorf("auth: record %q on andara.accounts.v1 does not decode: %w", r.Key, err)
		}
		switch v := rec.Record.(type) {
		case *accountsv1.AccountRecord_Account:
			if v.Account.GetAccountId() != r.Key {
				return fmt.Errorf("auth: record key %q names a different account than its value (%s)", r.Key, v.Account.GetAccountId())
			}
			s.index(v.Account)
		case *accountsv1.AccountRecord_Config:
			s.mode = v.Config.GetMode()
		case *accountsv1.AccountRecord_NameReservation:
			if !strings.HasPrefix(r.Key, NamePrefix) {
				return fmt.Errorf("auth: name reservation under key %q, want %s*", r.Key, NamePrefix)
			}
			s.indexReservation(r.Key, v.NameReservation)
		default:
			return fmt.Errorf("auth: record %q on andara.accounts.v1 is neither Account, AuthConfig, nor NameReservation", r.Key)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.refreshGauges()
	o.Log.LogAttrs(ctx, slog.LevelInfo, "account index loaded",
		slog.Int("records", n),
		slog.Int("accounts", len(s.byID)),
		slog.Int("character_names", len(s.roster.names)),
		slog.String("registration_mode", modeLabel(s.mode)),
		slog.Any("token_key_ids", o.Keys.KeyIDs()),
		slog.Float64("duration_ms", float64(o.Now().Sub(start).Microseconds())/1000),
	)
	return s, nil
}

// Close releases the logs.
func (s *Store) Close() error {
	return errors.Join(s.opts.Accounts.Close(), s.opts.Audit.Close())
}

// Metrics exposes the registered metrics, for tests.
func (s *Store) Metrics() *Metrics { return s.metrics }

// Auditor is the audit writer other packages (command ingress, content
// publish) record through.
func (s *Store) Auditor() *Auditor { return s.audit }

// RegistrationMode is the mode in effect.
func (s *Store) RegistrationMode() accountsv1.RegistrationMode {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.mode
}

// --- index -------------------------------------------------------------------

// index puts acc into every map, replacing whatever the same account_id held.
// Caller holds mu, or is Open before anyone else can see the Store.
func (s *Store) index(acc *accountsv1.Account) {
	id := acc.GetAccountId()
	if old := s.byID[id]; old != nil {
		s.unindex(old)
	}
	s.byID[id] = acc
	s.byUsername[acc.GetUsername()] = id
	for _, inv := range acc.GetInvites() {
		s.invites[key32(inv.GetCodeHash())] = id
	}
	for _, rt := range acc.GetRefreshTokens() {
		s.refresh[key32(rt.GetHash())] = id
	}
}

func (s *Store) unindex(acc *accountsv1.Account) {
	delete(s.byUsername, acc.GetUsername())
	for _, inv := range acc.GetInvites() {
		delete(s.invites, key32(inv.GetCodeHash()))
	}
	for _, rt := range acc.GetRefreshTokens() {
		delete(s.refresh, key32(rt.GetHash()))
	}
	delete(s.byID, acc.GetAccountId())
}

func key32(b []byte) [32]byte {
	var k [32]byte
	copy(k[:], b)
	return k
}

// lookupID returns the indexed Account, which must not be mutated.
func (s *Store) lookupID(id string) (*accountsv1.Account, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	a, ok := s.byID[id]
	return a, ok
}

func (s *Store) lookupUsername(username string) (*accountsv1.Account, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.byUsername[username]
	if !ok {
		return nil, false
	}
	a, ok := s.byID[id]
	return a, ok
}

// refreshGauges recomputes andara_accounts_total{role}. Caller holds mu or wmu.
func (s *Store) refreshGauges() {
	counts := map[Role]int{}
	for _, a := range s.byID {
		if a.GetStatus() != accountsv1.AccountStatus_ACTIVE {
			continue
		}
		for _, r := range RolesFromProto(a.GetRoles()) {
			counts[r]++
		}
	}
	for _, r := range AllRoles {
		s.metrics.Accounts.WithLabelValues(string(r)).Set(float64(counts[r]))
	}
}

// --- writes ------------------------------------------------------------------

// commit makes acc durable and then visible. Caller holds wmu. acc must be
// a clone the caller owns; after commit it belongs to the index.
func (s *Store) commit(ctx context.Context, acc *accountsv1.Account) error {
	acc.RecordVersion++
	normalize(acc)
	body, err := proto.Marshal(&accountsv1.AccountRecord{Record: &accountsv1.AccountRecord_Account{Account: acc}})
	if err != nil {
		return fmt.Errorf("auth: marshal account: %w", err)
	}
	if err := s.opts.Accounts.Append(ctx, acc.GetAccountId(), body); err != nil {
		return fmt.Errorf("auth: write account record: %w", err)
	}
	s.mu.Lock()
	s.index(acc)
	s.refreshGauges()
	s.mu.Unlock()
	return nil
}

// commitMode writes the AuthConfig record. Caller holds wmu.
func (s *Store) commitMode(ctx context.Context, mode accountsv1.RegistrationMode, by string) error {
	cfg := &accountsv1.AuthConfig{Mode: mode, ChangedBy: by, ChangedUnix: s.now().Unix()}
	body, err := proto.Marshal(&accountsv1.AccountRecord{Record: &accountsv1.AccountRecord_Config{Config: cfg}})
	if err != nil {
		return fmt.Errorf("auth: marshal config: %w", err)
	}
	if err := s.opts.Accounts.Append(ctx, ConfigKey, body); err != nil {
		return fmt.Errorf("auth: write config record: %w", err)
	}
	s.mu.Lock()
	s.mode = mode
	s.mu.Unlock()
	return nil
}

// normalize sorts every repeated field the record contract says is sorted
// and prunes refresh tokens that can never be presented again.
func normalize(acc *accountsv1.Account) {
	acc.Roles = RolesToProto(RolesFromProto(acc.Roles))
	slices.SortFunc(acc.RefreshTokens, func(a, b *accountsv1.RefreshTokenRecord) int {
		return strings.Compare(string(a.GetHash()), string(b.GetHash()))
	})
	slices.SortFunc(acc.Invites, func(a, b *accountsv1.Invite) int {
		return strings.Compare(string(a.GetCodeHash()), string(b.GetCodeHash()))
	})
	normalizeCharacters(acc)
}

// clone returns a copy the caller may mutate. Caller holds wmu; the read is
// under mu so the copy is of the current record.
func (s *Store) clone(id string) (*accountsv1.Account, bool) {
	a, ok := s.lookupID(id)
	if !ok {
		return nil, false
	}
	return proto.Clone(a).(*accountsv1.Account), true
}

// --- validation --------------------------------------------------------------

var usernameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{2,31}$`)

// NormalizeUsername lower-cases and trims. Uniqueness is on this form.
func NormalizeUsername(u string) string {
	return strings.ToLower(strings.TrimSpace(u))
}

func validUsername(u string) bool { return usernameRE.MatchString(u) }

// Password bounds. [ASSUMPTION] Eight characters minimum; the maximum bounds
// the Argon2 input, not the player.
const (
	MinPasswordLen = 8
	MaxSecretLen   = 8192
)

func validPassword(p string) error {
	if len(p) < MinPasswordLen {
		return fmt.Errorf("%w: password must be at least %d characters", ErrInvalidArgument, MinPasswordLen)
	}
	if len(p) > MaxSecretLen {
		return fmt.Errorf("%w: password too long", ErrInvalidArgument)
	}
	return nil
}

func modeLabel(m accountsv1.RegistrationMode) string {
	switch m {
	case accountsv1.RegistrationMode_CLOSED:
		return "closed"
	case accountsv1.RegistrationMode_INVITE:
		return "invite"
	case accountsv1.RegistrationMode_OPEN:
		return "open"
	}
	return "unspecified"
}

// ParseRegistrationMode reads closed, invite, or open.
func ParseRegistrationMode(s string) (accountsv1.RegistrationMode, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "closed":
		return accountsv1.RegistrationMode_CLOSED, nil
	case "invite":
		return accountsv1.RegistrationMode_INVITE, nil
	case "open":
		return accountsv1.RegistrationMode_OPEN, nil
	}
	return accountsv1.RegistrationMode_REGISTRATION_MODE_UNSPECIFIED, fmt.Errorf("registration mode must be closed, invite, or open, got %q", s)
}
