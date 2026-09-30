// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package lang

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSourceMapPlacesLoaderFindings is AW-CLI-002's finding-to-diagnostic
// mapping: a loader finding's chain lands at the position the compiler
// reports the same finding at, with the compiler's chain.
func TestSourceMapPlacesLoaderFindings(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("pack.aw", "pack p requires andara.core@1\n")
	write("z.aw", `zone z "Z" {
  fallback a

  room a "A" {
    exit north -> b
  }

  room b "B" {}
}
`)
	write("t.aw", "template T kind entity {}\n")
	out, ds := Compile(dir, corpusCore(t), nil)
	if out == nil {
		t.Fatalf("does not compile: %v", ds)
	}
	m := out.SourceMap

	for _, tc := range []struct {
		code      string
		chain     []string
		file      string
		line, col int
		wantChain []string
	}{
		{"missing_reverse_exit", []string{"z", "a", "north"}, "z.aw", 5, 5, []string{"z", "a", "north"}},
		{"unknown_room", []string{"z", "a", "north"}, "z.aw", 5, 19, []string{"z", "a", "north"}},
		{"orphan_room", []string{"z", "a"}, "z.aw", 4, 3, []string{"z", "a"}},
		{"duplicate_zone", []string{"z"}, "z.aw", 1, 1, []string{"z"}},
		// The loader names the missing Room; the compiler reports the Zone,
		// at its `fallback` reference.
		{"fallback_missing", []string{"z", "gone"}, "z.aw", 2, 12, []string{"z"}},
		{"chain_mismatch", []string{"p.T"}, "t.aw", 1, 1, []string{"p.T"}},
	} {
		d, ok := m.Place(tc.code, "m", tc.chain, SeverityWarning)
		if !ok {
			t.Errorf("%s %v: not placed", tc.code, tc.chain)
			continue
		}
		if d.File != tc.file || d.Line != tc.line || d.Col != tc.col || d.Code != tc.code || d.Severity != SeverityWarning ||
			len(d.Chain) != len(tc.wantChain) || (len(d.Chain) > 0 && d.Chain[len(d.Chain)-1] != tc.wantChain[len(tc.wantChain)-1]) {
			t.Errorf("%s %v placed as %+v", tc.code, tc.chain, d)
		}
	}

	for _, chain := range [][]string{nil, {"elsewhere"}, {"z", "nowhere"}, {"z", "a", "south"}} {
		if d, ok := m.Place("unknown_room", "m", chain, SeverityError); ok {
			t.Errorf("%v placed as %+v; a declaration the compile did not produce has no position", chain, d)
		}
	}
	var none *SourceMap
	if _, ok := none.Place("orphan_room", "m", []string{"z", "a"}, SeverityWarning); ok {
		t.Error("a nil map placed a finding")
	}
}
