// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package lang

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The cases in this file come from the review of PR #57. Each one is a hole the
// corpus does not reach — the corpus is well-formed content plus deliberate
// single mistakes, and these are mostly interactions — so each is pinned here
// rather than by a new corpus case, which would be architecture's to add.

// pack compiles an ad-hoc pack from an in-memory file set.
func pack(t *testing.T, files map[string]string) (*Output, []Diagnostic) {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		write(t, p, body)
	}
	return Compile(dir, corpusCore(t), nil)
}

// TestDuplicateFieldInOneComponent: a field stated twice has no answer to
// "which one is it", and every answer is silent. The loader refuses it
// (sim.validateComponentFields) and the Template merge would let the later
// value win, so without this the compiler either emits content the server
// refuses or hides the ambiguity.
func TestDuplicateFieldInOneComponent(t *testing.T) {
	for _, tc := range []struct{ name, file, body string }{
		{"on a Template", "t.aw", "template T kind entity {\n  component andara.core.Behavior { name: \"a\" name: \"b\" }\n}\n"},
		{"on a Room", "z.aw", "zone z \"Z\" {\n  room r \"R\" {\n    component andara.core.Behavior { name: \"a\" name: \"b\" }\n  }\n  fallback r\n}\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, ds := pack(t, map[string]string{
				"pack.aw": "pack p requires andara.core@1\n",
				tc.file:   tc.body,
			})
			if out != nil {
				t.Fatalf("a repeated field compiled clean: %v", ds)
			}
			if len(ds) != 1 || ds[0].Code != CodeInvalidComponentField {
				t.Fatalf("want one invalid_component_field, got %v", ds)
			}
			// At the second occurrence: the first is the one that could stand.
			if ds[0].Line != 2 && ds[0].Line != 3 {
				t.Errorf("unexpected line %d", ds[0].Line)
			}
			if !strings.Contains(ds[0].Message, "twice") {
				t.Errorf("message does not say what is wrong: %q", ds[0].Message)
			}
		})
	}
}

// TestDecompileShareAFileRatherThanOverwrite: the language has no reserved
// words, so `zone pack "Pack"` is legal and lands on the canonical layout's
// pack.aw. Overwriting the map entry dropped the pack declaration and produced
// source that cannot be recompiled.
func TestDecompileShareAFileRatherThanOverwrite(t *testing.T) {
	out, ds := pack(t, map[string]string{
		"pack.aw": "pack p requires andara.core@1\n",
		"z.aw":    "zone pack \"Pack\" {\n  room r \"R\" {}\n}\n",
	})
	if out == nil {
		t.Fatalf("compile: %v", ds)
	}
	files, err := Decompile(out)
	if err != nil {
		t.Fatal(err)
	}
	got := string(files["pack.aw"])
	if !strings.Contains(got, "pack p requires andara.core@1") {
		t.Errorf("the pack declaration was dropped:\n%s", got)
	}
	if !strings.Contains(got, `zone pack "Pack"`) {
		t.Errorf("the Zone was dropped:\n%s", got)
	}

	// What matters is that it recompiles.
	dir := t.TempDir()
	for name, body := range files {
		write(t, filepath.Join(dir, name), string(body))
	}
	again, ds := Compile(dir, corpusCore(t), nil)
	if again == nil {
		t.Fatalf("the decompiled pack does not recompile: %v", ds)
	}
	if len(again.Zones) != 1 || again.Zones[0].GetId() != "pack" {
		t.Errorf("the recompiled pack lost its Zone")
	}
}

// TestInvalidEscapeInEveryLiteral: a Zone name and a Room title are prose a
// Builder writes as readily as a desc. Without the check `zone z "bad\t"`
// compiled, and fmt re-quoting it from the decoded value turned it into
// `"bad\\t"` — a formatter changing what a string says, which is the one thing
// formatting.md promises it never does.
func TestInvalidEscapeInEveryLiteral(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		line, col  int
	}{
		{"Zone name", "zone z \"bad\\t\" {\n  room r \"R\" {}\n  fallback r\n}\n", 1, 12},
		{"Room title", "zone z \"Z\" {\n  room r \"bad\\q\" {}\n  fallback r\n}\n", 2, 14},
		{"desc", "zone z \"Z\" {\n  room r \"R\" {\n    desc \"a\\tb\"\n  }\n  fallback r\n}\n", 3, 12},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, ds := pack(t, map[string]string{
				"pack.aw": "pack p requires andara.core@1\n",
				"z.aw":    tc.body,
			})
			if out != nil {
				t.Fatalf("an invalid escape compiled clean: %v", ds)
			}
			if len(ds) != 1 || ds[0].Code != CodeInvalidEscape {
				t.Fatalf("want one invalid_escape, got %v", ds)
			}
			if ds[0].Line != tc.line || ds[0].Col != tc.col {
				t.Errorf("at %d:%d, want %d:%d", ds[0].Line, ds[0].Col, tc.line, tc.col)
			}
		})
	}

	// fmt reproduces the authored spelling rather than re-quoting the decoded
	// value, so the compiler still gets to report it.
	src := []byte("zone z \"bad\\t\" {\n  room r \"R\" {}\n}\n")
	got, ds := Format(src)
	if len(ds) > 0 {
		t.Fatal(ds[0])
	}
	if string(got) != string(src) {
		t.Errorf("fmt rewrote a literal it should have left alone:\n got %q\nwant %q", got, src)
	}
}

// TestFormatKeepsCommentsInsideTheirBlock: a comment between the last item and
// the closing brace fell out of the block and reappeared at the next
// declaration, and a comment-only block collapsed to `{}` with the comment
// moved outside. Both are fmt moving a comment across a declaration.
func TestFormatKeepsCommentsInsideTheirBlock(t *testing.T) {
	for _, tc := range []struct{ name, src string }{
		{"before a Zone's brace", "zone z \"Z\" {\n  room r \"R\" {}\n  // a note about the Zone\n}\n\ntemplate T kind entity {}\n"},
		{"a comment-only Zone", "zone z \"Z\" {\n  // nothing here yet\n}\n"},
		{"a comment-only Room", "zone z \"Z\" {\n  room r \"R\" {\n    // to be written\n  }\n}\n"},
		{"before a Template's brace", "template T kind entity {\n  component andara.core.Memory {}\n  // and more later\n}\n"},
		{"a comment-only Component", "template T kind entity {\n  component andara.core.Behavior {\n    // name goes here\n  }\n}\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ds := Format([]byte(tc.src))
			if len(ds) > 0 {
				t.Fatal(ds[0])
			}
			if string(got) != tc.src {
				t.Errorf("fmt moved a comment:\n--- got ---\n%s\n--- want ---\n%s", got, tc.src)
			}
			twice, _ := Format(got)
			if string(twice) != string(got) {
				t.Errorf("not idempotent:\n%s", twice)
			}
		})
	}
}

// TestCoreProvenanceNamesTheRealSetter: a core Template is already flattened
// and carries provenance naming the ancestor that actually set each field.
// Attributing its fields to the immediate core parent credits a Template for a
// value it inherited unchanged — the exact question provenance exists to
// answer, and the one `AW-CLI-002`'s `inspect template` prints.
func TestCoreProvenanceNamesTheRealSetter(t *testing.T) {
	coreOut, ds := pack(t, map[string]string{
		"pack.aw": "pack andara.core\n",
		"t.aw":    "template Base kind entity {\n  component andara.core.Behavior { name: \"core.idle\" }\n}\ntemplate Mid extends Base {}\n",
	})
	if coreOut == nil {
		t.Fatalf("the core pack does not compile: %v", ds)
	}
	core := &Pack{Name: CorePack, Version: 1, Templates: coreOut.Templates}

	dir := t.TempDir()
	write(t, filepath.Join(dir, "pack.aw"), "pack p requires andara.core@1\n")
	write(t, filepath.Join(dir, "t.aw"), "template Leaf extends andara.core.Mid {}\n")
	out, ds := Compile(dir, core, nil)
	if out == nil {
		t.Fatalf("compile: %v", ds)
	}
	prov := out.Templates[0].GetProvenance()
	if len(prov) != 1 {
		t.Fatalf("want one provenance entry, got %v", prov)
	}
	if got := prov[0].GetFrom(); got != "andara.core.Base" {
		t.Errorf("provenance names %q; andara.core.Mid inherited the value unchanged, so the setter is andara.core.Base", got)
	}
	// The loader checks that `from` is in the chain, so a wrong answer here is
	// also invalid_provenance at boot.
	var inChain bool
	for _, c := range out.Templates[0].GetChain() {
		if c == prov[0].GetFrom() {
			inChain = true
		}
	}
	if !inChain {
		t.Errorf("provenance names %q, which is not in the chain %v", prov[0].GetFrom(), out.Templates[0].GetChain())
	}
}

// TestEmptyDirectoryIsPackMissing: a --path that points somewhere a Builder did
// not mean reported success and would have published zero blobs.
func TestEmptyDirectoryIsPackMissing(t *testing.T) {
	out, ds := Compile(t.TempDir(), corpusCore(t), nil)
	if out != nil {
		t.Fatal("an empty directory compiled clean")
	}
	if len(ds) != 1 || ds[0].Code != CodePackMissing {
		t.Fatalf("want pack_missing, got %v", ds)
	}
}

// TestBareFallbackIsASyntaxError: the grammar is `"fallback" LOWER_ID`.
// Accepting a bare keyword let it compile clean and let fmt print it with a
// trailing space, which formatting.md §2 forbids outright.
func TestBareFallbackIsASyntaxError(t *testing.T) {
	_, ds := ParseFile("z.aw", "zone z \"Z\" {\n  fallback\n}\n")
	if len(ds) != 1 || ds[0].Code != CodeSyntax {
		t.Fatalf("want one syntax_error, got %v", ds)
	}
	if ds[0].Line != 3 || ds[0].Col != 1 {
		t.Errorf("at %d:%d, want 3:1 — the token that is there instead of a Room", ds[0].Line, ds[0].Col)
	}
}

// TestCompileIgnoresTheOutputTree: `--path . --out build` is the natural local
// layout, and a pack publishes its own sources under src/ — so without this the
// second run reads the first run's output back as pack input.
func TestCompileIgnoresTheOutputTree(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "pack.aw"), "pack p requires andara.core@1\n")
	write(t, filepath.Join(dir, "z.aw"), "zone z \"Z\" {\n  room r \"R\" {}\n}\n")

	// Simulate a previous run's output sitting inside the pack.
	if err := os.MkdirAll(filepath.Join(dir, "build", "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "build", "src", "pack.aw"), "pack p requires andara.core@1\n")

	if _, ds := Compile(dir, corpusCore(t), nil); len(ds) == 0 {
		t.Fatal("without Ignore the republished source should collide")
	}
	out, ds := CompileOpts(dir, corpusCore(t), nil, Options{Ignore: []string{"build"}})
	if out == nil {
		t.Fatalf("with the output tree ignored the pack should compile: %v", ds)
	}
}

// fallback_room (AW-SRV-012, errors.md §6): the first `fallback` is the
// field; a Zone that declares none is fallback_missing at its `zone` keyword,
// and one naming a Room the Zone lacks is fallback_missing at the Room id.
// Decompile writes the line back, so the field survives a round trip.
func TestFallbackRoom(t *testing.T) {
	out, ds := pack(t, map[string]string{
		"pack.aw": "pack p requires andara.core@1\n",
		"z.aw":    "zone z \"Z\" {\n  fallback b\n\n  room a \"A\" {}\n\n  room b \"B\" {}\n}\n",
	})
	if out == nil {
		t.Fatalf("compile: %v", ds)
	}
	if got := out.Zones[0].GetFallbackRoom(); got != "b" {
		t.Errorf("fallback_room = %q, want b", got)
	}
	files, err := DecompileWith(out, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(files["z.aw"]), "\n  fallback b\n") {
		t.Errorf("decompile dropped the fallback:\n%s", files["z.aw"])
	}

	for _, tc := range []struct {
		name, body string
		line, col  int
	}{
		{"declares none", "zone z \"Z\" {\n  room r \"R\" {}\n}\n", 1, 1},
		{"names no Room", "zone z \"Z\" {\n  fallback nowhere\n\n  room r \"R\" {}\n}\n", 2, 12},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, ds := pack(t, map[string]string{"pack.aw": "pack p requires andara.core@1\n", "z.aw": tc.body})
			if out != nil {
				t.Fatalf("compiled clean: %v", ds)
			}
			if len(ds) != 1 || ds[0].Code != CodeFallbackMissing || ds[0].Line != tc.line || ds[0].Col != tc.col ||
				len(ds[0].Chain) != 1 || ds[0].Chain[0] != "z" {
				t.Fatalf("want one fallback_missing at %d:%d chained to z, got %v", tc.line, tc.col, ds)
			}
		})
	}
}
