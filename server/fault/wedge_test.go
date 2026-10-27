// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package fault

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// Ticks run until d has passed since the first, then the next one blocks until
// its context ends, with one warn.
func TestWedgeTickAfter(t *testing.T) {
	var logs bytes.Buffer
	before := WedgeTickAfter(30*time.Second, slog.New(slog.NewJSONHandler(&logs, nil)))
	t0 := time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)
	for _, at := range []time.Duration{0, time.Second, 29 * time.Second} {
		done := make(chan struct{})
		go func() { before(context.Background(), t0.Add(at)); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("the tick at %s blocked; the wedge is at 30s", at)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { before(ctx, t0.Add(30*time.Second)); close(done) }()
	select {
	case <-done:
		t.Fatal("the tick at 30s ran; want it wedged")
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the wedge outlived its context")
	}
	if n := strings.Count(logs.String(), `"msg":"fault: wedging the tick loop"`); n != 1 {
		t.Errorf("%d wedge lines, want 1", n)
	}
}
