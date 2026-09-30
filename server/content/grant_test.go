// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package content

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/valesordev/andara/server/auth"
	"github.com/valesordev/andara/server/recordlog"
)

// AW-SRV-035 from the side that reads the grant: a Pack Grant made through
// the Account store is what AW-SRV-013's publish path authorizes on, with
// the real store as its PackHolder rather than a map.
func TestPackGrant_IsWhatThePublishPathAuthorizesOn(t *testing.T) {
	accounts := recordlog.NewMemory()
	openStore := func() *auth.Store {
		kr, err := auth.ParseKeyring(strings.NewReader("k1: " + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)) + "\n"))
		if err != nil {
			t.Fatal(err)
		}
		s, err := auth.Open(context.Background(), auth.Options{
			Accounts: accounts, Audit: recordlog.NewMemory(), Keys: kr,
			Argon2:     auth.Argon2Params{MemoryKiB: 64, Time: 1, Threads: 1},
			SessionTTL: time.Hour, RefreshTTL: time.Hour, InviteTTL: time.Hour,
		})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	store := openStore()
	op := auth.WithPrincipal(context.Background(), auth.Principal{AccountID: "op", Roles: []auth.Role{auth.RoleOperator}})
	id, err := store.CreateAccount(op, "alice", "correct horse battery", []auth.Role{auth.RoleBuilder})
	if err != nil {
		t.Fatal(err)
	}
	asAlice := auth.WithPrincipal(context.Background(), auth.Principal{AccountID: id, Roles: []auth.Role{auth.RoleBuilder}})

	var holder packHolderFunc = func(a string) []string { return store.BuilderPacks(a) }
	h := newPubHarness(t, func(ao *AdminOptions, _ *LoaderOptions) { ao.Accounts = holder })

	// Before any grant, a Builder holds nothing.
	if _, err := h.publish(asAlice, "town", townFiles(t)); reasonOf(err) != ErrReasonPackNotHeld {
		t.Fatalf("before the grant: %v", err)
	}

	// AC-1: granted town and docks, the publish of town passes.
	if _, err := store.SetBuilderPacks(op, id, []string{"town", "docks"}, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := h.publish(asAlice, "town", townFiles(t)); err != nil {
		t.Fatalf("a granted publish: %v", err)
	}
	// AC-2: a pack not granted is PERMISSION_DENIED pack_not_held.
	if _, err := h.publish(asAlice, "wilds", map[string][]byte{"src/pack.aw": []byte("pack wilds requires andara.core@1\n")}); reasonOf(err) != ErrReasonPackNotHeld || codeOf(err) != CodePermissionDenied {
		t.Errorf("an ungranted pack: %v", err)
	}

	// AC-10: after a restart the grant still authorizes.
	store = openStore()
	if _, err := h.publish(asAlice, "town", townFiles(t)); err != nil {
		t.Errorf("after a restart: %v", err)
	}

	// AC-4: cleared, town is refused again.
	if _, err := store.SetBuilderPacks(op, id, nil, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := h.publish(asAlice, "town", townFiles(t)); reasonOf(err) != ErrReasonPackNotHeld {
		t.Errorf("after clearing: %v", err)
	}
}

type packHolderFunc func(string) []string

func (f packHolderFunc) BuilderPacks(id string) []string { return f(id) }

func reasonOf(err error) string {
	var ae *AdminError
	if errors.As(err, &ae) {
		return ae.Reason
	}
	return ""
}

func codeOf(err error) Code {
	var ae *AdminError
	if errors.As(err, &ae) {
		return ae.Code
	}
	return -1
}
