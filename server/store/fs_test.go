// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/valesordev/andara/server/sim"
)

func TestFSPutGetRoundTrips(t *testing.T) {
	t.Parallel()
	s := NewFS(t.TempDir())
	key := sim.SnapshotKey("village", 1, 42)
	want := []byte("envelope bytes")
	if err := s.Put(context.Background(), key, want); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := s.Get(context.Background(), key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("Get = %q, want %q", got, want)
	}
}

func TestFSGetMissingIsNotFound(t *testing.T) {
	t.Parallel()
	s := NewFS(t.TempDir())
	_, err := s.Get(context.Background(), sim.SnapshotKey("village", 1, 7))
	if !errors.Is(err, sim.ErrSnapshotNotFound) {
		t.Fatalf("Get missing = %v, want ErrSnapshotNotFound", err)
	}
}

// AC-5: a write interrupted between the tmp file and the rename leaves nothing
// a listing returns, and the previous round stays the newest complete one.
func TestFSPartialWriteIsNotListedAndDoesNotShadowThePreviousRound(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s := NewFS(root)
	ctx := context.Background()

	complete := sim.SnapshotKey("village", 1, 10)
	if err := s.Put(ctx, complete, []byte("round one")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Simulate the kill: the tmp file for the next round exists, the rename
	// never happened.
	interrupted := sim.SnapshotKey("village", 1, 20)
	tmp := filepath.Join(root, filepath.FromSlash(interrupted)) + TempSuffix
	if err := os.WriteFile(tmp, []byte("half a round"), 0o644); err != nil {
		t.Fatalf("seed tmp: %v", err)
	}

	keys, err := s.List(ctx, "village")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) != 1 || keys[0] != complete {
		t.Fatalf("List = %v, want only %q", keys, complete)
	}
	if _, err := s.Get(ctx, interrupted); !errors.Is(err, sim.ErrSnapshotNotFound) {
		t.Fatalf("Get interrupted = %v, want ErrSnapshotNotFound", err)
	}
}

// A Put that replaces a key leaves no window in which the key reads short:
// the reader sees the old bytes or the new ones.
func TestFSPutIsAtomicOverAnExistingKey(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s := NewFS(root)
	ctx := context.Background()
	key := sim.SnapshotKey("village", 1, 10)
	if err := s.Put(ctx, key, []byte("old and rather long")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := s.Put(ctx, key, []byte("new")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := s.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "new" {
		t.Fatalf("Get = %q, want %q", got, "new")
	}
	entries, err := os.ReadDir(filepath.Join(root, "village", "1"))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("directory holds %d entries, want 1 (a leftover .tmp?): %v", len(entries), entries)
	}
}

func TestFSListIsNewestOffsetFirstAcrossStateVersions(t *testing.T) {
	t.Parallel()
	s := NewFS(t.TempDir())
	ctx := context.Background()
	// Written out of order, and the higher offset is under the *lower*
	// state_version: a lexical sort of the key strings would get this wrong.
	for _, w := range []struct {
		version uint32
		offset  int64
	}{{1, 30}, {2, 20}, {1, 10}} {
		if err := s.Put(ctx, sim.SnapshotKey("village", w.version, w.offset), []byte("x")); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	keys, err := s.List(ctx, "village")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []string{
		sim.SnapshotKey("village", 1, 30),
		sim.SnapshotKey("village", 2, 20),
		sim.SnapshotKey("village", 1, 10),
	}
	if len(keys) != len(want) {
		t.Fatalf("List = %v, want %v", keys, want)
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Fatalf("List = %v, want %v", keys, want)
		}
	}
}

// A Zone that has never been snapshotted is a normal startup state, not an
// unavailable store.
func TestFSListUnknownZoneIsEmpty(t *testing.T) {
	t.Parallel()
	s := NewFS(t.TempDir())
	keys, err := s.List(context.Background(), "nowhere")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) != 0 {
		t.Fatalf("List = %v, want empty", keys)
	}
}

// A ZoneID comes from authored content. A Builder must not be able to choose
// where the server writes by naming a Zone with a traversal.
func TestFSRefusesAKeyThatEscapesTheRoot(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "snapshots")
	s := NewFS(root)
	ctx := context.Background()
	for _, key := range []string{
		"../escaped/1/00000000000000000000",
		sim.SnapshotKey("../../etc", 1, 1),
		"",
	} {
		if err := s.Put(ctx, key, []byte("x")); err == nil {
			t.Fatalf("Put(%q) succeeded, want refusal", key)
		}
		if _, err := s.Get(ctx, key); err == nil {
			t.Fatalf("Get(%q) succeeded, want refusal", key)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "escaped")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a refused key still created a directory outside the root")
	}
}

// The key's zero-padding is what makes a lexical listing an offset-ordered
// listing, and it is permanent: changing the width renames every object.
func TestSnapshotKeyIsZeroPaddedToTwentyDigits(t *testing.T) {
	t.Parallel()
	got := sim.SnapshotKey("village", 1, 42)
	want := "village/1/00000000000000000042"
	if got != want {
		t.Fatalf("SnapshotKey = %q, want %q", got, want)
	}
}
