// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/prometheus/client_golang/prometheus"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	gamev1 "github.com/valesordev/andara/gen/go/andara/game/v1"
	"github.com/valesordev/andara/gen/go/andara/game/v1/gamev1connect"
	"github.com/valesordev/andara/internal/testpki"
)

// harness is one running Server with the telemetry a test asserts on.
type harness struct {
	t    *testing.T
	pki  *testpki.PKI
	srv  *Server
	reg  *prometheus.Registry
	logs *syncBuffer
	rec  *tracetest.SpanRecorder
	tp   *sdktrace.TracerProvider
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func defaultOptions(pki *testpki.PKI) Options {
	return Options{
		Listen:            "127.0.0.1:0",
		TLSCertFile:       pki.CertFile,
		TLSKeyFile:        pki.KeyFile,
		MaxRecvBytes:      65536,
		MaxRequestTimeout: 30 * time.Second,
		DrainTimeout:      5 * time.Second,
		ProtocolMin:       1,
		ProtocolMax:       1,
		Build:             BuildInfo{Version: "test", Commit: "abc123"},
		Environment:       "test",
	}
}

func start(t *testing.T, mutate func(*Options)) *harness {
	t.Helper()
	pki := testpki.New(t)
	opts := defaultOptions(pki)

	logs := &syncBuffer{}
	h := slog.NewJSONHandler(logs, &slog.HandlerOptions{
		Level: slog.LevelDebug,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey {
				return slog.String("ts", a.Value.Time().UTC().Format(time.RFC3339Nano))
			}
			return a
		},
	})
	opts.Log = slog.New(h).With("service", "andara-server", "env", "test")

	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	opts.Tracer = tp.Tracer("andara-server")

	reg := prometheus.NewRegistry()
	opts.Registry = reg

	if mutate != nil {
		mutate(&opts)
	}
	srv, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
		_ = tp.Shutdown(context.Background())
	})
	return &harness{t: t, pki: pki, srv: srv, reg: reg, logs: logs, rec: rec, tp: tp}
}

func (h *harness) baseURL() string { return "https://" + h.srv.Addr().String() }

// httpClient is a fresh HTTP/2-capable client with its own connection pool,
// so a test can drop its connections independently of every other client.
func (h *harness) httpClient() (*http.Client, *http.Transport) {
	tr := &http.Transport{
		TLSClientConfig:   h.pki.ClientTLS(),
		ForceAttemptHTTP2: true,
	}
	return &http.Client{Transport: tr}, tr
}

func (h *harness) game(opts ...connect.ClientOption) gamev1connect.GameClient {
	c, _ := h.httpClient()
	return gamev1connect.NewGameClient(c, h.baseURL(), opts...)
}

func (h *harness) open(t *testing.T, client gamev1connect.GameClient) *gamev1.OpenSessionResponse {
	t.Helper()
	resp, err := client.OpenSession(context.Background(), connect.NewRequest(&gamev1.OpenSessionRequest{
		ProtocolVersion: 1, AuthToken: "test-token", ClientName: "gateway-test/0",
	}))
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	return resp.Msg
}

// waitFor polls until cond holds or the deadline passes.
func waitFor(t *testing.T, d time.Duration, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// findLog returns the first JSON log line whose msg contains needle.
func findLog(t *testing.T, logs *syncBuffer, needle string) map[string]any {
	t.Helper()
	for _, line := range strings.Split(logs.String(), "\n") {
		if line == "" || !strings.Contains(line, needle) {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("bad log line %q: %v", line, err)
		}
		if msg, _ := m["msg"].(string); strings.Contains(msg, needle) {
			return m
		}
	}
	t.Fatalf("no log line with msg containing %q; logs:\n%s", needle, logs.String())
	return nil
}

func spanByName(t *testing.T, rec *tracetest.SpanRecorder, name string) sdktrace.ReadOnlySpan {
	t.Helper()
	var names []string
	for _, s := range rec.Ended() {
		names = append(names, s.Name())
		if s.Name() == name {
			return s
		}
	}
	t.Fatalf("no ended span named %q; have %v", name, names)
	return nil
}

func strAttr(s sdktrace.ReadOnlySpan, key string) string {
	for _, a := range s.Attributes() {
		if string(a.Key) == key {
			return a.Value.String()
		}
	}
	return ""
}
