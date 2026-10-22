// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package store_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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

// The grammar allows a tag ending in .tmp. On the fs store it is a tag like
// any other: reported, and not taken for (or clobbered by) the temporary file
// of another tag's write (PR #565 review).
func TestFSTagsEndingInTmpAreTags(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fs := store.NewFS(t.TempDir())
	keys := []string{sim.SnapshotKey("town", 1, 42, 7)}
	if err := store.TagRound(ctx, fs, keys, "deploy:v1.tmp"); err != nil {
		t.Fatal(err)
	}
	if tags, err := store.RoundTags(ctx, fs, keys); err != nil || !reflect.DeepEqual(tags, []string{"deploy:v1.tmp"}) {
		t.Fatalf("RoundTags = %v, %v; want the .tmp tag", tags, err)
	}
	if err := store.TagRound(ctx, fs, keys, "deploy:v1"); err != nil {
		t.Fatal(err)
	}
	if tags, err := store.RoundTags(ctx, fs, keys); err != nil || !reflect.DeepEqual(tags, []string{"deploy:v1", "deploy:v1.tmp"}) {
		t.Fatalf("after tagging deploy:v1, RoundTags = %v, %v; want both tags", tags, err)
	}
}

// A tag write cut short leaves its temporary file in the tag directory; it is
// not a tag, so neither ListTags nor RoundTags reports it.
func TestFSInterruptedTagWriteIsNotATag(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := t.TempDir()
	fs := store.NewFS(root)
	keys := []string{sim.SnapshotKey("town", 1, 42, 7)}
	if err := store.TagRound(ctx, fs, keys, "deploy:v1"); err != nil {
		t.Fatal(err)
	}
	tmp := filepath.Join(root, filepath.FromSlash(store.TagKey(keys[0], "deploy:v2"))) + store.TempSuffix
	if err := os.WriteFile(tmp, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if names, err := fs.ListTags(ctx, keys[0]); err != nil || !reflect.DeepEqual(names, []string{"deploy:v1"}) {
		t.Fatalf("ListTags = %v, %v; want only deploy:v1", names, err)
	}
	if tags, err := store.RoundTags(ctx, fs, keys); err != nil || !reflect.DeepEqual(tags, []string{"deploy:v1"}) {
		t.Fatalf("RoundTags = %v, %v; want only deploy:v1", tags, err)
	}
}

// No tag name can end in the fs store's temporary suffix: the suffix holds a
// character outside the tag grammar.
func TestFSTempSuffixIsOutsideTheTagGrammar(t *testing.T) {
	t.Parallel()
	if store.TagNamePattern.MatchString("deploy:v1" + store.TempSuffix) {
		t.Fatalf("deploy:v1%s is a legal tag name, so a tag can be mistaken for a temporary file", store.TempSuffix)
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
