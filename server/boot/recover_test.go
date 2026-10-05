// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package boot

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	logv1 "github.com/valesordev/andara/gen/go/andara/log/v1"
	"github.com/valesordev/andara/server/content"
	"github.com/valesordev/andara/server/recovery"
	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/simtest"
)

func TestStartExit_MapsRecoveryRefusals(t *testing.T) {
	for _, c := range []struct {
		err  error
		want int
	}{
		{errors.New("broker down"), ExitFail},
		{fmt.Errorf("recovery: %w", recovery.Classify(&sim.HashMismatchError{})), 8},
		{fmt.Errorf("recovery: %w", recovery.Classify(&sim.RestoreMismatch{})), 6},
		{fmt.Errorf("recovery: %w", recovery.Classify(&sim.ErrRoundIncomplete{})), 7},
		{fmt.Errorf("recovery: %w", recovery.Classify(&sim.ErrStateVersion{})), 4},
	} {
		if got := StartExit(c.err); got != c.want {
			t.Errorf("%v: exit %d, want %d", c.err, got, c.want)
		}
	}
	if !Lingers(recovery.Classify(&sim.HashMismatchError{})) || !Lingers(recovery.Classify(&sim.SeedMismatch{})) {
		t.Error("exits 8 and 6 linger")
	}
	if Lingers(recovery.Classify(&sim.ErrRoundIncomplete{})) || Lingers(errors.New("x")) {
		t.Error("exit 7 and a plain error must not linger")
	}
}

func httpStatus(t *testing.T, base, path string) int {
	t.Helper()
	resp, err := (&http.Client{Transport: &http.Transport{DisableKeepAlives: true}}).Get(base + path)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// AC-14: during the linger /metrics and /livez answer 200 and /readyz and
// /startedz 503; the clock ends it; a signal ends it at once.
func TestHoldMismatch_ServesOperatorHTTPThenReturns(t *testing.T) {
	rt, _ := runtime(t, fixture(t, "valid"), false)
	for _, c := range []struct {
		name string
		end  func(tick chan time.Time, cancel context.CancelFunc)
	}{
		{"the clock", func(tick chan time.Time, _ context.CancelFunc) { tick <- time.Now() }},
		{"a signal", func(_ chan time.Time, cancel context.CancelFunc) { cancel() }},
	} {
		t.Run(c.name, func(t *testing.T) {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			tick := make(chan time.Time)
			var asked time.Duration
			done := make(chan struct{})
			go func() {
				rt.HoldMismatch(ctx, ln, 60*time.Second, func(d time.Duration) <-chan time.Time { asked = d; return tick })
				close(done)
			}()
			base := "http://" + ln.Addr().String()
			deadline := time.Now().Add(5 * time.Second)
			for httpStatus(t, base, "/livez") != 200 && time.Now().Before(deadline) {
				time.Sleep(5 * time.Millisecond)
			}
			for path, want := range map[string]int{"/livez": 200, "/metrics": 200, "/readyz": 503, "/startedz": 503} {
				if got := httpStatus(t, base, path); got != want {
					t.Errorf("%s = %d, want %d", path, got, want)
				}
			}
			select {
			case <-done:
				t.Fatal("returned before the linger ended")
			default:
			}
			c.end(tick, cancel)
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("did not return")
			}
			if asked != 60*time.Second {
				t.Errorf("waited %s, want 60s", asked)
			}
		})
	}
}

// Ready is the first live tick completed within 10 tick budgets of schedule,
// on top of the Gateway serving (AW-SRV-007).
func TestReady_WaitsForTheFirstLiveTickWithinTenBudgets(t *testing.T) {
	rt, _ := runtime(t, fixture(t, "valid"), false)
	rt.Cfg.SimTickBudget = 50 * time.Millisecond
	rt.MarkReady()
	var ticks uint64
	var lag time.Duration
	rt.live = func() (uint64, time.Duration) { return ticks, lag }
	for _, c := range []struct {
		ticks uint64
		lag   time.Duration
		want  bool
	}{
		{0, 0, false},
		{1, 499 * time.Millisecond, true},
		{9, 500 * time.Millisecond, false},
		{9, time.Second, false},
	} {
		ticks, lag = c.ticks, c.lag
		if got := rt.Ready(); got != c.want {
			t.Errorf("ticks %d lag %s: Ready = %v, want %v", c.ticks, c.lag, got, c.want)
		}
		if got := httpGet(rt.Handler(), "/readyz"); (got == 200) != c.want {
			t.Errorf("ticks %d lag %s: /readyz = %d", c.ticks, c.lag, got)
		}
	}
	rt.Drain()
	ticks, lag = 5, 0
	if rt.Ready() {
		t.Error("Ready while draining")
	}
}

func httpGet(h http.Handler, path string) int {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec.Code
}

// recovery.pin_round, recovery.require_snapshot and recovery.replay_batch
// reach the Options boot recovery runs with; a store is opened only when
// something reads rounds.
func TestRecoverOptions_CarryTheRecoveryKeys(t *testing.T) {
	rt, _ := runtime(t, fixture(t, "valid"), false)
	rt.replay = &memLog{}
	rt.Cfg.SnapshotStore, rt.Cfg.SnapshotFSPath = "fs", t.TempDir()
	var inEffect bool

	rt.Cfg.SnapshotInterval = 0
	o, release, err := rt.RecoverOptions(context.Background(), sim.Config{}, &inEffect)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if o.Store != nil || o.Round != nil || o.RequireSnapshot {
		t.Fatalf("snapshots off, nothing pinned: store %v round %v require %v", o.Store, o.Round, o.RequireSnapshot)
	}

	// recover --verify reads the rounds that exist, with snapshots off.
	rt.Cfg.RecoveryPinRound = 0
	rt.ReadRounds = true
	if o, release, err = rt.RecoverOptions(context.Background(), sim.Config{}, &inEffect); err != nil || o.Store == nil {
		t.Fatalf("ReadRounds: store %v err %v", o.Store, err)
	} else {
		release()
	}
	rt.ReadRounds = false

	rt.Cfg.RecoveryPinRound, rt.Cfg.RecoveryRequireSnapshot, rt.Cfg.RecoveryReplayBatch = 4200, true, 7
	o, release, err = rt.RecoverOptions(context.Background(), sim.Config{}, &inEffect)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if o.Store == nil || o.Round == nil || *o.Round != 4200 || !o.RequireSnapshot || o.ReplayBatch != 7 {
		t.Fatalf("pinned: store %v round %v require %v batch %d", o.Store, o.Round, o.RequireSnapshot, o.ReplayBatch)
	}
}

// AC-5 through StartTickLoop: a rewritten tail boundary is a *recovery.Failure
// whose exit is 8 and which lingers, the gauge is 0 and counted under
// reason=hash, and nothing past the tick loop was built, so no grpc.listen was
// ever bound.
func TestStartTickLoop_ARewrittenBoundaryIsExit8WithTheGaugeAtZero(t *testing.T) {
	rt := recoveringRuntime(t, &memLog{recs: simtest.MemorySource{}})
	swap := &logv1.ContentSwap{PackId: content.DirPack}
	topo, err := rt.Content.Prepare(nil, swap)
	if err != nil {
		t.Fatal(err)
	}
	d := sim.ContentDigest(topo)
	swap.WorldDigest = d[:]
	live := sim.NewEngine(sim.EmptyWorld(), nil, sim.Config{Seed: 5, Partitions: allPartitionsForTest(), Handlers: sim.Handlers(), Content: rt.Content})
	log := &memLog{recs: simtest.MemorySource{}}
	log.record(t, live, nil, &logv1.LoggedCommand{Command: &logv1.LoggedCommand_ContentSwap{ContentSwap: swap}},
		simtest.Bind("town", "ch-1", "Aldric", "plaza"), simtest.Look("town", "ch-1"), nil, nil)
	log.boundaries[4].StateHash[0] ^= 0xff // tick 5
	rt.replay = log

	if n := testutil.CollectAndCount(rt.Tel.Reg, "andara_recovery_state_hash_match"); n != 0 {
		t.Fatalf("the gauge has %d samples before recovery sets it", n)
	}
	_, err = rt.StartTickLoop(context.Background())
	var hm *recovery.HashMismatchError
	if !errors.As(err, &hm) || hm.Tick != 5 || hm.Round != 0 {
		t.Fatalf("err %v, want a hash mismatch at tick 5 from a cold start", err)
	}
	if StartExit(err) != recovery.ExitHashMismatch || !Lingers(err) {
		t.Fatalf("exit %d lingers %v, want 8 and true", StartExit(err), Lingers(err))
	}
	if testutil.ToFloat64(rt.RecoveryMetrics().HashMatch()) != 0 ||
		testutil.ToFloat64(rt.RecoveryMetrics().Failures.WithLabelValues(recovery.ReasonHash)) != 1 {
		t.Fatal("gauge not 0, or failures{hash} not 1")
	}
	if rt.Ready() || rt.started.Load() {
		t.Fatal("a refused recovery must not be ready or started")
	}
}

// ExitOnStartError: a mismatch lingers on the listener it is given and exits
// 8; every other refusal returns at once without listening.
func TestExitOnStartError_LingersOnlyForAMismatch(t *testing.T) {
	rt, _ := runtime(t, fixture(t, "valid"), false)
	rt.Cfg.RecoveryMismatchLinger = time.Minute
	tctx, cancelT := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancelT()
	listens := 0
	listen := func() (net.Listener, error) {
		listens++
		return net.Listen("tcp", "127.0.0.1:0")
	}
	var asked time.Duration
	tick := make(chan time.Time, 1)
	tick <- time.Now()
	wait := func(d time.Duration) <-chan time.Time { asked = d; return tick }

	if code := rt.ExitOnStartError(context.Background(), recovery.Classify(&sim.HashMismatchError{}), listen, wait); code != 8 || listens != 1 || asked != time.Minute {
		t.Fatalf("a hash mismatch: exit %d, listens %d, waited %s", code, listens, asked)
	}
	// A restore mismatch (exit 6) lingers too: the same listener, the same wait.
	for _, e := range []error{&sim.RestoreMismatch{}, &sim.SeedMismatch{}, &sim.ContentDigestError{}} {
		before := listens
		tick <- time.Now()
		if code := rt.ExitOnStartError(tctx, recovery.Classify(e), listen, wait); code != 6 || listens != before+1 {
			t.Fatalf("%T: exit %d, listens %d, want 6 and one more listen", e, code, listens-before)
		}
	}
	listens = 1
	for _, err := range []error{recovery.Classify(&sim.ErrRoundIncomplete{}), recovery.Classify(&sim.ErrStateVersion{}), errors.New("broker down")} {
		if code := rt.ExitOnStartError(context.Background(), err, listen, wait); code == 0 || listens != 1 {
			t.Fatalf("%v: exit %d, listens %d: it must not linger", err, code, listens)
		}
	}
	rt.Cfg.RecoveryMismatchLinger = 0
	if code := rt.ExitOnStartError(context.Background(), recovery.Classify(&sim.HashMismatchError{}), listen, wait); code != 8 || listens != 1 {
		t.Fatalf("linger 0s: exit %d, listens %d", code, listens)
	}
}

type fakeOrphans struct{ got []sim.CharacterBody }

func (f *fakeOrphans) MarkOrphans(_ context.Context, b []sim.CharacterBody) (int, int) {
	f.got = b
	return len(b), 0
}

// Boot releases the bodies a crash left standing, as soon as recovery has
// them: a Character present with no Session is handed to the roster.
func TestStartTickLoop_ReleasesTheBodiesACrashLeftStanding(t *testing.T) {
	rt := recoveringRuntime(t, &memLog{recs: simtest.MemorySource{}})
	swap := &logv1.ContentSwap{PackId: content.DirPack}
	topo, err := rt.Content.Prepare(nil, swap)
	if err != nil {
		t.Fatal(err)
	}
	d := sim.ContentDigest(topo)
	swap.WorldDigest = d[:]
	live := sim.NewEngine(sim.EmptyWorld(), nil, sim.Config{Seed: 5, Partitions: allPartitionsForTest(), Handlers: sim.Handlers(), Content: rt.Content})
	log := &memLog{recs: simtest.MemorySource{}}
	log.record(t, live, nil, &logv1.LoggedCommand{Command: &logv1.LoggedCommand_ContentSwap{ContentSwap: swap}},
		simtest.Bind("town", "ch-1", "Aldric", "plaza"))
	rt.replay = log
	f := &fakeOrphans{}
	rt.orphans = f

	// The broker isn't there: the boot fails after recovery, which is far enough.
	_, serr := rt.StartTickLoop(context.Background())
	if len(f.got) != 1 || f.got[0].ID != "ch-1" || f.got[0].Zone != "town" {
		t.Fatalf("released %v, want ch-1 in town (boot said %v)", f.got, serr)
	}
}

// A seed above 2^63 is logged as a decimal string: as a JSON number it reaches
// Loki rounded (#423).
func TestSeedIsLoggedAsADecimalString(t *testing.T) {
	var buf bytes.Buffer
	l := slog.New(slog.NewJSONHandler(&buf, nil))
	l.LogAttrs(context.Background(), slog.LevelInfo, "x", seedAttr(16406829232824261652))
	if !strings.Contains(buf.String(), `"seed":"16406829232824261652"`) {
		t.Fatalf("log line %s", buf.String())
	}
}

// The boot summary names the recovery.run trace, round or no round.
func TestRecoveredFromTheLogCarriesTheTraceID(t *testing.T) {
	rt, logs := runtime(t, fixture(t, "valid"), false)
	if code := rt.LoadContent(context.Background()); code != ExitOK {
		t.Fatalf("load: %s", logs.String())
	}
	rt.Cfg.SimSource, rt.Cfg.SimSeed, rt.Cfg.SimPartitions = "kafka", 5, allPartitionsForTest()
	rt.replay = &memLog{recs: simtest.MemorySource{}}
	_, _ = rt.StartTickLoop(context.Background()) // fails at the missing broker, after recovery
	var line string
	for _, l := range strings.Split(logs.String(), "\n") {
		if strings.Contains(l, "recovered from the log") {
			line = l
		}
	}
	if line == "" || !strings.Contains(line, `"trace_id"`) || !strings.Contains(line, `"round_tick"`) {
		t.Fatalf("no summary line with a trace_id in:\n%s", logs.String())
	}
}
