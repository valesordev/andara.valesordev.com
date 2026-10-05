// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package recovery_test

import (
	"context"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/valesordev/andara/server/recovery"
	"github.com/valesordev/andara/server/sim"
)

func attrs(s sdktrace.ReadOnlySpan) map[string]attribute.Value {
	out := map[string]attribute.Value{}
	for _, kv := range s.Attributes() {
		out[string(kv.Key)] = kv.Value
	}
	return out
}

// AW-SRV-007 §7: `recovery.load_snapshot` is per Zone with `zone_id`, `key` and
// `bytes` (the loaded payload's size), and wraps the read.
func TestLoadSnapshotSpansCarryTheKeyAndTheBytesRead(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 8, 4)
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	o := w.opts(t)
	o.Tracer = tp.Tracer("test")
	if _, _, err := recovery.Recover(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	var loads []sdktrace.ReadOnlySpan
	for _, s := range rec.Ended() {
		if s.Name() == "recovery.load_snapshot" {
			loads = append(loads, s)
		}
	}
	if len(loads) != len(w.owned) {
		t.Fatalf("%d load_snapshot spans, want one per Zone (%d)", len(loads), len(w.owned))
	}
	for _, s := range loads {
		a := attrs(s)
		key := a["key"].AsString()
		raw, err := w.fs.Get(context.Background(), key)
		if err != nil {
			t.Fatal(err)
		}
		if a["zone_id"].AsString() == "" || !strings.HasPrefix(key, a["zone_id"].AsString()+"/") || a["bytes"].AsInt64() != int64(len(raw)) {
			t.Errorf("span attributes %v, want zone_id, key and bytes=%d", a, len(raw))
		}
	}
}

// A named round reads only its own objects: no load_snapshot span for any other
// tick (AC-15).
func TestANamedRoundLoadsNoOtherTick(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 8, 4)
	rec := tracetest.NewSpanRecorder()
	o := w.opts(t)
	o.Tracer = sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)).Tracer("test")
	tick := sim.Tick(4)
	o.Round = &tick
	if _, _, err := recovery.Recover(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	loads := 0
	for _, s := range rec.Ended() {
		if s.Name() == "recovery.load_snapshot" {
			loads++
		}
		if s.Name() == "recovery.load_snapshot" && !strings.Contains(attrs(s)["key"].AsString(), "/00000000000000000004/") {
			t.Errorf("a span for another tick: %v", attrs(s))
		}
	}
	if loads != len(w.owned) {
		t.Errorf("%d load_snapshot spans for the named round, want %d", loads, len(w.owned))
	}
}
