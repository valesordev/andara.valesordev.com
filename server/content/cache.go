// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package content

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// BlobCache is a content-addressed cache of blob bodies on local disk
// (content.cache_dir). Blobs are immutable and keyed by the hash of their own
// body, so a cache entry can never go stale — which is the whole reason the
// cache is safe to keep across restarts and across content versions.
//
// A hit is verified, not trusted. The file name is the hash, so re-hashing the
// body is the one check that distinguishes a good entry from a truncated or
// corrupted one, and it costs a sha256 of something already in memory. An
// entry that fails is removed and treated as a miss, because a cache that can
// serve a wrong body would make AC-7 — cold and cached resolve to byte-identical
// topologies — false in the one case that matters.
type BlobCache struct {
	// Dir is created on first Put. An empty Dir disables the cache: every
	// read is a miss and every write a no-op, which is what a test that wants
	// to exercise the cold path asks for.
	Dir string
}

// ErrCacheMiss is returned by Get when the cache does not hold the hash.
var ErrCacheMiss = errors.New("content: blob not cached")

// Get returns the cached body for hash, verifying it against the hash before
// returning it. ErrCacheMiss when absent, unreadable, or failing verification.
func (c BlobCache) Get(hash []byte) ([]byte, error) {
	if c.Dir == "" || len(hash) == 0 {
		return nil, ErrCacheMiss
	}
	path := c.path(hash)
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, ErrCacheMiss
	}
	sum := sha256.Sum256(body)
	if !equalHash(sum[:], hash) {
		// Not an error the caller can act on: the broker still has the blob.
		// Drop the entry so the next run does not pay for it again.
		_ = os.Remove(path)
		return nil, ErrCacheMiss
	}
	return body, nil
}

// Put stores body under its hash. A write failure is not an error the caller
// should care about — the blob is in hand either way — so Put reports one only
// for the benefit of a log line.
func (c BlobCache) Put(hash, body []byte) error {
	if c.Dir == "" || len(hash) == 0 {
		return nil
	}
	if err := os.MkdirAll(c.Dir, 0o755); err != nil {
		return fmt.Errorf("content: cache dir %s: %w", c.Dir, err)
	}
	path := c.path(hash)
	tmp, err := os.CreateTemp(c.Dir, ".blob-*")
	if err != nil {
		return fmt.Errorf("content: cache temp in %s: %w", c.Dir, err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return fmt.Errorf("content: cache write %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("content: cache close %s: %w", path, err)
	}
	// Rename into place so a reader never sees a partial body. Two processes
	// racing on the same hash write the same bytes, so the loser is harmless.
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("content: cache rename %s: %w", path, err)
	}
	return nil
}

func (c BlobCache) path(hash []byte) string {
	return filepath.Join(c.Dir, hex.EncodeToString(hash))
}

func equalHash(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
