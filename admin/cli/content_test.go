// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// The corpus is the AW-CLI-005 conformance corpus, reached from admin/cli.
// These tests are about the command surface — exit codes, rendering, the cache
// — and not about the compiler, which content/lang's own tests cover against
// the corpus case by case.
const corpusRoot = "../../docs/specs/content-language/v1/corpus"

func corpusCase(parts ...string) string {
	return filepath.Join(append([]string{corpusRoot}, parts...)...)
}

// contentEnv gives the CLI a private core-pack cache and populates it from the
// corpus's own andara.core, so that `content compile` resolves offline — which
// is the only way it ever resolves (AC-5).
func contentEnv(t *testing.T) map[string]string {
	t.Helper()
	cache := t.TempDir()
	env := isolatedEnv(t, map[string]string{"ANDARA_CONTENT_CACHE": cache})
	res := runCLI(t, []string{"content", "fetch-core", "--from", corpusCase("valid", "core"), "--version", "1"}, env)
	if res.exit != ExitOK {
		t.Fatalf("fetch-core exit=%d stderr=%q", res.exit, res.stderr)
	}
	return env
}

// TestContentCompileReportsEveryFinding is AC-2: stderr carries one line per
// finding as `file:line:col: CODE message` with the declaration chain indented
// beneath, and the command exits 1.
func TestContentCompileReportsEveryFinding(t *testing.T) {
	env := contentEnv(t)
	res := runCLI(t, []string{"content", "compile", "--path", corpusCase("invalid", "semantic", "multiple-findings")}, env)

	if res.exit != ExitFail {
		t.Errorf("exit=%d, want %d", res.exit, ExitFail)
	}
	// Every finding, not just the first: a Builder fixing ten broken exits
	// should need one compile, not ten (errors.md rule 4).
	for _, want := range []string{
		"a.aw:3:10: unknown_direction",
		"a.aw:4:19: unknown_room",
		"b.aw:2:13: unknown_component_type",
	} {
		if !strings.Contains(res.stderr, want) {
			t.Errorf("stderr does not carry %q:\n%s", want, res.stderr)
		}
	}
	// The chain is indented two spaces beneath its finding.
	if !strings.Contains(res.stderr, "unknown_room") || !strings.Contains(res.stderr, "\n  north\n") {
		t.Errorf("the Exit finding does not carry its chain:\n%s", res.stderr)
	}
	// Sorted by file, then line, then column (errors.md rule 5).
	if strings.Index(res.stderr, "a.aw:3:10") > strings.Index(res.stderr, "a.aw:4:19") {
		t.Error("findings are not sorted by line")
	}
	if strings.Index(res.stderr, "a.aw:4:19") > strings.Index(res.stderr, "b.aw:2:13") {
		t.Error("findings are not sorted by file")
	}
}

// TestContentCompileJSONIsOneEnvelope is AC-2's other half. errors.md §1 puts
// the findings inside AW-CLI-001's error envelope, so a consumer reading stdout
// with one Decode gets the whole answer — decodeJSON fails on trailing tokens,
// which is the assertion that matters here.
func TestContentCompileJSONIsOneEnvelope(t *testing.T) {
	env := contentEnv(t)
	res := runCLI(t, []string{"content", "compile", "--output", "json",
		"--path", corpusCase("invalid", "semantic", "unknown-direction")}, env)

	if res.exit != ExitFail {
		t.Fatalf("exit=%d stderr=%q", res.exit, res.stderr)
	}
	body := decodeJSON(t, res.stdout)
	errObj, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("no error envelope: %v", body)
	}
	detail, ok := errObj["detail"].(map[string]any)
	if !ok {
		t.Fatalf("no detail: %v", errObj)
	}
	ds, ok := detail["diagnostics"].([]any)
	if !ok || len(ds) != 1 {
		t.Fatalf("want one diagnostic in the envelope, got %v", detail["diagnostics"])
	}
	d := ds[0].(map[string]any)
	for k, want := range map[string]any{
		"file": "z.aw", "line": 3.0, "col": 10.0,
		"code": "unknown_direction", "severity": "error",
	} {
		if d[k] != want {
			t.Errorf("diagnostic %s = %v, want %v", k, d[k], want)
		}
	}
	if chain, _ := d["chain"].([]any); len(chain) != 2 || chain[0] != "z" || chain[1] != "r" {
		t.Errorf("chain = %v, want [z r]", d["chain"])
	}
}

// TestContentCompileWarningsExitZero is errors.md rule 7.
func TestContentCompileWarningsExitZero(t *testing.T) {
	env := contentEnv(t)
	res := runCLI(t, []string{"content", "compile", "--path", corpusCase("valid", "warn-orphan-room")}, env)

	if res.exit != ExitOK {
		t.Errorf("a warning failed the compile: exit=%d stderr=%q", res.exit, res.stderr)
	}
	if !strings.Contains(res.stderr, "orphan_room") {
		t.Errorf("the warning is silent:\n%s", res.stderr)
	}
	if !strings.Contains(res.stdout, "1 zones, 3 rooms, 0 templates") {
		t.Errorf("stdout = %q", res.stdout)
	}
}

// TestContentCompileWritesTheBlobs covers --out and semantics.md §6's layout:
// Zones at the root, Templates under templates/, sources under src/.
func TestContentCompileWritesTheBlobs(t *testing.T) {
	env := contentEnv(t)
	out := t.TempDir()
	res := runCLI(t, []string{"content", "compile", "--path", corpusCase("valid", "town"), "--out", out}, env)
	if res.exit != ExitOK {
		t.Fatalf("exit=%d stderr=%q", res.exit, res.stderr)
	}
	for _, want := range []string{
		"town.json", "docks.json", "wilds.json",
		filepath.Join("templates", "town.Merchant.json"),
		filepath.Join("src", "npcs.aw"),
	} {
		if _, err := os.Stat(filepath.Join(out, want)); err != nil {
			t.Errorf("no blob at %s: %v", want, err)
		}
	}
	// The published source is what the Builder wrote, comments and all — which
	// is what preserves what compile drops (ADR-0009).
	src, err := os.ReadFile(filepath.Join(out, "src", "npcs.aw"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "// The people of the town.") {
		t.Error("the published source lost its comments")
	}
}

// TestContentFmtRewritesAndIsANoOp is AC-3: fmt rewrites to the canonical form
// and a second run changes nothing.
func TestContentFmtRewritesAndIsANoOp(t *testing.T) {
	env := contentEnv(t)
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "pack.aw"), "pack p requires andara.core@1\n", 0o644)
	writeFile(t, filepath.Join(dir, "z.aw"), "zone z \"Z\"    {\n     room r \"R\"   {\n\n\t\texit north -> h\n\n}\n  room h \"H\" {exit south -> r}\n}\n", 0o644)

	res := runCLI(t, []string{"content", "fmt", "--path", dir}, env)
	if res.exit != ExitOK {
		t.Fatalf("exit=%d stderr=%q", res.exit, res.stderr)
	}
	once, err := os.ReadFile(filepath.Join(dir, "z.aw"))
	if err != nil {
		t.Fatal(err)
	}
	want := "zone z \"Z\" {\n  room r \"R\" {\n    exit north -> h\n  }\n\n  room h \"H\" {\n    exit south -> r\n  }\n}\n"
	if string(once) != want {
		t.Errorf("fmt output:\n%q\nwant:\n%q", once, want)
	}

	// A second run is a no-op, and --check then exits 0.
	if res := runCLI(t, []string{"content", "fmt", "--path", dir}, env); res.exit != ExitOK {
		t.Fatalf("second run exit=%d", res.exit)
	}
	twice, err := os.ReadFile(filepath.Join(dir, "z.aw"))
	if err != nil {
		t.Fatal(err)
	}
	if string(twice) != string(once) {
		t.Errorf("fmt is not idempotent:\n%q\nthen\n%q", once, twice)
	}
	if res := runCLI(t, []string{"content", "fmt", "--check", "--path", dir}, env); res.exit != ExitOK {
		t.Errorf("--check on formatted source exit=%d stdout=%q", res.exit, res.stdout)
	}
}

// TestContentFmtCheckListsWhatWouldChange is AC-3's --check clause: exit 1
// listing the files, and nothing rewritten.
func TestContentFmtCheckListsWhatWouldChange(t *testing.T) {
	env := contentEnv(t)
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "pack.aw"), "pack p requires andara.core@1\n", 0o644)
	unformatted := "zone z \"Z\"  {\n  room r \"R\" {}\n}\n"
	writeFile(t, filepath.Join(dir, "z.aw"), unformatted, 0o644)

	res := runCLI(t, []string{"content", "fmt", "--check", "--path", dir}, env)
	if res.exit != ExitFail {
		t.Errorf("exit=%d, want %d", res.exit, ExitFail)
	}
	if !strings.Contains(res.stdout, "z.aw") {
		t.Errorf("--check did not list the file: %q", res.stdout)
	}
	got, err := os.ReadFile(filepath.Join(dir, "z.aw"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != unformatted {
		t.Error("--check rewrote the file")
	}
}

// TestContentFmtCheckIsCleanOverTheCorpus is formatting.md AC-2: every .aw a
// Builder reads is already canonical.
func TestContentFmtCheckIsCleanOverTheCorpus(t *testing.T) {
	env := contentEnv(t)
	for _, sub := range []string{"valid", "pending", "roundtrip"} {
		res := runCLI(t, []string{"content", "fmt", "--check", "--path", filepath.Join(corpusRoot, sub)}, env)
		if res.exit != ExitOK {
			t.Errorf("corpus/%s is not fmt-clean: exit=%d stdout=%q stderr=%q", sub, res.exit, res.stdout, res.stderr)
		}
	}
}

// TestContentDecompileRoundTrips is AC-4: the produced .aw compile to the same
// canonical blobs, and for a pack authored canonically they are the source.
func TestContentDecompileRoundTrips(t *testing.T) {
	env := contentEnv(t)
	out := t.TempDir()
	src := corpusCase("roundtrip", "zones")
	res := runCLI(t, []string{"content", "decompile", "--path", src, "--out", out}, env)
	if res.exit != ExitOK {
		t.Fatalf("exit=%d stderr=%q", res.exit, res.stderr)
	}
	for _, name := range []string{"pack.aw", "town.aw", "docks.aw"} {
		got, err := os.ReadFile(filepath.Join(out, name))
		if err != nil {
			t.Fatalf("decompile produced no %s: %v", name, err)
		}
		want, err := os.ReadFile(filepath.Join(src, name))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Errorf("%s does not round-trip:\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
		}
	}

	// And the reconstructed pack compiles to the same blobs.
	blobsA, blobsB := t.TempDir(), t.TempDir()
	if r := runCLI(t, []string{"content", "compile", "--path", src, "--out", blobsA}, env); r.exit != ExitOK {
		t.Fatalf("compiling the original: exit=%d", r.exit)
	}
	if r := runCLI(t, []string{"content", "compile", "--path", out, "--out", blobsB}, env); r.exit != ExitOK {
		t.Fatalf("compiling the decompiled: exit=%d stderr=%q", r.exit, r.stderr)
	}
	for _, name := range []string{"town.json", "docks.json"} {
		a, err := os.ReadFile(filepath.Join(blobsA, name))
		if err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(filepath.Join(blobsB, name))
		if err != nil {
			t.Fatal(err)
		}
		if string(a) != string(b) {
			t.Errorf("%s differs after a round trip:\n--- original ---\n%s\n--- decompiled ---\n%s", name, a, b)
		}
	}
}

// TestContentCompileOfflineWithoutCache is AC-5's second clause: no cache is
// core_version_mismatch telling the Builder what to run, not a missing-file
// error.
func TestContentCompileOfflineWithoutCache(t *testing.T) {
	env := isolatedEnv(t, map[string]string{"ANDARA_CONTENT_CACHE": t.TempDir()})
	res := runCLI(t, []string{"content", "compile", "--path", corpusCase("valid", "town")}, env)

	if res.exit != ExitFail {
		t.Errorf("exit=%d, want %d", res.exit, ExitFail)
	}
	if !strings.Contains(res.stderr, "core_version_mismatch") {
		t.Errorf("stderr does not name the code:\n%s", res.stderr)
	}
	if !strings.Contains(res.stderr, "fetch-core") {
		t.Errorf("stderr does not say what to run:\n%s", res.stderr)
	}
}

// TestContentFetchCoreHasNoServerToFetchFrom records the deviation: no Admin
// RPC serves a published ContentVersion yet, so the command exits 3 — the
// AW-CLI-001 taxonomy's "server unreachable" — and says which story owns it.
func TestContentFetchCoreHasNoServerToFetchFrom(t *testing.T) {
	env := isolatedEnv(t, map[string]string{"ANDARA_CONTENT_CACHE": t.TempDir()})
	res := runCLI(t, []string{"content", "fetch-core"}, env)

	if res.exit != ExitConnect {
		t.Errorf("exit=%d, want %d", res.exit, ExitConnect)
	}
	if !strings.Contains(res.stderr, "--from") {
		t.Errorf("the message does not offer the alternative:\n%s", res.stderr)
	}
}

// TestContentCompileEmitsTheSpan is the story's Observability requirement: a
// `cli.command` root span with a `content.compile` child carrying files, zones,
// templates, and diagnostics. No metrics — a compile is a thing a Builder runs,
// not a thing a server serves.
func TestContentCompileEmitsTheSpan(t *testing.T) {
	env := contentEnv(t)
	rec := tracetest.NewSpanRecorder()

	var stdout, stderr bytes.Buffer
	exit := execute(&runtime{
		args:      []string{"content", "compile", "--path", corpusCase("valid", "town")},
		stdout:    &stdout,
		stderr:    &stderr,
		lookupEnv: lookupFrom(env),
		tpOptions: []sdktrace.TracerProviderOption{sdktrace.WithSpanProcessor(rec)},
	})
	if exit != ExitOK {
		t.Fatalf("exit=%d stderr=%q", exit, stderr.String())
	}

	var compile, root sdktrace.ReadOnlySpan
	for _, s := range rec.Ended() {
		switch s.Name() {
		case "content.compile":
			compile = s
		case "cli.command":
			root = s
		}
	}
	if root == nil {
		t.Fatal("no cli.command root span")
	}
	if compile == nil {
		t.Fatalf("no content.compile span; recorded %d spans", len(rec.Ended()))
	}
	if compile.Parent().SpanID() != root.SpanContext().SpanID() {
		t.Error("content.compile is not a child of cli.command")
	}

	// corpus/valid/town is 6 source files, 3 Zones, 3 Templates, no findings.
	want := map[string]int64{"files": 6, "zones": 3, "templates": 3, "diagnostics": 0}
	got := map[string]int64{}
	for _, a := range compile.Attributes() {
		if _, ok := want[string(a.Key)]; ok {
			got[string(a.Key)] = a.Value.AsInt64()
		}
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("span attribute %s = %d, want %d", k, got[k], v)
		}
	}
}

// TestContentCompileOutPrunesStaleBlobs: a Builder who deletes a Template and
// recompiles into the same --out kept the old JSON, and the directory loader
// globs `templates/*.json` — so the removed content stayed live in a pack they
// believed they had rebuilt.
func TestContentCompileOutPrunesStaleBlobs(t *testing.T) {
	env := contentEnv(t)
	src, out := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(src, "pack.aw"), "pack p requires andara.core@1\n", 0o644)
	writeFile(t, filepath.Join(src, "keep.aw"), "template Keep kind entity {}\n", 0o644)
	writeFile(t, filepath.Join(src, "gone.aw"), "template Gone kind entity {}\n", 0o644)
	writeFile(t, filepath.Join(src, "z.aw"), "zone z \"Z\" {\n  room r \"R\" {}\n}\n", 0o644)

	if res := runCLI(t, []string{"content", "compile", "--path", src, "--out", out}, env); res.exit != ExitOK {
		t.Fatalf("exit=%d stderr=%q", res.exit, res.stderr)
	}
	// Something the compiler did not write stays put.
	writeFile(t, filepath.Join(out, "NOTES.md"), "mine\n", 0o644)

	if err := os.Remove(filepath.Join(src, "gone.aw")); err != nil {
		t.Fatal(err)
	}
	if res := runCLI(t, []string{"content", "compile", "--path", src, "--out", out}, env); res.exit != ExitOK {
		t.Fatalf("exit=%d stderr=%q", res.exit, res.stderr)
	}

	for _, gone := range []string{
		filepath.Join("templates", "p.Gone.json"),
		filepath.Join("src", "gone.aw"),
	} {
		if _, err := os.Stat(filepath.Join(out, gone)); err == nil {
			t.Errorf("%s survived the source being deleted", gone)
		}
	}
	for _, kept := range []string{
		filepath.Join("templates", "p.Keep.json"),
		filepath.Join("src", "keep.aw"),
		"z.json",
		"NOTES.md",
	} {
		if _, err := os.Stat(filepath.Join(out, kept)); err != nil {
			t.Errorf("%s was pruned and should not have been: %v", kept, err)
		}
	}
}

// TestContentCompileOutInsidePath: `--path . --out build` is the natural local
// layout. A pack publishes its own sources under src/, so without excluding the
// output tree the next run reads the last run's output back as pack input and
// reports duplicate_pack against the Builder's own build directory.
func TestContentCompileOutInsidePath(t *testing.T) {
	env := contentEnv(t)
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "pack.aw"), "pack p requires andara.core@1\n", 0o644)
	writeFile(t, filepath.Join(src, "z.aw"), "zone z \"Z\" {\n  room r \"R\" {}\n}\n", 0o644)

	out := filepath.Join(src, "build")
	for i := 1; i <= 2; i++ {
		res := runCLI(t, []string{"content", "compile", "--path", src, "--out", out}, env)
		if res.exit != ExitOK {
			t.Fatalf("run %d: exit=%d stderr=%q", i, res.exit, res.stderr)
		}
		if !strings.Contains(res.stdout, "1 zones, 1 rooms, 0 templates") {
			t.Errorf("run %d: stdout=%q", i, res.stdout)
		}
	}
}
