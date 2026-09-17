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
)

func (rt *runtime) startSpan() {
	rt.tp = sdktrace.NewTracerProvider()
	tracer := rt.tp.Tracer("andara-cli")
	ctx, span := tracer.Start(context.Background(), "cli.command")
	span.SetAttributes(attribute.String("command", rt.command))
	rt.span = span
	rt.ctx = ctx
	rt.started = time.Now()
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
