// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The file holds the advertised historyLimit lines and no more, and is
// read back most-recent first.
func TestHistory_Bounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "andara", "history")
	h := loadHistory(path)
	for i := 0; i < historyLimit+25; i++ {
		h.Add(fmt.Sprintf("line %d", i))
	}
	h.Add("line 1024") // a repeat of the last entry is dropped
	h.Add("   ")
	if h.Len() != historyLimit {
		t.Fatalf("Len = %d, want %d", h.Len(), historyLimit)
	}
	if got := h.At(0); got != "line 1024" {
		t.Errorf("At(0) = %q", got)
	}
	if got := h.At(historyLimit - 1); got != "line 25" {
		t.Errorf("oldest = %q, want line 25", got)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != historyLimit || lines[0] != "line 25" || lines[len(lines)-1] != "line 1024" {
		t.Errorf("file holds %d lines, first %q, last %q", len(lines), lines[0], lines[len(lines)-1])
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode %04o, want 0600", perm)
	}

	// Loaded again: the same lines, newest first.
	again := loadHistory(path)
	if again.Len() != historyLimit || again.At(0) != "line 1024" {
		t.Errorf("reloaded Len=%d At(0)=%q", again.Len(), again.At(0))
	}
	// A missing file is an empty history, not an error.
	if loadHistory(filepath.Join(t.TempDir(), "none")).Len() != 0 {
		t.Error("a missing file loaded lines")
	}
}
