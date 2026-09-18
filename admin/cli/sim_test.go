// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package cli

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func runRepl(t *testing.T, stdin string, extra ...string) runResult {
	t.Helper()
	content, err := filepath.Abs(filepath.Join("..", "..", "testdata", "content", "valid"))
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	args := append([]string{"sim", "repl", "--content", content}, extra...)
	exit := execute(&runtime{args: args, stdout: &stdout, stderr: &stderr, lookupEnv: lookupFrom(isolatedEnv(t, nil)), stdin: strings.NewReader(stdin)})
	return runResult{stdout: stdout.String(), stderr: stderr.String(), exit: exit}
}

// The story's operator transcript: look, north, west, frobnicate — the
// pipeline in-process with a fake log.
func TestSimRepl_Transcript(t *testing.T) {
	res := runRepl(t, "look\nnorth\nwest\nfrobnicate\n")
	if res.exit != ExitOK {
		t.Fatalf("exit=%d stderr=%s", res.exit, res.stderr)
	}
	for _, want := range []string{
		"The Pier",
		"exits: north, south",
		"you leaves north.",
		"you arrives from the south.",
		"rejected (post-log): no_such_exit: there is no exit west",
		`rejected (pre-log, parse): unknown_verb: unknown verb "frobnicate" — nothing in the log`,
	} {
		if !strings.Contains(res.stdout, want) {
			t.Errorf("stdout lacks %q:\n%s", want, res.stdout)
		}
	}
	// The cross-Zone move (pier → plaza) resolves one tick after it left.
	left := strings.Index(res.stdout, "[tick 2] you leaves north.")
	arrived := strings.Index(res.stdout, "[tick 3] you arrives from the south.")
	if left < 0 || arrived < left {
		t.Errorf("cross-Zone arrival not one tick later:\n%s", res.stdout)
	}
}

func TestSimRepl_JSON(t *testing.T) {
	res := runRepl(t, "look\nxyzzy\n", "-o", "json")
	if res.exit != ExitOK {
		t.Fatalf("exit=%d stderr=%s", res.exit, res.stderr)
	}
	dec := json.NewDecoder(strings.NewReader(res.stdout))
	var objs []map[string]any
	for dec.More() {
		var v map[string]any
		if err := dec.Decode(&v); err != nil {
			t.Fatalf("stdout is not a JSON stream: %v\n%s", err, res.stdout)
		}
		objs = append(objs, v)
	}
	if len(objs) != 3 {
		t.Fatalf("objects = %d:\n%s", len(objs), res.stdout)
	}
	if objs[1]["type"] != "room_described" {
		t.Fatalf("second object = %v", objs[1])
	}
	rej := objs[2]["rejected"].(map[string]any)
	if rej["stage"] != "parse" || rej["code"] != "unknown_verb" || rej["pre_log"] != true {
		t.Fatalf("rejection = %v", rej)
	}
}

func TestSimRepl_Flags(t *testing.T) {
	res := runRepl(t, "look\n", "--start", "town/hall", "--character", "alice")
	if res.exit != ExitOK || !strings.Contains(res.stdout, "Town Hall") {
		t.Fatalf("exit=%d\n%s%s", res.exit, res.stdout, res.stderr)
	}
	if res := runRepl(t, "", "--start", "town/nowhere"); res.exit != ExitUsage {
		t.Fatalf("bad --start: exit=%d stderr=%s", res.exit, res.stderr)
	}
	if res := runRepl(t, "", "--verb-table", filepath.Join(t.TempDir(), "missing.json")); res.exit != ExitUsage {
		t.Fatalf("bad --verb-table: exit=%d stderr=%s", res.exit, res.stderr)
	}
	res = runCLI(t, []string{"sim", "repl"}, nil)
	if res.exit != ExitUsage || !strings.Contains(res.stderr, "--content is required") {
		t.Fatalf("no --content: exit=%d stderr=%s", res.exit, res.stderr)
	}
	res = runCLI(t, []string{"sim", "repl", "--content", t.TempDir()}, nil)
	if res.exit != ExitFail || !strings.Contains(res.stderr, "no_zones_found") {
		t.Fatalf("empty content: exit=%d stderr=%s", res.exit, res.stderr)
	}
}

// AW-SRV-004's operator plan: two observers in different Rooms watch a
// move; each is sent only what its Room perceives, and a world tap sees
// everything.
func TestSimRepl_TapEvents(t *testing.T) {
	res := runRepl(t, "north\nlook\n", "--start", "town/plaza", "--tap-events", "town/hall,docks/pier,world")
	if res.exit != ExitOK {
		t.Fatalf("exit=%d stderr=%s", res.exit, res.stderr)
	}
	for _, want := range []string{
		"[tick 1] you leaves north.",
		"[tick 1] you arrives from the south.",
		"[tick 1, town/hall] you arrives from the south.",
		"[tick 1, world] you leaves north.",
		"[tick 2] Town Hall",
	} {
		if !strings.Contains(res.stdout, want) {
			t.Errorf("stdout lacks %q:\n%s", want, res.stdout)
		}
	}
	if strings.Contains(res.stdout, "docks/pier]") {
		t.Errorf("the pier perceived a move in town:\n%s", res.stdout)
	}
	if strings.Contains(res.stdout, "town/hall] Town Hall") {
		t.Errorf("a bystander in the hall was sent the looker's description:\n%s", res.stdout)
	}
	if res := runRepl(t, "", "--tap-events", "nowhere"); res.exit != ExitUsage {
		t.Fatalf("bad tap: exit=%d stderr=%s", res.exit, res.stderr)
	}
}
