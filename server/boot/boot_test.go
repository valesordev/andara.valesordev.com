// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package boot

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/valesordev/andara/server/config"
	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/telemetry"
)

func TestLoadContent_ValidThreeZones(t *testing.T) {
	rt, logs := runtime(t, fixture(t, "valid"), false)
	code := rt.LoadContent(context.Background())
	if code != ExitOK {
		t.Fatalf("exit %d; logs=%s", code, logs.String())
	}
	if rt.World == nil {
		t.Fatal("no World built")
	}
	// Validated is not in effect: the log is the source of that, and only
	// ReconcileContent makes the process ready (AW-SRV-012).
	if rt.Ready() {
		t.Fatal("ready before any content is in effect")
	}
	if got := testutil.ToFloat64(rt.Tel.Metrics.ZonesLoaded); got != 3 {
		t.Errorf("zones_loaded = %v, want 3", got)
	}
	if got := testutil.ToFloat64(rt.Tel.Metrics.RoomsLoaded.WithLabelValues("town")); got != 2 {
		t.Errorf("rooms_loaded{zone=town} = %v, want 2", got)
	}
	if got := testutil.ToFloat64(rt.Tel.Metrics.RoomsLoaded.WithLabelValues("wilds")); got != 2 {
		t.Errorf("rooms_loaded{zone=wilds} = %v", got)
	}
	if got := testutil.ToFloat64(rt.Tel.Metrics.RoomsLoaded.WithLabelValues("docks")); got != 2 {
		t.Errorf("rooms_loaded{zone=docks} = %v", got)
	}

	h := rt.Handler()
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("readyz before content is in effect = %d, want 503", rr.Code)
	}
	serveMemory(t, rt)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rr.Code != http.StatusOK {
		t.Errorf("readyz with content in effect = %d", rr.Code)
	}
	if v, _ := rt.Engine.Content(); len(v) != 1 || len(rt.World.Zones) != 3 {
		t.Errorf("in effect: %v, %d Zones; want the directory's genesis swap and its three Zones", v, len(rt.World.Zones))
	}
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rr.Body.String()
	for _, want := range []string{
		`andara_content_active_version{pack="dir"} 0`,
		`andara_build_info{commit="",content_version="0",env="",pack="dir",version=""} 1`,
		"andara_content_reload_stall_seconds_count 1",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics missing %s", want)
		}
	}
	if !strings.Contains(body, "andara_content_zones_loaded") {
		t.Errorf("metrics missing zones_loaded:\n%s", body)
	}
	if !strings.Contains(body, "andara_content_rooms_loaded") {
		t.Errorf("metrics missing rooms_loaded")
	}
	if !strings.Contains(body, "andara_content_load_duration_seconds") {
		t.Errorf("metrics missing load_duration")
	}
}

// AW-SRV-005 AC-8: Drain flips /readyz to 503 while the World stays loaded,
// so a load balancer stops routing here during the drain window.
func TestDrain_ReadyzGoes503(t *testing.T) {
	rt, logs := runtime(t, fixture(t, "valid"), false)
	if code := rt.LoadContent(context.Background()); code != ExitOK {
		t.Fatalf("exit %d; logs=%s", code, logs.String())
	}
	h := rt.Handler()
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz before content is in effect = %d, want 503", rr.Code)
	}
	serveMemory(t, rt)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("readyz before drain = %d", rr.Code)
	}
	rt.Drain()
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("readyz during drain = %d, want 503", rr.Code)
	}
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/livez", nil))
	if rr.Code != http.StatusOK {
		t.Errorf("livez during drain = %d, want 200: draining is not dying", rr.Code)
	}
	if rt.World == nil {
		t.Error("World unloaded by Drain")
	}
}

func TestLoadContent_DanglingLogsErrorFields(t *testing.T) {
	rt, logs := runtime(t, fixture(t, "dangling"), false)
	code := rt.LoadContent(context.Background())
	if code != ExitFail {
		t.Fatalf("exit %d, want 1", code)
	}
	line := findLog(t, logs, "unknown_room")
	for _, key := range []string{"ts", "level", "msg", "service", "env", "code", "file", "zone", "room", "detail", "trace_id"} {
		if _, ok := line[key]; !ok {
			t.Errorf("missing log field %q in %v", key, line)
		}
	}
	if line["level"] != "ERROR" && line["level"] != "error" {
		t.Errorf("level = %v, want error", line["level"])
	}
	if line["code"] != "unknown_room" {
		t.Errorf("code = %v", line["code"])
	}
	if !strings.Contains(fmtString(line["file"]), "town.json") {
		t.Errorf("file = %v", line["file"])
	}
	if line["room"] != "plaza" {
		t.Errorf("room = %v", line["room"])
	}
	if !strings.Contains(fmtString(line["detail"]), "nowhere") {
		t.Errorf("detail = %v", line["detail"])
	}
	rr := httptest.NewRecorder()
	rt.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("readyz = %d, want 503", rr.Code)
	}
}

func TestLoadContent_MalformedHasLine(t *testing.T) {
	rt, logs := runtime(t, fixture(t, "malformed"), false)
	code := rt.LoadContent(context.Background())
	if code != ExitFail {
		t.Fatalf("exit %d", code)
	}
	line := findLog(t, logs, "malformed_file")
	n, ok := line["line"].(float64)
	if !ok || n == 0 {
		t.Errorf("line = %v, want non-zero", line["line"])
	}
}

func TestLoadContent_EmptyDir(t *testing.T) {
	rt, logs := runtime(t, t.TempDir(), false)
	code := rt.LoadContent(context.Background())
	if code != ExitFail {
		t.Fatalf("exit %d; %s", code, logs.String())
	}
	line := findLog(t, logs, "no_zones_found")
	if !strings.Contains(fmtString(line["detail"]), "dir:") {
		t.Errorf("detail = %v", line["detail"])
	}
}

// AW-SRV-012 implemented the kafka source, so the finding that used to say
// "not implemented" is gone. What replaces it is the boot rule the story
// states: a boot with nothing loadable exits 1 naming the reason. Here the
// reason is that content.source=kafka was configured with no brokers, which
// the operator can act on; a boot that failed silently, or that came up
// serving an empty World, could not be acted on at all.
func TestLoadContent_KafkaWithoutBrokersFailsNamingTheReason(t *testing.T) {
	cfg := config.Config{
		ContentSource: config.DefaultContentSource,
		ServiceName:   "andara-server",
		Environment:   "test",
		LogLevel:      "info",
	}
	var logs bytes.Buffer
	tel := telemetry.Setup(cfg, &logs)
	t.Cleanup(func() { tel.Shutdown(context.Background()) })
	rt := New(cfg, tel)
	if code := rt.LoadContent(context.Background()); code != ExitFail {
		t.Fatalf("exit %d", code)
	}
	line := findLog(t, &logs, "malformed_file")
	if !strings.Contains(fmtString(line["detail"]), "brokers") {
		t.Errorf("detail = %v, should name what is missing", line["detail"])
	}
}

func TestLoadContent_OrphanWarnNotFatal(t *testing.T) {
	dir := t.TempDir()
	writeJSON(t, dir, "town.json", `{
		"formatVersion":1,"id":"town","name":"Town","fallbackRoom":"plaza",
		"rooms":[{"id":"plaza","title":"Plaza","description":"d"}]
	}`)
	rt, logs := runtime(t, dir, false)
	code := rt.LoadContent(context.Background())
	if code != ExitOK {
		t.Fatalf("exit %d; %s", code, logs.String())
	}
	line := findLog(t, logs, "orphan_room")
	if line["level"] != "WARN" && line["level"] != "warn" {
		t.Errorf("level = %v, want warn", line["level"])
	}
}

func TestHandler_LivezAlwaysOK(t *testing.T) {
	rt, _ := runtime(t, t.TempDir(), false)
	rr := httptest.NewRecorder()
	rt.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/livez", nil))
	if rr.Code != http.StatusOK {
		t.Errorf("livez = %d", rr.Code)
	}
}

func TestValidationErrorsMetric(t *testing.T) {
	rt, _ := runtime(t, fixture(t, "dangling"), false)
	_ = rt.LoadContent(context.Background())
	got := testutil.ToFloat64(rt.Tel.Metrics.ValidationErrors.WithLabelValues(string(sim.ErrUnknownRoom)))
	if got < 1 {
		t.Errorf("validation_errors_total{code=unknown_room} = %v", got)
	}
}

func runtime(t *testing.T, path string, strict bool) (*Runtime, *bytes.Buffer) {
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
	return New(cfg, tel), &logs
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("..", "..", "testdata", "content", name))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func writeJSON(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func findLog(t *testing.T, logs *bytes.Buffer, code string) map[string]any {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(logs.Bytes()))
	for {
		var m map[string]any
		if err := dec.Decode(&m); err != nil {
			if err == io.EOF {
				break
			}
			t.Fatalf("json: %v\n%s", err, logs.String())
		}
		if fmtString(m["code"]) == code {
			return m
		}
	}
	t.Fatalf("no log line with code %s in %s", code, logs.String())
	return nil
}

func fmtString(v any) string {
	s, _ := v.(string)
	return s
}

// serveMemory brings rt's loaded content into effect the way main does, over
// sim.source=memory: the ingress, the tick loop running, and ReconcileContent,
// which produces the genesis swap and waits for it to apply (AW-SRV-012). The
// loop is stopped at cleanup.
func serveMemory(t *testing.T, rt *Runtime) {
	t.Helper()
	rt.Cfg.SimSource = "memory"
	rt.Cfg.SimTickRate = 50
	rt.Cfg.SimTickBudget = 10 * time.Millisecond
	rt.Cfg.SimMaxPerTick = config.DefaultSimMaxPerTick
	rt.Cfg.SimDrainTimeout = time.Second
	rt.Cfg.SimPartitions = allPartitionsForTest()
	rt.Cfg.SimCheckpointEveryTicks = 10
	rt.Cfg.SubscriberBuffer = 64
	rt.Cfg.MaxSubscribers = 10
	rt.Cfg.MaxIntentBytes = 4096
	rt.Cfg.IngressRateLimit = config.DefaultIngressRateLimit
	rt.Cfg.IngressAgentRateLimit = config.DefaultIngressAgentRateLimit
	rt.Cfg.IngressBurst = config.DefaultIngressBurst
	rt.Cfg.IngressMaxPending = config.DefaultIngressMaxPending
	rt.Cfg.IngressProduceDeadline = config.DefaultIngressProduceDeadline
	rt.Cfg.IngressTransitHold = config.DefaultIngressTransitHold
	ctx := context.Background()
	if err := rt.LoadVerbs(ctx); err != nil {
		t.Fatal(err)
	}
	if err := rt.StartIngress(ctx); err != nil {
		t.Fatal(err)
	}
	loop, err := rt.StartTickLoop(ctx)
	if err != nil {
		t.Fatal(err)
	}
	loopCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- loop.Run(loopCtx) }()
	t.Cleanup(func() { stop(); <-done })
	if code := rt.ReconcileContent(ctx); code != ExitOK {
		t.Fatalf("reconcile: exit %d", code)
	}
	// main marks ready once the Gateway serves; there is none here.
	rt.MarkReady()
}
