// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package content

import (
	"context"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// AW-SRV-012 §8 (2026-09-25): the instruments a load and a pointer move drive,
// asserted on the metric objects and the recorded spans rather than on the
// Loader's own view of itself.
func TestLoader_InstrumentsALoadAndAPointerMove(t *testing.T) {
	spans := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)
	m.SetBuild("v1.2.3", "abc123", "test")

	s := newFakeStore()
	s.publish("andara.core", 1, 0, map[string]string{"core.json": zoneJSON("core", "void")})
	l := NewLoader(LoaderOptions{Store: s, Packs: []string{"andara.core"}, Metrics: m, Tracer: tp.Tracer("test")})
	attachEngine(l)
	if rejects, err := l.LoadAll(context.Background()); err != nil || len(rejects) != 0 {
		t.Fatalf("genesis: %v %v", rejects, err)
	}

	// Every phase of a load is observed.
	for _, phase := range []string{PhaseResolve, PhaseValidate, PhaseBuild} {
		if n := sampleCount(t, reg, "andara_content_load_phase_duration_seconds", "phase", phase); n == 0 {
			t.Errorf("phase=%q was not observed", phase)
		}
	}

	// The load's spans: resolve and validate under content.load, and the
	// build under validate.
	byName := map[string]sdktrace.ReadOnlySpan{}
	for _, sp := range spans.Ended() {
		byName[sp.Name()] = sp
	}
	load := byName["content.load"]
	if load == nil {
		t.Fatal("no content.load span")
	}
	for name, parent := range map[string]string{"content.resolve": "content.load", "content.validate": "content.load", "content.build": "content.validate"} {
		sp, p := byName[name], byName[parent]
		if sp == nil || p == nil || sp.Parent().SpanID() != p.SpanContext().SpanID() {
			t.Errorf("%s is not a child of %s", name, parent)
		}
	}

	// A pointer move: the gauges follow what is applied.
	s.publish("andara.core", 2, 0, map[string]string{"core.json": zoneJSON("core", "void")})
	if rejects := l.Apply(context.Background(), PointerMove{Pack: "andara.core", Version: 2}); len(rejects) != 0 {
		t.Fatalf("move: %v", rejects)
	}
	if v := testutil.ToFloat64(m.ActiveVersion.WithLabelValues("andara.core")); v != 2 {
		t.Errorf("andara_content_active_version{pack=andara.core} = %v, want 2", v)
	}
	want := `andara_build_info{commit="abc123",content_version="2",env="test",pack="andara.core",version="v1.2.3"} 1`
	if err := testutil.GatherAndCompare(reg, strings.NewReader("# HELP andara_build_info Always 1: the running build, and the content version of each pack in effect.\n# TYPE andara_build_info gauge\n"+want+"\n"), "andara_build_info"); err != nil {
		t.Errorf("andara_build_info: %v", err)
	}
}

// sampleCount is the observation count of the histogram series name{label=value}.
func sampleCount(t *testing.T, reg *prometheus.Registry, name, label, value string) uint64 {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			if hasLabel(m, label, value) {
				return m.GetHistogram().GetSampleCount()
			}
		}
	}
	return 0
}

func hasLabel(m *dto.Metric, name, value string) bool {
	for _, l := range m.GetLabel() {
		if l.GetName() == name && l.GetValue() == value {
			return true
		}
	}
	return false
}
