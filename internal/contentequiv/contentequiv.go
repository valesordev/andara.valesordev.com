// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

// Package contentequiv is AW-CLI-002 AC-4's fixture set: one set of packs,
// each with the findings every runner must produce for it, compared by code,
// position and chain (errors.md §4). Message text is left out on purpose, as
// the corpus sidecars leave it out: wording improves, and a finding is the
// same finding when it does.
//
// Three runners hold themselves to it:
//   - the compiler, as `make content-conformance` runs it (lang.Conformance
//     for the corpus, lang.Compile for the dev fixture);
//   - `andara-cli content validate --path` (admin/cli);
//   - the publish gate, Admin.PublishVersion, with each finding placed back
//     on its source by the compiler's source map (server/content).
//
// None of them is compared with another directly. Each is compared with the
// fixture's expected findings, so a disagreement names the runner that moved.
package contentequiv

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/valesordev/andara/content/lang"
)

// Case is one pack and what every runner must report for it.
type Case struct {
	// Name is the case's path under the corpus, or "fixtures/town".
	Name string
	// Dir is the pack directory, relative to the repository root.
	Dir string
	// Pack is the pack the source declares.
	Pack string
	// Compiles is whether the source compiles, which is whether there is
	// anything to publish: the gate can only run a case that does.
	Compiles bool
	// Want is the expected findings, in Form, in order.
	Want []string
}

// Corpus is the conformance corpus, relative to the repository root.
const Corpus = "docs/specs/content-language/v1/corpus"

// DevFixture is the dev fixture's source, relative to the repository root:
// the Content Language that compiles to testdata/content/valid/, which
// AW-SRV-001's and AW-SRV-013's tests load (TestDevFixtureSourceMatchesTestContent).
const DevFixture = "content/fixtures/town"

// skipped are corpus cases that are not a pack a Builder could validate or
// publish as they stand, with the reason.
var skipped = map[string]string{
	// The corpus compiles every case as pack `p` (corpus README), and this
	// case declares `elsewhere` to prove pack_mismatch fires. `content
	// validate --path` has no pack name to compare with but the one the
	// source declares, since a directory's name is not its pack's.
	"invalid/semantic/pack-mismatch": "pack_mismatch needs a caller-supplied pack name",
	// andara.core is published by the server at boot and never by a Builder
	// (AW-SRV-013); the gate refuses it before validating.
	"valid/core": "andara.core is not publishable",
}

// Cases returns the fixture set: every valid and semantically invalid corpus
// case, and the dev fixture. root is the repository root.
func Cases(root string) ([]Case, error) {
	var out []Case
	for _, group := range []struct {
		dir      string
		compiles bool
	}{{"valid", true}, {"invalid/semantic", false}} {
		entries, err := os.ReadDir(filepath.Join(root, Corpus, group.dir))
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			name := group.dir + "/" + e.Name()
			if _, skip := skipped[name]; skip {
				continue
			}
			dir := filepath.Join(Corpus, group.dir, e.Name())
			want, err := readSidecar(filepath.Join(root, dir, "expected.errors"))
			if err != nil {
				return nil, err
			}
			pack := "p"
			if e.Name() == "town" {
				pack = "town" // the corpus's anchor, compiled as the pack it reproduces
			}
			out = append(out, Case{Name: name, Dir: dir, Pack: pack, Compiles: group.compiles, Want: want})
		}
	}
	want, err := readSidecar(filepath.Join(root, "internal", "contentequiv", "testdata", "town.errors"))
	if err != nil {
		return nil, err
	}
	out = append(out, Case{Name: "fixtures/town", Dir: DevFixture, Pack: "town", Compiles: true, Want: want})
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Form is one finding as errors.md §4 writes it: `file:line:col: code`, the
// chain indented beneath.
func Form(file string, line, col int, code string, chain []string) string {
	s := fmt.Sprintf("%s:%d:%d: %s", file, line, col, code)
	for _, c := range chain {
		s += "\n  " + c
	}
	return s
}

// FromDiagnostics is findings in Form.
func FromDiagnostics(ds []lang.Diagnostic) []string {
	out := make([]string, 0, len(ds))
	for _, d := range ds {
		out = append(out, Form(d.File, d.Line, d.Col, d.Code, d.Chain))
	}
	return out
}

// Diff reports how got differs from want, in order, or nil.
func Diff(want, got []string) []string {
	var out []string
	for i := 0; i < len(want) || i < len(got); i++ {
		switch {
		case i >= len(got):
			out = append(out, "missing:\n"+want[i])
		case i >= len(want):
			out = append(out, "unexpected:\n"+got[i])
		case want[i] != got[i]:
			out = append(out, "differs:\nwant "+want[i]+"\ngot  "+got[i])
		}
	}
	return out
}

// readSidecar reads an .errors file; a missing one is no findings.
func readSidecar(path string) ([]string, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	type finding struct {
		file, code string
		line, col  int
		chain      []string
	}
	var fs []finding
	for _, line := range strings.Split(string(b), "\n") {
		switch {
		case line == "" || strings.HasPrefix(line, "#"):
			continue
		case strings.HasPrefix(line, "  "):
			if len(fs) == 0 {
				return nil, fmt.Errorf("%s: a chain line before any finding", path)
			}
			fs[len(fs)-1].chain = append(fs[len(fs)-1].chain, strings.TrimSpace(line))
			continue
		}
		head, code, ok := strings.Cut(line, ": ")
		bits := strings.Split(head, ":")
		if !ok || len(bits) != 3 {
			return nil, fmt.Errorf("%s: %q is not `file:line:col: code`", path, line)
		}
		ln, err1 := strconv.Atoi(bits[1])
		col, err2 := strconv.Atoi(bits[2])
		if err1 != nil || err2 != nil {
			return nil, fmt.Errorf("%s: %q is not `file:line:col: code`", path, line)
		}
		fs = append(fs, finding{file: bits[0], line: ln, col: col, code: strings.TrimSpace(code)})
	}
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, Form(f.file, f.line, f.col, f.code, f.chain))
	}
	return out, nil
}
