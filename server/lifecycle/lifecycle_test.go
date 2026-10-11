// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package lifecycle_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
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

type neverRounds struct{ canceled *bool }

func (neverRounds) Force() <-chan tickloop.ForcedRound        { return make(chan tickloop.ForcedRound) }
func (n neverRounds) CancelForce(<-chan tickloop.ForcedRound) { *n.canceled = true }

func (*fakes) CancelForce(<-chan tickloop.ForcedRound) {}

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
