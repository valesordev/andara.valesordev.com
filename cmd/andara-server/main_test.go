// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRun_ValidateOnlyValid(t *testing.T) {
	path := abs(t, filepath.Join("..", "..", "testdata", "content", "valid"))
	var stdout, stderr bytes.Buffer
	code := run([]string{"--validate-only", "--content-source=dir", "--content-path=" + path}, emptyEnv, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d; stderr=%s", code, stderr.String())
	}
}

func TestRun_ValidateOnlyDangling(t *testing.T) {
	path := abs(t, filepath.Join("..", "..", "testdata", "content", "dangling"))
	var stdout, stderr bytes.Buffer
	code := run([]string{"--validate-only", "--content-source=dir", "--content-path=" + path}, emptyEnv, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit %d, want 1; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "unknown_room") {
		t.Errorf("stderr missing unknown_room: %s", stderr.String())
	}
}

func TestRun_ValidateOnlyEnvPath(t *testing.T) {
	path := abs(t, filepath.Join("..", "..", "testdata", "content", "valid"))
	env := func(k string) (string, bool) {
		switch k {
		case "ANDARA_CONTENT_SOURCE":
			return "dir", true
		case "ANDARA_CONTENT_PATH":
			return path, true
		case "ANDARA_OTLP_ENDPOINT":
			return "", true
		default:
			return "", false
		}
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{"--validate-only"}, env, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d; %s", code, stderr.String())
	}
}

func emptyEnv(k string) (string, bool) {
	if k == "ANDARA_OTLP_ENDPOINT" {
		return "", true
	}
	return "", false
}

func abs(t *testing.T, p string) string {
	t.Helper()
	a, err := filepath.Abs(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(a); err != nil {
		t.Fatal(err)
	}
	return a
}
