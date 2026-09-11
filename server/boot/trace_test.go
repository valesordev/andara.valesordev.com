// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package boot

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/valesordev/andara/server/config"
	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/telemetry"
)

// The Observability requirements name two spans and their attributes. Nothing
// asserted either before this file: a registered instrument that never emits
// looks identical to a working one until an incident, which is why CLAUDE.md §8
// asks for instrumentation verified against a backend rather than merely
// declared. A SpanRecorder is that backend in a unit test.
func recordingRuntime(t *testing.T, path string, strict bool) (*Runtime, *tracetest.SpanRecorder, *bytes.Buffer) {
	t.Helper()
	cfg := config.Config{
		ContentSource: "dir",
		ContentPath:   path,
		StrictOrphans: strict,
		ServiceName:   "andara-server",
		Environment:   "test",
		LogLevel:      "info",
	}
	var logs bytes.Buffer
	tel := telemetry.Setup(cfg, &logs)
	t.Cleanup(func() { tel.Shutdown(context.Background()) })

	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	tel.TP = tp
	tel.Tracer = tp.Tracer("andara-server")

	return New(cfg, tel), rec, &logs
}

func spanByName(t *testing.T, rec *tracetest.SpanRecorder, name string) sdktrace.ReadOnlySpan {
	t.Helper()
	var found sdktrace.ReadOnlySpan
	names := make([]string, 0, len(rec.Ended()))
	for _, s := range rec.Ended() {
		names = append(names, s.Name())
		if s.Name() == name {
			found = s
		}
	}
	if found == nil {
		t.Fatalf("no span named %q; recorded %v", name, names)
	}
	return found
}

func intAttr(t *testing.T, s sdktrace.ReadOnlySpan, key string) int64 {
	t.Helper()
	for _, a := range s.Attributes() {
		if string(a.Key) == key {
			return a.Value.AsInt64()
		}
	}
	t.Fatalf("span %s has no attribute %q; has %v", s.Name(), key, s.Attributes())
	return 0
}

func TestLoadContent_EmitsLoadAndValidateSpans(t *testing.T) {
	rt, rec, logs := recordingRuntime(t, fixture(t, "valid"), false)
	if code := rt.LoadContent(context.Background()); code != ExitOK {
		t.Fatalf("exit %d; logs=%s", code, logs.String())
	}

	load := spanByName(t, rec, "content.load")
	if got := intAttr(t, load, "zone_count"); got != 3 {
		t.Errorf("content.load zone_count = %d, want 3", got)
	}
	if got := intAttr(t, load, "room_count"); got != 6 {
		t.Errorf("content.load room_count = %d, want 6", got)
	}

	validate := spanByName(t, rec, "content.validate")
	if got := intAttr(t, validate, "error_count"); got != 0 {
		t.Errorf("content.validate error_count = %d, want 0", got)
	}

	// content.validate is a child of content.load, not a sibling: the story
	// declares the parent/child relationship, and a flat pair of spans would not
	// show how much of a slow boot was spent validating.
	if validate.Parent().SpanID() != load.SpanContext().SpanID() {
		t.Errorf("content.validate parent = %v, want content.load %v",
			validate.Parent().SpanID(), load.SpanContext().SpanID())
	}
	if load.Parent().IsValid() {
		t.Errorf("content.load should be the boot root, has parent %v", load.Parent())
	}
}

func TestLoadContent_ValidateSpanCountsErrors(t *testing.T) {
	rt, rec, _ := recordingRuntime(t, fixture(t, "dangling"), false)
	if code := rt.LoadContent(context.Background()); code != ExitFail {
		t.Fatalf("exit %d, want 1", code)
	}
	if got := intAttr(t, spanByName(t, rec, "content.validate"), "error_count"); got < 1 {
		t.Errorf("content.validate error_count = %d, want >= 1", got)
	}
	load := spanByName(t, rec, "content.load")
	if got := intAttr(t, load, "zone_count"); got != 0 {
		t.Errorf("failed load reported zone_count = %d, want 0", got)
	}
}

// A failure in the source adapter never reaches BuildWorld. The span must still
// say a boot failed rather than reporting a clean validation.
func TestLoadContent_ValidateSpanCountsSourceFailures(t *testing.T) {
	rt, rec, _ := recordingRuntime(t, t.TempDir(), false)
	if code := rt.LoadContent(context.Background()); code != ExitFail {
		t.Fatalf("exit %d, want 1", code)
	}
	if got := intAttr(t, spanByName(t, rec, "content.validate"), "error_count"); got < 1 {
		t.Errorf("content.validate error_count = %d on an empty content set, want >= 1", got)
	}
}

// Findings on the boot path carry the boot trace_id and no session_id: there is
// no Session yet, and a correlation field that is always empty trains people to
// ignore it.
func TestLoadContent_FindingsCarryTheBootTraceID(t *testing.T) {
	rt, rec, logs := recordingRuntime(t, fixture(t, "dangling"), false)
	if code := rt.LoadContent(context.Background()); code != ExitFail {
		t.Fatalf("exit %d, want 1", code)
	}
	line := findLog(t, logs, "unknown_room")
	want := spanByName(t, rec, "content.validate").SpanContext().TraceID().String()
	if got := fmtString(line["trace_id"]); got != want {
		t.Errorf("trace_id = %q, want the boot trace %q", got, want)
	}
	if _, ok := line["session_id"]; ok {
		t.Error("boot-path findings must not carry session_id")
	}
}

// AC-6 at the process level, both ways round. The default is a warning and a
// World that serves; content.strict_orphans turns the identical content into a
// refusal. Only the sim layer tested this before, so the config key reaching
// BuildWorld was unverified.
func TestLoadContent_OrphanFixtureServesByDefault(t *testing.T) {
	rt, _, logs := recordingRuntime(t, fixture(t, "orphan"), false)
	if code := rt.LoadContent(context.Background()); code != ExitOK {
		t.Fatalf("exit %d, want 0; logs=%s", code, logs.String())
	}
	line := findLog(t, logs, "orphan_room")
	if line["level"] != "WARN" && line["level"] != "warn" {
		t.Errorf("level = %v, want warn", line["level"])
	}
	if line["room"] != "attic" {
		t.Errorf("room = %v, want attic", line["room"])
	}
	if !rt.Ready() {
		t.Error("an orphan must not keep the World from serving")
	}
	if got := testutil.ToFloat64(rt.Tel.Metrics.ZonesLoaded); got != 1 {
		t.Errorf("zones_loaded = %v, want 1", got)
	}
	if got := testutil.ToFloat64(
		rt.Tel.Metrics.ValidationErrors.WithLabelValues(string(sim.ErrOrphanRoom))); got < 1 {
		t.Errorf("validation_errors_total{code=orphan_room} = %v, want >= 1", got)
	}
}

func TestLoadContent_StrictOrphansRefusesToBoot(t *testing.T) {
	rt, rec, logs := recordingRuntime(t, fixture(t, "orphan"), true)
	if code := rt.LoadContent(context.Background()); code != ExitFail {
		t.Fatalf("exit %d, want 1; logs=%s", code, logs.String())
	}
	line := findLog(t, logs, "orphan_room")
	if line["level"] != "ERROR" && line["level"] != "error" {
		t.Errorf("level = %v, want error under strict_orphans", line["level"])
	}
	if rt.Ready() {
		t.Error("Ready() is true after a refused boot")
	}
	rr := httptest.NewRecorder()
	rt.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("readyz = %d, want 503", rr.Code)
	}
	if got := intAttr(t, spanByName(t, rec, "content.validate"), "error_count"); got < 1 {
		t.Errorf("content.validate error_count = %d, want >= 1", got)
	}
}
