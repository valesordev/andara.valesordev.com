// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package content

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestBlobCache_RoundTrip(t *testing.T) {
	c := BlobCache{Dir: t.TempDir()}
	body := []byte("a zone definition")
	sum := sha256.Sum256(body)
	if err := c.Put(sum[:], body); err != nil {
		t.Fatal(err)
	}
	got, err := c.Get(sum[:])
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("got %q", got)
	}
}

// A hit is verified, not trusted. A cache that could serve a wrong body would
// make AC-7 false in the one case that matters — and the file name alone
// cannot tell a good entry from a truncated one.
func TestBlobCache_CorruptEntryIsAMissAndIsRemoved(t *testing.T) {
	dir := t.TempDir()
	c := BlobCache{Dir: dir}
	body := []byte("the real body")
	sum := sha256.Sum256(body)
	if err := c.Put(sum[:], body); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, hex.EncodeToString(sum[:]))
	if err := os.WriteFile(path, []byte("truncat"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := c.Get(sum[:]); !errors.Is(err, ErrCacheMiss) {
		t.Fatalf("a body that does not hash to its own key must be a miss, got %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("the bad entry should be removed so the next run does not pay for it again")
	}
}

func TestBlobCache_AbsentIsAMiss(t *testing.T) {
	c := BlobCache{Dir: t.TempDir()}
	sum := sha256.Sum256([]byte("never stored"))
	if _, err := c.Get(sum[:]); !errors.Is(err, ErrCacheMiss) {
		t.Fatalf("err = %v", err)
	}
}

// An empty Dir disables the cache rather than writing to the working
// directory, which is what a test exercising the cold path asks for.
func TestBlobCache_DisabledIsAlwaysAMiss(t *testing.T) {
	var c BlobCache
	body := []byte("x")
	sum := sha256.Sum256(body)
	if err := c.Put(sum[:], body); err != nil {
		t.Fatalf("put on a disabled cache should be a no-op, got %v", err)
	}
	if _, err := c.Get(sum[:]); !errors.Is(err, ErrCacheMiss) {
		t.Fatalf("err = %v", err)
	}
}
