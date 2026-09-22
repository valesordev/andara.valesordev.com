// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package auth

import (
	"context"
	"errors"
	"strings"
	"testing"

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
