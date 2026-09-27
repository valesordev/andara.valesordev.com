// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package content

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"testing"
	"time"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// The Loader's lines that name content.load's trace are logged under it:
// the log bridge stamps the record's trace from the context, and Loki
// indexes that, so the record and the trace_id attribute agree
// (AW-SRV-014 §8 sweep). Both the produced line and the bounded wait's.
func TestLoader_LinesLogUnderContentLoad(t *testing.T) {
	for _, tc := range []struct {
		msg     string
		swallow bool
	}{
		{"content version accepted; swap produced", false},
		{"content swap produced but not applied within the bounded wait; retrying later", true},
	} {
		t.Run(tc.msg, func(t *testing.T) {
			tp := sdktrace.NewTracerProvider()
			t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
			var buf bytes.Buffer
			c := &ctxCapture{next: slog.NewJSONHandler(&buf, nil)}
			s := newFakeStore()
			s.publish("andara.core", 1, 0, map[string]string{"core.json": zoneJSON("core", "void")})
			l := NewLoader(LoaderOptions{Store: s, Packs: []string{"andara.core"}, Metrics: NewMetrics(nil),
				Log: slog.New(c), Tracer: tp.Tracer("t"), ApplyWait: 50 * time.Millisecond})
			attachEngine(l).swallow = tc.swallow
			if _, err := l.LoadAll(context.Background()); err != nil {
				t.Fatal(err)
			}
			sc, ok := c.of(tc.msg)
			if !ok {
				t.Fatalf("no %q record:\n%s", tc.msg, buf.String())
			}
			if !sc.HasTraceID() {
				t.Fatalf("%q logged with no trace context", tc.msg)
			}
			var line map[string]any
			for _, b := range bytes.Split(buf.Bytes(), []byte("\n")) {
				if bytes.Contains(b, []byte(`"msg":"`+tc.msg+`"`)) {
					if err := json.Unmarshal(b, &line); err != nil {
						t.Fatal(err)
					}
				}
			}
			if line["trace_id"] != sc.TraceID().String() {
				t.Fatalf("attribute trace_id %v, record %s", line["trace_id"], sc.TraceID())
			}
		})
	}
}

// ctxCapture keeps the span context each record was handled with.
type ctxCapture struct {
	next slog.Handler
	mu   sync.Mutex
	recs map[string]trace.SpanContext
}

func (c *ctxCapture) of(msg string) (trace.SpanContext, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	sc, ok := c.recs[msg]
	return sc, ok
}

func (c *ctxCapture) Enabled(ctx context.Context, l slog.Level) bool { return c.next.Enabled(ctx, l) }

func (c *ctxCapture) Handle(ctx context.Context, r slog.Record) error {
	c.mu.Lock()
	if c.recs == nil {
		c.recs = map[string]trace.SpanContext{}
	}
	c.recs[r.Message] = trace.SpanContextFromContext(ctx)
	c.mu.Unlock()
	return c.next.Handle(ctx, r)
}

// WithAttrs and WithGroup drop the capture; the Loader logs through the
// logger it was given.
func (c *ctxCapture) WithAttrs(as []slog.Attr) slog.Handler { return c.next.WithAttrs(as) }
func (c *ctxCapture) WithGroup(g string) slog.Handler       { return c.next.WithGroup(g) }
