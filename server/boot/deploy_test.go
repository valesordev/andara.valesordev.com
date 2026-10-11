// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package boot

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/valesordev/andara/server/recovery"
	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/store"
	"github.com/valesordev/andara/server/telemetry"
)

func deployRuntime(t *testing.T, now time.Time) (*Runtime, *bytes.Buffer, *tracetest.SpanRecorder) {
	t.Helper()
	logs := &bytes.Buffer{}
	spans := tracetest.NewSpanRecorder()
	rt := &Runtime{Tel: &telemetry.Telemetry{
		Log:    slog.New(slog.NewJSONHandler(logs, nil)),
		Reg:    prometheus.NewRegistry(),
		Tracer: sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans)).Tracer("test"),
	}}
	rt.now = func() time.Time { return now }
	rt.ready.Store(true)
	return rt, logs, spans
}

// AW-SRV-054 AC-7: the first serving answer observes the interruption once,
// from the head boundary's time to now, logs `deploy interruption` with the
// seconds and the trace_id, and a round tagged deploy:<v> puts the old pod's
// deploy_id on deploy.recovery. Later answers observe nothing.
func TestServingObservesTheInterruptionOnce(t *testing.T) {
	t.Parallel()
	head := time.Unix(1_760_000_000, 0)
	rt, logs, spans := deployRuntime(t, head.Add(42*time.Second))
	fs := store.NewFS(t.TempDir())
	keys := []string{sim.SnapshotKey("town", 1, 420, 7)}
	if err := fs.Put(context.Background(), keys[0], []byte("body")); err != nil {
		t.Fatal(err)
	}
	if err := store.TagRound(context.Background(), fs, keys, "deploy:sha-0123456789ab"); err != nil {
		t.Fatal(err)
	}
	rep := recovery.Report{HeadProducedAt: head, Round: store.Round{Tick: 420, Zones: []store.ZoneSnapshotRef{{Zone: "town", Key: keys[0]}}}}
	rt.noteRecovered(context.Background(), fs, rep)

	for range 3 {
		if !rt.Ready() {
			t.Fatal("not ready")
		}
	}
	h := rt.deploy.hist
	if n := testutil.CollectAndCount(h); n != 1 {
		t.Fatalf("collected %d series", n)
	}
	if got := histCount(t, h); got != 1 {
		t.Fatalf("andara_deploy_interruption_seconds count = %d, want 1", got)
	}
	if got := histSum(t, h); got != 42 {
		t.Fatalf("andara_deploy_interruption_seconds sum = %v, want 42", got)
	}
	var span sdktrace.ReadOnlySpan
	for _, s := range spans.Ended() {
		if s.Name() == "deploy.recovery" {
			span = s
		}
	}
	if span == nil {
		t.Fatal("no deploy.recovery span")
	}
	var deployID string
	for _, a := range span.Attributes() {
		if a.Key == "deploy_id" {
			deployID = a.Value.AsString()
		}
	}
	if deployID != "sha-0123456789ab@420" {
		t.Fatalf("deploy.recovery deploy_id = %q", deployID)
	}
	if n := strings.Count(logs.String(), `"msg":"deploy interruption"`); n != 1 {
		t.Fatalf("%d deploy interruption lines, want 1:\n%s", n, logs)
	}
	if !strings.Contains(logs.String(), `"seconds":42`) || !strings.Contains(logs.String(), `"trace_id":"`+span.SpanContext().TraceID().String()+`"`) {
		t.Fatalf("deploy interruption line lacks seconds or trace_id:\n%s", logs)
	}
}

// A boot whose log had no Tick Boundary Record observes nothing.
func TestServingWithNoBoundaryObservesNothing(t *testing.T) {
	t.Parallel()
	rt, logs, _ := deployRuntime(t, time.Unix(1_760_000_000, 0))
	rt.noteRecovered(context.Background(), nil, recovery.Report{})
	if !rt.Ready() {
		t.Fatal("not ready")
	}
	if got := histCount(t, rt.deploy.hist); got != 0 {
		t.Fatalf("observed %d with no boundary", got)
	}
	if strings.Contains(logs.String(), "deploy interruption") {
		t.Fatalf("logged an interruption with no boundary:\n%s", logs)
	}
}

func histOf(t *testing.T, h prometheus.Histogram) *dto.Histogram {
	t.Helper()
	m := &dto.Metric{}
	if err := h.Write(m); err != nil {
		t.Fatal(err)
	}
	return m.GetHistogram()
}

func histCount(t *testing.T, h prometheus.Histogram) uint64 { return histOf(t, h).GetSampleCount() }

func histSum(t *testing.T, h prometheus.Histogram) float64 { return histOf(t, h).GetSampleSum() }
