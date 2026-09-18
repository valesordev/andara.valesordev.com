// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel/attribute"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/valesordev/andara/server/config"
)

// recordingHandler keeps every record it was handed, with the attrs and
// groups applied to it.
type recordingHandler struct {
	level  slog.Level
	store  *recordStore // shared by every derived handler
	attrs  []slog.Attr
	groups []string
}

type recordStore struct {
	mu      sync.Mutex
	records []map[string]any
}

func newRecording(level slog.Level) *recordingHandler {
	return &recordingHandler{level: level, store: &recordStore{}}
}

func (r *recordingHandler) records() []map[string]any {
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	return append([]map[string]any(nil), r.store.records...)
}

func (r *recordingHandler) Enabled(_ context.Context, l slog.Level) bool { return l >= r.level }

func (r *recordingHandler) Handle(_ context.Context, rec slog.Record) error {
	m := map[string]any{"msg": rec.Message, "level": rec.Level.String()}
	for _, a := range r.attrs {
		m[a.Key] = a.Value.Any()
	}
	rec.Attrs(func(a slog.Attr) bool {
		key := a.Key
		if len(r.groups) > 0 {
			key = strings.Join(r.groups, ".") + "." + key
		}
		m[key] = a.Value.Any()
		return true
	})
	r.store.mu.Lock()
	r.store.records = append(r.store.records, m)
	r.store.mu.Unlock()
	return nil
}

func (r *recordingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &recordingHandler{level: r.level, store: r.store, attrs: append(append([]slog.Attr(nil), r.attrs...), attrs...), groups: r.groups}
}

func (r *recordingHandler) WithGroup(name string) slog.Handler {
	return &recordingHandler{level: r.level, store: r.store, attrs: r.attrs, groups: append(append([]string(nil), r.groups...), name)}
}

// AC-5: every record reaches both handlers with equal attributes, and
// WithAttrs / WithGroup on the fan-out reach both.
func TestFanout_BothHandlersSeeEverything(t *testing.T) {
	a, b := newRecording(slog.LevelDebug), newRecording(slog.LevelDebug)
	log := slog.New(newFanout(a, b)).With("service", "andara-server").WithGroup("req")
	log.Info("session opened", "session_id", "abc", "n", 3)
	log.Debug("quiet", "k", "v")
	ra, rb := a.records(), b.records()
	if len(ra) != 2 || len(rb) != 2 {
		t.Fatalf("a=%d b=%d", len(ra), len(rb))
	}
	for i := range ra {
		ja, _ := json.Marshal(ra[i])
		jb, _ := json.Marshal(rb[i])
		if !bytes.Equal(ja, jb) {
			t.Fatalf("record %d differs:\n%s\n%s", i, ja, jb)
		}
	}
	if ra[0]["service"] != "andara-server" || ra[0]["req.session_id"] != "abc" {
		t.Errorf("attrs and group not applied: %v", ra[0])
	}
	// Enabled is any: a handler below the level is skipped, the other still
	// gets the record.
	warnOnly := newRecording(slog.LevelWarn)
	log = slog.New(newFanout(warnOnly, a))
	log.Info("only a")
	if len(warnOnly.records()) != 0 || len(a.records()) != 3 {
		t.Errorf("level gating: warnOnly=%d a=%d", len(warnOnly.records()), len(a.records()))
	}
	// The first error is returned, and every handler still runs.
	log = slog.New(newFanout(failingHandler{}, a))
	if err := log.Handler().Handle(context.Background(), slog.NewRecord(time.Now(), slog.LevelInfo, "x", 0)); err == nil {
		t.Error("error swallowed")
	}
	if len(a.records()) != 4 {
		t.Error("a handler after a failing one did not run")
	}
}

type failingHandler struct{}

func (failingHandler) Enabled(context.Context, slog.Level) bool  { return true }
func (failingHandler) Handle(context.Context, slog.Record) error { return errors.New("boom") }
func (f failingHandler) WithAttrs([]slog.Attr) slog.Handler      { return f }
func (f failingHandler) WithGroup(string) slog.Handler           { return f }

// AC-3: no endpoint, no exporter — the stderr handler alone, unchanged.
func TestSetup_NoEndpointNoExporter(t *testing.T) {
	cfg, err := config.Parse([]string{"--validate-only"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg.OTLPEndpoint = ""
	var stderr bytes.Buffer
	tel := Setup(cfg, &stderr)
	defer tel.Shutdown(context.Background())
	if tel.LP != nil || tel.LogExport != nil || tel.SetupErr != nil {
		t.Fatalf("exporter built without an endpoint: %+v", tel)
	}
	if _, ok := tel.Log.Handler().(*fanoutHandler); ok {
		t.Fatal("fan-out built without an endpoint")
	}
	tel.Log.Info("hello", "k", "v")
	if !strings.Contains(stderr.String(), `"msg":"hello"`) || strings.Contains(stderr.String(), "otlp") {
		t.Errorf("stderr: %s", stderr.String())
	}
	if _, err := tel.Reg.Gather(); err != nil {
		t.Fatal(err)
	}
}

// memExporter records what the processor exported, and can fail.
type memExporter struct {
	mu      sync.Mutex
	records []sdklog.Record
	fail    error
	exports int
}

func (m *memExporter) Export(_ context.Context, recs []sdklog.Record) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.exports++
	if m.fail != nil {
		// A real exporter spends its timeout on a dead collector; that
		// is what lets the queue fill behind it.
		time.Sleep(100 * time.Millisecond)
		return m.fail
	}
	m.records = append(m.records, recs...)
	return nil
}
func (m *memExporter) Shutdown(context.Context) error   { return nil }
func (m *memExporter) ForceFlush(context.Context) error { return nil }

func (m *memExporter) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.records)
}

// The record → OTLP mapping: the message, the attributes, the resource, and
// trace_id/span_id as record fields — not attributes — when emitted inside
// a span.
func TestLogExport_RecordCarriesTraceContext(t *testing.T) {
	exp := &memExporter{}
	reg := prometheus.NewRegistry()
	proc := newQueueProcessor(exp, newLogExportMetrics(reg), func(error) {})
	lp := sdklog.NewLoggerProvider(sdklog.WithProcessor(proc))
	defer func() { _ = lp.Shutdown(context.Background()) }()
	var stderr bytes.Buffer
	stderrH := slog.NewJSONHandler(&stderr, nil)
	log := slog.New(newFanout(stderrH, otelslog.NewHandler("test", otelslog.WithLoggerProvider(lp)))).With("service", "andara-server")

	tp := sdktrace.NewTracerProvider()
	ctx, span := tp.Tracer("t").Start(context.Background(), "andara.game.v1.Game/OpenSession")
	log.LogAttrs(ctx, slog.LevelInfo, "session opened", slog.String("session_id", "s-1"), slog.String("trace_id", span.SpanContext().TraceID().String()))
	span.End()
	if err := proc.ForceFlush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if exp.count() != 1 {
		t.Fatalf("%d records exported", exp.count())
	}
	rec := exp.records[0]
	if rec.Body().AsString() != "session opened" {
		t.Errorf("body %q", rec.Body().AsString())
	}
	if rec.TraceID() != span.SpanContext().TraceID() || rec.SpanID() != span.SpanContext().SpanID() {
		t.Errorf("trace context not on the record: trace=%s span=%s", rec.TraceID(), rec.SpanID())
	}
	attrs := map[string]string{}
	rec.WalkAttributes(func(kv attribute.KeyValue) bool {
		attrs[string(kv.Key)] = kv.Value.AsString()
		return true
	})
	if attrs["session_id"] != "s-1" || attrs["service"] != "andara-server" || attrs["trace_id"] != span.SpanContext().TraceID().String() {
		t.Errorf("attrs %v", attrs)
	}
	// And stderr has the same values.
	var line map[string]any
	if err := json.Unmarshal(stderr.Bytes(), &line); err != nil {
		t.Fatal(err)
	}
	if line["session_id"] != "s-1" || line["trace_id"] != attrs["trace_id"] || line["msg"] != "session opened" {
		t.Errorf("stderr line %v", line)
	}
}

// AC-4 in-process: with the exporter failing the queue fills, records are
// dropped and counted, the caller never blocks, and export resumes when the
// exporter does.
func TestLogExport_BoundedQueueDropsAndCounts(t *testing.T) {
	exp := &memExporter{fail: errors.New("collector unreachable")}
	reg := prometheus.NewRegistry()
	m := newLogExportMetrics(reg)
	var warnings atomic.Int32
	proc := newQueueProcessor(exp, m, func(error) { warnings.Add(1) })
	lp := sdklog.NewLoggerProvider(sdklog.WithProcessor(proc))
	log := slog.New(otelslog.NewHandler("test", otelslog.WithLoggerProvider(lp)))

	start := time.Now()
	for i := 0; i < LogQueueSize*3; i++ {
		log.Info("flood", "i", i)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("emitting into a full queue blocked")
	}
	// The queue holds LogQueueSize; the run loop may have drained some into
	// a failing export, so at least the overflow beyond queue + one batch
	// was dropped.
	if d := testutil.ToFloat64(m.Dropped); d < float64(LogQueueSize*3-LogQueueSize-LogExportBatch*2) {
		t.Fatalf("dropped %v, want most of the flood", d)
	}
	if testutil.ToFloat64(m.QueueSize) > LogQueueSize {
		t.Fatal("queue gauge above the bound")
	}
	// The exporter's failure was reported through the rate-limited warn,
	// not through the logger.
	_ = proc.ForceFlush(context.Background())
	if warnings.Load() == 0 {
		t.Error("no warning for a failing export")
	}
	// The collector returns: new lines land.
	exp.mu.Lock()
	exp.fail = nil
	exp.mu.Unlock()
	log.Info("after outage")
	_ = proc.ForceFlush(context.Background())
	found := false
	exp.mu.Lock()
	for _, r := range exp.records {
		if r.Body().AsString() == "after outage" {
			found = true
		}
	}
	exp.mu.Unlock()
	if !found {
		t.Error("a line after the outage was not exported")
	}
	if err := lp.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestRateLimitedWarn(t *testing.T) {
	var buf bytes.Buffer
	warn := rateLimitedWarn(slog.New(slog.NewJSONHandler(&buf, nil)))
	for range 5 {
		warn(errors.New("x"))
	}
	if n := strings.Count(buf.String(), "otlp log export failed"); n != 1 {
		t.Fatalf("%d warnings in a minute, want 1", n)
	}
}
