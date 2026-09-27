// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// A teardown with no RPC in flight — a dropped connection — logs session
// closed under the Session's own span: the log bridge stamps the record's
// trace from the context, and Loki indexes that, so the record and the
// trace_id attribute name the same trace (AW-SRV-014 §8 sweep).
func TestSessionStore_DroppedCloseLogsUnderTheSessionSpan(t *testing.T) {
	tp := sdktrace.NewTracerProvider()
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	var buf bytes.Buffer
	c := &ctxCapture{next: slog.NewJSONHandler(&buf, nil)}
	st := newSessionStore(NewMetrics(nil), slog.New(c), tp.Tracer("t"))
	st.connOpened(1)
	s, err := st.open(context.Background(), 1, "a/0", 1, "", Principal{})
	if err != nil {
		t.Fatal(err)
	}
	st.connClosed(1)

	sc, ok := c.of("session closed")
	if !ok {
		t.Fatal("no session closed record")
	}
	if want := s.span.SpanContext(); sc.TraceID() != want.TraceID() || sc.SpanID() != want.SpanID() {
		t.Fatalf("session closed logged under %s/%s, want session.lifetime %s/%s", sc.TraceID(), sc.SpanID(), want.TraceID(), want.SpanID())
	}
	var line map[string]any
	for _, l := range bytes.Split(buf.Bytes(), []byte("\n")) {
		if bytes.Contains(l, []byte(`"msg":"session closed"`)) {
			if err := json.Unmarshal(l, &line); err != nil {
				t.Fatal(err)
			}
		}
	}
	if line["trace_id"] != sc.TraceID().String() {
		t.Fatalf("attribute trace_id %v, record %s", line["trace_id"], sc.TraceID())
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

// WithAttrs and WithGroup drop the capture; the store logs through the
// logger it was given.
func (c *ctxCapture) WithAttrs(as []slog.Attr) slog.Handler { return c.next.WithAttrs(as) }
func (c *ctxCapture) WithGroup(g string) slog.Handler       { return c.next.WithGroup(g) }
