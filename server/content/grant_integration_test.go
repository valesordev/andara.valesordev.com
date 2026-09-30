// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

//go:build integration

package content

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
	"google.golang.org/protobuf/proto"

	auditv1 "github.com/valesordev/andara/gen/go/andara/audit/v1"
	"github.com/valesordev/andara/internal/eventually"
	"github.com/valesordev/andara/server/auth"
	"github.com/valesordev/andara/server/recordlog"
)

// TestPackGrant_AgainstABroker is AW-SRV-035's integration test: the Account
// store and the audit log on Redpanda, and the publish path authorizing on
// the store. Grant, publish (AC-1), refuse an ungranted pack (AC-2), replace
// (AC-3), clear (AC-4), the audit record (AC-9) and a restart (AC-10).
func TestPackGrant_AgainstABroker(t *testing.T) {
	tp, auditTopic, cl := publishTopics(t)
	ctx := context.Background()
	accountsTopic := fmt.Sprintf("andara.test.grant.accounts.%d", time.Now().UnixNano())
	adm := kadm.NewClient(cl)
	if _, err := adm.CreateTopic(ctx, 6, 1, map[string]*string{"cleanup.policy": kadm.StringPtr("compact")}, accountsTopic); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = adm.DeleteTopics(context.Background(), accountsTopic) })

	b := &brokerPublish{t: t, tp: tp, auditTop: auditTopic, cl: cl}
	kr, err := auth.ParseKeyring(strings.NewReader("k1: " + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)) + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	var store *auth.Store
	openStore := func() {
		store, err = auth.Open(ctx, auth.Options{
			Accounts: b.open(accountsTopic, 0), Audit: b.open(auditTopic, 0), Keys: kr,
			Argon2:     auth.Argon2Params{MemoryKiB: 64, Time: 1, Threads: 1},
			SessionTTL: time.Hour, RefreshTTL: time.Hour, InviteTTL: time.Hour,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	openStore()
	b.holder = packHolderFunc(func(a string) []string { return store.BuilderPacks(a) })
	b.start()

	op := auth.WithPrincipal(ctx, auth.Principal{AccountID: "op", Roles: []auth.Role{auth.RoleOperator}})
	id, err := store.CreateAccount(op, "alice", "correct horse battery", []auth.Role{auth.RoleBuilder})
	if err != nil {
		t.Fatal(err)
	}
	asAlice := auth.WithPrincipal(ctx, auth.Principal{AccountID: id, Roles: []auth.Role{auth.RoleBuilder}})
	publish := func(pack string) error {
		_, err := publishThrough(b.admin, asAlice, pack, townFiles(t), b.reg.Newest(pack))
		return err
	}

	// AC-1 and AC-2.
	if _, err := store.SetBuilderPacks(op, id, []string{"town", "docks"}, 0); err != nil {
		t.Fatal(err)
	}
	if err := publish("town"); err != nil {
		t.Fatalf("a granted publish: %v", err)
	}
	if err := publish("wilds"); reasonOf(err) != ErrReasonPackNotHeld {
		t.Errorf("an ungranted pack: %v", err)
	}

	// AC-3, then AC-10: the replaced set is what a restarted store reads.
	if _, err := store.SetBuilderPacks(op, id, []string{"town"}, 0); err != nil {
		t.Fatal(err)
	}
	openStore()
	if got := store.BuilderPacks(id); !slices.Equal(got, []string{"town"}) {
		t.Fatalf("after a restart: %v", got)
	}
	if err := publish("town"); err != nil {
		t.Errorf("a publish after the restart: %v", err)
	}

	// AC-4.
	if _, err := store.SetBuilderPacks(op, id, nil, 0); err != nil {
		t.Fatal(err)
	}
	if err := publish("town"); reasonOf(err) != ErrReasonPackNotHeld {
		t.Errorf("after clearing: %v", err)
	}

	// AC-9, read back from the broker: one ok record per change, with the
	// before and after sets, polled per live-assertions.md.
	audit := b.open(auditTopic, 0)
	want := []string{"[] -> [docks town]", "[docks town] -> [town]", "[town] -> []"}
	eventually.Observed(t, 30*time.Second, "three set_builder_packs records on the audit topic", func() (bool, string) {
		var got []string
		err := audit.Replay(ctx, func(r recordlog.Record) error {
			var rec auditv1.AuditRecord
			if err := proto.Unmarshal(r.Value, &rec); err != nil {
				return err
			}
			if rec.GetAction() == auth.ActionSetBuilderPacks && rec.GetOutcome() == auth.AuditOK && rec.GetTarget() == id && rec.GetActorAccountId() == "op" {
				got = append(got, fmt.Sprintf("%v -> %v", rec.GetBuilderPacksBefore(), rec.GetBuilderPacksAfter()))
			}
			return nil
		})
		if err != nil {
			return false, err.Error()
		}
		slices.Sort(got)
		w := slices.Clone(want)
		slices.Sort(w)
		return slices.Equal(got, w), fmt.Sprint(got)
	})
}
