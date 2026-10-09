// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package content

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/protobuf/proto"

	"github.com/valesordev/andara/content/core"
	adminv1 "github.com/valesordev/andara/gen/go/andara/admin/v1"
	contentv1 "github.com/valesordev/andara/gen/go/andara/content/v1"
	"github.com/valesordev/andara/server/auth"
	"github.com/valesordev/andara/server/command"
)

// AW-SRV-045: a pointer move carries the activation's traceparent, and the
// Loader's content.load links to every activation it coalesced.

// syncBuffer is a log sink the Loader's goroutines and the test share.
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

// tp is a valid W3C traceparent whose trace and span ids end in n.
func tp(n byte) string {
	const hex = "0123456789abcdef"
	d := string(hex[n%15+1]) // never all-zero, which is not a valid id
	return "00-" + strings.Repeat(d, 32) + "-" + strings.Repeat(d, 16) + "-01"
}

type linkRig struct {
	l     *Loader
	h     *engineHarness
	s     *fakeStore
	spans *tracetest.SpanRecorder
	logs  *syncBuffer
}

func newLinkRig(t *testing.T, packs ...string) *linkRig {
	t.Helper()
	r := &linkRig{s: newFakeStore(), spans: tracetest.NewSpanRecorder(), logs: &syncBuffer{}}
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(r.spans))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	r.s.publish("andara.core", 1, 0, map[string]string{"core.json": zoneJSON("core", "void")})
	for _, p := range packs {
		if p != CorePack {
			r.s.publish(p, 1, 1, map[string]string{p + ".json": zoneJSON(p, "nave")})
		}
	}
	r.l = NewLoader(LoaderOptions{
		Store: r.s, Packs: packs, Metrics: NewMetrics(nil), Tracer: provider.Tracer("test"),
		Log: slog.New(slog.NewJSONHandler(r.logs, nil)),
	})
	r.h = attachEngine(r.l)
	if _, err := r.l.LoadAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	return r
}

// follow starts Follow over a watcher with a short debounce; stop ends it.
func (r *linkRig) follow(t *testing.T, debounce time.Duration) (w *fakeWatcher, stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	w = &fakeWatcher{ch: make(chan PointerMove, 16)}
	done := make(chan struct{})
	go func() { defer close(done); _ = r.l.Follow(ctx, w, debounce, nil) }()
	return w, func() { cancel(); <-done }
}

// awaitLoads is loads once the first span for pack@version has ended. The
// Loader publishes the version before the deferred span.End runs, so a test
// that waits on Versions() and then reads the recorder once can see none
// (#486, main at bb76f62); the span is polled to a deadline, as
// docs/specs/testing/live-assertions.md says.
func (r *linkRig) awaitLoads(t *testing.T, pack string, version int64) []sdktrace.ReadOnlySpan {
	t.Helper()
	waitUntil(t, func() bool { return len(r.loads(pack, version)) > 0 }, fmt.Sprintf("the content.load span for %s@%d to end", pack, version))
	return r.loads(pack, version)
}

// loads are the ended content.load spans for pack@version, in order.
func (r *linkRig) loads(pack string, version int64) []sdktrace.ReadOnlySpan {
	var out []sdktrace.ReadOnlySpan
	for _, sp := range r.spans.Ended() {
		if sp.Name() != "content.load" {
			continue
		}
		var p string
		var v int64
		for _, a := range sp.Attributes() {
			switch a.Key {
			case "pack":
				p = a.Value.AsString()
			case "version":
				v = a.Value.AsInt64()
			}
		}
		if p == pack && v == version {
			out = append(out, sp)
		}
	}
	return out
}

func linkTo(t *testing.T, sp sdktrace.ReadOnlySpan, i int, traceparent string, pack string, version int64) {
	t.Helper()
	links := sp.Links()
	if i >= len(links) {
		t.Fatalf("link %d missing: %d links", i, len(links))
	}
	want := trace.SpanContextFromContext(command.ParentFrom(context.Background(), traceparent))
	got := links[i]
	if got.SpanContext.TraceID() != want.TraceID() || got.SpanContext.SpanID() != want.SpanID() {
		t.Fatalf("link %d = %s/%s, want %s/%s", i, got.SpanContext.TraceID(), got.SpanContext.SpanID(), want.TraceID(), want.SpanID())
	}
	attrs := map[attribute.Key]attribute.Value{}
	for _, a := range got.Attributes {
		attrs[a.Key] = a.Value
	}
	if attrs["content.pack"].AsString() != pack || attrs["content.version"].AsInt64() != version {
		t.Fatalf("link %d attributes = %v, want pack %s version %d", i, got.Attributes, pack, version)
	}
}

// AC-2: one move, one link to its traceparent, and the span keeps its parent.
func TestFollow_OneMoveLinksTheLoadToItsActivation(t *testing.T) {
	r := newLinkRig(t, CorePack)
	r.s.publish(CorePack, 2, 0, map[string]string{"core.json": zoneJSON("core", "void")})
	w, stop := r.follow(t, 5*time.Millisecond)
	defer stop()
	w.ch <- PointerMove{Pack: CorePack, Version: 2, TraceParent: tp(7)}
	waitUntil(t, func() bool { return r.l.Versions()[CorePack] == 2 }, "core@2 to load")

	loads := r.awaitLoads(t, CorePack, 2)
	if len(loads) != 1 || len(loads[0].Links()) != 1 {
		t.Fatalf("loads = %d, links = %v", len(loads), loads)
	}
	linkTo(t, loads[0], 0, tp(7), CorePack, 2)
}

// AC-3: three moves of one pack inside one window make one load, for the
// newest version, with a link per move and no parent from any of them.
func TestFollow_ACoalescedBurstLinksEveryMove(t *testing.T) {
	r := newLinkRig(t, CorePack)
	for v := uint64(2); v <= 4; v++ {
		r.s.publish(CorePack, v, 0, map[string]string{"core.json": zoneJSON("core", "void")})
	}
	w, stop := r.follow(t, 50*time.Millisecond)
	defer stop()
	for v := uint64(2); v <= 4; v++ {
		w.ch <- PointerMove{Pack: CorePack, Version: v, TraceParent: tp(byte(v))}
	}
	waitUntil(t, func() bool { return r.l.Versions()[CorePack] == 4 }, "core@4 to load")

	if n := len(r.loads(CorePack, 2)) + len(r.loads(CorePack, 3)); n != 0 {
		t.Fatalf("%d loads of the superseded versions", n)
	}
	loads := r.awaitLoads(t, CorePack, 4)
	if len(loads) != 1 || len(loads[0].Links()) != 3 {
		t.Fatalf("loads = %d for v4, links = %v", len(loads), loads[0].Links())
	}
	for i, v := range []int64{2, 3, 4} {
		linkTo(t, loads[0], i, tp(byte(v)), CorePack, v)
	}
	for _, link := range loads[0].Links() {
		_ = link
	}
	if loads[0].Parent().IsValid() {
		t.Fatalf("content.load has a parent: %v", loads[0].Parent())
	}
}

// AC-3, second half: moves of two packs in one window are two loads, each
// linking only its own pack's moves.
func TestFollow_ABurstAcrossPacksLinksEachLoadToItsOwnMoves(t *testing.T) {
	r := newLinkRig(t, "abbey", "chapel")
	r.s.publish("abbey", 2, 1, map[string]string{"abbey.json": zoneJSON("abbey", "nave")})
	r.s.publish("chapel", 2, 1, map[string]string{"chapel.json": zoneJSON("chapel", "nave")})
	w, stop := r.follow(t, 50*time.Millisecond)
	defer stop()
	w.ch <- PointerMove{Pack: "abbey", Version: 2, TraceParent: tp(1)}
	w.ch <- PointerMove{Pack: "chapel", Version: 2, TraceParent: tp(2)}
	waitUntil(t, func() bool { return r.l.Versions()["abbey"] == 2 && r.l.Versions()["chapel"] == 2 }, "both packs to load")

	for pack, n := range map[string]byte{"abbey": 1, "chapel": 2} {
		loads := r.awaitLoads(t, pack, 2)
		if len(loads) != 1 || len(loads[0].Links()) != 1 {
			t.Fatalf("%s: loads %d, links %v", pack, len(loads), loads)
		}
		linkTo(t, loads[0], 0, tp(n), pack, 2)
	}
}

// ACs 4 and 5: a move with no trace_parent (the boot's core activation, or a
// record from before the field) adds no link, says nothing, and loads.
func TestFollow_AMoveWithNoTraceParentAddsNoLink(t *testing.T) {
	r := newLinkRig(t, CorePack)
	r.s.publish(CorePack, 2, 0, map[string]string{"core.json": zoneJSON("core", "void")})
	w, stop := r.follow(t, 5*time.Millisecond)
	defer stop()
	w.ch <- PointerMove{Pack: CorePack, Version: 2}
	waitUntil(t, func() bool { return r.l.Versions()[CorePack] == 2 }, "core@2 to load")

	if loads := r.awaitLoads(t, CorePack, 2); len(loads) != 1 || len(loads[0].Links()) != 0 {
		t.Fatalf("loads = %v", loads)
	}
	if strings.Contains(r.logs.String(), "trace_parent") {
		t.Fatalf("an absent trace_parent was reported:\n%s", r.logs)
	}
}

// AC-6: a malformed trace_parent adds no link and logs one warn naming the
// pack, the version and the value cut to 128 bytes; the load goes ahead.
func TestFollow_AMalformedTraceParentIsReportedOnceAndTruncated(t *testing.T) {
	r := newLinkRig(t, CorePack)
	r.s.publish(CorePack, 2, 0, map[string]string{"core.json": zoneJSON("core", "void")})
	w, stop := r.follow(t, 5*time.Millisecond)
	defer stop()
	bad := strings.Repeat("x", 300)
	w.ch <- PointerMove{Pack: CorePack, Version: 2, TraceParent: bad}
	waitUntil(t, func() bool { return r.l.Versions()[CorePack] == 2 }, "core@2 to load despite the bad value")

	if loads := r.awaitLoads(t, CorePack, 2); len(loads) != 1 || len(loads[0].Links()) != 0 {
		t.Fatalf("loads = %v", loads)
	}
	var warns []string
	for _, line := range strings.Split(strings.TrimSpace(r.logs.String()), "\n") {
		if strings.Contains(line, `"level":"WARN"`) && strings.Contains(line, "trace_parent") {
			warns = append(warns, line)
		}
	}
	if len(warns) != 1 {
		t.Fatalf("%d warns:\n%s", len(warns), r.logs)
	}
	w0 := warns[0]
	if !strings.Contains(w0, `"pack":"andara.core"`) || !strings.Contains(w0, `"version":2`) ||
		!strings.Contains(w0, `"trace_parent":"`+strings.Repeat("x", 128)+`"`) || strings.Contains(w0, strings.Repeat("x", 129)) {
		t.Fatalf("warn = %s", w0)
	}
}

// AC-8: a refused load is retried with its backoff, and each retry's
// content.load carries the links of the attempt it retries.
func TestFollow_ARetryCarriesTheLinksOfTheAttemptItRetries(t *testing.T) {
	prevI, prevM := retryInitial, retryMax
	retryInitial, retryMax = 10*time.Millisecond, 40*time.Millisecond
	t.Cleanup(func() { retryInitial, retryMax = prevI, prevM })

	r := newLinkRig(t, CorePack)
	r.s.publish(CorePack, 2, 0, map[string]string{"core.json": zoneJSON("core", "void")})
	r.h.mu.Lock()
	r.h.fail = errors.New("broker unreachable")
	r.h.mu.Unlock()
	w, stop := r.follow(t, 5*time.Millisecond)
	defer stop()
	w.ch <- PointerMove{Pack: CorePack, Version: 2, TraceParent: tp(9)}
	waitUntil(t, func() bool { return len(r.loads(CorePack, 2)) >= 3 }, "two retries")
	r.h.mu.Lock()
	r.h.fail = nil
	r.h.mu.Unlock()
	waitUntil(t, func() bool { return r.l.Versions()[CorePack] == 2 }, "the retry to load core@2")

	for i, sp := range r.loads(CorePack, 2) {
		if len(sp.Links()) != 1 {
			t.Fatalf("attempt %d has %d links", i, len(sp.Links()))
		}
		linkTo(t, sp, 0, tp(9), CorePack, 2)
	}
}

// AC-1: ActivateVersion writes the traceparent of its own server span on the
// pointer record it produces.
func TestActivateVersion_TheRecordCarriesTheServerSpansTraceParent(t *testing.T) {
	spans := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	// A real tracer on the Admin: with the default no-op one, content.activate
	// shares its parent's span context and the test could not tell the spans apart.
	h := newPubHarness(t, func(ao *AdminOptions, _ *LoaderOptions) { ao.Tracer = provider.Tracer("admin") })
	if _, err := h.publish(builder(alice), "town", townFiles(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.admin.ApproveVersion(builder(bob), &adminv1.ApproveVersionRequest{PackId: "town", Version: 1}); err != nil {
		t.Fatal(err)
	}
	ctx, server := provider.Tracer("test").Start(builder(alice), "andara.admin.v1.AdminService/ActivateVersion")
	defer server.End()
	want := command.TraceParent(ctx)

	before := len(h.active.Records())
	if _, err := h.admin.ActivateVersion(ctx, &adminv1.ActivateVersionRequest{PackId: "town", Version: 1}); err != nil {
		t.Fatal(err)
	}
	recs := h.active.Records()[before:]
	if len(recs) != 1 {
		t.Fatalf("%d pointer records", len(recs))
	}
	var ptr contentv1.ActiveVersion
	if err := proto.Unmarshal(recs[0].Value, &ptr); err != nil {
		t.Fatal(err)
	}
	if want == "" || ptr.GetTraceParent() != want {
		t.Fatalf("trace_parent = %q, want the server span's %q", ptr.GetTraceParent(), want)
	}
	for _, sp := range spans.Ended() {
		if sp.Name() == "content.activate" && strings.Contains(ptr.GetTraceParent(), sp.SpanContext().SpanID().String()) {
			t.Fatalf("trace_parent names the internal content.activate span, not the server span")
		}
	}
	var activate bool
	for _, sp := range spans.Ended() {
		activate = activate || sp.Name() == "content.activate"
	}
	if !activate {
		t.Fatal("no content.activate span recorded: the test is not telling the two spans apart")
	}
}

// AC-4: the boot's core activation carries none, and neither does the
// pointer a registry holds for it.
func TestMovePointer_TheBootsCoreActivationCarriesNoTraceParent(t *testing.T) {
	h := newPubHarness(t, nil) // seeds core the way the boot does
	if av, ok := h.reg.Pointer(CorePack); !ok || av.GetTraceParent() != "" {
		t.Fatalf("core pointer = %v", av)
	}
}

// AC-4, through BootCore itself: even under a recording span, the boot's
// activation writes no trace_parent.
func TestBootCore_TheCoreActivationCarriesNoTraceParent(t *testing.T) {
	provider := sdktrace.NewTracerProvider()
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	ctx, span := provider.Tracer("test").Start(context.Background(), "boot")
	defer span.End()
	s := newCoreStore(t)
	log := slog.New(slog.DiscardHandler)
	res, err := BootCore(ctx, CoreBootOptions{
		Registry: s.reg, Auditor: auth.NewAuditor(s.audit, log, nil, nil), Metrics: NewPublishMetrics(nil), Log: log,
		Pack: CorePack, Version: 1, Blobs: core.Blobs(), Build: "v0.9.0", Tracer: provider.Tracer("test"),
	})
	if err != nil || !res.Activated {
		t.Fatalf("boot: %+v %v", res, err)
	}
	if av, ok := s.reg.Pointer(CorePack); !ok || av.GetTraceParent() != "" {
		t.Fatalf("core pointer = %v", av)
	}
}

// A pointer that flaps faster than the debounce makes one window of many moves;
// the load keeps the newest 128 links, not all of them.
func TestFollow_LinksPerWindowAreBounded(t *testing.T) {
	r := newLinkRig(t, CorePack)
	const first, last = 2, 201
	for v := uint64(first); v <= last; v++ {
		r.s.publish(CorePack, v, 0, map[string]string{"core.json": zoneJSON("core", "void")})
	}
	w, stop := r.follow(t, 300*time.Millisecond)
	defer stop()
	for v := uint64(first); v <= last; v++ {
		w.ch <- PointerMove{Pack: CorePack, Version: v, TraceParent: tp(byte(v))}
	}
	waitUntil(t, func() bool { return r.l.Versions()[CorePack] == last }, "the newest version to load")

	loads := r.awaitLoads(t, CorePack, last)
	if len(loads) != 1 || len(loads[0].Links()) != maxLoadLinks {
		t.Fatalf("loads %d, links %d, want one load with %d", len(loads), len(loads[0].Links()), maxLoadLinks)
	}
	linkTo(t, loads[0], maxLoadLinks-1, tp(byte(last)), CorePack, last)
	linkTo(t, loads[0], 0, tp(byte(last-maxLoadLinks+1)), CorePack, last-maxLoadLinks+1)
}

// Apply, handed a move directly, links its trace_parent too: only Follow
// coalesces, but nothing else drops the field.
func TestApply_AMoveHandedInDirectlyLinksItsTraceParent(t *testing.T) {
	r := newLinkRig(t, CorePack)
	r.s.publish(CorePack, 2, 0, map[string]string{"core.json": zoneJSON("core", "void")})
	if rej := r.l.Apply(context.Background(), PointerMove{Pack: CorePack, Version: 2, TraceParent: tp(5)}); len(rej) != 0 {
		t.Fatal(rej)
	}
	loads := r.loads(CorePack, 2)
	if len(loads) != 1 || len(loads[0].Links()) != 1 {
		t.Fatalf("loads = %v", loads)
	}
	linkTo(t, loads[0], 0, tp(5), CorePack, 2)
}
