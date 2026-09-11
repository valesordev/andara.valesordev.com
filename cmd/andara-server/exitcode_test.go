// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every AC in AW-SRV-001 that describes a failure says "exit code 1". Six of
// them were only ever tested at the sim or loader layer, where the return value
// is a slice of findings rather than a process status. A story whose whole point
// is fail-fast should not take it on faith that the findings reach os.Exit: a
// boot that logs ten errors and serves anyway is the exact failure this story
// exists to make impossible, and it would have passed every test that existed.
func TestRun_BrokenFixturesExitOne(t *testing.T) {
	cases := []struct {
		ac      string
		fixture string
		code    string // the ErrCode that must appear on stderr
	}{
		{ac: "AC-2", fixture: "dangling", code: "unknown_room"},
		{ac: "AC-3", fixture: "duplicate-room", code: "duplicate_room"},
		{ac: "AC-5", fixture: "missing-zone", code: "unknown_zone"},
		{ac: "AC-7", fixture: "unsupported-version", code: "unsupported_format_version"},
		{ac: "AC-8", fixture: "malformed", code: "malformed_file"},
		{ac: "AC-8", fixture: "bad-field-type", code: "malformed_file"},
		{ac: "AC-8", fixture: "unknown-field", code: "malformed_file"},
	}
	for _, tc := range cases {
		t.Run(tc.ac+"/"+tc.fixture, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run([]string{
				"--validate-only",
				"--content-source=dir",
				"--content-path=" + abs(t, filepath.Join("..", "..", "testdata", "content", tc.fixture)),
			}, emptyEnv, &stdout, &stderr)
			if code != 1 {
				t.Fatalf("exit %d, want 1; stderr=%s", code, stderr.String())
			}
			if !strings.Contains(stderr.String(), tc.code) {
				t.Errorf("stderr does not name %s:\n%s", tc.code, stderr.String())
			}
		})
	}
}

// AC-9: an empty content set is a configuration error, not an empty World.
func TestRun_EmptyContentExitsOne(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := run([]string{"--validate-only", "--content-source=dir", "--content-path=" + dir},
		emptyEnv, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "no_zones_found") {
		t.Errorf("stderr does not name no_zones_found:\n%s", stderr.String())
	}
	if !strings.Contains(stderr.String(), dir) {
		t.Errorf("stderr does not name the content source %s:\n%s", dir, stderr.String())
	}
}

// The default source is kafka and kafka is AW-SRV-012. It must refuse by name
// rather than boot an empty World.
func TestRun_DefaultSourceRefusesByName(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"--validate-only"}, emptyEnv, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	for _, want := range []string{"no_zones_found", "kafka", "AW-SRV-012"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("stderr does not name %q:\n%s", want, stderr.String())
		}
	}
}

// An unparseable --content-source must fail before any load is attempted.
func TestRun_BadSourceExitsOne(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--content-source=s3"}, emptyEnv, &stdout, &stderr); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
}

// AC-6: an orphan is legal. The process serves, and --validate-only exits 0,
// unless the operator asked for strict orphans — the same content, the opposite
// answer, which is the whole reason the key exists.
func TestRun_OrphanExitsZeroUnlessStrict(t *testing.T) {
	path := abs(t, filepath.Join("..", "..", "testdata", "content", "orphan"))

	var stdout, stderr bytes.Buffer
	if code := run([]string{"--validate-only", "--content-source=dir", "--content-path=" + path},
		emptyEnv, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d, want 0; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "orphan_room") {
		t.Errorf("an orphan must be warned about, never silent:\n%s", stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"--validate-only", "--content-source=dir", "--content-path=" + path,
		"--strict-orphans"}, emptyEnv, &stdout, &stderr); code != 1 {
		t.Fatalf("strict-orphans: exit %d, want 1; stderr=%s", code, stderr.String())
	}
}

// The exit-code contract says the process prints *every* finding before exiting,
// "a Builder fixing ten broken exits should need one boot, not ten". One finding
// plus an exit status would satisfy every other test in this file.
func TestRun_PrintsEveryFindingNotJustTheFirst(t *testing.T) {
	dir := t.TempDir()
	var b strings.Builder
	b.WriteString(`{"formatVersion":1,"id":"town","name":"Town","rooms":[`)
	b.WriteString(`{"id":"plaza","title":"Plaza","description":"d","exits":[`)
	for i := 0; i < 10; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"direction":"d` + string(rune('0'+i)) + `","toRoom":"missing` + string(rune('0'+i)) + `"}`)
	}
	b.WriteString(`]}]}`)
	if err := os.WriteFile(filepath.Join(dir, "town.json"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if code := run([]string{"--validate-only", "--content-source=dir", "--content-path=" + dir},
		emptyEnv, &stdout, &stderr); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}

	found := map[string]bool{}
	dec := json.NewDecoder(bytes.NewReader(stderr.Bytes()))
	for {
		var m map[string]any
		if err := dec.Decode(&m); err != nil {
			if err == io.EOF {
				break
			}
			t.Fatalf("stderr is not a stream of JSON log lines: %v\n%s", err, stderr.String())
		}
		if m["code"] == "unknown_room" {
			if d, ok := m["detail"].(string); ok {
				for i := 0; i < 10; i++ {
					if strings.Contains(d, "missing"+string(rune('0'+i))) {
						found["missing"+string(rune('0'+i))] = true
					}
				}
			}
		}
	}
	if len(found) != 10 {
		t.Errorf("reported %d of 10 broken exits; a Builder needs one boot, not ten: %v",
			len(found), stderr.String())
	}
}
