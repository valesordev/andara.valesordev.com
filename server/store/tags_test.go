// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package store_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/store"
)

// A round's tag is a zero-byte object beside each of its objects, readable
// back by name, and invisible to the listing rounds are discovered from
// (lifecycle.md, Round tags; AW-SRV-054's pre-stop writes it).
func TestTagRoundWritesATagEveryListingSkips(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fs := store.NewFS(t.TempDir())
	keys := []string{sim.SnapshotKey("town", 1, 42, 7), sim.SnapshotKey("wilds", 1, 42, 3)}
	for _, k := range keys {
		if err := fs.Put(ctx, k, []byte("body")); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.TagRound(ctx, fs, keys, "deploy:sha-0123456789ab"); err != nil {
		t.Fatalf("TagRound: %v", err)
	}
	// Idempotent: the same tag again is no error and no second name.
	if err := store.TagRound(ctx, fs, keys, "deploy:sha-0123456789ab"); err != nil {
		t.Fatalf("TagRound again: %v", err)
	}
	b, err := fs.Get(ctx, store.TagKey(keys[0], "deploy:sha-0123456789ab"))
	if err != nil || len(b) != 0 {
		t.Fatalf("tag object = %q, %v; want zero bytes", b, err)
	}
	tags, err := store.RoundTags(ctx, fs, keys)
	if err != nil || !reflect.DeepEqual(tags, []string{"deploy:sha-0123456789ab"}) {
		t.Fatalf("RoundTags = %v, %v", tags, err)
	}
	listed, err := fs.List(ctx, "town")
	if err != nil || !reflect.DeepEqual(listed, keys[:1]) {
		t.Fatalf("List(town) = %v, %v; want only the snapshot object", listed, err)
	}
}

// A tag on only some of a round's objects, a tagging cut short, does not name
// the round: a name counts when every object carries it (lifecycle.md, Round
// tags, ListSnapshotRounds' rule).
func TestRoundTagsReportsOnlyATagOnEveryObject(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fs := store.NewFS(t.TempDir())
	keys := []string{sim.SnapshotKey("town", 1, 42, 7), sim.SnapshotKey("wilds", 1, 42, 3)}
	if err := store.TagRound(ctx, fs, keys[1:], "rollback:42"); err != nil {
		t.Fatal(err)
	}
	if err := store.TagRound(ctx, fs, keys, "deploy:v1"); err != nil {
		t.Fatal(err)
	}
	if tags, err := store.RoundTags(ctx, fs, keys); err != nil || !reflect.DeepEqual(tags, []string{"deploy:v1"}) {
		t.Fatalf("RoundTags = %v, %v; want only the tag on every object", tags, err)
	}
	if tags, err := store.RoundTags(ctx, fs, keys[1:]); err != nil || !reflect.DeepEqual(tags, []string{"deploy:v1", "rollback:42"}) {
		t.Fatalf("RoundTags of the tagged object = %v, %v", tags, err)
	}
}

func TestTagRoundRefusesANameOutsideTheGrammar(t *testing.T) {
	t.Parallel()
	fs := store.NewFS(t.TempDir())
	for _, name := range []string{"deploy:", "release:v1", "deploy:a/b", "deploy:" + strings.Repeat("a", 201)} {
		if err := store.TagRound(context.Background(), fs, []string{sim.SnapshotKey("town", 1, 1, 1)}, name); !errors.Is(err, store.ErrTagName) {
			t.Errorf("TagRound(%q) = %v, want ErrTagName", name, err)
		}
	}
}
