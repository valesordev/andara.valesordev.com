// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package telemetry

import (
	"context"
	"log/slog"
)

// fanoutHandler sends every record to every handler: stderr and the OTLP
// bridge see the same lines (AW-SRV-024). Enabled if any is; Handle calls
// all and returns the first error; WithAttrs and WithGroup reach every one,
// so `service` and `env` are on the collector's copy too.
type fanoutHandler struct {
	handlers []slog.Handler
}

func newFanout(handlers ...slog.Handler) slog.Handler {
	return &fanoutHandler{handlers: handlers}
}

// Enabled implements slog.Handler.
func (f *fanoutHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, h := range f.handlers {
		if h.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

// Handle implements slog.Handler. A handler that is not enabled for the
// level is skipped, since slog only consults the fan-out's Enabled.
func (f *fanoutHandler) Handle(ctx context.Context, r slog.Record) error {
	var first error
	for _, h := range f.handlers {
		if !h.Enabled(ctx, r.Level) {
			continue
		}
		if err := h.Handle(ctx, r.Clone()); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// WithAttrs implements slog.Handler.
func (f *fanoutHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := make([]slog.Handler, len(f.handlers))
	for i, h := range f.handlers {
		out[i] = h.WithAttrs(attrs)
	}
	return &fanoutHandler{handlers: out}
}

// WithGroup implements slog.Handler.
func (f *fanoutHandler) WithGroup(name string) slog.Handler {
	out := make([]slog.Handler, len(f.handlers))
	for i, h := range f.handlers {
		out[i] = h.WithGroup(name)
	}
	return &fanoutHandler{handlers: out}
}
