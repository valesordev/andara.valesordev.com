// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package core

import (
	"bytes"
	"testing"
)

// AW-SRV-013 AC-19: the core this build embeds is the one VERSIONS records
// for VERSION. A change to templates/ without a new VERSION and a new line in
// VERSIONS fails here, naming both digests, rather than at a boot that finds
// andara.core@N already in the store with other bytes (AC-16).
func TestEmbeddedCoreIsTheOneVERSIONSRecords(t *testing.T) {
	recorded, err := Recorded()
	if err != nil {
		t.Fatal(err)
	}
	v := Version()
	want, ok := recorded[v]
	if !ok {
		t.Fatalf("content/core/VERSIONS has no line for VERSION %d; add `%d %x`", v, v, Digest(Blobs()))
	}
	if got := Digest(Blobs()); !bytes.Equal(got, want) {
		t.Fatalf("the build embeds andara.core@%d with digest %x, and VERSIONS records %x for %d: "+
			"changing content/core/ needs a new VERSION and a new VERSIONS line, never a rewritten one", v, got, want, v)
	}
	for rv := range recorded {
		if rv > v {
			t.Errorf("VERSIONS records %d, past VERSION %d", rv, v)
		}
	}
	if n := len(Blobs()); n != 4 {
		t.Errorf("the core embeds %d templates, want 4", n)
	}
}
