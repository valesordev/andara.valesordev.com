// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package boot

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// AW-SRV-003 at the boot layer: the built-in verb table loads and the
// pipeline metrics are registered; a file replaces it; a bad file fails
// the boot.
func TestLoadVerbs(t *testing.T) {
	rt, _, logs := recordingRuntime(t, t.TempDir(), false)
	if err := rt.LoadVerbs(context.Background()); err != nil {
		t.Fatal(err)
	}
	if rt.Verbs == nil || len(rt.Verbs.Verbs()) != 14 || rt.Commands == nil {
		t.Fatalf("verbs=%v commands=%v", rt.Verbs, rt.Commands)
	}
	if got := testutil.CollectAndCount(rt.Commands.Commands, "andara_commands_total"); got != 14 {
		t.Fatalf("andara_commands_total series = %d", got)
	}
	if !strings.Contains(logs.String(), `"source":"builtin"`) {
		t.Fatalf("logs: %s", logs.String())
	}

	path := filepath.Join(t.TempDir(), "verbs.json")
	if err := os.WriteFile(path, []byte(`{"verbs": [{"name": "look", "kind": "look"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	rt2, _, _ := recordingRuntime(t, t.TempDir(), false)
	rt2.Cfg.VerbTablePath = path
	if err := rt2.LoadVerbs(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(rt2.Verbs.Verbs()) != 1 {
		t.Fatalf("verbs = %v", rt2.Verbs.Verbs())
	}

	rt3, _, _ := recordingRuntime(t, t.TempDir(), false)
	rt3.Cfg.VerbTablePath = filepath.Join(t.TempDir(), "missing.json")
	if err := rt3.LoadVerbs(context.Background()); err == nil {
		t.Fatal("a missing verb table file did not fail the boot")
	}
}
