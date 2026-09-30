// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"google.golang.org/genproto/googleapis/rpc/errdetails"

	"github.com/valesordev/andara/content/lang"
	"github.com/valesordev/andara/internal/eventually"
	"github.com/valesordev/andara/server/recordlog"
)

// AW-CLI-003: the Builder's publish path, against contentStack.

// memoryStack is the stack over in-memory topics.
func memoryStack(t *testing.T) *contentStack {
	t.Helper()
	logs := map[string]recordlog.Log{}
	return startContentStack(t, func(name string) recordlog.Log {
		if l, ok := logs[name]; ok {
			return l
		}
		logs[name] = recordlog.NewMemory()
		return logs[name]
	}, nil, nil)
}

// copyPack copies a pack directory and applies edits: file -> (old -> new).
func copyPack(t *testing.T, src string, edits map[string][2]string) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), filepath.Base(src))
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if ed, ok := edits[e.Name()]; ok {
			if ed[1] == "\x00delete" {
				continue
			}
			if !bytes.Contains(b, []byte(ed[0])) {
				t.Fatalf("%s has no %q to edit", e.Name(), ed[0])
			}
			b = bytes.Replace(b, []byte(ed[0]), []byte(ed[1]), 1)
		}
		if err := os.WriteFile(filepath.Join(dst, e.Name()), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dst
}

func mustRun(t *testing.T, env map[string]string, args ...string) runResult {
	t.Helper()
	res := runCLI(t, args, env)
	if res.exit != ExitOK {
		t.Fatalf("%v: exit=%d stdout=%q stderr=%q", args, res.exit, res.stdout, res.stderr)
	}
	return res
}

func jsonOf(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, s)
	}
	return m
}

// awaitServing polls `server info` until it names pack@version, within
// content.reload_debounce + 2 s (AC-2; live-assertions.md).
func (s *contentStack) awaitServing(t *testing.T, env map[string]string, pv string) {
	t.Helper()
	eventually.Observed(t, s.debounce+2*time.Second, "server info shows content "+pv, func() (bool, string) {
		res := runCLI(t, []string{"server", "info"}, env)
		return strings.Contains(res.stdout, "content "+pv+"\n"), res.stdout
	})
}

// testTwoIdentities is the rehearsal the Definition of done asks for: two
// Builders holding town, one publishing and one approving, through
// publish, approve, activate, rollback, history, diff and fetch (AC-1–8).
func testTwoIdentities(t *testing.T, s *contentStack) {
	_, alice := s.builder(t, "alice", "town")
	_, bob := s.builder(t, "bob", "town")

	// AC-1: the first publish uploads every blob; a second of the same
	// source uploads none, and is the next version with the first as
	// parent. The pointer doesn't move.
	res := mustRun(t, alice, "content", "publish", "--path", devFixture, "-o", "json")
	first := jsonOf(t, res.stdout)
	if first["version"] != 1.0 || first["blobs_uploaded"] != first["blobs_total"] || first["blobs_total"].(float64) < 10 {
		t.Fatalf("first publish: %s", res.stdout)
	}
	res = mustRun(t, alice, "content", "publish", "--path", devFixture)
	if res.stdout != "town@2 published (parent 1), awaiting approval\n" {
		t.Errorf("stdout = %q", res.stdout)
	}
	res = mustRun(t, alice, "content", "publish", "--path", devFixture, "-o", "json")
	if again := jsonOf(t, res.stdout); again["blobs_uploaded"] != 0.0 || again["parent_version"] != 2.0 {
		t.Errorf("an unchanged source uploaded blobs: %s", res.stdout)
	}
	if got := s.loader.Versions()["town"]; got != 0 {
		t.Fatalf("publishing moved the pointer: town@%d is in effect", got)
	}

	// AC-3: activating an unapproved version.
	res = runCLI(t, []string{"content", "activate", "town", "1", "--yes", "-o", "json"}, alice)
	if res.exit != ExitFail || jsonErrorCode(t, res.stdout) != "unapproved" ||
		!strings.Contains(res.stdout, "town@1 needs approval by a second builder holding the pack or an operator (published by alice)") {
		t.Fatalf("unapproved: exit=%d stdout=%q", res.exit, res.stdout)
	}

	// AC-2: approved by bob, activated by alice, and in effect.
	res = mustRun(t, bob, "content", "approve", "town", "1")
	if !strings.HasPrefix(res.stdout, "town@1 approved by bob\n") {
		t.Errorf("approve: %q", res.stdout)
	}
	res = mustRun(t, alice, "content", "activate", "town", "1", "--yes")
	if res.stdout != "town@1 active (nothing was active)\n" {
		t.Errorf("activate: %q", res.stdout)
	}
	s.awaitServing(t, alice, "town@1")

	// A changed version: plaza retitled, with an Exit west to a new Room.
	changed := copyPack(t, devFixture, map[string][2]string{
		"town.aw": {"  room plaza \"Market Plaza\" {\n", "  room plaza \"The Market\" {\n    exit west -> well\n"},
	})
	b, err := os.ReadFile(filepath.Join(changed, "town.aw"))
	if err != nil {
		t.Fatal(err)
	}
	b = bytes.Replace(b, []byte("  room hall"), []byte("  room well \"The Well\" {\n    exit east -> plaza\n  }\n\n  room hall"), 1)
	if err := os.WriteFile(filepath.Join(changed, "town.aw"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	res = mustRun(t, alice, "content", "publish", "--path", changed)
	if res.stdout != "town@4 published (parent 3), awaiting approval\n" {
		t.Fatalf("publish the change: %q", res.stdout)
	}
	mustRun(t, bob, "content", "approve", "town", "4")
	res = mustRun(t, alice, "content", "activate", "town", "4", "--yes")
	if res.stdout != "town@4 active (was town@1)\n" {
		t.Errorf("activate 4: %q", res.stdout)
	}
	s.awaitServing(t, alice, "town@4")

	// AC-4: roll back to the version active before, and forward again.
	res = mustRun(t, alice, "content", "rollback", "town", "--yes")
	if res.stdout != "town@1 active (rolled back from town@4)\n" {
		t.Errorf("rollback: %q", res.stdout)
	}
	s.awaitServing(t, alice, "town@1")

	// AC-6: every version, newest first, the active one marked, with
	// author, approver and active intervals.
	res = mustRun(t, alice, "content", "history", "town")
	lines := strings.Split(strings.TrimRight(res.stdout, "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("history:\n%s", res.stdout)
	}
	for i, want := range []*regexp.Regexp{
		regexp.MustCompile(`^  town@4  published by alice at \S+, approved by \S+ at \S+; active \S+ to \S+$`),
		regexp.MustCompile(`^  town@3  published by alice at \S+, not approved; never active$`),
		regexp.MustCompile(`^  town@2  published by alice at \S+, not approved; never active$`),
		regexp.MustCompile(`^\* town@1  published by alice at \S+, approved by \S+ at \S+; active \S+ to \S+, \S+ to now$`),
	} {
		if !want.MatchString(lines[i]) {
			t.Errorf("history line %d = %q, want %s", i, lines[i], want)
		}
	}
	// AC-4: 4 is published, inactive, and can be activated again.
	mustRun(t, alice, "content", "activate", "town", "4", "--yes")
	s.awaitServing(t, alice, "town@4")

	// AC-7: what changed from 1 to 4, pointing into 4's source.
	res = mustRun(t, alice, "content", "diff", "town", "1", "4")
	for _, want := range []string{
		`~ room town/plaza: title "Market Plaza" -> "The Market"  (town.aw:`,
		"+ room town/well  (town.aw:",
		"+ exit town/plaza west -> well  (town.aw:",
	} {
		if !strings.Contains(res.stdout, want) {
			t.Errorf("diff lacks %q:\n%s", want, res.stdout)
		}
	}
	if res := mustRun(t, alice, "content", "diff", "town", "1", "2"); res.stdout != "town@1 and town@2 define the same content\n" {
		t.Errorf("diff of identical versions: %q", res.stdout)
	}

	// AC-8: fetched sources compile to exactly the blobs town@4's manifest
	// names.
	// By default fetch writes to the directory name the version was
	// published from, which its Templates record, so it compiles back to
	// the same bytes.
	wd := t.TempDir()
	t.Chdir(wd)
	res = mustRun(t, alice, "content", "fetch", "town", "4")
	out := filepath.Join(wd, "town")
	if !strings.HasPrefix(res.stdout, filepath.Join("town", "")) {
		t.Errorf("fetch wrote to %q", res.stdout)
	}
	p, err := embeddedCore()
	if err != nil {
		t.Fatal(err)
	}
	compiled, ds := lang.Compile(out, p, nil)
	if compiled == nil {
		t.Fatalf("fetched sources don't compile: %v", ds)
	}
	cv, ok := s.reg.Manifest("town", 4)
	if !ok {
		t.Fatal("no manifest for town@4")
	}
	want := map[string]string{}
	for _, r := range cv.GetBlobs() {
		want[r.GetPath()] = string(r.GetHash())
	}
	got := map[string]string{}
	for _, b := range compiled.Blobs {
		sum := sha256.Sum256(b.Bytes)
		got[b.Path] = string(sum[:])
	}
	if len(got) != len(want) {
		t.Errorf("fetched source compiles to %d blobs; the manifest names %d", len(got), len(want))
	}
	for p, h := range want {
		if got[p] != h {
			t.Errorf("%s differs from the manifest's", p)
		}
	}
}

func TestContentPublishPath_TwoIdentities(t *testing.T) {
	testTwoIdentities(t, memoryStack(t))
}

// AC-9 and AC-10: a live-world change without --yes and no terminal, and
// --override without --reason, are usage errors before any RPC.
func TestContentActivate_UsageBeforeAnyRPC(t *testing.T) {
	env := isolatedEnv(t, nil) // no server, no credential: an RPC would fail differently
	res := runCLI(t, []string{"content", "activate", "town", "8", "--override"}, env)
	if res.exit != ExitUsage || !strings.Contains(res.stderr, "--override needs --reason") {
		t.Errorf("--override without --reason: exit=%d stderr=%q", res.exit, res.stderr)
	}

	s := memoryStack(t)
	_, alice := s.builder(t, "alice", "town")
	mustRun(t, alice, "content", "publish", "--path", devFixture)
	for _, args := range [][]string{
		{"content", "activate", "town", "1"},
		{"content", "rollback", "town", "--to", "1"},
	} {
		res := runCLI(t, append(args, "-o", "json"), alice)
		if res.exit != ExitUsage || jsonErrorCode(t, res.stdout) != CodeYesRequired || !strings.Contains(res.stdout, "--yes required") {
			t.Errorf("%v on a non-TTY: exit=%d stdout=%q", args, res.exit, res.stdout)
		}
	}
	if got := s.loader.Versions()["town"]; got != 0 {
		t.Errorf("a refused confirmation moved the pointer to %d", got)
	}
}

// runTTY is runCLI with someone at the terminal answering.
func runTTY(t *testing.T, args []string, env map[string]string, answer string) runResult {
	t.Helper()
	var stdout, stderr bytes.Buffer
	exit := execute(&runtime{
		args: args, stdout: &stdout, stderr: &stderr, lookupEnv: lookupFrom(env),
		stdin: strings.NewReader(answer), tty: func() bool { return true },
	})
	return runResult{stdout: stdout.String(), stderr: stderr.String(), exit: exit}
}

// AC-11: an Operator approving their own publish is asked first; declining
// changes nothing; a non-TTY needs --yes; the printed line comes from the
// server's self_approval.
func TestContentApprove_OperatorSelfApproval(t *testing.T) {
	s := memoryStack(t)
	oper := s.identity(t, "oper", "operator-password")
	mustRun(t, oper, "content", "publish", "--path", devFixture)

	res := runCLI(t, []string{"content", "approve", "town", "1", "-o", "json"}, oper)
	if res.exit != ExitUsage || jsonErrorCode(t, res.stdout) != CodeYesRequired {
		t.Errorf("non-TTY without --yes: exit=%d stdout=%q", res.exit, res.stdout)
	}
	res = runTTY(t, []string{"content", "approve", "town", "1"}, oper, "n\n")
	if res.exit != ExitFail || !strings.Contains(res.stderr, "You published town@1. Approve it yourself as oper? [y/N]") {
		t.Errorf("declined: exit=%d stderr=%q", res.exit, res.stderr)
	}
	if cv, _ := s.reg.Manifest("town", 1); cv.GetApprovedBy() != "" {
		t.Fatal("a declined prompt approved the version")
	}
	res = runTTY(t, []string{"content", "approve", "town", "1"}, oper, "y\n")
	if res.exit != ExitOK || res.stdout != "town@1 approved by oper (self-approval: you published it)\n" {
		t.Errorf("approved: exit=%d stdout=%q stderr=%q", res.exit, res.stdout, res.stderr)
	}

	// Another's approval asks nothing and says nothing about self.
	mustRun(t, oper, "content", "publish", "--path", devFixture)
	_, bob := s.builder(t, "bob", "town")
	if res := mustRun(t, bob, "content", "approve", "town", "2"); res.stdout != "town@2 approved by bob\n" {
		t.Errorf("bob approves: %q", res.stdout)
	}
}

// AC-12: an activation the Loader would refuse prints its reason and
// subjects, with the reason as error.code.
func TestContentActivate_RefusalReasonAndSubjects(t *testing.T) {
	s := memoryStack(t)
	_, alice := s.builder(t, "alice", "town")
	_, bob := s.builder(t, "bob", "town")
	mustRun(t, alice, "content", "publish", "--path", devFixture)
	mustRun(t, bob, "content", "approve", "town", "1")
	mustRun(t, alice, "content", "activate", "town", "1", "--yes")
	s.awaitServing(t, alice, "town@1")

	// Purgatory is entered at spawn, never on foot, so a version without
	// it validates; removing a Zone in effect is refused at activation.
	without := copyPack(t, devFixture, map[string][2]string{"purgatory.aw": {"", "\x00delete"}})
	mustRun(t, alice, "content", "publish", "--path", without)
	mustRun(t, bob, "content", "approve", "town", "2")
	res := runCLI(t, []string{"content", "activate", "town", "2", "--yes"}, alice)
	if res.exit != ExitFail || !strings.Contains(res.stderr, "town@2 refused: zone_removed (purgatory)") {
		t.Errorf("exit=%d stderr=%q", res.exit, res.stderr)
	}
	res = runCLI(t, []string{"content", "activate", "town", "2", "--yes", "-o", "json"}, alice)
	var env jsonErrorEnvelope
	if err := json.Unmarshal([]byte(res.stdout), &env); err != nil || env.Error.Code != "zone_removed" {
		t.Fatalf("json: %s (%v)", res.stdout, err)
	}
	if subjects, _ := env.Error.Detail["subjects"].([]any); len(subjects) != 1 || subjects[0] != "purgatory" {
		t.Errorf("subjects = %v", env.Error.Detail["subjects"])
	}
	if env.Error.Detail["trace_id"] == "" || env.Error.Detail["trace_id"] == nil {
		t.Error("the error envelope carries no trace_id")
	}
}

// AC-13: server info, human and JSON.
func TestServerInfo(t *testing.T) {
	s := memoryStack(t)
	_, alice := s.builder(t, "alice", "town")
	_, bob := s.builder(t, "bob", "town")
	mustRun(t, alice, "content", "publish", "--path", devFixture)
	mustRun(t, bob, "content", "approve", "town", "1")
	mustRun(t, alice, "content", "activate", "town", "1", "--yes")
	s.awaitServing(t, alice, "town@1")

	res := mustRun(t, alice, "server", "info")
	want := regexp.MustCompile(`^version \S*\ncommit \S*\nenvironment \S*\nprotocol 1-1\ncontent andara\.core@1\ncontent town@1\ncontent_digest [0-9a-f]{64}\n$`)
	if !want.MatchString(res.stdout) {
		t.Errorf("server info:\n%s", res.stdout)
	}
	res = mustRun(t, alice, "server", "info", "-o", "json")
	var out struct {
		Content []struct {
			Pack    string `json:"pack"`
			Version uint64 `json:"version"`
		} `json:"content"`
		ProtocolMin uint32 `json:"protocol_min"`
	}
	if err := json.Unmarshal([]byte(res.stdout), &out); err != nil || len(out.Content) != 2 || out.Content[1].Pack != "town" || out.Content[1].Version != 1 {
		t.Errorf("json: %s (%v)", res.stdout, err)
	}

	s.stop(t)
	if res := runCLI(t, []string{"server", "info"}, alice); res.exit != ExitConnect {
		t.Errorf("server down: exit=%d", res.exit)
	}
}

// AC-14: an Operator rolls andara.core's pointer back with no approval, and
// a publish of a pack declaring andara.core prints the server's refusal.
func TestContent_CoreRollbackAndPublish(t *testing.T) {
	s := memoryStack(t)
	oper := s.identity(t, "oper", "operator-password")

	res := runCLI(t, []string{"content", "publish", "--path", corpusCase("valid", "core")}, oper)
	if res.exit != ExitFail || !strings.Contains(res.stderr, "andara.core is published by the server at boot") {
		t.Errorf("publish core: exit=%d stderr=%q", res.exit, res.stderr)
	}

	// A core@2, published and activated as a newer server's boot would,
	// then rolled back to 1.
	if err := s.bootCore(t, 2); err != nil {
		t.Fatal(err)
	}
	s.awaitServing(t, oper, "andara.core@2")
	res = mustRun(t, oper, "content", "rollback", "andara.core", "--yes")
	if res.stdout != "andara.core@1 active (rolled back from andara.core@2)\n" {
		t.Errorf("rollback: %q", res.stdout)
	}
	s.awaitServing(t, oper, "andara.core@1")
}

// AC-5: local validation passes and the server refuses. The findings print as
// local ones do, placed on the source; unreachable is 3.
func TestContentPublish_ServerRefusalPrintsLikeLocal(t *testing.T) {
	s := memoryStack(t)
	_, alice := s.builder(t, "alice", "town", "annex")
	_, bob := s.builder(t, "bob", "town")
	mustRun(t, alice, "content", "publish", "--path", devFixture)
	mustRun(t, bob, "content", "approve", "town", "1")
	mustRun(t, alice, "content", "activate", "town", "1", "--yes")
	s.awaitServing(t, alice, "town@1")

	// A pack of its own that validates alone, and declares a Zone id town
	// already has: only the server sees both.
	annex := filepath.Join(t.TempDir(), "annex")
	if err := os.MkdirAll(annex, 0o755); err != nil {
		t.Fatal(err)
	}
	writeAW(t, annex, "pack.aw", "pack annex requires andara.core@1\n")
	writeAW(t, annex, "z.aw", "zone town \"Another Town\" {\n  fallback gate\n\n  room gate \"Gate\" {}\n}\n")
	if res := runCLI(t, []string{"content", "validate", "--path", annex}, alice); res.exit != ExitOK {
		t.Fatalf("validates locally: exit=%d stderr=%q", res.exit, res.stderr)
	}
	res := runCLI(t, []string{"content", "publish", "--path", annex}, alice)
	if res.exit != ExitFail || !strings.Contains(res.stderr, filepath.Join(annex, "z.aw")+":1:1: duplicate_zone ") {
		t.Errorf("exit=%d stderr=%q", res.exit, res.stderr)
	}
	res = runCLI(t, []string{"content", "publish", "--path", annex, "-o", "json"}, alice)
	// The collision also leaves town's other Zones' Exits unresolved: the
	// gate's findings about another pack's Zones stay on its blobs, since
	// this source has no line for them. Its own is placed on z.aw.
	var ds []jsonDiagnostic
	if err := json.Unmarshal([]byte(res.stdout), &ds); err != nil {
		t.Fatalf("json: %s (%v)", res.stdout, err)
	}
	placed := false
	for _, d := range ds {
		if d.Code == "duplicate_zone" {
			placed = d.File == "z.aw" && d.Line == 1 && d.Col == 1
		}
	}
	if !placed || res.exit != ExitFail {
		t.Errorf("exit=%d json: %s", res.exit, res.stdout)
	}

	s.stop(t)
	if res := runCLI(t, []string{"content", "publish", "--path", devFixture}, alice); res.exit != ExitConnect {
		t.Errorf("server down: exit=%d stderr=%q", res.exit, res.stderr)
	}
}

// A stale parent is explained, not retried: the reason is error.code and
// the message says what to run.
func TestContentPublish_StaleParentIsExplained(t *testing.T) {
	ce := connect.NewError(connect.CodeFailedPrecondition, errors.New("town was published at 9 since parent 8"))
	if d, err := connect.NewErrorDetail(&errdetails.ErrorInfo{Domain: contentDomain, Reason: "stale_parent"}); err == nil {
		ce.AddDetail(d)
	}
	rt := &runtime{settings: &resolved{Output: outputHuman}}
	err := rt.publishRefused(ce, &validated{}, "town", 8)
	var ae *AppError
	if !errors.As(err, &ae) || ae.Exit != ExitFail || ae.Code != "stale_parent" || !strings.Contains(ae.Message, "run `content history town` and publish again") {
		t.Errorf("err = %#v", err)
	}
}

// The Observability requirements: content.publish with blobs_total,
// blobs_uploaded and bytes; one content.publish_blob span per stream; pack
// and version on cli.command; override and reason on activate's.
func TestContentPublish_Spans(t *testing.T) {
	s := memoryStack(t)
	_, alice := s.builder(t, "alice", "town")
	rec := tracetest.NewSpanRecorder()
	var stdout, stderr bytes.Buffer
	exit := execute(&runtime{
		args: []string{"content", "publish", "--path", devFixture}, stdout: &stdout, stderr: &stderr,
		lookupEnv: lookupFrom(alice), tpOptions: []sdktrace.TracerProviderOption{sdktrace.WithSpanProcessor(rec)},
	})
	if exit != ExitOK {
		t.Fatalf("exit=%d stderr=%q", exit, stderr.String())
	}
	var publish sdktrace.ReadOnlySpan
	blobs := 0
	attrs := map[string]map[string]any{}
	for _, sp := range rec.Ended() {
		m := map[string]any{}
		for _, kv := range sp.Attributes() {
			m[string(kv.Key)] = kv.Value.AsInterface()
		}
		attrs[sp.Name()] = m
		switch sp.Name() {
		case "content.publish":
			publish = sp
		case "content.publish_blob":
			blobs++
		}
	}
	if publish == nil {
		t.Fatal("no content.publish span")
	}
	p := attrs["content.publish"]
	if p["blobs_uploaded"] != int64(blobs) || p["blobs_total"] != int64(blobs) || p["bytes"].(int64) <= 0 {
		t.Errorf("content.publish = %v with %d blob spans", p, blobs)
	}
	if c := attrs["cli.command"]; c["pack"] != "town" || c["version"] != int64(1) {
		t.Errorf("cli.command = %v", c)
	}

	// --override without approval, as an Operator.
	oper := s.identity(t, "oper", "operator-password")
	rec = tracetest.NewSpanRecorder()
	exit = execute(&runtime{
		args:   []string{"content", "activate", "town", "1", "--yes", "--override", "--reason", "demo"},
		stdout: &stdout, stderr: &stderr, lookupEnv: lookupFrom(oper),
		tpOptions: []sdktrace.TracerProviderOption{sdktrace.WithSpanProcessor(rec)},
	})
	if exit != ExitOK {
		t.Fatalf("override: exit=%d stderr=%q", exit, stderr.String())
	}
	for _, sp := range rec.Ended() {
		if sp.Name() != "cli.command" {
			continue
		}
		m := map[string]any{}
		for _, kv := range sp.Attributes() {
			m[string(kv.Key)] = kv.Value.AsInterface()
		}
		if m["override"] != true || m["reason"] != "demo" || !strings.Contains(m["confirmation"].(string), "override: demo") {
			t.Errorf("cli.command = %v", m)
		}
	}
}
