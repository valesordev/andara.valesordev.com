// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

// Package core is the andara.core pack this build ships: its compiled
// Templates, the version the build numbers them (VERSION), and every core
// ever shipped (VERSIONS). The server publishes it at boot (AW-SRV-013,
// ADR-0004 and ADR-0010 §8, amended 2026-09-28), and andara-cli embeds the
// same (AW-CLI-002).
//
// VERSIONS is append-only: one line per core ever shipped, `<N> <digest>`.
// Rewriting a line would let two builds publish different bytes as the same
// andara.core@N, which the server refuses at boot, but only after it has
// shipped. The test in this package holds the embedded digest to the line
// for VERSION, so a change to templates/ without a new VERSION and a new
// line fails `make check`.
package core

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"slices"
	"strconv"
	"strings"
)

// Pack is the core pack's ID.
const Pack = "andara.core"

//go:embed VERSION VERSIONS templates/*.json
var files embed.FS

// Version is the core version this build ships: content/core/VERSION.
func Version() uint64 {
	b, err := files.ReadFile("VERSION")
	if err != nil {
		panic(err) // embedded; absent only if the build is broken
	}
	v, err := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
	if err != nil {
		panic(fmt.Sprintf("content/core/VERSION is not a version: %q", b))
	}
	return v
}

// Blobs is the pack's blobs by published path (templates/<name>.json).
func Blobs() map[string][]byte {
	out := map[string][]byte{}
	entries, err := fs.Glob(files, "templates/*.json")
	if err != nil {
		panic(err)
	}
	for _, p := range entries {
		b, err := files.ReadFile(p)
		if err != nil {
			panic(err)
		}
		out[p] = b
	}
	return out
}

// Digest is the sha256 of the sorted list of the blobs' sha256 hashes: the
// value the audit record's blob_hashes_sha256 carries, and what a stored
// andara.core@N is compared with.
func Digest(blobs map[string][]byte) []byte {
	hashes := make([][]byte, 0, len(blobs))
	for _, b := range blobs {
		h := sha256.Sum256(b)
		hashes = append(hashes, h[:])
	}
	slices.SortFunc(hashes, bytes.Compare)
	h := sha256.New()
	for _, x := range hashes {
		h.Write(x)
	}
	return h.Sum(nil)
}

// Recorded parses VERSIONS: each version's digest, as shipped.
func Recorded() (map[uint64][]byte, error) {
	b, err := files.ReadFile("VERSIONS")
	if err != nil {
		return nil, err
	}
	out := map[uint64][]byte{}
	var last uint64
	for i, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			return nil, fmt.Errorf("VERSIONS line %d: want `<N> <digest-hex>`, got %q", i+1, line)
		}
		v, err := strconv.ParseUint(f[0], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("VERSIONS line %d: %q is not a version", i+1, f[0])
		}
		if v <= last {
			return nil, fmt.Errorf("VERSIONS line %d: version %d does not follow %d", i+1, v, last)
		}
		d, err := hex.DecodeString(f[1])
		if err != nil || len(d) != sha256.Size {
			return nil, fmt.Errorf("VERSIONS line %d: %q is not a sha256", i+1, f[1])
		}
		out[v], last = d, v
	}
	return out, nil
}
