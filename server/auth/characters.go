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
	"time"
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

// AllCharacters lists an Account's whole roster, sorted by character_id: the
// ACTIVE Characters and the DELETED ones, purged or not (AW-SRV-032). The
// returned records must not be mutated.
func (s *Store) AllCharacters(accountID string) []*accountsv1.CharacterRef {
	acc, ok := s.lookupID(accountID)
	if !ok {
		return nil
	}
	return slices.Clone(acc.GetCharacters())
}

// RosterCounts is how many roster entries are ACTIVE and how many DELETED,
// purged or not, over every Account: andara_roster_characters.
func (s *Store) RosterCounts() (active, deleted int) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, acc := range s.byID {
		for _, c := range acc.GetCharacters() {
			if c.GetStatus() == accountsv1.CharacterStatus_CHARACTER_STATUS_ACTIVE {
				active++
			} else {
				deleted++
			}
		}
	}
	return active, deleted
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
	// ACTIVE and DELETED alike count, until the purge: a deleted Character
	// keeps its slot for the retention window, so deleting is not a way to hold
	// six names (AW-SRV-032 AC-1). The entry and its reservation outlive the
	// purge; only the slot is freed.
	if countSlots(acc) >= s.roster.max {
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
		slog.String("name", fmt.Sprintf("%q", name)), slog.Int("roster", countSlots(acc)),
		slog.String("session_id", SessionIDFrom(ctx)), slog.String("trace_id", traceID(ctx)))
	return proto.Clone(ref).(*accountsv1.CharacterRef), nil
}

// DeleteCharacter soft-deletes an ACTIVE Character: status DELETED and
// deleted_unix set (AW-SRV-032). The body is left dormant for the retention
// window, the name reservation stays for good, and the entry keeps counting
// against character.max_per_account. A Character that is not on the Account,
// or is already deleted, is ErrNoSuchCharacter. Whether the Character is live
// is the Gateway's to check before it calls this; the store knows nothing of
// Sessions.
func (s *Store) DeleteCharacter(ctx context.Context, accountID, characterID string) (*accountsv1.CharacterRef, error) {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	acc, ok := s.clone(accountID)
	if !ok {
		return nil, ErrNotFound
	}
	i := slices.IndexFunc(acc.Characters, func(c *accountsv1.CharacterRef) bool {
		return c.GetCharacterId() == characterID && c.GetStatus() == accountsv1.CharacterStatus_CHARACTER_STATUS_ACTIVE
	})
	if i < 0 {
		return nil, ErrNoSuchCharacter
	}
	c := acc.Characters[i]
	c.Status = accountsv1.CharacterStatus_CHARACTER_STATUS_DELETED
	c.DeletedUnix = s.now().Unix()
	if err := s.commit(ctx, acc); err != nil {
		return nil, err
	}
	s.log.LogAttrs(ctx, slog.LevelInfo, "character deleted",
		slog.String("account_id", accountID), slog.String("character_id", characterID),
		slog.Int64("deleted_unix", c.DeletedUnix),
		slog.String("session_id", SessionIDFrom(ctx)), slog.String("trace_id", traceID(ctx)))
	return proto.Clone(c).(*accountsv1.CharacterRef), nil
}

// ExpiredCharacter is a deleted Character whose retention has run out and
// whose purge has not been produced.
type ExpiredCharacter struct {
	AccountID string
	Ref       *accountsv1.CharacterRef
}

// ExpiredCharacters is every DELETED Character with no purged_unix whose
// deleted_unix plus retention is at or before now, sorted by Account then
// Character (AW-SRV-032's sweep). The returned records must not be mutated.
func (s *Store) ExpiredCharacters(retention time.Duration) []ExpiredCharacter {
	cutoff := s.now().Add(-retention).Unix()
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []ExpiredCharacter
	for id, acc := range s.byID {
		for _, c := range acc.GetCharacters() {
			if c.GetStatus() == accountsv1.CharacterStatus_CHARACTER_STATUS_DELETED && c.GetPurgedUnix() == 0 && c.GetDeletedUnix() <= cutoff {
				out = append(out, ExpiredCharacter{AccountID: id, Ref: c})
			}
		}
	}
	slices.SortFunc(out, func(a, b ExpiredCharacter) int {
		if c := strings.Compare(a.AccountID, b.AccountID); c != 0 {
			return c
		}
		return strings.Compare(a.Ref.GetCharacterId(), b.Ref.GetCharacterId())
	})
	return out
}

// MarkPurged records that the PurgeCharacter for a deleted Character is
// durable in the log, so the sweep never produces it again. It reports false
// when the entry was already marked, and ErrNoSuchCharacter for one that is
// not DELETED.
func (s *Store) MarkPurged(ctx context.Context, accountID, characterID string) (bool, error) {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	acc, ok := s.clone(accountID)
	if !ok {
		return false, ErrNotFound
	}
	i := slices.IndexFunc(acc.Characters, func(c *accountsv1.CharacterRef) bool { return c.GetCharacterId() == characterID })
	if i < 0 || acc.Characters[i].GetStatus() != accountsv1.CharacterStatus_CHARACTER_STATUS_DELETED {
		return false, ErrNoSuchCharacter
	}
	if acc.Characters[i].GetPurgedUnix() != 0 {
		return false, nil
	}
	acc.Characters[i].PurgedUnix = s.now().Unix()
	return true, s.commit(ctx, acc)
}

// ReclaimReservations removes every name reservation that no Character is
// behind (AW-SRV-032 AC-8): the debris of a crash between CreateCharacter's
// two writes. A reservation whose Character is on its Account's roster, in any
// status, is never removed — a deleted name stays reserved for good. It runs
// under the writer lock, so it cannot see a create between its reservation and
// its Account record. Returns the keys removed.
func (s *Store) ReclaimReservations(ctx context.Context) ([]string, error) {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	s.mu.RLock()
	var dead []string
	for key, res := range s.roster.names {
		acc := s.byID[res.GetAccountId()]
		if acc == nil || !slices.ContainsFunc(acc.GetCharacters(), func(c *accountsv1.CharacterRef) bool { return c.GetCharacterId() == res.GetCharacterId() }) {
			dead = append(dead, key)
		}
	}
	s.mu.RUnlock()
	slices.Sort(dead)
	var removed []string
	for _, key := range dead {
		// A tombstone: a nil value deletes the key at compaction, and replays
		// as an empty record that Open reads as "no reservation".
		if err := s.opts.Accounts.Append(ctx, NamePrefix+key, nil); err != nil {
			return removed, fmt.Errorf("auth: remove name reservation: %w", err)
		}
		s.mu.Lock()
		delete(s.roster.names, key)
		s.mu.Unlock()
		removed = append(removed, key)
	}
	return removed, nil
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

// countSlots is how many roster slots the Account holds: every entry not yet
// purged.
func countSlots(acc *accountsv1.Account) int {
	n := 0
	for _, c := range acc.GetCharacters() {
		if c.GetPurgedUnix() == 0 {
			n++
		}
	}
	return n
}

// normalizeCharacters keeps the roster sorted by character_id.
func normalizeCharacters(acc *accountsv1.Account) {
	slices.SortFunc(acc.Characters, func(a, b *accountsv1.CharacterRef) int {
		return strings.Compare(a.GetCharacterId(), b.GetCharacterId())
	})
}
