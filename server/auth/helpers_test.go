// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package auth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/expfmt"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"google.golang.org/protobuf/proto"

	accountsv1 "github.com/valesordev/andara/gen/go/andara/accounts/v1"
	auditv1 "github.com/valesordev/andara/gen/go/andara/audit/v1"
	"github.com/valesordev/andara/server/recordlog"
)

// testArgon is small enough to run thousands of times under -race and
// still a real Argon2id derivation.
var testArgon = Argon2Params{MemoryKiB: 64, Time: 1, Threads: 1}

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

// clock is a settable time source.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type fixture struct {
	t        *testing.T
	store    *Store
	accounts *recordlog.Memory
	audit    *recordlog.Memory
	keys     *Keyring
	clock    *clock
	logs     *syncBuffer
	reg      *prometheus.Registry
	spans    *tracetest.SpanRecorder
}

func testKeyring(t *testing.T, ids ...string) *Keyring {
	t.Helper()
	var sb strings.Builder
	for _, id := range ids {
		// Derived from the id, so the same id is the same key in every
		// keyring a test builds — which is what a real rotation looks like.
		secret := sha256.Sum256([]byte("test-key-" + id))
		sb.WriteString(id + ": " + base64.StdEncoding.EncodeToString(secret[:]) + "\n")
	}
	kr, err := ParseKeyring(strings.NewReader(sb.String()))
	if err != nil {
		t.Fatal(err)
	}
	return kr
}

func newFixture(t *testing.T, mutate func(*Options)) *fixture {
	t.Helper()
	f := &fixture{
		t:        t,
		accounts: recordlog.NewMemory(),
		audit:    recordlog.NewMemory(),
		keys:     testKeyring(t, "k1"),
		clock:    &clock{t: time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)},
		logs:     &syncBuffer{},
		reg:      prometheus.NewRegistry(),
		spans:    tracetest.NewSpanRecorder(),
	}
	f.reopen(mutate)
	return f
}

// reopen builds a fresh Store over the same logs, keyring, and clock —
// what a server restart does.
func (f *fixture) reopen(mutate func(*Options)) {
	f.t.Helper()
	h := slog.NewJSONHandler(f.logs, &slog.HandlerOptions{Level: slog.LevelDebug})
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(f.spans))
	f.reg = prometheus.NewRegistry()
	o := Options{
		Accounts:   f.accounts,
		Audit:      f.audit,
		Keys:       f.keys,
		Argon2:     testArgon,
		SessionTTL: time.Hour,
		RefreshTTL: 30 * 24 * time.Hour,
		InviteTTL:  7 * 24 * time.Hour,
		RateLimit:  RateLimit{}, // off unless a test turns it on
		Log:        slog.New(h),
		Tracer:     tp.Tracer("test"),
		Registry:   f.reg,
		Now:        f.clock.now,
	}
	if mutate != nil {
		mutate(&o)
	}
	s, err := Open(context.Background(), o)
	if err != nil {
		f.t.Fatal(err)
	}
	f.store = s
	f.t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
}

// operatorCtx is a context carrying an operator Principal, the way the
// Gateway's auth interceptor would hand it to an Admin handler.
func (f *fixture) operatorCtx() context.Context {
	return WithPrincipal(context.Background(), Principal{AccountID: "op", Roles: []Role{RoleOperator}})
}

// bootstrapOperator creates a real operator Account and returns a ctx
// acting as it, plus the account id.
func (f *fixture) bootstrapOperator(username, password string) (context.Context, string) {
	f.t.Helper()
	id, err := f.store.CreateAccount(f.operatorCtx(), username, password, []Role{RoleOperator})
	if err != nil {
		f.t.Fatal(err)
	}
	return WithPrincipal(context.Background(), Principal{AccountID: id, Roles: []Role{RoleOperator}}), id
}

// auditRecords decodes everything on the audit log.
func (f *fixture) auditRecords() []*auditv1.AuditRecord {
	f.t.Helper()
	var out []*auditv1.AuditRecord
	for _, r := range f.audit.Records() {
		var rec auditv1.AuditRecord
		if err := proto.Unmarshal(r.Value, &rec); err != nil {
			f.t.Fatal(err)
		}
		out = append(out, &rec)
	}
	return out
}

// accountRecords decodes the accounts log in append order.
func (f *fixture) accountRecords() []*accountsv1.AccountRecord {
	f.t.Helper()
	var out []*accountsv1.AccountRecord
	for _, r := range f.accounts.Records() {
		var rec accountsv1.AccountRecord
		if err := proto.Unmarshal(r.Value, &rec); err != nil {
			f.t.Fatal(err)
		}
		out = append(out, &rec)
	}
	return out
}

// metricsText renders the registry the way /metrics would.
func (f *fixture) metricsText() string {
	f.t.Helper()
	mfs, err := f.reg.Gather()
	if err != nil {
		f.t.Fatal(err)
	}
	var b bytes.Buffer
	enc := expfmt.NewEncoder(&b, expfmt.NewFormat(expfmt.TypeTextPlain))
	for _, mf := range mfs {
		if err := enc.Encode(mf); err != nil {
			f.t.Fatal(err)
		}
	}
	return b.String()
}

func mustRegister(t *testing.T, f *fixture, username, password, code string) string {
	t.Helper()
	id, err := f.store.Register(context.Background(), username, password, code, "peer")
	if err != nil {
		t.Fatalf("register %s: %v", username, err)
	}
	return id
}

func mustAuth(t *testing.T, f *fixture, username, password string) TokenPair {
	t.Helper()
	pair, err := f.store.Authenticate(context.Background(), username, password, "peer")
	if err != nil {
		t.Fatalf("authenticate %s: %v", username, err)
	}
	return pair
}
