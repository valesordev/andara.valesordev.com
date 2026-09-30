// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/valesordev/andara/content/core"
	"github.com/valesordev/andara/content/lang"
	contentv1 "github.com/valesordev/andara/gen/go/andara/content/v1"
	"github.com/valesordev/andara/internal/contentequiv"
	"github.com/valesordev/andara/server/auth"
	"github.com/valesordev/andara/server/content"
	"github.com/valesordev/andara/server/gateway"
	"github.com/valesordev/andara/server/recordlog"
)

// AW-CLI-002: `content validate` and `content inspect`.

const devFixture = "../../content/fixtures/town"

// offlineEnv is a Builder's laptop with nothing on it: no config, no
// credential, and an empty core cache. No server address is ever dialed by
// --path, so nothing here reaches a network (AC-5).
func offlineEnv(t *testing.T) map[string]string {
	t.Helper()
	return isolatedEnv(t, map[string]string{"ANDARA_CONTENT_CACHE": t.TempDir()})
}

// AC-1: a dangling Exit exits 1 with one finding per line, as
// `file:line:col: CODE message`, the message naming the Room, the Direction,
// and the target.
func TestContentValidate_DanglingExit(t *testing.T) {
	res := runCLI(t, []string{"content", "validate", "--path", corpusCase("invalid", "semantic", "unknown-room")}, offlineEnv(t))
	if res.exit != ExitFail {
		t.Fatalf("exit=%d, want %d; stderr=%q", res.exit, ExitFail, res.stderr)
	}
	want := filepath.Join(corpusCase("invalid", "semantic", "unknown-room"), "z.aw") +
		`:5:19: unknown_room Room "r" exits north to nowhere, and Zone "z" has no Room "nowhere"`
	if first := strings.SplitN(res.stderr, "\n", 2)[0]; first != want {
		t.Errorf("first line:\n got %q\nwant %q", first, want)
	}
	if res.stdout != "" {
		t.Errorf("stdout = %q, want nothing", res.stdout)
	}
}

// AC-2: valid source exits 0 and prints the counts and the core.
func TestContentValidate_ValidPrintsCounts(t *testing.T) {
	res := runCLI(t, []string{"content", "validate", "--path", devFixture}, offlineEnv(t))
	if res.exit != ExitOK {
		t.Fatalf("exit=%d stderr=%q", res.exit, res.stderr)
	}
	if want := "4 zones, 7 rooms, 3 templates, core andara.core@1\n"; res.stdout != want {
		t.Errorf("stdout = %q, want %q", res.stdout, want)
	}
	// The dev fixture's one warning rides out on stderr, and does not fail.
	if !strings.Contains(res.stderr, "purgatory.aw:6:5: missing_reverse_exit") {
		t.Errorf("stderr does not carry the warning:\n%s", res.stderr)
	}
}

// AC-3: under --output json a failure's stdout is a JSON array of Diagnostic
// and nothing else, and stderr is the exit summary alone.
func TestContentValidate_JSONIsTheArrayAlone(t *testing.T) {
	dir := corpusCase("invalid", "semantic", "multiple-findings")
	res := runCLI(t, []string{"content", "validate", "--path", dir, "--output", "json"}, offlineEnv(t))
	if res.exit != ExitFail {
		t.Fatalf("exit=%d, want %d", res.exit, ExitFail)
	}
	dec := json.NewDecoder(strings.NewReader(res.stdout))
	var got []jsonDiagnostic
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("stdout is not a JSON array: %v\n%s", err, res.stdout)
	}
	if dec.More() {
		t.Errorf("stdout carries more than the array:\n%s", res.stdout)
	}
	if len(got) != 3 || got[0].Code != "unknown_direction" || got[0].File != "a.aw" || got[0].Line != 3 || got[0].Col != 10 || got[0].Severity != "error" {
		t.Errorf("diagnostics = %+v", got)
	}
	if want := dir + ": 3 finding(s) refuse the pack\n"; res.stderr != want {
		t.Errorf("stderr = %q, want only the summary %q", res.stderr, want)
	}

	// Valid, it is still the array — of warnings — and the summary on stderr.
	res = runCLI(t, []string{"content", "validate", "--path", devFixture, "-o", "json"}, offlineEnv(t))
	if res.exit != ExitOK {
		t.Fatalf("exit=%d stderr=%q", res.exit, res.stderr)
	}
	got = nil
	if err := json.Unmarshal([]byte(res.stdout), &got); err != nil || len(got) != 1 || got[0].Severity != "warning" {
		t.Errorf("stdout = %q (%v)", res.stdout, err)
	}
}

// AC-4, the CLI's runner: every case in the fixture set validates to exactly
// the findings the fixture expects — the ones the compiler's conformance run
// and the publish gate are held to (internal/contentequiv).
func TestContentValidate_AgreesWithTheEquivalenceFixture(t *testing.T) {
	cases, err := contentequiv.Cases("../..")
	if err != nil {
		t.Fatal(err)
	}
	env := offlineEnv(t)
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			res := runCLI(t, []string{"content", "validate", "--path", filepath.Join("../..", c.Dir), "-o", "json"}, env)
			wantExit := ExitOK
			if !c.Compiles {
				wantExit = ExitFail
			}
			if res.exit != wantExit {
				t.Fatalf("exit=%d, want %d; stderr=%q", res.exit, wantExit, res.stderr)
			}
			var ds []jsonDiagnostic
			if err := json.Unmarshal([]byte(res.stdout), &ds); err != nil {
				t.Fatalf("stdout: %v\n%s", err, res.stdout)
			}
			got := make([]string, 0, len(ds))
			for _, d := range ds {
				got = append(got, contentequiv.Form(d.File, d.Line, d.Col, d.Code, d.Chain))
			}
			for _, d := range contentequiv.Diff(c.Want, got) {
				t.Error(d)
			}
		})
	}
}

// AC-5: offline, with no cache, a pack requiring the embedded core validates;
// one requiring another core fails naming both and the release to use; and
// with that core cached, it validates against the cache.
func TestContentValidate_EmbeddedCoreThenCache(t *testing.T) {
	env := offlineEnv(t)
	if res := runCLI(t, []string{"content", "validate", "--path", devFixture}, env); res.exit != ExitOK {
		t.Fatalf("against the embedded core: exit=%d stderr=%q", res.exit, res.stderr)
	}

	other := requiringCore(t, devFixture, 2)
	res := runCLI(t, []string{"content", "validate", "--path", other}, env)
	if res.exit != ExitFail {
		t.Fatalf("exit=%d, want %d", res.exit, ExitFail)
	}
	for _, want := range []string{
		"core_version_mismatch",
		"requires andara.core@2",
		"this andara-cli embeds andara.core@1; use the andara-cli release that embeds andara.core@2",
	} {
		if !strings.Contains(res.stderr, want) {
			t.Errorf("stderr does not say %q:\n%s", want, res.stderr)
		}
	}

	if res := runCLI(t, []string{"content", "fetch-core", "--from", corpusCase("valid", "core"), "--version", "2"}, env); res.exit != ExitOK {
		t.Fatalf("fetch-core: exit=%d stderr=%q", res.exit, res.stderr)
	}
	res = runCLI(t, []string{"content", "validate", "--path", other}, env)
	if res.exit != ExitOK {
		t.Fatalf("against the cached core: exit=%d stderr=%q", res.exit, res.stderr)
	}
	if !strings.HasSuffix(res.stdout, "core andara.core@2\n") {
		t.Errorf("stdout = %q, want the cached core named", res.stdout)
	}
}

// AC-7: a flattened Template shows the ancestor that set each field, and the
// one that added each marker Component.
func TestContentInspect_TemplateProvenance(t *testing.T) {
	res := runCLI(t, []string{"content", "inspect", "template", "town.Merchant", "--path", devFixture}, offlineEnv(t))
	if res.exit != ExitOK {
		t.Fatalf("exit=%d stderr=%q", res.exit, res.stderr)
	}
	for _, want := range []string{
		"chain:     andara.core.Entity > andara.core.Npc > town.Merchant\n",
		"  Behavior.name = \"town.merchant\"  (town.Merchant)\n",
		"  Memory  (andara.core.Npc)\n",
	} {
		if !strings.Contains(res.stdout, want) {
			t.Errorf("stdout lacks %q:\n%s", want, res.stdout)
		}
	}

	// A field set by a core ancestor is attributed to it, not to the Template
	// being inspected.
	dir := t.TempDir()
	writeAW(t, dir, "pack.aw", "pack p requires andara.core@1\n")
	writeAW(t, dir, "t.aw", "template Base extends andara.core.Npc {\n  component andara.core.Behavior { name: \"base\" }\n}\n\ntemplate Leaf extends Base {}\n")
	res = runCLI(t, []string{"content", "inspect", "template", "p.Leaf", "--path", dir}, offlineEnv(t))
	if !strings.Contains(res.stdout, "  Behavior.name = \"base\"  (p.Base)\n") {
		t.Errorf("an inherited field is not attributed to its setter:\n%s%s", res.stdout, res.stderr)
	}
}

// AC-8: a Room's Exits in the closed Direction order, compass first, each
// marked with whether its target leads back.
func TestContentInspect_RoomExitsInDirectionOrder(t *testing.T) {
	dir := t.TempDir()
	writeAW(t, dir, "pack.aw", "pack p requires andara.core@1\n")
	writeAW(t, dir, "z.aw", `zone market "Market" {
  fallback square

  room square "Square" {
    exit up -> loft
    exit west -> stall
    exit north -> gate
  }

  room gate "Gate" {
    exit south -> square
  }

  room stall "Stall" {
    exit east -> square
  }

  room loft "Loft" {}
}
`)
	res := runCLI(t, []string{"content", "inspect", "room", "market/square", "--path", dir}, offlineEnv(t))
	if res.exit != ExitOK {
		t.Fatalf("exit=%d stderr=%q", res.exit, res.stderr)
	}
	_, exits, _ := strings.Cut(res.stdout, "exits:\n")
	want := "" +
		"  north  -> gate   back: south\n" +
		"  west   -> stall  back: east\n" +
		"  up     -> loft   one-way: no Exit back\n"
	if exits != want {
		t.Errorf("exits:\n%s\nwant:\n%s", exits, want)
	}
}

func TestContentInspect_ZoneAndMissing(t *testing.T) {
	env := offlineEnv(t)
	res := runCLI(t, []string{"content", "inspect", "zone", "town", "--path", devFixture}, env)
	if res.exit != ExitOK || !strings.Contains(res.stdout, "fallback:    plaza\n") || !strings.Contains(res.stdout, "exits:       4, 2 leaving the Zone\n") {
		t.Errorf("exit=%d stdout:\n%s", res.exit, res.stdout)
	}
	if res := runCLI(t, []string{"content", "inspect", "room", "town/nowhere", "--path", devFixture}, env); res.exit != ExitFail {
		t.Errorf("a missing Room: exit=%d, want %d", res.exit, ExitFail)
	}
	if res := runCLI(t, []string{"content", "inspect", "room", "town", "--path", devFixture}, env); res.exit != ExitUsage {
		t.Errorf("a Room ref without a Zone: exit=%d, want %d", res.exit, ExitUsage)
	}
	// Nothing to inspect in a pack that doesn't compile: its findings, exit 1.
	res = runCLI(t, []string{"content", "inspect", "zone", "z", "--path", corpusCase("invalid", "semantic", "unknown-room")}, env)
	if res.exit != ExitFail || !strings.Contains(res.stderr, "unknown_room") {
		t.Errorf("exit=%d stderr=%q", res.exit, res.stderr)
	}
}

// AC-9: `version` names the embedded core after built_at, and JSON carries it.
func TestVersion_NamesTheEmbeddedCore(t *testing.T) {
	res := runCLI(t, []string{"version"}, nil)
	lines := strings.Split(strings.TrimSpace(res.stdout), "\n")
	if len(lines) != 4 || !strings.HasPrefix(lines[2], "built_at:") || lines[3] != "core:     andara.core@1" {
		t.Errorf("version:\n%s", res.stdout)
	}
	res = runCLI(t, []string{"version", "-o", "json"}, nil)
	var v map[string]any
	if err := json.Unmarshal([]byte(res.stdout), &v); err != nil || v["core_version"] != float64(core.Version()) {
		t.Errorf("json = %s (%v)", res.stdout, err)
	}
}

// AC-10: the core this CLI resolves against is the one VERSIONS records for
// VERSION: the parsed Templates, re-encoded as the server publishes them,
// hash to the recorded digest. The server's side of the same check is
// content/core's (AW-SRV-013 AC-19); together they hold both binaries to one
// andara.core@N.
func TestEmbeddedCoreIsTheOneVERSIONSRecords(t *testing.T) {
	p, err := embeddedCore()
	if err != nil {
		t.Fatal(err)
	}
	if uint64(p.Version) != core.Version() {
		t.Fatalf("resolves as andara.core@%d, VERSION is %d", p.Version, core.Version())
	}
	blobs := map[string][]byte{}
	for _, tmpl := range p.Templates {
		blobs[path.Join("templates", tmpl.GetName()+".json")] = lang.CanonicalJSON(tmpl)
	}
	recorded, err := core.Recorded()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := core.Digest(blobs), recorded[core.Version()]; !bytes.Equal(got, want) {
		t.Errorf("the CLI's andara.core@%d has digest %x; VERSIONS records %x", core.Version(), got, want)
	}
}

// The Observability requirement: a `cli.command` root span with
// `content.compile` and `content.validate` children carrying counts.
func TestContentValidate_EmitsTheSpans(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	var stdout, stderr bytes.Buffer
	exit := execute(&runtime{
		args:      []string{"content", "validate", "--path", devFixture},
		stdout:    &stdout,
		stderr:    &stderr,
		lookupEnv: lookupFrom(offlineEnv(t)),
		tpOptions: []sdktrace.TracerProviderOption{sdktrace.WithSpanProcessor(rec)},
	})
	if exit != ExitOK {
		t.Fatalf("exit=%d stderr=%q", exit, stderr.String())
	}
	spans := map[string]sdktrace.ReadOnlySpan{}
	for _, s := range rec.Ended() {
		spans[s.Name()] = s
	}
	root, ok := spans["cli.command"]
	if !ok {
		t.Fatalf("no cli.command span in %v", spans)
	}
	want := map[string]map[string]int64{
		"content.compile":  {"zones": 4, "rooms": 7, "templates": 3, "diagnostics": 1},
		"content.validate": {"zones": 4, "rooms": 7, "templates": 3, "error_count": 0, "warning_count": 1},
	}
	for name, attrs := range want {
		s, ok := spans[name]
		if !ok {
			t.Errorf("no %s span", name)
			continue
		}
		if s.Parent().SpanID() != root.SpanContext().SpanID() {
			t.Errorf("%s is not a child of cli.command", name)
		}
		got := map[string]int64{}
		for _, kv := range s.Attributes() {
			got[string(kv.Key)] = kv.Value.AsInt64()
		}
		for k, v := range attrs {
			if got[k] != v {
				t.Errorf("%s %s = %d, want %d", name, k, got[k], v)
			}
		}
	}
}

func TestContentValidate_FlagErrors(t *testing.T) {
	env := offlineEnv(t)
	for _, args := range [][]string{
		{"--path", devFixture, "--pack", "town", "--version", "1"},
		{"--pack", "town"},
		{"--version", "3"},
		{"--path", filepath.Join(t.TempDir(), "absent")},
	} {
		if res := runCLI(t, append([]string{"content", "validate"}, args...), env); res.exit != ExitUsage {
			t.Errorf("%v: exit=%d, want %d", args, res.exit, ExitUsage)
		}
	}
}

func writeAW(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// --- --pack: a published version over Admin (AC-6) ---------------------------

// contentServer is the live gateway with the content publish path behind
// it, over the given logs: the registry, the Loader, and Admin as a
// content.source=kafka server wires them.
type contentServer struct {
	*liveServer
	reg *content.Registry
}

// store is how Admin reads blob bodies back. Nil reads them from the
// registry's own cache, as the in-memory harness does; the integration test
// passes a KafkaResolver on a cold cache, so every GetBlob reads the broker.
func startContentServer(t *testing.T, logs func(name string) recordlog.Log, store content.Store) *contentServer {
	t.Helper()
	cache := content.BlobCache{Dir: t.TempDir()}
	reg, err := content.OpenRegistry(context.Background(), content.RegistryOptions{
		Blobs: logs("blobs"), Versions: logs("versions"), Active: logs("active"), Audit: logs("audit"), Cache: cache,
	})
	if err != nil {
		t.Fatal(err)
	}
	if store == nil {
		store = registryStore{reg, cache}
	}
	admin, err := content.NewAdmin(content.AdminOptions{
		Registry: reg, Loader: content.NewLoader(content.LoaderOptions{Store: store}), Blobs: store,
		Accounts: noPacks{}, Auditor: auth.NewAuditor(logs("audit"), slog.New(slog.DiscardHandler), nil, nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	s := startServerWith(t, nil, func(o *gateway.Options) { o.ContentAdmin = admin })
	return &contentServer{liveServer: s, reg: reg}
}

// publish writes pack@next straight to the registry: the manifest a publish
// would write, without the gate, so a test can hold a version the gate would
// refuse.
func (s *contentServer) publish(t *testing.T, pack string, coreVersion uint64, bodies map[string][]byte) uint64 {
	t.Helper()
	ctx := context.Background()
	var refs []*contentv1.BlobRef
	for p, body := range bodies {
		sum := sha256.Sum256(body)
		if _, err := s.reg.PutBlob(ctx, sum[:], "application/octet-stream", body); err != nil {
			t.Fatal(err)
		}
		refs = append(refs, &contentv1.BlobRef{Path: p, Hash: sum[:], SizeBytes: uint64(len(body))})
	}
	cv, err := s.reg.Publish(ctx, &contentv1.ContentVersion{PackId: pack, Blobs: refs, CoreVersion: coreVersion, Author: "test"}, s.reg.Newest(pack), "test")
	if err != nil {
		t.Fatal(err)
	}
	return cv.GetVersion()
}

type noPacks struct{}

func (noPacks) BuilderPacks(string) []string { return nil }

// registryStore is the read side over what the registry wrote, as the
// KafkaResolver is in the server.
type registryStore struct {
	r     *content.Registry
	cache content.BlobCache
}

func (s registryStore) Active(context.Context) (map[string]uint64, error) { return s.r.Pointers(), nil }
func (s registryStore) Manifest(_ context.Context, pack string, v uint64) (*contentv1.ContentVersion, error) {
	cv, ok := s.r.Manifest(pack, v)
	if !ok {
		return nil, &content.ErrManifestMissing{Pack: pack, Version: v}
	}
	return cv, nil
}
func (s registryStore) Blobs(_ context.Context, refs []*contentv1.BlobRef) (map[string][]byte, error) {
	out := map[string][]byte{}
	for _, ref := range refs {
		body, err := s.cache.Get(ref.GetHash())
		if err != nil {
			return nil, &content.ErrBlobMissing{Hash: ref.GetHash(), Path: ref.GetPath()}
		}
		out[ref.GetPath()] = body
	}
	return out, nil
}

// compiledFixture is the dev fixture as `content publish` would send it:
// every compiled blob and every source.
func compiledFixture(t *testing.T) map[string][]byte {
	t.Helper()
	p, err := embeddedCore()
	if err != nil {
		t.Fatal(err)
	}
	out, ds := lang.Compile(devFixture, p, nil)
	if out == nil {
		t.Fatalf("the dev fixture does not compile: %v", ds)
	}
	bodies := map[string][]byte{}
	for _, b := range out.Blobs {
		bodies[b.Path] = b.Bytes
	}
	return bodies
}

func testContentValidatePack(t *testing.T, s *contentServer) {
	env := s.env(t)
	env["ANDARA_CONTENT_CACHE"] = t.TempDir()
	login(t, env)

	good := s.publish(t, "town", 1, compiledFixture(t))
	res := runCLI(t, []string{"content", "validate", "--pack", "town", "--version", itoa(good)}, env)
	if res.exit != ExitOK {
		t.Fatalf("a published valid version: exit=%d stderr=%q", res.exit, res.stderr)
	}
	if want := "4 zones, 7 rooms, 3 templates, core andara.core@1\n"; res.stdout != want {
		t.Errorf("stdout = %q, want %q", res.stdout, want)
	}
	// The warning is placed on the published source, under pack@version.
	if !strings.Contains(res.stderr, "town@"+itoa(good)+"/purgatory.aw:6:5: missing_reverse_exit") {
		t.Errorf("the warning is not placed on the source:\n%s", res.stderr)
	}

	// A version with a dangling Exit and no sources, which the gate would
	// have refused: the validator refuses it, on the blob.
	broken := map[string][]byte{}
	for p, b := range compiledFixture(t) {
		if strings.HasPrefix(p, lang.SourcePrefix) {
			continue
		}
		if p == "town.json" {
			b = bytes.Replace(b, []byte(`"toRoom": "hall"`), []byte(`"toRoom": "nowhere"`), 1)
		}
		broken[p] = b
	}
	bad := s.publish(t, "town", 1, broken)
	res = runCLI(t, []string{"content", "validate", "--pack", "town", "--version", itoa(bad), "-o", "json"}, env)
	if res.exit != ExitFail {
		t.Fatalf("a published invalid version: exit=%d stdout=%q stderr=%q", res.exit, res.stdout, res.stderr)
	}
	var ds []jsonDiagnostic
	if err := json.Unmarshal([]byte(res.stdout), &ds); err != nil || len(ds) != 1 || ds[0].Code != "unknown_room" || ds[0].File != "town.json" ||
		strings.Join(ds[0].Chain, "/") != "town/plaza/north" {
		t.Errorf("diagnostics = %+v (%v)", ds, err)
	}

	// Sources that don't compile, beside blobs that are valid: the blobs are
	// what the server holds and loads, so they decide the result, and the
	// source's finding rides out as a warning.
	withBadSource := compiledFixture(t)
	withBadSource["src/town.aw"] = []byte("zone town \"Town\" {\n")
	mixed := s.publish(t, "town", 1, withBadSource)
	res = runCLI(t, []string{"content", "validate", "--pack", "town", "--version", itoa(mixed)}, env)
	if res.exit != ExitOK {
		t.Fatalf("valid blobs, broken source: exit=%d stderr=%q", res.exit, res.stderr)
	}
	if !strings.Contains(res.stderr, "syntax_error the published source does not compile:") {
		t.Errorf("the source's finding is not reported:\n%s", res.stderr)
	}
	if want := "4 zones, 7 rooms, 3 templates, core andara.core@1\n"; res.stdout != want {
		t.Errorf("stdout = %q, want %q", res.stdout, want)
	}

	// A source path that leaves the pack is refused before anything is
	// written, for validate and inspect alike.
	escape := "pwned-" + strconv.FormatInt(time.Now().UnixNano(), 10) + ".aw"
	withEscape := compiledFixture(t)
	withEscape["src/../"+escape] = []byte("pack town requires andara.core@1\n")
	hostile := s.publish(t, "town", 1, withEscape)
	for _, args := range [][]string{
		{"content", "validate", "--pack", "town", "--version", itoa(hostile)},
		{"content", "inspect", "zone", "town", "--pack", "town", "--version", itoa(hostile)},
	} {
		res = runCLI(t, append(args, "-o", "json"), env)
		if res.exit != ExitFail || !strings.Contains(res.stdout, `"code":"unsafe_source_path"`) {
			t.Errorf("%v: exit=%d stdout=%q", args[1], res.exit, res.stdout)
		}
	}
	if _, err := os.Stat(filepath.Join(os.TempDir(), escape)); !os.IsNotExist(err) {
		t.Errorf("a source outside the pack was written: %v", err)
	}

	// Server unreachable is exit 3.
	s.stop(t)
	if res := runCLI(t, []string{"content", "validate", "--pack", "town", "--version", itoa(good)}, env); res.exit != ExitConnect {
		t.Errorf("server down: exit=%d, want %d; stderr=%q", res.exit, ExitConnect, res.stderr)
	}
}

// AC-6 over in-memory topics. The same test runs against a throwaway
// Redpanda under -tags integration (validate_integration_test.go).
func TestContentValidate_PublishedVersion(t *testing.T) {
	logs := map[string]recordlog.Log{}
	s := startContentServer(t, func(name string) recordlog.Log {
		if l, ok := logs[name]; ok {
			return l
		}
		logs[name] = recordlog.NewMemory()
		return logs[name]
	}, nil)
	testContentValidatePack(t, s)
}

func itoa(v uint64) string { return strconv.FormatUint(v, 10) }
