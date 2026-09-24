// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

func (rt *runtime) startSpan() {
	// tpOptions is a test seam. A CLI process exports nowhere by default —
	// AW-CLI-001 puts the trace id in the log line and leaves collection to the
	// operator's environment — so without it a span assertion would have
	// nothing to read and the observability requirement would be checked by
	// looking at the source.
	rt.tp = sdktrace.NewTracerProvider(rt.tpOptions...)
	tracer := rt.tp.Tracer("andara-cli")
	ctx, span := tracer.Start(context.Background(), "cli.command")
	span.SetAttributes(attribute.String("command", rt.command))
	rt.span = span
	rt.ctx = ctx
	rt.started = time.Now()
}

// childSpan opens a span under the command's root span. AW-CLI-001 gives every
// command a `cli.command` root; a command whose work is worth timing on its own
// — a compile over a pack of a few hundred files — hangs a named child off it
// rather than flattening its attributes onto the root, so that a trace shows
// where the wall clock went.
//
// It returns a no-op span when telemetry was never started, so a caller never
// has to nil-check before setting an attribute.
func (rt *runtime) childSpan(name string) (context.Context, trace.Span) {
	if rt.tp == nil || rt.ctx == nil {
		return context.Background(), noop.Span{}
	}
	return rt.tp.Tracer("andara-cli").Start(rt.ctx, name)
}

func (rt *runtime) traceID() string {
	if rt.span == nil {
		return ""
	}
	sc := rt.span.SpanContext()
	if !sc.HasTraceID() {
		return ""
	}
	return sc.TraceID().String()
}

func (rt *runtime) finish(code int) {
	if rt.span != nil {
		outcome := "success"
		if code != 0 {
			outcome = "error"
		}
		rt.span.SetAttributes(
			attribute.String("outcome", outcome),
			attribute.Int("exit_code", code),
		)
		rt.span.End()
	}
	if !rt.started.IsZero() {
		rt.log("debug", fmt.Sprintf("command completed in %s", time.Since(rt.started).String()))
	}
	if rt.tp != nil {
		_ = rt.tp.Shutdown(context.Background())
	}
}

func (rt *runtime) log(level, msg string) {
	if rt.settings == nil {
		return
	}
	if !levelEnabled(rt.settings.LogLevel, level) {
		return
	}
	if rt.settings.Output == outputJSON {
		_ = json.NewEncoder(rt.stderr).Encode(logLine{
			TS:      time.Now().UTC().Format(time.RFC3339Nano),
			Level:   level,
			Msg:     msg,
			Command: rt.command,
			TraceID: rt.traceID(),
		})
		return
	}
	_, _ = fmt.Fprintf(rt.stderr, "%s: %s\n", level, msg)
}
