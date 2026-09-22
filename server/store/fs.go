// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

// Package store implements sim.WorldStore: where snapshot objects live
// (AW-SRV-006). The interface is the sim's, because ADR-0002 makes persistence
// an adapter behind an interface the sim layer owns; the implementations are
// here, because server/sim reaches no filesystem and no network and `make lint`
// enforces it.
//
// Two implementations, one contract. `fs` is what `make up` and the tests run
// against and what AW-INF-003 mounts a volume for; `s3` is the cluster. Both
// make Put atomic — the key is visible with complete contents or not at all —
// because AC-5 turns on it: an interrupted write must leave the previous round
// as the newest complete one rather than a truncated object for AW-SRV-007 to
// find and trust.
package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/valesordev/andara/server/sim"
)

// TempSuffix is what an in-progress fs write is named until it is renamed into
// place. A listing skips it: a partial object is not an object (AC-5).
const TempSuffix = ".tmp"

// FS is a sim.WorldStore over a directory tree — the volume AW-INF-003 mounts,
// or a t.TempDir().
//
// Put writes {key}.tmp and renames. Rename within a filesystem is atomic, so a
// process killed between the write and the rename leaves a .tmp that List does
// not return and Get does not resolve, and the previous round stays the newest
// complete one. The data and then the containing directory are fsynced before
// the rename returns, because a rename that is durable while its target's
// contents are not is exactly the corruption this is meant to prevent: the key
// would survive a power loss naming a file of zeroes.
type FS struct {
	// Root is the directory keys are resolved under. Created on first Put.
	Root string
	// DirMode and FileMode default to 0o755 and 0o644 when zero.
	DirMode  os.FileMode
	FileMode os.FileMode
}

// NewFS returns a store rooted at dir.
func NewFS(dir string) *FS { return &FS{Root: dir} }

func (f *FS) dirMode() os.FileMode {
	if f.DirMode != 0 {
		return f.DirMode
	}
	return 0o755
}

func (f *FS) fileMode() os.FileMode {
	if f.FileMode != 0 {
		return f.FileMode
	}
	return 0o644
}

// path resolves a key under Root, refusing anything that would escape it.
//
// Keys are built by sim.SnapshotKey from a ZoneID, and a ZoneID comes from
// authored content: a Builder who names a Zone `../../etc` must not be able to
// choose where the server writes. The check is here rather than at the key's
// construction because this is the layer that touches a filesystem, and a
// store that is only safe when its caller validated first is a store that will
// eventually be called by something that did not.
func (f *FS) path(key string) (string, error) {
	if key == "" {
		return "", fmt.Errorf("store: empty key")
	}
	root, err := filepath.Abs(f.Root)
	if err != nil {
		return "", err
	}
	p := filepath.Join(root, filepath.FromSlash(key))
	if p != root && !strings.HasPrefix(p, root+string(os.PathSeparator)) {
		return "", fmt.Errorf("store: key %q escapes the store root", key)
	}
	return p, nil
}

// Put writes envelope at key, atomically. Implements sim.WorldStore.
func (f *FS) Put(ctx context.Context, key string, envelope []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	final, err := f.path(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(final), f.dirMode()); err != nil {
		return fmt.Errorf("%w: mkdir: %w", sim.ErrStoreUnavailable, err)
	}
	tmp := final + TempSuffix
	if err := f.writeSynced(tmp, envelope); err != nil {
		// Best-effort cleanup. If this fails too, the leftover is a .tmp,
		// which no listing returns and the next round overwrites.
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("%w: rename: %w", sim.ErrStoreUnavailable, err)
	}
	// The rename itself must reach the disk, or a power loss can leave the
	// directory entry pointing at neither name.
	return syncDir(filepath.Dir(final))
}

func (f *FS) writeSynced(path string, b []byte) error {
	fh, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.fileMode())
	if err != nil {
		return fmt.Errorf("%w: create: %w", sim.ErrStoreUnavailable, err)
	}
	if _, err := fh.Write(b); err != nil {
		_ = fh.Close()
		return fmt.Errorf("%w: write: %w", sim.ErrStoreUnavailable, err)
	}
	if err := fh.Sync(); err != nil {
		_ = fh.Close()
		return fmt.Errorf("%w: sync: %w", sim.ErrStoreUnavailable, err)
	}
	if err := fh.Close(); err != nil {
		return fmt.Errorf("%w: close: %w", sim.ErrStoreUnavailable, err)
	}
	return nil
}

// syncDir fsyncs a directory so a rename into it is durable. A directory that
// cannot be opened for reading is not fatal on every platform, so the failure
// is reported but the object is already written.
func syncDir(dir string) error {
	fh, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("%w: open dir: %w", sim.ErrStoreUnavailable, err)
	}
	defer func() { _ = fh.Close() }()
	if err := fh.Sync(); err != nil {
		return fmt.Errorf("%w: sync dir: %w", sim.ErrStoreUnavailable, err)
	}
	return nil
}

// Get reads the object at key. Implements sim.WorldStore.
func (f *FS) Get(ctx context.Context, key string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p, err := f.path(key)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil, fmt.Errorf("%w: %s", sim.ErrSnapshotNotFound, key)
	case err != nil:
		return nil, fmt.Errorf("%w: read: %w", sim.ErrStoreUnavailable, err)
	}
	return b, nil
}

// List returns a Zone's keys, newest first. Implements sim.WorldStore.
//
// Ordered by tick descending — the recency the caller means by "newest" — with
// the offset as a tiebreak that cannot actually tie, since a Zone gets one
// object per boundary. Parsed rather than sorted lexically, so a tree holding
// more than one state_version still answers correctly: a lexical sort would
// rank every version-2 object above every version-1 one regardless of which is
// further along.
//
// A Zone with no objects is an empty list and no error: a Zone that has never
// been snapshotted is a normal state at startup, not a missing store.
func (f *FS) List(ctx context.Context, zone sim.ZoneID) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dir, err := f.path(string(zone))
	if err != nil {
		return nil, err
	}
	var found []snapshotEntry
	// {zone}/{state_version}/{tick}/{offset}: walk the two levels beneath the
	// Zone and keep whatever parses as a key.
	versions, err := os.ReadDir(dir)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("%w: list: %w", sim.ErrStoreUnavailable, err)
	}
	for _, v := range versions {
		if !v.IsDir() {
			continue
		}
		ticks, err := os.ReadDir(filepath.Join(dir, v.Name()))
		if err != nil {
			return nil, fmt.Errorf("%w: list: %w", sim.ErrStoreUnavailable, err)
		}
		for _, tk := range ticks {
			if !tk.IsDir() {
				continue
			}
			objects, err := os.ReadDir(filepath.Join(dir, v.Name(), tk.Name()))
			if err != nil {
				return nil, fmt.Errorf("%w: list: %w", sim.ErrStoreUnavailable, err)
			}
			for _, o := range objects {
				if o.IsDir() || strings.HasSuffix(o.Name(), TempSuffix) {
					continue
				}
				key := strings.Join([]string{string(zone), v.Name(), tk.Name(), o.Name()}, "/")
				if e, ok := newSnapshotEntry(key); ok {
					found = append(found, e)
				}
			}
		}
	}
	return sortSnapshotKeys(found), nil
}

// snapshotEntry is one parsed key, for ordering a listing.
type snapshotEntry struct {
	key    string
	tick   sim.Tick
	offset int64
}

func newSnapshotEntry(key string) (snapshotEntry, bool) {
	_, _, tick, offset, ok := sim.ParseSnapshotKey(key)
	if !ok {
		return snapshotEntry{}, false
	}
	return snapshotEntry{key: key, tick: tick, offset: offset}, true
}

// sortSnapshotKeys orders newest first: by tick, then by offset. Shared by both
// stores so `snapshot list` reads the same against a volume and a bucket.
func sortSnapshotKeys(found []snapshotEntry) []string {
	sort.Slice(found, func(i, j int) bool {
		if found[i].tick != found[j].tick {
			return found[i].tick > found[j].tick
		}
		return found[i].offset > found[j].offset
	})
	keys := make([]string, len(found))
	for i, e := range found {
		keys[i] = e.key
	}
	return keys
}
