// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	accountsv1 "github.com/valesordev/andara/gen/go/andara/accounts/v1"
)

// The folding table: case, NFKC confusables, whitespace — one name each.
func TestFoldName(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"Aldric", "aldric"},
		{"ALDRIC", "aldric"},
		{"  Aldric ", "aldric"},
		{"Ａldric", "aldric"},     // fullwidth A, NFKC
		{"Straße", "strasse"},    // ß case-folds to ss
		{"ﬁnn", "finn"},          // fi ligature, NFKC
		{"Ångström", "ångström"}, // composed and decomposed agree
		{"Ångström", "ångström"},
		{"O'Neil", "o'neil"},
		// NFKC puts a space at the edge that was not in the input: the
		// trim is last, or these two names take two keys (review of
		// PR #43). U+037A GREEK YPOGEGRAMMENI normalizes to " ι".
		{"\u037aab", "\u03b9ab"},
		{"\u03b9ab", "\u03b9ab"},
	} {
		if got := FoldName(tc.in); got != tc.want {
			t.Errorf("FoldName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// AC-1, AC-2, AC-3: the cap, the reservation across Accounts in any case,
// and the pattern — each refused with its own error, nothing written.
func TestCreateCharacter_Rules(t *testing.T) {
	f := newFixture(t, nil)
	ctx := context.Background()
	opCtx, _ := f.bootstrapOperator("oper", "operator-password")
	a, err := f.store.CreateAccount(opCtx, "alice", "correct horse battery", nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.store.CreateAccount(opCtx, "bob", "correct horse battery", nil)
	if err != nil {
		t.Fatal(err)
	}
	before := len(f.accounts.Records())

	// The pattern names the rule and reserves nothing.
	for _, bad := range []string{"", "Al", "1Aldric", "Aldric!", strings.Repeat("a", 25), "Ald\tric"} {
		_, err := f.store.CreateCharacter(ctx, a, bad, "town", "plaza")
		if !errors.Is(err, ErrNameInvalid) || !strings.Contains(err.Error(), "must match") {
			t.Fatalf("%q: err = %v, want ErrNameInvalid naming the rule", bad, err)
		}
	}
	if n := len(f.accounts.Records()); n != before {
		t.Fatalf("an invalid name wrote %d records", n-before)
	}

	ref, err := f.store.CreateCharacter(ctx, a, "Aldric", "town", "plaza")
	if err != nil {
		t.Fatal(err)
	}
	if ref.GetName() != "Aldric" || ref.GetStatus() != accountsv1.CharacterStatus_CHARACTER_STATUS_ACTIVE || ref.GetZoneId() != "town" || ref.GetRoomId() != "plaza" || ref.GetCharacterId() == "" {
		t.Fatalf("ref %v", ref)
	}
	// Reservation first, then the Account, on the log.
	recs := f.accountRecords()
	if len(recs) != before+2 {
		t.Fatalf("want two records, got %d", len(recs)-before)
	}
	if nr := recs[before].GetNameReservation(); nr.GetCharacterId() != ref.GetCharacterId() || nr.GetAccountId() != a {
		t.Fatalf("first record %v, want the reservation", recs[before])
	}
	if raw := f.accounts.Records()[before]; raw.Key != NamePrefix+"aldric" {
		t.Fatalf("reservation key %q", raw.Key)
	}
	if acc := recs[before+1].GetAccount(); len(acc.GetCharacters()) != 1 || acc.GetCharacters()[0].GetName() != "Aldric" {
		t.Fatalf("second record %v, want the Account with the Character", recs[before+1])
	}

	// Taken: any case, any Account, one fixed message, nothing written.
	before = len(f.accounts.Records())
	for _, dup := range []string{"Aldric", "aldric", "ALDRIC", " Aldric ", "Ａldric"} {
		for _, acct := range []string{a, b} {
			_, err := f.store.CreateCharacter(ctx, acct, dup, "town", "plaza")
			if !errors.Is(err, ErrNameTaken) || err.Error() != ErrNameTaken.Error() {
				t.Fatalf("%q on %s: err = %v, want ErrNameTaken alone", dup, acct, err)
			}
		}
	}
	if n := len(f.accounts.Records()); n != before {
		t.Fatalf("a taken name wrote %d records", n-before)
	}

	// The cap: five on a, a sixth refused naming it.
	for _, name := range []string{"Brin", "Cael", "Dara", "Eryn"} {
		if _, err := f.store.CreateCharacter(ctx, a, name, "town", "plaza"); err != nil {
			t.Fatal(err)
		}
	}
	before = len(f.accounts.Records())
	_, err = f.store.CreateCharacter(ctx, a, "Finn", "town", "plaza")
	if !errors.Is(err, ErrRosterFull) || !strings.Contains(err.Error(), "5") {
		t.Fatalf("sixth: err = %v, want ErrRosterFull naming 5", err)
	}
	if n := len(f.accounts.Records()); n != before {
		t.Fatal("a refused sixth wrote something")
	}
	if _, err := f.store.CreateCharacter(ctx, b, "Finn", "town", "plaza"); err != nil {
		t.Fatalf("the name a full roster was refused is still free: %v", err)
	}
	if got := f.store.Characters(a); len(got) != 5 || got[0].GetCharacterId() > got[1].GetCharacterId() {
		t.Fatalf("roster %v", got)
	}

	m := f.metricsText()
	for _, want := range []string{
		`andara_character_creations_total{outcome="ok"} 6`,
		`andara_character_creations_total{outcome="name_invalid"} 6`,
		`andara_character_creations_total{outcome="name_taken"} 10`,
		`andara_character_creations_total{outcome="roster_full"} 1`,
	} {
		if !strings.Contains(m, want) {
			t.Errorf("metrics lack %s", want)
		}
	}
}

// The roster and the reservations survive a restart, and a reservation
// with no Character behind it — the crash between the two writes — still
// holds the name.
func TestCharacters_SurviveRestart(t *testing.T) {
	f := newFixture(t, nil)
	ctx := context.Background()
	opCtx, _ := f.bootstrapOperator("oper", "operator-password")
	a, _ := f.store.CreateAccount(opCtx, "alice", "correct horse battery", nil)
	ref, err := f.store.CreateCharacter(ctx, a, "Aldric", "town", "plaza")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.SetCharacterPosition(ctx, a, ref.GetCharacterId(), "town", "hall"); err != nil {
		t.Fatal(err)
	}
	// An orphaned reservation, as a crash would leave it.
	orphan := &accountsv1.NameReservation{CharacterId: "gone", AccountId: a}
	body, _ := proto.Marshal(&accountsv1.AccountRecord{Record: &accountsv1.AccountRecord_NameReservation{NameReservation: orphan}})
	if err := f.accounts.Append(ctx, NamePrefix+"ghost", body); err != nil {
		t.Fatal(err)
	}

	f.reopen(nil)
	got, err := f.store.Character(a, ref.GetCharacterId())
	if err != nil || got.GetRoomId() != "hall" {
		t.Fatalf("after restart: %v %v", got, err)
	}
	if _, err := f.store.CreateCharacter(ctx, a, "aldric", "town", "plaza"); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("reservation lost on restart: %v", err)
	}
	if _, err := f.store.CreateCharacter(ctx, a, "Ghost", "town", "plaza"); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("an orphaned reservation does not hold the name: %v", err)
	}
	if _, err := f.store.Character(a, "nope"); !errors.Is(err, ErrNoSuchCharacter) {
		t.Fatalf("unknown character: %v", err)
	}
	if err := f.store.SetCharacterPosition(ctx, a, "nope", "town", "hall"); !errors.Is(err, ErrNoSuchCharacter) {
		t.Fatalf("position of an unknown character: %v", err)
	}
}

// The cap and the pattern are configuration.
func TestCharacters_Options(t *testing.T) {
	f := newFixture(t, func(o *Options) { o.Characters = CharacterOptions{MaxPerAccount: 1, NamePattern: `^[a-z]{3}$`} })
	ctx := context.Background()
	opCtx, _ := f.bootstrapOperator("oper", "operator-password")
	a, _ := f.store.CreateAccount(opCtx, "alice", "correct horse battery", nil)
	if _, err := f.store.CreateCharacter(ctx, a, "Abc", "town", "plaza"); !errors.Is(err, ErrNameInvalid) {
		t.Fatalf("pattern not applied: %v", err)
	}
	if _, err := f.store.CreateCharacter(ctx, a, "abc", "town", "plaza"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.CreateCharacter(ctx, a, "def", "town", "plaza"); !errors.Is(err, ErrRosterFull) {
		t.Fatalf("cap not applied: %v", err)
	}
	if f.store.MaxCharacters() != 1 {
		t.Fatal("MaxCharacters")
	}
	if _, err := newRoster(CharacterOptions{NamePattern: "("}); err == nil {
		t.Fatal("a bad pattern compiled")
	}
}

// AW-SRV-032 AC-1, AC-2, AC-3: a deleted Character keeps its slot and its name
// until the purge frees the slot; the name is never freed.
func TestDeleteCharacter_KeepsSlotAndName(t *testing.T) {
	f := newFixture(t, func(o *Options) { o.Characters = CharacterOptions{MaxPerAccount: 2} })
	ctx := context.Background()
	opCtx, _ := f.bootstrapOperator("oper", "operator-password")
	a, _ := f.store.CreateAccount(opCtx, "alice", "correct horse battery", nil)
	b, _ := f.store.CreateAccount(opCtx, "bob", "correct horse battery", nil)
	one, err := f.store.CreateCharacter(ctx, a, "Aldric", "town", "plaza")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.CreateCharacter(ctx, a, "Brenna", "town", "plaza"); err != nil {
		t.Fatal(err)
	}

	f.clock.advance(24 * time.Hour)
	del, err := f.store.DeleteCharacter(ctx, a, one.GetCharacterId())
	if err != nil {
		t.Fatal(err)
	}
	if del.GetStatus() != accountsv1.CharacterStatus_CHARACTER_STATUS_DELETED || del.GetDeletedUnix() != f.clock.now().Unix() {
		t.Fatalf("deleted ref %v", del)
	}
	// Gone from the playable roster, present in the whole one.
	if _, err := f.store.Character(a, one.GetCharacterId()); !errors.Is(err, ErrNoSuchCharacter) {
		t.Fatalf("a deleted Character is selectable: %v", err)
	}
	if n := len(f.store.AllCharacters(a)); n != 2 {
		t.Fatalf("whole roster has %d entries, want 2", n)
	}
	if act, dead := f.store.RosterCounts(); act != 1 || dead != 1 {
		t.Fatalf("counts %d active, %d deleted", act, dead)
	}
	// Twice: already deleted is no such character.
	if _, err := f.store.DeleteCharacter(ctx, a, one.GetCharacterId()); !errors.Is(err, ErrNoSuchCharacter) {
		t.Fatalf("second delete: %v", err)
	}
	// Another Account's Character is not deletable by this one.
	if _, err := f.store.DeleteCharacter(ctx, b, one.GetCharacterId()); !errors.Is(err, ErrNoSuchCharacter) {
		t.Fatalf("delete across Accounts: %v", err)
	}
	// AC-1: still counts.
	if _, err := f.store.CreateCharacter(ctx, a, "Corin", "town", "plaza"); !errors.Is(err, ErrRosterFull) {
		t.Fatalf("create past the cap with a deleted Character: %v", err)
	}
	// AC-2: any letter case, any Account, before and after the purge.
	for _, name := range []string{"aldric", "ALDRIC"} {
		if _, err := f.store.CreateCharacter(ctx, b, name, "town", "plaza"); !errors.Is(err, ErrNameTaken) {
			t.Fatalf("%q before the purge: %v", name, err)
		}
	}

	// Not expired until deleted_unix + retention.
	if got := f.store.ExpiredCharacters(30 * 24 * time.Hour); len(got) != 0 {
		t.Fatalf("expired early: %v", got)
	}
	f.clock.advance(30 * 24 * time.Hour)
	got := f.store.ExpiredCharacters(30 * 24 * time.Hour)
	if len(got) != 1 || got[0].AccountID != a || got[0].Ref.GetCharacterId() != one.GetCharacterId() {
		t.Fatalf("expired %v", got)
	}
	marked, err := f.store.MarkPurged(ctx, a, one.GetCharacterId())
	if err != nil || !marked {
		t.Fatalf("mark purged: %v %v", marked, err)
	}
	if marked, err := f.store.MarkPurged(ctx, a, one.GetCharacterId()); err != nil || marked {
		t.Fatalf("second mark: %v %v", marked, err)
	}
	if got := f.store.ExpiredCharacters(30 * 24 * time.Hour); len(got) != 0 {
		t.Fatalf("a purged Character is produced again: %v", got)
	}
	if _, err := f.store.MarkPurged(ctx, a, "nope"); !errors.Is(err, ErrNoSuchCharacter) {
		t.Fatalf("mark of an unknown Character: %v", err)
	}

	// The purge frees the slot, never the name.
	for _, name := range []string{"Aldric", "aldric"} {
		if _, err := f.store.CreateCharacter(ctx, b, name, "town", "plaza"); !errors.Is(err, ErrNameTaken) {
			t.Fatalf("%q after the purge: %v", name, err)
		}
	}
	if _, err := f.store.CreateCharacter(ctx, a, "Corin", "town", "plaza"); err != nil {
		t.Fatalf("the purge did not free the slot: %v", err)
	}

	// All of it survives a restart (AC-7: the reservation lives on the topic).
	f.reopen(func(o *Options) { o.Characters = CharacterOptions{MaxPerAccount: 2} })
	if _, err := f.store.CreateCharacter(ctx, b, "ALDRIC", "town", "plaza"); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("reservation lost on restart: %v", err)
	}
	if act, dead := f.store.RosterCounts(); act != 2 || dead != 1 {
		t.Fatalf("counts after restart %d active, %d deleted", act, dead)
	}
}

// AC-8: a reservation with no Character is removed; one with a Character
// behind it, deleted or not, never is; the removal survives a restart.
func TestReclaimReservations(t *testing.T) {
	f := newFixture(t, nil)
	ctx := context.Background()
	opCtx, _ := f.bootstrapOperator("oper", "operator-password")
	a, _ := f.store.CreateAccount(opCtx, "alice", "correct horse battery", nil)
	live, _ := f.store.CreateCharacter(ctx, a, "Aldric", "town", "plaza")
	dead, _ := f.store.CreateCharacter(ctx, a, "Brenna", "town", "plaza")
	if _, err := f.store.DeleteCharacter(ctx, a, dead.GetCharacterId()); err != nil {
		t.Fatal(err)
	}
	// A crash between the two writes: the reservation, no Character.
	orphan := &accountsv1.NameReservation{CharacterId: "gone", AccountId: a}
	body, _ := proto.Marshal(&accountsv1.AccountRecord{Record: &accountsv1.AccountRecord_NameReservation{NameReservation: orphan}})
	if err := f.accounts.Append(ctx, NamePrefix+"ghost", body); err != nil {
		t.Fatal(err)
	}
	f.reopen(nil)
	if _, err := f.store.CreateCharacter(ctx, a, "Ghost", "town", "plaza"); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("an orphaned reservation holds the name until the sweep: %v", err)
	}

	removed, err := f.store.ReclaimReservations(ctx)
	if err != nil || len(removed) != 1 || removed[0] != "ghost" {
		t.Fatalf("reclaimed %v, %v", removed, err)
	}
	if removed, err := f.store.ReclaimReservations(ctx); err != nil || len(removed) != 0 {
		t.Fatalf("second sweep reclaimed %v, %v", removed, err)
	}
	for _, name := range []string{live.GetName(), dead.GetName()} {
		if _, err := f.store.CreateCharacter(ctx, a, name, "town", "plaza"); !errors.Is(err, ErrNameTaken) {
			t.Fatalf("%q lost its reservation: %v", name, err)
		}
	}

	f.reopen(nil)
	if _, err := f.store.CreateCharacter(ctx, a, "Ghost", "town", "plaza"); err != nil {
		t.Fatalf("the reclaimed name is not creatable after a restart: %v", err)
	}
	if _, err := f.store.CreateCharacter(ctx, a, "brenna", "town", "plaza"); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("a deleted Character's name was reclaimed: %v", err)
	}
}
