// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package lifecycle_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	adminv1 "github.com/valesordev/andara/gen/go/andara/admin/v1"
	"github.com/valesordev/andara/server/lifecycle"
	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/store"
	"github.com/valesordev/andara/server/tickloop"
)

// fakes records the order the sequence touches each part in.
type fakes struct {
	mu      sync.Mutex
	steps   []string
	round   tickloop.ForcedRound
	slept   []time.Duration
	forces  int
	refused bool
}

func (f *fakes) step(s string) { f.mu.Lock(); f.steps = append(f.steps, s); f.mu.Unlock() }

func (f *fakes) AnnounceStopping(msg string, back time.Duration) int {
	f.step(fmt.Sprintf("announce %q %s", msg, back))
	return 2
}
func (f *fakes) RefuseRestarting() {
	f.mu.Lock()
	f.refused = true
	f.mu.Unlock()
	f.step("refuse")
}
func (f *fakes) WaitAdmitted(context.Context) bool { f.step("admitted"); return true }
func (f *fakes) Force() <-chan tickloop.ForcedRound {
	f.mu.Lock()
	f.forces++
	r := f.round
	f.mu.Unlock()
	f.step("force")
	ch := make(chan tickloop.ForcedRound, 1)
	ch <- r
	return ch
}

type harness struct {
	f     *fakes
	fs    *store.FS
	svc   *lifecycle.Service
	logs  *bytes.Buffer
	spans *tracetest.SpanRecorder
}

func newHarness(t *testing.T, round tickloop.ForcedRound, mutate func(*lifecycle.Options)) *harness {
	t.Helper()
	h := &harness{f: &fakes{round: round}, fs: store.NewFS(t.TempDir()), logs: &bytes.Buffer{}, spans: tracetest.NewSpanRecorder()}
	for _, k := range round.Keys {
		if err := h.fs.Put(context.Background(), k, []byte("body")); err != nil {
			t.Fatal(err)
		}
	}
	o := lifecycle.Options{
		Announcer: h.f, Refuser: h.f, Rounds: h.f, Store: h.fs,
		Committed:     func() sim.Tick { return round.Tick },
		NoticeLead:    10 * time.Second,
		AdmitWait:     5 * time.Second,
		ExpectedBack:  60 * time.Second,
		ImageTag:      "sha-0123456789ab",
		UploadTimeout: time.Second,
		TickInterval:  100 * time.Millisecond,
		Log:           slog.New(slog.NewJSONHandler(h.logs, nil)),
		Tracer:        sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(h.spans)).Tracer("test"),
		Sleep: func(_ context.Context, d time.Duration) {
			h.f.mu.Lock()
			h.f.slept = append(h.f.slept, d)
			h.f.mu.Unlock()
			if d >= time.Second {
				h.f.step("sleep " + d.String())
			}
		},
	}
	if mutate != nil {
		mutate(&o)
	}
	h.svc = lifecycle.New(o)
	return h
}

func (h *harness) call(t *testing.T) *adminv1.PrepareStopResponse {
	t.Helper()
	resp, err := h.svc.PrepareStop(context.Background(), connect.NewRequest(&adminv1.PrepareStopRequest{}))
	if err != nil {
		t.Fatalf("PrepareStop: %v", err)
	}
	return resp.Msg
}

func goodRound() tickloop.ForcedRound {
	return tickloop.ForcedRound{Tick: 420, Hash: [32]byte{0xab, 0xcd}, Keys: []string{sim.SnapshotKey("town", 1, 420, 7), sim.SnapshotKey("wilds", 1, 420, 3)}}
}

// AC-1 and AC-2: the sequence runs in the contract's order — announce, the
// notice lead, refuse, the admitted wait, the round — and a complete round is
// tagged deploy:<image tag> on every object, reported with its tick, hash and
// deploy_id, and logged as `prestop complete` under deploy.prestop_snapshot.
func TestPrepareStopRunsTheSequenceInOrder(t *testing.T) {
	t.Parallel()
	h := newHarness(t, goodRound(), nil)
	resp := h.call(t)
	hash := goodRound().Hash
	want := []string{`announce "" 1m0s`, "sleep 10s", "refuse", "admitted", "force"}
	if !reflect.DeepEqual(h.f.steps, want) {
		t.Fatalf("steps = %q, want %q", h.f.steps, want)
	}
	if resp.GetOutcome() != adminv1.PrepareStopOutcome_PREPARE_STOP_OUTCOME_COMPLETE || resp.GetTick() != 420 ||
		!bytes.Equal(resp.GetStateHash(), hash[:]) || resp.GetTag() != "deploy:sha-0123456789ab" ||
		resp.GetDeployId() != "sha-0123456789ab@420" {
		t.Fatalf("response = %v", resp)
	}
	tags, err := store.RoundTags(context.Background(), h.fs, goodRound().Keys[:1])
	if err != nil || !reflect.DeepEqual(tags, []string{"deploy:sha-0123456789ab"}) {
		t.Fatalf("tags on the first object = %v, %v", tags, err)
	}
	tags, _ = store.RoundTags(context.Background(), h.fs, goodRound().Keys[1:])
	if len(tags) != 1 {
		t.Fatalf("tags on the second object = %v, want the deploy tag", tags)
	}
	var span sdktrace.ReadOnlySpan
	for _, s := range h.spans.Ended() {
		if s.Name() == "deploy.prestop_snapshot" {
			span = s
		}
	}
	if span == nil {
		t.Fatal("no deploy.prestop_snapshot span")
	}
	attrs := map[string]string{}
	for _, a := range span.Attributes() {
		attrs[string(a.Key)] = a.Value.AsString()
	}
	if attrs["deploy_id"] != "sha-0123456789ab@420" {
		t.Fatalf("span deploy_id = %q", attrs["deploy_id"])
	}
	line := logLine(t, h.logs, "prestop complete")
	for _, f := range []string{`"tick":420`, `"hash":"abcd`, `"tag":"deploy:sha-0123456789ab"`, `"outcome":"complete"`, `"duration_ms":`, `"refused_ms":`,
		`"trace_id":"` + span.SpanContext().TraceID().String() + `"`} {
		if !strings.Contains(line, f) {
			t.Errorf("prestop complete line lacks %s: %s", f, line)
		}
	}
}

// AC-6: a second call returns the first call's response and repeats no step.
func TestPrepareStopTwiceRepeatsNothing(t *testing.T) {
	t.Parallel()
	h := newHarness(t, goodRound(), nil)
	first := h.call(t)
	steps := len(h.f.steps)
	second := h.call(t)
	if len(h.f.steps) != steps || h.f.forces != 1 {
		t.Fatalf("the second call ran steps %q", h.f.steps[steps:])
	}
	if first.String() != second.String() {
		t.Fatalf("second response %v, want the first's %v", second, first)
	}
}

// AC-5: a round past snapshot.upload_timeout is TIMEOUT and a store failure
// STORE_ERROR; neither writes a tag, and each logs its cause at error.
func TestPrepareStopFailuresWriteNoTag(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		err  error
		want adminv1.PrepareStopOutcome
		exit int
	}{
		{"timeout", fmt.Errorf("zone town: %w", context.DeadlineExceeded), adminv1.PrepareStopOutcome_PREPARE_STOP_OUTCOME_TIMEOUT, lifecycle.ExitTimeout},
		{"store", fmt.Errorf("zone town: %w", sim.ErrStoreUnavailable), adminv1.PrepareStopOutcome_PREPARE_STOP_OUTCOME_STORE_ERROR, lifecycle.ExitStoreError},
		{"disabled", tickloop.ErrSnapshotsDisabled, adminv1.PrepareStopOutcome_PREPARE_STOP_OUTCOME_STORE_ERROR, lifecycle.ExitStoreError},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := goodRound()
			r.Err = c.err
			h := newHarness(t, r, nil)
			resp := h.call(t)
			if resp.GetOutcome() != c.want || resp.GetTag() != "" || resp.GetTick() != 0 || resp.GetDeployId() != "" {
				t.Fatalf("response = %v, want %v with no tag", resp, c.want)
			}
			if got := lifecycle.Exit(resp, nil); got != c.exit {
				t.Fatalf("exit %d, want %d", got, c.exit)
			}
			if tags, _ := store.RoundTags(context.Background(), h.fs, r.Keys); len(tags) != 0 {
				t.Fatalf("a failed round was tagged %v", tags)
			}
			if !strings.Contains(logLine(t, h.logs, "prestop snapshot failed"), `"level":"ERROR"`) {
				t.Fatal("no error line naming the cause")
			}
		})
	}
}

// A round that never arrives inside upload_timeout plus two ticks is TIMEOUT.
func TestPrepareStopTimesOutWaitingForTheRound(t *testing.T) {
	t.Parallel()
	canceled := false
	h := newHarness(t, goodRound(), func(o *lifecycle.Options) {
		o.UploadTimeout = 20 * time.Millisecond
		o.Rounds = neverRounds{canceled: &canceled}
	})
	if resp := h.call(t); resp.GetOutcome() != adminv1.PrepareStopOutcome_PREPARE_STOP_OUTCOME_TIMEOUT {
		t.Fatalf("outcome %v, want TIMEOUT", resp.GetOutcome())
	}
	// Withdrawn: no round is taken later for a pre-stop that gave up.
	if !canceled {
		t.Fatal("the timed-out Force request was not withdrawn")
	}
}

// AC-5: a tag cut short by a store error is STORE_ERROR and names no round,
// though the first object's tag was written.
func TestPrepareStopTagFailureNamesNoRound(t *testing.T) {
	t.Parallel()
	r := goodRound()
	var fs *store.FS
	h := newHarness(t, r, func(o *lifecycle.Options) {
		fs = o.Store.(*store.FS)
		o.Store = &tagStore{FS: fs, fail: store.TagKey(r.Keys[1], "deploy:sha-0123456789ab")}
	})
	resp := h.call(t)
	if resp.GetOutcome() != adminv1.PrepareStopOutcome_PREPARE_STOP_OUTCOME_STORE_ERROR || resp.GetTag() != "" {
		t.Fatalf("response = %v, want STORE_ERROR with no tag", resp)
	}
	if tags, _ := store.RoundTags(context.Background(), fs, r.Keys[:1]); len(tags) != 1 {
		t.Fatalf("first object's tags = %v; the test wants the tag cut short after it", tags)
	}
	if tags, _ := store.RoundTags(context.Background(), fs, r.Keys); len(tags) != 0 {
		t.Fatalf("a round whose tagging failed is named %v", tags)
	}
}

// The round, its tag and the wait for them share one budget,
// snapshot.upload_timeout plus two ticks: the term the grace bound and
// `prestop`'s own bound count once (lifecycle.md, Configuration). A round that
// arrives late and a tag that hangs are one TIMEOUT inside it, not two.
func TestPrepareStopRoundAndTagShareOneBudget(t *testing.T) {
	t.Parallel()
	r := goodRound()
	const upload, tick = time.Second, 100 * time.Millisecond
	h := newHarness(t, r, func(o *lifecycle.Options) {
		o.UploadTimeout, o.TickInterval = upload, tick
		o.Rounds = lateRounds{r: r, after: upload}
		o.Store = &tagStore{FS: o.Store.(*store.FS), hang: true}
	})
	start := time.Now()
	resp := h.call(t)
	took := time.Since(start)
	if resp.GetOutcome() != adminv1.PrepareStopOutcome_PREPARE_STOP_OUTCOME_TIMEOUT {
		t.Fatalf("outcome %v, want TIMEOUT", resp.GetOutcome())
	}
	if took > upload+2*tick+300*time.Millisecond {
		t.Fatalf("PrepareStop took %s, past upload_timeout plus two ticks (%s)", took, upload+2*tick)
	}
}

// tagStore fails the Put of one tag key, or hangs every tag Put until its
// context ends.
type tagStore struct {
	*store.FS
	fail string
	hang bool
}

func (s *tagStore) Put(ctx context.Context, key string, b []byte) error {
	if s.hang && strings.Contains(key, ".tag/") {
		<-ctx.Done()
		return ctx.Err()
	}
	if key == s.fail {
		return fmt.Errorf("put %s: %w", key, sim.ErrStoreUnavailable)
	}
	return s.FS.Put(ctx, key, b)
}

// lateRounds delivers the round after a delay.
type lateRounds struct {
	r     tickloop.ForcedRound
	after time.Duration
}

func (l lateRounds) Force() <-chan tickloop.ForcedRound {
	ch := make(chan tickloop.ForcedRound, 1)
	time.AfterFunc(l.after, func() { ch <- l.r })
	return ch
}
func (lateRounds) CancelForce(<-chan tickloop.ForcedRound) bool { return false }

type neverRounds struct{ canceled *bool }

func (neverRounds) Force() <-chan tickloop.ForcedRound { return make(chan tickloop.ForcedRound) }
func (n neverRounds) CancelForce(<-chan tickloop.ForcedRound) bool {
	*n.canceled = true
	return true
}

func (*fakes) CancelForce(<-chan tickloop.ForcedRound) bool { return false }

// AC-5 and ruling 1: over the socket, the response's outcome is the exit
// code; the socket is mode 0600; no socket exits 3.
func TestPrestopOverTheSocket(t *testing.T) {
	t.Parallel()
	dir, err := os.MkdirTemp("", "aw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "c.sock")
	h := newHarness(t, goodRound(), nil)
	srv, err := lifecycle.Listen(path, h.svc)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != lifecycle.SocketMode {
		t.Fatalf("socket mode %v, %v; want 0600", fi.Mode().Perm(), err)
	}
	resp, err := lifecycle.Call(context.Background(), path)
	if code := lifecycle.Exit(resp, err); code != lifecycle.ExitComplete {
		t.Fatalf("exit %d (%v), want 0", code, err)
	}
	if out := lifecycle.Format(resp); !strings.Contains(out, "outcome=complete\ntick=420\n") || !strings.Contains(out, "deploy_id=sha-0123456789ab@420") {
		t.Fatalf("printed %q", out)
	}
	_ = srv.Close()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket left behind after Close: %v", err)
	}
	resp, err = lifecycle.Call(context.Background(), path)
	if code := lifecycle.Exit(resp, err); code != lifecycle.ExitNoSocket {
		t.Fatalf("no socket: exit %d (%v), want 3", code, err)
	}
}

// AC-5: a socket file a previous process left behind, with nothing listening,
// is no socket (exit 3); a call whose time ran out before it connected is a
// timeout (exit 1), not a missing socket.
func TestPrestopStaleSocketAndExpiredCall(t *testing.T) {
	t.Parallel()
	dir, err := os.MkdirTemp("", "aw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	stale := filepath.Join(dir, "stale.sock")
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: stale, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	ln.SetUnlinkOnClose(false)
	_ = ln.Close()
	if _, err := os.Stat(stale); err != nil {
		t.Fatalf("the stale socket file is gone: %v", err)
	}
	resp, err := lifecycle.Call(context.Background(), stale)
	if code := lifecycle.Exit(resp, err); code != lifecycle.ExitNoSocket {
		t.Fatalf("stale socket: exit %d (%v), want 3", code, err)
	}

	live := filepath.Join(dir, "c.sock")
	srv, err := lifecycle.Listen(live, newHarness(t, goodRound(), nil).svc)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	resp, err = lifecycle.Call(ctx, live)
	if code := lifecycle.Exit(resp, err); code != lifecycle.ExitTimeout {
		t.Fatalf("expired call: exit %d (%v), want 1", code, err)
	}
}

// A sequence that panics still releases a second call, which gets an error
// rather than waiting forever.
func TestPrepareStopAfterAPanicDoesNotHang(t *testing.T) {
	t.Parallel()
	h := newHarness(t, goodRound(), func(o *lifecycle.Options) { o.Announcer = panicky{} })
	func() {
		defer func() { _ = recover() }()
		_, _ = h.svc.PrepareStop(context.Background(), connect.NewRequest(&adminv1.PrepareStopRequest{}))
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := h.svc.PrepareStop(ctx, connect.NewRequest(&adminv1.PrepareStopRequest{}))
	if connect.CodeOf(err) != connect.CodeInternal {
		t.Fatalf("second call after a panic: %v, want Internal", err)
	}
}

type panicky struct{}

func (panicky) AnnounceStopping(string, time.Duration) int { panic("announce") }

func logLine(t *testing.T, b *bytes.Buffer, msg string) string {
	t.Helper()
	for _, l := range strings.Split(b.String(), "\n") {
		if strings.Contains(l, `"msg":"`+msg) {
			return l
		}
	}
	t.Fatalf("no %q line in:\n%s", msg, b.String())
	return ""
}

// The sequence's "commit offsets" step: PrepareStop returns only once the
// loop has committed through the round's tick.
func TestPrepareStopWaitsForTheRoundsCommit(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	polls := 0
	h := newHarness(t, goodRound(), func(o *lifecycle.Options) {
		o.Committed = func() sim.Tick {
			mu.Lock()
			defer mu.Unlock()
			polls++
			if polls <= 3 {
				return goodRound().Tick - 1
			}
			return goodRound().Tick
		}
	})
	resp := h.call(t)
	mu.Lock()
	n := polls
	mu.Unlock()
	if n < 4 {
		t.Fatalf("PrepareStop returned after %d polls of the committed tick, before it reached the round's", n)
	}
	if resp.GetOutcome() != adminv1.PrepareStopOutcome_PREPARE_STOP_OUTCOME_COMPLETE {
		t.Fatalf("outcome %v", resp.GetOutcome())
	}
	if strings.Contains(h.logs.String(), "were not committed yet") {
		t.Fatalf("a commit that came is logged as missing:\n%s", h.logs.String())
	}
}

// A commit that never comes is logged, and the round is still the recovery
// point: the outcome stays COMPLETE.
func TestPrepareStopLogsACommitThatNeverComes(t *testing.T) {
	t.Parallel()
	h := newHarness(t, goodRound(), func(o *lifecycle.Options) {
		o.Committed = func() sim.Tick { return goodRound().Tick - 1 }
		o.TickInterval = 10 * time.Millisecond
		o.Sleep = func(ctx context.Context, d time.Duration) {
			if d < time.Second {
				// The commit poll: wait for real, so the bounded wait elapses.
				select {
				case <-time.After(d):
				case <-ctx.Done():
				}
			}
		}
	})
	resp := h.call(t)
	if resp.GetOutcome() != adminv1.PrepareStopOutcome_PREPARE_STOP_OUTCOME_COMPLETE {
		t.Fatalf("outcome %v, want COMPLETE: the round is the recovery point either way", resp.GetOutcome())
	}
	line := logLine(t, h.logs, "prestop: the round's offsets were not committed yet")
	for _, f := range []string{`"level":"WARN"`, `"tick":420`, `"committed":419`, `"trace_id":"`} {
		if !strings.Contains(line, f) {
			t.Errorf("warn line lacks %s: %s", f, line)
		}
	}
	if strings.Contains(line, `"trace_id":"00000000000000000000000000000000"`) {
		t.Errorf("warn line carries no trace: %s", line)
	}
}

// AC-1's refused_ms counts from the refusal and duration_ms from the start,
// so the notice lead is in duration_ms only (lifecycle.md).
func TestPrepareStopRefusedMsStartsAtTheRefusal(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	clock := time.Unix(1_800_000_000, 0)
	polls := 0
	h := newHarness(t, goodRound(), func(o *lifecycle.Options) {
		o.Now = func() time.Time { mu.Lock(); defer mu.Unlock(); return clock }
		o.Sleep = func(_ context.Context, d time.Duration) { mu.Lock(); clock = clock.Add(d); mu.Unlock() }
		// Two polls short of the commit: 20ms pass after the refusal.
		o.Committed = func() sim.Tick {
			mu.Lock()
			defer mu.Unlock()
			polls++
			if polls <= 2 {
				return goodRound().Tick - 1
			}
			return goodRound().Tick
		}
	})
	resp := h.call(t)
	if resp.GetDurationMs() != 10_020 || resp.GetRefusedMs() != 20 {
		t.Fatalf("duration_ms %d refused_ms %d, want 10020 and 20 (notice lead 10s, then 20ms to the commit)", resp.GetDurationMs(), resp.GetRefusedMs())
	}
}
