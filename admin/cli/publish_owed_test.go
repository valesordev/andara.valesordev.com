// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	auditv1 "github.com/valesordev/andara/gen/go/andara/audit/v1"
	"github.com/valesordev/andara/server/recordlog"
)

// AW-CLI-003's §8 review, "What closes it" (architecture, 2026-09-30).

// publishCopy publishes an edited copy of the dev fixture as alice. Deleting a
// file is the edit "\x00delete".
func publishCopy(t *testing.T, env map[string]string, edits map[string][2]string, extra map[string]string) {
	t.Helper()
	dir := copyPack(t, devFixture, edits)
	for name, body := range extra {
		writeAW(t, dir, name, body)
	}
	mustRun(t, env, "content", "publish", "--path", dir)
}

// AC-7: diff names a Zone added and one removed, and Component fields
// changed, removed and added on Templates, each pointing into the source.
func TestContentDiff_ZonesTemplatesAndFields(t *testing.T) {
	s := memoryStack(t)
	_, alice := s.builder(t, "alice", "town")
	// town@1: the fixture, with the Lantern carrying an empty Behavior.
	publishCopy(t, alice, map[string][2]string{
		"items.aw": {"template Lantern extends andara.core.Item {}", "template Lantern extends andara.core.Item {\n  component andara.core.Behavior {}\n}"},
	}, nil)
	// town@2: Purgatory gone, a garden added, the Merchant's name changed,
	// the Guard's removed, and the Lantern's added.
	guardless := copyPack(t, devFixture, map[string][2]string{
		"purgatory.aw": {"", "\x00delete"},
		"items.aw":     {"template Lantern extends andara.core.Item {}", "template Lantern extends andara.core.Item {\n  component andara.core.Behavior { name: \"lantern.glow\" }\n}"},
	})
	b, err := os.ReadFile(filepath.Join(guardless, "npcs.aw"))
	if err != nil {
		t.Fatal(err)
	}
	b = bytes.Replace(b, []byte(`name: "town.merchant"`), []byte(`name: "town.trader"`), 1)
	b = bytes.Replace(b, []byte(`component andara.core.Behavior { name: "town.guard" }`), []byte(`component andara.core.Behavior {}`), 1)
	if err := os.WriteFile(filepath.Join(guardless, "npcs.aw"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	writeAW(t, guardless, "garden.aw", "zone garden \"Garden\" {\n  fallback bed\n\n  room bed \"Flower Bed\" {}\n}\n")
	mustRun(t, alice, "content", "publish", "--path", guardless)

	res := mustRun(t, alice, "content", "diff", "town", "1", "2")
	for _, want := range []string{
		"- zone purgatory  (town@1 purgatory.aw:1)",
		"+ zone garden  (garden.aw:1)",
		`~ field town.Merchant: Behavior.name "town.merchant" -> "town.trader"  (npcs.aw:12)`,
		`- field town.Guard: Behavior.name = "town.guard"  (npcs.aw:30)`,
		`+ field town.Lantern: Behavior.name = "lantern.glow"  (items.aw:3)`,
	} {
		if !strings.Contains(res.stdout, want+"\n") {
			t.Errorf("diff lacks %q:\n%s", want, res.stdout)
		}
	}
	// The same, as data, every line with its file and line.
	res = mustRun(t, alice, "content", "diff", "town", "1", "2", "-o", "json")
	var out struct {
		Changes []change `json:"changes"`
	}
	if err := json.Unmarshal([]byte(res.stdout), &out); err != nil || len(out.Changes) < 5 {
		t.Fatalf("json: %s (%v)", res.stdout, err)
	}
	for _, c := range out.Changes {
		if c.File == "" || c.Line == 0 {
			t.Errorf("a change without file:line: %+v", c)
		}
	}
}

// fetch and diff report the server's reason as error.code, as every publish
// path command does: a version that isn't published is not_found, and a
// pack the caller doesn't hold is pack_not_held.
func TestContentFetchAndDiff_ServerReasons(t *testing.T) {
	s := memoryStack(t)
	_, alice := s.builder(t, "alice", "town")
	_, carol := s.builder(t, "carol") // holds nothing
	mustRun(t, alice, "content", "publish", "--path", devFixture)
	mustRun(t, alice, "content", "publish", "--path", devFixture)

	for _, tc := range []struct {
		name string
		env  map[string]string
		args []string
		code string
	}{
		{"fetch an unpublished version", alice, []string{"content", "fetch", "town", "9", "--out", t.TempDir()}, "not_found"},
		{"diff against an unpublished version", alice, []string{"content", "diff", "town", "1", "9"}, "not_found"},
		{"fetch a pack not held", carol, []string{"content", "fetch", "town", "1", "--out", t.TempDir()}, "pack_not_held"},
		{"diff a pack not held", carol, []string{"content", "diff", "town", "1", "2"}, "pack_not_held"},
	} {
		res := runCLI(t, append(tc.args, "-o", "json"), tc.env)
		if res.exit != ExitFail || jsonErrorCode(t, res.stdout) != tc.code {
			t.Errorf("%s: exit=%d stdout=%q", tc.name, res.exit, res.stdout)
		}
	}
}

// testStaleParent: a publish whose parent another publish overtook, between
// its uploads and its PublishVersion, exits 1 stale_parent with the hint, and
// is not retried.
func testStaleParent(t *testing.T, s *contentStack) {
	_, alice := s.builder(t, "alice", "town")
	mustRun(t, alice, "content", "publish", "--path", devFixture) // town@1, the parent both start from
	raw := &contentServer{reg: s.reg}
	var stdout, stderr bytes.Buffer
	exit := execute(&runtime{
		args: []string{"content", "publish", "--path", devFixture, "-o", "json"}, stdout: &stdout, stderr: &stderr,
		lookupEnv: lookupFrom(alice),
		beforePublishVersion: func() {
			raw.publish(t, "town", 1, map[string][]byte{"src/pack.aw": []byte("pack town requires andara.core@1\n")})
		},
	})
	if exit != ExitFail {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exit, stdout.String(), stderr.String())
	}
	var env jsonErrorEnvelope
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil || env.Error.Code != "stale_parent" ||
		!strings.Contains(env.Error.Message, "someone published after version 1") || !strings.Contains(env.Error.Message, "run `content history town` and publish again") {
		t.Fatalf("stdout = %s (%v)", stdout.String(), err)
	}
	if v := s.reg.Newest("town"); v != 2 {
		t.Errorf("town@%d is newest; the stale publish wrote something, or retried", v)
	}
}

func TestContentPublish_StaleParent(t *testing.T) { testStaleParent(t, memoryStack(t)) }

// SRE's confirmation-line check: activate and rollback, at --log-level info,
// log the confirmation they showed. The line decodes, carries the text, and
// its trace_id is the JSON result's and the server's audit record's.
func TestContentActivate_ConfirmationLineJoinsTheServersRecord(t *testing.T) {
	s := memoryStack(t)
	_, alice := s.builder(t, "alice", "town")
	_, bob := s.builder(t, "bob", "town")
	for range 2 {
		mustRun(t, alice, "content", "publish", "--path", devFixture)
	}
	mustRun(t, bob, "content", "approve", "town", "1")
	mustRun(t, bob, "content", "approve", "town", "2")
	mustRun(t, alice, "content", "activate", "town", "1", "--yes")

	for _, tc := range []struct {
		args, text, action string
	}{
		{"content activate town 2 --yes", "activate town@2, replacing town@1", "activate"},
		{"content rollback town --yes", "roll back to town@1, replacing town@2", "rollback"},
	} {
		res := runCLI(t, append(strings.Fields(tc.args), "-o", "json", "--log-level", "info"), alice)
		if res.exit != ExitOK {
			t.Fatalf("%s: exit=%d stderr=%q", tc.args, res.exit, res.stderr)
		}
		var result struct {
			TraceID string `json:"trace_id"`
		}
		if err := json.Unmarshal([]byte(res.stdout), &result); err != nil || result.TraceID == "" {
			t.Fatalf("%s: result %q (%v)", tc.args, res.stdout, err)
		}
		var line *logLine
		for _, l := range strings.Split(strings.TrimSpace(res.stderr), "\n") {
			var ll logLine
			if json.Unmarshal([]byte(l), &ll) == nil && strings.HasPrefix(ll.Msg, "confirmation: ") {
				line = &ll
			}
		}
		if line == nil {
			t.Fatalf("%s: no confirmation line in %q", tc.args, res.stderr)
		}
		if line.Level != "info" || line.Command != "content "+strings.Fields(tc.args)[1] || !strings.Contains(line.Msg, tc.text) || line.TS == "" {
			t.Errorf("%s: line = %+v", tc.args, line)
		}
		if line.TraceID != result.TraceID {
			t.Errorf("%s: the line's trace_id %s is not the result's %s", tc.args, line.TraceID, result.TraceID)
		}
		if got := auditTrace(t, s.audit, tc.action); got != result.TraceID {
			t.Errorf("%s: the server's %s record has trace_id %q, the CLI %q", tc.args, tc.action, got, result.TraceID)
		}
	}
}

// auditTrace is the trace_id of the last ok record of action on the audit topic.
func auditTrace(t *testing.T, audit recordlog.Log, action string) string {
	t.Helper()
	var id string
	err := audit.Replay(context.Background(), func(r recordlog.Record) error {
		var rec auditv1.AuditRecord
		if err := proto.Unmarshal(r.Value, &rec); err != nil {
			return err
		}
		if rec.GetAction() == action && rec.GetOutcome() == "ok" {
			id = rec.GetTraceId()
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}
