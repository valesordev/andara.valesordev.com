// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package boot

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/valesordev/andara/server/fault"
)

// probeStates reads andara_probe_state from a scrape, by probe; a missing
// series is absent from the map.
func probeStates(t *testing.T, rt *Runtime) map[string]float64 {
	t.Helper()
	mfs, err := rt.Tel.Reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]float64{}
	for _, mf := range mfs {
		if mf.GetName() != "andara_probe_state" {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "probe" {
					got[l.GetValue()] = m.GetGauge().GetValue()
				}
			}
		}
	}
	return got
}

// simLag reads andara_simulation_lag_seconds from a scrape.
func simLag(t *testing.T, rt *Runtime) float64 {
	t.Helper()
	mfs, err := rt.Tel.Reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() == "andara_simulation_lag_seconds" {
			return mf.GetMetric()[0].GetGauge().GetValue()
		}
	}
	t.Fatal("andara_simulation_lag_seconds not gathered")
	return 0
}

func get(h http.Handler, path string) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
	return rr
}

func wantProbes(t *testing.T, rt *Runtime, startup, readiness, liveness float64) {
	t.Helper()
	got := probeStates(t, rt)
	want := map[string]float64{"startup": startup, "readiness": readiness, "liveness": liveness}
	if len(got) != 3 {
		t.Fatalf("andara_probe_state series %v, want exactly startup, readiness and liveness", got)
	}
	for p, v := range want {
		if got[p] != v {
			t.Errorf("andara_probe_state{probe=%q} = %v, want %v (all: %v)", p, got[p], v, got)
		}
	}
}

// AC-4, AC-7: before the tick loop exists, /livez is 200, and all three
// andara_probe_state series are present, each matching its endpoint.
func TestLivez_BeforeTheLoopStarts(t *testing.T) {
	rt, _ := runtime(t, t.TempDir(), false)
	h := rt.Handler()
	if rr := get(h, "/livez"); rr.Code != http.StatusOK || rr.Body.String() != "ok\n" {
		t.Fatalf("livez = %d %q, want 200 ok", rr.Code, rr.Body.String())
	}
	wantProbes(t, rt, 0, 0, 1)
	// A second Handler, as the recovery server and main each build one,
	// registers nothing twice.
	_ = rt.Handler()
}

// AC-1, AC-7: a loop that completes ticks is live; every series follows its
// endpoint as the boot moves through started and ready.
func TestLivez_TickingIsLive(t *testing.T) {
	rt, logs := runtime(t, fixture(t, "valid"), false)
	if code := rt.LoadContent(context.Background()); code != ExitOK {
		t.Fatalf("exit %d; logs=%s", code, logs.String())
	}
	h := rt.Handler()
	serveMemory(t, rt)
	if rr := get(h, "/livez"); rr.Code != http.StatusOK {
		t.Fatalf("livez = %d, want 200", rr.Code)
	}
	wantProbes(t, rt, 0, 1, 1)
	rt.MarkStarted()
	wantProbes(t, rt, 1, 1, 1)
	for path, want := range map[string]int{"/startedz": 200, "/readyz": 200, "/livez": 200} {
		if rr := get(h, path); rr.Code != want {
			t.Errorf("%s = %d, want %d", path, rr.Code, want)
		}
	}
	rt.Drain()
	wantProbes(t, rt, 1, 0, 1)
}

// AC-2, AC-5, AC-7, AC-9, and AC-8's in-process half: the fault injector
// wedges a running loop, /livez turns 503 tick_wedged past
// sim.tick_budget_ms × 100, the wedge is logged once, the lag rises at
// scrape, and the drain makes the pod live again.
func TestLivez_AWedgedLoopIs503(t *testing.T) {
	rt, logs := runtime(t, fixture(t, "valid"), false)
	if code := rt.LoadContent(context.Background()); code != ExitOK {
		t.Fatalf("exit %d; logs=%s", code, logs.String())
	}
	sb := liveLogs(rt)
	// startMemoryLoop's budget is 10 ms: the wedge is found a second after
	// the last tick.
	rt.BeforeTick = fault.WedgeTickAfter(200*time.Millisecond, rt.Tel.Log)
	h := rt.Handler()
	serveMemory(t, rt)
	rt.MarkStarted()

	deadline := time.Now().Add(10 * time.Second)
	var rr *httptest.ResponseRecorder
	for rr = get(h, "/livez"); rr.Code == http.StatusOK; rr = get(h, "/livez") {
		if time.Now().After(deadline) {
			t.Fatalf("livez still 200 after 10s of a wedged loop; logs:\n%s", sb.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("livez = %d, want 503", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	if body := rr.Body.String(); body != `{"reason":"tick_wedged"}` {
		t.Errorf("body = %q", body)
	}
	if !strings.Contains(sb.String(), `"msg":"fault: wedging the tick loop"`) {
		t.Error("no fault line: the wedge did not come from the injector")
	}
	wantProbes(t, rt, 1, 1, 0)
	for range 3 {
		_ = get(h, "/livez")
	}
	if n := strings.Count(sb.String(), `"msg":"tick loop wedged"`); n != 1 {
		t.Errorf("%d wedged lines, want 1 for one wedge however often it's polled", n)
	}

	// The wedge is a second past the last tick; the lag at scrape is past it.
	first := simLag(t, rt)
	if first <= 0.5 {
		t.Errorf("lag while wedged = %v, want above 0.5", first)
	}
	time.Sleep(50 * time.Millisecond)
	if next := simLag(t, rt); next <= first {
		t.Errorf("lag at the next scrape = %v, want above %v", next, first)
	}

	rt.Drain()
	if rr := get(h, "/livez"); rr.Code != http.StatusOK {
		t.Errorf("livez while draining = %d, want 200", rr.Code)
	}
	wantProbes(t, rt, 1, 0, 1)
}
