// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package lang

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Conformance runs the AW-CLI-005 corpus against this compiler: every valid
// pair byte for byte, every invalid pair against its .errors sidecar, and every
// round-trip pair an identity (AC-1).
//
// It is the corpus's half of ADR-0009's compatibility mechanism. Protobuf
// compatibility is machine-checkable with `buf breaking` and language
// compatibility is not — unless there is a corpus of source that must keep
// compiling, checked by something other than the compiler itself. Every
// expected output here was hand-authored; none of it came from this code.

// CaseResult is one corpus case's outcome.
type CaseResult struct {
	Case    string   `json:"case"`
	Kind    string   `json:"kind"` // valid | invalid | roundtrip | pending
	OK      bool     `json:"ok"`
	Skipped bool     `json:"skipped,omitempty"`
	Gating  string   `json:"gating,omitempty"` // the story a pending case waits on
	Reasons []string `json:"reasons,omitempty"`
}

// Report is what `make content-conformance` writes to content-conformance.json
// and what CI publishes per run.
type Report struct {
	Cases   []CaseResult `json:"cases"`
	Passed  int          `json:"passed"`
	Failed  int          `json:"failed"`
	Skipped int          `json:"skipped"`
}

// Conformance runs every case under corpusDir.
func Conformance(corpusDir string) (*Report, error) {
	core, err := loadCorpusCore(corpusDir)
	if err != nil {
		return nil, err
	}
	rep := &Report{}
	groups := []struct{ kind, dir string }{
		{"valid", filepath.Join("valid")},
		{"invalid", filepath.Join("invalid", "semantic")},
		{"invalid", filepath.Join("invalid", "encoding")},
		{"roundtrip", filepath.Join("roundtrip")},
		{"pending", filepath.Join("pending")},
	}
	for _, g := range groups {
		cases, err := caseDirs(filepath.Join(corpusDir, g.dir))
		if err != nil {
			return nil, err
		}
		for _, c := range cases {
			rep.add(runCase(g.kind, c, core))
		}
	}
	// invalid/syntax/ is flat files rather than directories: those never reach
	// a resolver, so a case that were a pack could not be one (corpus README).
	files, err := filepath.Glob(filepath.Join(corpusDir, "invalid", "syntax", "*.aw"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	for _, f := range files {
		rep.add(runSyntaxCase(f))
	}
	sort.Slice(rep.Cases, func(i, j int) bool { return rep.Cases[i].Case < rep.Cases[j].Case })
	return rep, nil
}

func (rep *Report) add(c CaseResult) {
	rep.Cases = append(rep.Cases, c)
	switch {
	case c.Skipped:
		rep.Skipped++
	case c.OK:
		rep.Passed++
	default:
		rep.Failed++
	}
}

// loadCorpusCore compiles corpus/valid/core/ so every other case resolves
// against it as andara.core@1, which is what the corpus README says they do.
// Compiling it rather than reading content/core/ is deliberate: it makes the
// corpus self-contained, and valid/core/ is separately held byte-identical to
// the shipped seed.
func loadCorpusCore(corpusDir string) (*Pack, error) {
	out, ds := Compile(filepath.Join(corpusDir, "valid", "core"), nil, nil)
	if out == nil {
		return nil, fmt.Errorf("corpus/valid/core does not compile: %v", ds)
	}
	return &Pack{Name: CorePack, Version: 1, Templates: out.Templates}, nil
}

func caseDirs(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, filepath.Join(root, e.Name()))
		}
	}
	sort.Strings(out)
	return out, nil
}

func runCase(kind, dir string, core *Pack) CaseResult {
	name := caseName(dir)
	res := CaseResult{Case: name, Kind: kind}

	if kind == "pending" {
		// A pending case is a specified construct waiting on a protobuf field,
		// not a failing case and not a skipped test (semantics.md §9). It is
		// skipped here, printing the story that unblocks it, and the day the
		// field lands that story moves the case into valid/.
		res.Skipped = true
		res.Gating = pendingStory(dir)
		return res
	}

	// The core pack is its own case and resolves against nothing.
	var against *Pack
	if name != "valid/core" {
		against = core
	}
	out, ds := CompileOpts(dir, against, nil, Options{Pack: corpusPack(name)})

	wantErrs, haveSidecar := readSidecar(dir)
	if haveSidecar {
		res.Reasons = append(res.Reasons, diffFindings(wantErrs, ds)...)
	} else if len(ds) > 0 {
		for _, d := range ds {
			res.Reasons = append(res.Reasons, "unexpected "+d.Code+" at "+d.File+":"+strconv.Itoa(d.Line)+":"+strconv.Itoa(d.Col))
		}
	}

	expectedDir := filepath.Join(dir, "expected")
	if _, err := os.Stat(expectedDir); err == nil {
		if out == nil {
			res.Reasons = append(res.Reasons, "expected output, but the compile failed")
		} else {
			res.Reasons = append(res.Reasons, diffExpected(expectedDir, out)...)
		}
	}

	if kind == "roundtrip" && out != nil {
		res.Reasons = append(res.Reasons, diffRoundTrip(dir, out)...)
	}

	res.OK = len(res.Reasons) == 0
	return res
}

func runSyntaxCase(path string) CaseResult {
	res := CaseResult{Case: "invalid/syntax/" + strings.TrimSuffix(filepath.Base(path), ".aw"), Kind: "invalid"}
	b, err := os.ReadFile(path)
	if err != nil {
		res.Reasons = append(res.Reasons, err.Error())
		return res
	}
	_, ds := ParseFile(filepath.Base(path), string(b))
	want, err := os.ReadFile(strings.TrimSuffix(path, ".aw") + ".errors")
	if err != nil {
		res.Reasons = append(res.Reasons, err.Error())
		return res
	}
	res.Reasons = diffFindings(parseSidecar(string(want)), ds)
	res.OK = len(res.Reasons) == 0
	return res
}

// corpusPack is the pack name a corpus case is compiled as.
//
// pack_mismatch compares the declaration against "the pack being compiled"
// (semantics.md §2), which is an input Compile cannot take from a directory:
// the corpus compiles `pack p` out of a directory called `minimal` and
// `pack andara.core` out of one called `core`, so the directory's name is not
// it. What the corpus does have is a convention — every case is `p` except the
// two anchors, which are named after their packs — and this encodes it so that
// invalid/semantic/pack-mismatch/, which declares `elsewhere`, is checked
// rather than skipped.
//
// It is a convention read off the corpus rather than one the corpus states.
// docs/feedback/AW-CLI-006-content-language-compiler.md §5 asks for a marker
// file, as corpus/pending/ already has for its gating story, so that the
// harness stops inferring this.
func corpusPack(name string) string {
	switch name {
	case "valid/core":
		return CorePack
	case "valid/town":
		return "town"
	default:
		return "p"
	}
}

func caseName(dir string) string {
	parts := strings.Split(filepath.ToSlash(dir), "/")
	if len(parts) >= 2 {
		return strings.Join(parts[len(parts)-2:], "/")
	}
	return dir
}

func pendingStory(dir string) string {
	b, err := os.ReadFile(filepath.Join(dir, "PENDING"))
	if err != nil {
		return "unknown"
	}
	line := strings.TrimSpace(strings.SplitN(string(b), "\n", 2)[0])
	if i := strings.IndexAny(line, " \t"); i > 0 {
		return line[:i]
	}
	return line
}

// finding is one line of a .errors sidecar with its chain.
type finding struct {
	file  string
	line  int
	col   int
	code  string
	chain []string
}

func (f finding) String() string {
	s := fmt.Sprintf("%s:%d:%d: %s", f.file, f.line, f.col, f.code)
	for _, c := range f.chain {
		s += "\n  " + c
	}
	return s
}

func readSidecar(dir string) ([]finding, bool) {
	b, err := os.ReadFile(filepath.Join(dir, "expected.errors"))
	if err != nil {
		return nil, false
	}
	return parseSidecar(string(b)), true
}

// parseSidecar reads the format errors.md §4 fixes: one finding per unindented
// line as `file:line:col: code`, its chain indented two spaces beneath, `#` a
// comment. No message text, deliberately — AC-3 compares code, position, and
// chain, and pinning wording would make every improvement to an error message a
// corpus change.
func parseSidecar(s string) []finding {
	var out []finding
	for _, line := range strings.Split(s, "\n") {
		trimmed := strings.TrimRight(line, "\r")
		switch {
		case trimmed == "" || strings.HasPrefix(trimmed, "#"):
			continue
		case strings.HasPrefix(trimmed, "  "):
			if len(out) > 0 {
				out[len(out)-1].chain = append(out[len(out)-1].chain, strings.TrimSpace(trimmed))
			}
			continue
		}
		head, code, ok := strings.Cut(trimmed, ": ")
		if !ok {
			continue
		}
		bits := strings.Split(head, ":")
		if len(bits) != 3 {
			continue
		}
		ln, _ := strconv.Atoi(bits[1])
		col, _ := strconv.Atoi(bits[2])
		out = append(out, finding{file: bits[0], line: ln, col: col, code: strings.TrimSpace(code)})
	}
	return out
}

func fromDiagnostics(ds []Diagnostic) []finding {
	out := make([]finding, 0, len(ds))
	for _, d := range ds {
		out = append(out, finding{file: d.File, line: d.Line, col: d.Col, code: d.Code, chain: d.Chain})
	}
	return out
}

// diffFindings compares in order, because findings are sorted and two compiles
// of the same source produce the same sequence (errors.md rule 5).
func diffFindings(want []finding, got []Diagnostic) []string {
	have := fromDiagnostics(got)
	var out []string
	for i := 0; i < len(want) || i < len(have); i++ {
		switch {
		case i >= len(have):
			out = append(out, "missing finding:\n"+want[i].String())
		case i >= len(want):
			out = append(out, "unexpected finding:\n"+have[i].String())
		case want[i].String() != have[i].String():
			out = append(out, "finding differs:\n  want "+strings.ReplaceAll(want[i].String(), "\n", "\n  ")+
				"\n  got  "+strings.ReplaceAll(have[i].String(), "\n", "\n  "))
		}
	}
	return out
}

// diffExpected compares the compiled blobs against the hand-authored expected
// tree, byte for byte.
func diffExpected(expectedDir string, out *Output) []string {
	want := map[string][]byte{}
	_ = filepath.WalkDir(expectedDir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".json") {
			return err
		}
		rel, _ := filepath.Rel(expectedDir, p)
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		want[filepath.ToSlash(rel)] = b
		return nil
	})

	got := map[string][]byte{}
	for _, b := range out.Blobs {
		if b.MediaType == BlobMediaType {
			got[b.Path] = b.Bytes
		}
	}

	var reasons []string
	for _, p := range sortedKeys(want) {
		g, ok := got[p]
		if !ok {
			reasons = append(reasons, "no blob emitted for "+p)
			continue
		}
		if string(g) != string(want[p]) {
			reasons = append(reasons, "blob differs: "+p+"\n"+unifiedish(want[p], g))
		}
	}
	for _, p := range sortedKeys(got) {
		if _, ok := want[p]; !ok {
			reasons = append(reasons, "unexpected blob "+p)
		}
	}
	return reasons
}

// diffRoundTrip is AC-4's compiled → source → compiled identity, and
// formatting.md AC-3's stronger claim for canonical source: a pack authored in
// canonical order decompiles to itself byte for byte.
func diffRoundTrip(dir string, out *Output) []string {
	files, err := Decompile(out)
	if err != nil {
		return []string{"decompile: " + err.Error()}
	}
	var reasons []string
	for _, name := range sortedKeys(files) {
		want, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			reasons = append(reasons, "decompile produced "+name+", which the case does not hold")
			continue
		}
		if string(want) != string(files[name]) {
			reasons = append(reasons, "decompiled source differs: "+name+"\n"+unifiedish(want, files[name]))
		}
	}
	return reasons
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// unifiedish renders the first differing line with a little context. A full
// diff library would be a dependency for a failure path.
func unifiedish(want, got []byte) string {
	w := strings.Split(string(want), "\n")
	g := strings.Split(string(got), "\n")
	for i := 0; i < len(w) || i < len(g); i++ {
		var wl, gl string
		if i < len(w) {
			wl = w[i]
		}
		if i < len(g) {
			gl = g[i]
		}
		if wl != gl {
			return fmt.Sprintf("  line %d:\n    want %q\n    got  %q", i+1, wl, gl)
		}
	}
	return "  (identical line by line; trailing bytes differ)"
}
