// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package boot

import (
	"context"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/valesordev/andara/server/sim"
)

// AW-SRV-021 observability: the Component counter is emitting, labeled by type,
// and the count is what the fixture declares. A registered instrument that never
// increments looks identical to a working one until an incident (CLAUDE.md §8).
func TestLoadContent_CountsComponentsByType(t *testing.T) {
	rt, rec, logs := recordingRuntime(t, fixture(t, "components"), false)
	if code := rt.LoadContent(context.Background()); code != ExitOK {
		t.Fatalf("exit %d, want 0; logs=%s", code, logs.String())
	}
	// Three on the cellar, one on the Zone.
	for _, ct := range []string{
		"andara.core.Dark", "andara.core.Indoors", "andara.core.NoMagic", "andara.core.NoRecall",
	} {
		if got := testutil.ToFloat64(rt.Tel.Metrics.Components.WithLabelValues(ct)); got != 1 {
			t.Errorf("components_total{component_type=%q} = %v, want 1", ct, got)
		}
	}
	if got := intAttr(t, spanByName(t, rec, "content.load"), "component_count"); got != 4 {
		t.Errorf("content.load component_count = %d, want 4", got)
	}
}

// Content that declares no Components must not touch the counter at all. A
// zero-valued series for every registered type would put four permanent series
// on every deployment that has never used one.
func TestLoadContent_NoComponentsNoSeries(t *testing.T) {
	rt, _, logs := recordingRuntime(t, fixture(t, "valid"), false)
	if code := rt.LoadContent(context.Background()); code != ExitOK {
		t.Fatalf("exit %d, want 0; logs=%s", code, logs.String())
	}
	if got := testutil.CollectAndCount(rt.Tel.Metrics.Components); got != 0 {
		t.Errorf("components_total has %d series on component-less content, want 0", got)
	}
}

// AW-SRV-021 AC-7 at the boot layer: the warning counter is emitting, the
// warning is logged at warn with the fields the observability contract names,
// and the World still serves.
func TestLoadContent_MissingReverseExitWarnsAndServes(t *testing.T) {
	rt, rec, logs := recordingRuntime(t, fixture(t, "one-way"), false)
	if code := rt.LoadContent(context.Background()); code != ExitOK {
		t.Fatalf("a one-way Exit must not refuse the boot: exit %d; logs=%s", code, logs.String())
	}
	if rt.World == nil {
		t.Error("no World after a load that only warned")
	}
	line := findLog(t, logs, string(sim.ErrMissingReverseExit))
	if line["level"] != "WARN" && line["level"] != "warn" {
		t.Errorf("level = %v, want warn", line["level"])
	}
	for _, field := range []string{"file", "zone", "room", "line", "trace_id"} {
		if line[field] == nil || line[field] == "" {
			t.Errorf("the warning carries no %s: %v", field, line)
		}
	}
	if line["room"] != "plaza" {
		t.Errorf("room = %v, want the Room the Exit leaves from", line["room"])
	}
	if got := testutil.ToFloat64(
		rt.Tel.Metrics.LoadWarnings.WithLabelValues(string(sim.ErrMissingReverseExit))); got != 1 {
		t.Errorf("load_warnings_total{kind=missing_reverse_exit} = %v, want 1", got)
	}
	if got := intAttr(t, spanByName(t, rec, "content.validate"), "warning_count"); got != 1 {
		t.Errorf("content.validate warning_count = %d, want 1", got)
	}
	if got := intAttr(t, spanByName(t, rec, "content.validate"), "error_count"); got != 0 {
		t.Errorf("content.validate error_count = %d, want 0", got)
	}
}

// An orphan counts as a warning under the default policy and as an error under
// strict_orphans. The two counters have to agree with the log level, which is
// the whole reason boot routes both through one place.
func TestLoadContent_OrphanCountsAsWarningUnlessStrict(t *testing.T) {
	rt, _, logs := recordingRuntime(t, fixture(t, "orphan"), false)
	if code := rt.LoadContent(context.Background()); code != ExitOK {
		t.Fatalf("exit %d, want 0; logs=%s", code, logs.String())
	}
	if got := testutil.ToFloat64(
		rt.Tel.Metrics.LoadWarnings.WithLabelValues(string(sim.ErrOrphanRoom))); got < 1 {
		t.Errorf("load_warnings_total{kind=orphan_room} = %v, want >= 1", got)
	}

	strict, _, slogs := recordingRuntime(t, fixture(t, "orphan"), true)
	if code := strict.LoadContent(context.Background()); code != ExitFail {
		t.Fatalf("strict: exit %d, want 1; logs=%s", code, slogs.String())
	}
	if got := testutil.CollectAndCount(strict.Tel.Metrics.LoadWarnings); got != 0 {
		t.Errorf("strict_orphans counted %d warnings; an orphan is an error there", got)
	}
}

// AW-SRV-021 AC-2 and AC-6 at the boot layer: a rejection refuses the boot, is
// logged at error, and carries the file, the Room, and the line.
func TestLoadContent_RejectionsLogAtErrorWithProvenance(t *testing.T) {
	for _, tc := range []struct {
		fixture string
		code    sim.ErrCode
		room    string
		line    bool
	}{
		{fixture: "unknown-component", code: sim.ErrUnknownComponent, room: "plaza", line: true},
		{fixture: "duplicate-component", code: sim.ErrDuplicateComponent, room: "plaza", line: true},
		{fixture: "bad-direction", code: sim.ErrUnknownDirection, room: "plaza", line: true},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			rt, _, logs := recordingRuntime(t, fixture(t, tc.fixture), false)
			if code := rt.LoadContent(context.Background()); code != ExitFail {
				t.Fatalf("exit %d, want 1; logs=%s", code, logs.String())
			}
			if rt.Ready() {
				t.Error("Ready() is true after a refused boot")
			}
			line := findLog(t, logs, string(tc.code))
			if line["level"] != "ERROR" && line["level"] != "error" {
				t.Errorf("level = %v, want error", line["level"])
			}
			if line["room"] != tc.room {
				t.Errorf("room = %v, want %s", line["room"], tc.room)
			}
			if line["file"] == nil || line["file"] == "" {
				t.Errorf("the rejection names no file: %v", line)
			}
			if tc.line && line["line"] == nil {
				t.Errorf("the rejection carries no line: %v", line)
			}
			if got := testutil.ToFloat64(
				rt.Tel.Metrics.ValidationErrors.WithLabelValues(string(tc.code))); got < 1 {
				t.Errorf("validation_errors_total{code=%s} = %v, want >= 1", tc.code, got)
			}
		})
	}
}
