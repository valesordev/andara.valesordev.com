// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package contentequiv

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/valesordev/andara/content/core"
	"github.com/valesordev/andara/content/lang"
)

const root = "../.."

// TestCompilerAgrees is AC-4's first runner: the compiler, as `make
// content-conformance` runs it. Every corpus case in the set passes the
// conformance run, which compares its findings with the same sidecar Cases
// reads, and the dev fixture compiles to its expected findings.
func TestCompilerAgrees(t *testing.T) {
	cases, err := Cases(root)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := lang.Conformance(filepath.Join(root, Corpus))
	if err != nil {
		t.Fatal(err)
	}
	results := map[string]lang.CaseResult{}
	for _, c := range rep.Cases {
		results[c.Case] = c
	}
	embedded, err := lang.PackFromBlobs(core.Pack, uint32(core.Version()), core.Blobs())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			if c.Dir == DevFixture {
				out, ds := lang.Compile(filepath.Join(root, c.Dir), embedded, nil)
				if (out != nil) != c.Compiles {
					t.Fatalf("compiled = %t, want %t: %v", out != nil, c.Compiles, ds)
				}
				for _, d := range Diff(c.Want, FromDiagnostics(ds)) {
					t.Error(d)
				}
				return
			}
			r, ok := results[caseKey(c.Name)]
			if !ok {
				t.Fatalf("the conformance run has no case %s", caseKey(c.Name))
			}
			if !r.OK {
				t.Errorf("the conformance run disagrees with the sidecar: %v", r.Reasons)
			}
		})
	}
	if len(cases) < 40 {
		t.Errorf("the fixture set has %d cases; the corpus alone has more than 40", len(cases))
	}
}

// caseKey is a case's name as lang.Conformance reports it.
func caseKey(name string) string {
	return strings.TrimPrefix(name, "invalid/")
}
