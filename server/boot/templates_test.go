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

	"github.com/valesordev/andara/server/sim"
)

// AW-SRV-022 at the boot layer: the fixture's seven Templates load, the
// per-pack gauge and log line are emitting, and the span carries the count.
func TestLoadContent_Templates(t *testing.T) {
	path, err := filepath.Abs(filepath.Join("..", "..", "testdata", "templates"))
	if err != nil {
		t.Fatal(err)
	}
	rt, rec, logs := recordingRuntime(t, path, false)
	if code := rt.LoadContent(context.Background()); code != ExitOK {
		t.Fatalf("exit %d; logs=%s", code, logs.String())
	}
	if rt.Templates == nil || rt.Templates.Len() != 7 {
		t.Fatalf("registry %v", rt.Templates)
	}
	if got := testutil.ToFloat64(rt.Tel.Metrics.TemplatesLoaded.WithLabelValues("andara.core")); got != 4 {
		t.Errorf("templates_loaded{pack=andara.core} = %v, want 4", got)
	}
	if got := testutil.ToFloat64(rt.Tel.Metrics.TemplatesLoaded.WithLabelValues("town")); got != 3 {
		t.Errorf("templates_loaded{pack=town} = %v, want 3", got)
	}
	if got := intAttr(t, spanByName(t, rec, "content.load"), "template_count"); got != 7 {
		t.Errorf("content.load template_count = %d, want 7", got)
	}
	for _, want := range []string{`"msg":"templates loaded"`, `"pack":"andara.core","templates":4`, `"pack":"town","templates":3`} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("logs lack %s:\n%s", want, logs.String())
		}
	}
}

// zonesOnly is the valid fixture's Zone files without its templates/
// directory — the dev World carries the core pack since AW-SRV-014, and
// these tests want a World with none.
func zonesOnly(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	valid := fixture(t, "valid")
	entries, _ := os.ReadDir(valid)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(valid, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, e.Name()), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// Content with no templates/ directory loads with an empty registry and no
// gauge series: a World of Rooms is still a World.
func TestLoadContent_NoTemplates(t *testing.T) {
	rt, _, logs := recordingRuntime(t, zonesOnly(t), false)
	if code := rt.LoadContent(context.Background()); code != ExitOK {
		t.Fatalf("exit %d; logs=%s", code, logs.String())
	}
	if rt.Templates == nil || rt.Templates.Len() != 0 {
		t.Fatalf("registry %v", rt.Templates)
	}
	if got := testutil.CollectAndCount(rt.Tel.Metrics.TemplatesLoaded); got != 0 {
		t.Errorf("templates_loaded has %d series, want 0", got)
	}
}

// A refused Template refuses the boot: exit 1, the finding at error with
// the template named, counted by code, and Ready() false.
func TestLoadContent_TemplateFindingRefusesBoot(t *testing.T) {
	dir := zonesOnly(t)
	if err := os.MkdirAll(filepath.Join(dir, "templates"), 0o700); err != nil {
		t.Fatal(err)
	}
	raw := `{"formatVersion":1,"name":"town.Raw","kind":"ENTITY","chain":["town.Raw"],"resolved":false}`
	if err := os.WriteFile(filepath.Join(dir, "templates", "town.Raw.json"), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	rt, _, logs := recordingRuntime(t, dir, false)
	if code := rt.LoadContent(context.Background()); code != ExitFail {
		t.Fatalf("exit %d, want 1; logs=%s", code, logs.String())
	}
	if rt.Ready() || rt.Templates != nil {
		t.Error("refused boot left the runtime ready or with a registry")
	}
	line := findLog(t, logs, string(sim.ErrUnflattenedTemplate))
	if line["level"] != "ERROR" || line["template"] != "town.Raw" {
		t.Errorf("finding line %v", line)
	}
	if got := testutil.ToFloat64(rt.Tel.Metrics.ValidationErrors.WithLabelValues(string(sim.ErrUnflattenedTemplate))); got != 1 {
		t.Errorf("validation_errors_total{unflattened_template} = %v", got)
	}
}
