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
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
	"google.golang.org/protobuf/proto"

	accountsv1 "github.com/valesordev/andara/gen/go/andara/accounts/v1"
)

// The roster (AW-SRV-014, ADR-0006): a Character's identity is Account
// state — its ID, its name, and the Gateway's last knowledge of where its
// body is — written on andara.accounts.v1 under the same single-writer
// lock as everything else here. The body is World state and lives in the
// sim. Name uniqueness holds across that boundary: a name is reserved on
// the topic under "name/{fold(name)}" before the Account record that
// carries the Character is written, so two Accounts can never hold one
// name however the two writes interleave with a crash.

// NamePrefix keys a NameReservation record.
const NamePrefix = "name/"

// DefaultMaxCharacters is character.max_per_account when unset (ADR-0006).
const DefaultMaxCharacters = 5

// DefaultNamePattern is character.name_pattern when unset: a letter, then
// two to twenty-three letters, apostrophes, spaces, or hyphens.
const DefaultNamePattern = `^[\p{L}][\p{L}' -]{2,23}$`

// CharacterOptions is the roster's configuration.
type CharacterOptions struct {
	// MaxPerAccount is character.max_per_account; zero means DefaultMaxCharacters.
	MaxPerAccount int
	// NamePattern is character.name_pattern, RE2; empty means DefaultNamePattern.
	NamePattern string
}

// Roster errors. Each carries its fixed message; the Gateway maps them to
// the codes of AW-SRV-014's taxonomy.
var (
	// ErrRosterFull: the Account holds character.max_per_account
	// Characters. RESOURCE_EXHAUSTED, reason roster_full.
	ErrRosterFull = errors.New("the roster is full")
	// ErrNameTaken: the folded name is reserved, by any Character on any
	// Account — including a reservation with no Character behind it.
	// ALREADY_EXISTS, reason name_taken. One message, whatever the cause.
	ErrNameTaken = errors.New("that name is taken")
	// ErrNameInvalid: the name fails character.name_pattern.
	// INVALID_ARGUMENT, reason name_invalid; the message names the rule.
	ErrNameInvalid = errors.New("that name is not allowed")
	// ErrNoSuchCharacter: not on the Account, or not ACTIVE. NOT_FOUND,
	// reason no_such_character.
	ErrNoSuchCharacter = errors.New("no such character")
)

// Character creation outcomes, andara_character_creations_total{outcome}.
const (
	CreationOK          = "ok"
	CreationRosterFull  = "roster_full"
	CreationNameTaken   = "name_taken"
	CreationNameInvalid = "name_invalid"
)

// roster is the Store's Character index: reservations by folded name, and
// the compiled name rule.
type roster struct {
	max     int
	pattern *regexp.Regexp
	names   map[string]*accountsv1.NameReservation // folded name -> holder
}

func newRoster(o CharacterOptions) (*roster, error) {
	if o.MaxPerAccount <= 0 {
		o.MaxPerAccount = DefaultMaxCharacters
	}
	if o.NamePattern == "" {
		o.NamePattern = DefaultNamePattern
	}
	re, err := regexp.Compile(o.NamePattern)
	if err != nil {
		return nil, fmt.Errorf("character.name_pattern: %w", err)
	}
	return &roster{max: o.MaxPerAccount, pattern: re, names: map[string]*accountsv1.NameReservation{}}, nil
}

// FoldName is the reservation key: Unicode NFKC, case-folded, trimmed, in
// that order. "Aldric", "aldric", "ALDRIC", and a fullwidth "Ａldric" are
// one name. The trim is last because NFKC can put a space at an edge that
// was not there — U+037A normalizes to a space and an iota — and a
// transform that trimmed first would give two keys to one name (review of
// PR #43).
func FoldName(name string) string {
	return strings.TrimSpace(cases.Fold().String(norm.NFKC.String(name)))
}

// MaxCharacters is character.max_per_account.
func (s *Store) MaxCharacters() int { return s.roster.max }

// Characters lists an Account's roster, sorted by character_id, ACTIVE
// only. A missing Account is an empty roster. The returned records must
// not be mutated.
func (s *Store) Characters(accountID string) []*accountsv1.CharacterRef {
	acc, ok := s.lookupID(accountID)
	if !ok {
		return nil
	}
	out := make([]*accountsv1.CharacterRef, 0, len(acc.GetCharacters()))
	for _, c := range acc.GetCharacters() {
		if c.GetStatus() == accountsv1.CharacterStatus_CHARACTER_STATUS_ACTIVE {
			out = append(out, c)
		}
	}
	return out
}

// Character is one ACTIVE Character on the Account, or ErrNoSuchCharacter.
func (s *Store) Character(accountID, characterID string) (*accountsv1.CharacterRef, error) {
	for _, c := range s.Characters(accountID) {
		if c.GetCharacterId() == characterID {
			return c, nil
		}
	}
	return nil, ErrNoSuchCharacter
}

// CreateCharacter adds a Character to the Account's roster with its body
// to be spawned at zone/room: the name checked against the rule and the
// reservations, the cap counted over ACTIVE and DELETED alike (a deleted
// Character keeps its slot until purged, AW-SRV-032), the reservation
// written, then the Account record. Nothing is written on a refusal.
func (s *Store) CreateCharacter(ctx context.Context, accountID, name string, zone, room string) (*accountsv1.CharacterRef, error) {
	name = strings.TrimSpace(name)
	if !utf8.ValidString(name) || !s.roster.pattern.MatchString(name) {
		s.metrics.CharacterCreations.WithLabelValues(CreationNameInvalid).Inc()
		return nil, fmt.Errorf("%w: a name must match %s", ErrNameInvalid, s.roster.pattern.String())
	}
	key := FoldName(name)

	s.wmu.Lock()
	defer s.wmu.Unlock()
	acc, ok := s.clone(accountID)
	if !ok {
		return nil, ErrNotFound
	}
	if len(acc.GetCharacters()) >= s.roster.max {
		s.metrics.CharacterCreations.WithLabelValues(CreationRosterFull).Inc()
		return nil, fmt.Errorf("%w: an account holds at most %d characters", ErrRosterFull, s.roster.max)
	}
	s.mu.RLock()
	_, taken := s.roster.names[key]
	s.mu.RUnlock()
	if taken {
		s.metrics.CharacterCreations.WithLabelValues(CreationNameTaken).Inc()
		return nil, ErrNameTaken
	}

	ref := &accountsv1.CharacterRef{
		CharacterId: newAccountID(),
		Name:        name,
		Status:      accountsv1.CharacterStatus_CHARACTER_STATUS_ACTIVE,
		ZoneId:      zone,
		RoomId:      room,
		CreatedUnix: s.now().Unix(),
	}
	// Reservation first: a crash between the two writes loses the name,
	// never the Account's consistency.
	res := &accountsv1.NameReservation{CharacterId: ref.CharacterId, AccountId: accountID}
	body, err := proto.Marshal(&accountsv1.AccountRecord{Record: &accountsv1.AccountRecord_NameReservation{NameReservation: res}})
	if err != nil {
		return nil, fmt.Errorf("auth: marshal name reservation: %w", err)
	}
	if err := s.opts.Accounts.Append(ctx, NamePrefix+key, body); err != nil {
		return nil, fmt.Errorf("auth: write name reservation: %w", err)
	}
	s.mu.Lock()
	s.roster.names[key] = res
	s.mu.Unlock()

	acc.Characters = append(acc.Characters, ref)
	if err := s.commit(ctx, acc); err != nil {
		return nil, err
	}
	s.metrics.CharacterCreations.WithLabelValues(CreationOK).Inc()
	s.log.LogAttrs(ctx, slog.LevelInfo, "character created",
		slog.String("account_id", accountID), slog.String("character_id", ref.CharacterId),
		slog.String("name", fmt.Sprintf("%q", name)), slog.Int("roster", len(acc.Characters)),
		slog.String("session_id", SessionIDFrom(ctx)), slog.String("trace_id", traceID(ctx)))
	return proto.Clone(ref).(*accountsv1.CharacterRef), nil
}

// SetCharacterPosition records the Gateway's last knowledge of where the
// body is — written at unbind from the routing table's entry — so the
// next BindCharacter is routed there. A Character no longer on the roster
// is ErrNoSuchCharacter; a position already recorded is not rewritten.
func (s *Store) SetCharacterPosition(ctx context.Context, accountID, characterID, zone, room string) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	acc, ok := s.clone(accountID)
	if !ok {
		return ErrNotFound
	}
	i := slices.IndexFunc(acc.Characters, func(c *accountsv1.CharacterRef) bool { return c.GetCharacterId() == characterID })
	if i < 0 {
		return ErrNoSuchCharacter
	}
	c := acc.Characters[i]
	if c.GetZoneId() == zone && c.GetRoomId() == room {
		return nil
	}
	c.ZoneId, c.RoomId = zone, room
	return s.commit(ctx, acc)
}

// indexReservation records a name/* record read at boot.
func (s *Store) indexReservation(key string, res *accountsv1.NameReservation) {
	s.roster.names[strings.TrimPrefix(key, NamePrefix)] = res
}

// normalizeCharacters keeps the roster sorted by character_id.
func normalizeCharacters(acc *accountsv1.Account) {
	slices.SortFunc(acc.Characters, func(a, b *accountsv1.CharacterRef) int {
		return strings.Compare(a.GetCharacterId(), b.GetCharacterId())
	})
}
