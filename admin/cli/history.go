// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package cli

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// The play history: what was typed, so the up arrow works across sessions.
// It lives under $XDG_STATE_HOME/andara/history (AW-CLI-004) — state, not
// config — and is opt-out with --no-history. Nothing typed at a password
// prompt reaches it, because play has no such prompt: ReadPassword bypasses
// the terminal's history, and credentials come from `auth login`.

const historyLimit = 1000

func (rt *runtime) stateHome() string {
	if v, ok := rt.lookupEnv("XDG_STATE_HOME"); ok && strings.TrimSpace(v) != "" {
		return v
	}
	if home, ok := rt.lookupEnv("HOME"); ok && home != "" {
		return filepath.Join(home, ".local", "state")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".local", "state")
	}
	return ""
}

func (rt *runtime) defaultHistoryPath() string {
	return filepath.Join(rt.stateHome(), "andara", "history")
}

// fileHistory implements term.History over a file: the most recent
// historyLimit lines are loaded at start, and every line read is appended.
// Index 0 is the most recent, as the interface requires.
type fileHistory struct {
	path  string
	lines []string // oldest first
}

// loadHistory reads path, or starts empty when it does not exist. A file
// that cannot be read is not an error: history is a convenience, and play
// must not refuse to start over it.
func loadHistory(path string) *fileHistory {
	h := &fileHistory{path: path}
	f, err := os.Open(path)
	if err != nil {
		return h
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		if line := sc.Text(); line != "" {
			h.lines = append(h.lines, line)
		}
	}
	if n := len(h.lines); n > historyLimit {
		h.lines = append([]string(nil), h.lines[n-historyLimit:]...)
	}
	return h
}

// Add records a line: blank lines and a repeat of the last entry are
// dropped, and the file is written best-effort — appended while under
// the limit, rewritten from the kept lines once over it, so the file
// holds the advertised historyLimit lines and no more.
func (h *fileHistory) Add(entry string) {
	entry = strings.TrimSpace(entry)
	if entry == "" {
		return
	}
	if n := len(h.lines); n > 0 && h.lines[n-1] == entry {
		return
	}
	h.lines = append(h.lines, entry)
	compact := false
	if len(h.lines) > historyLimit {
		h.lines = h.lines[1:]
		compact = true
	}
	if h.path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(h.path), 0o700); err != nil {
		return
	}
	if compact {
		h.rewrite()
		return
	}
	f, err := os.OpenFile(h.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(entry + "\n")
}

// rewrite replaces the file with the kept lines, atomically.
func (h *fileHistory) rewrite() {
	tmp := h.path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strings.Join(h.lines, "\n")+"\n"), 0o600); err != nil {
		return
	}
	if err := os.Rename(tmp, h.path); err != nil {
		_ = os.Remove(tmp)
	}
}

func (h *fileHistory) Len() int { return len(h.lines) }

func (h *fileHistory) At(idx int) string {
	if idx < 0 || idx >= len(h.lines) {
		panic("history index out of range")
	}
	return h.lines[len(h.lines)-1-idx]
}
