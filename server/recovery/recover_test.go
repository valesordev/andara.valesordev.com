// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package recovery_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/valesordev/andara/server/recovery"
	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/simtest"
	"github.com/valesordev/andara/server/store"
	"github.com/valesordev/andara/server/tickloop"
)

// memBoundaries is a Boundaries over a slice, the way the broker's reader
// serves one: SeekAfter positions at the first boundary past the tick.
type memBoundaries struct {
	all  []tickloop.Boundary
	next int
	seek bool
}

func (m *memBoundaries) SeekAfter(_ context.Context, tick sim.Tick) (int64, error) {
	m.next, m.seek = len(m.all), false
	for i, b := range m.all {
		if b.Tick > tick {
			m.next, m.seek = i, true
			break
		}
	}
	return int64(m.next), nil
}
func (m *memBoundaries) BoundaryAfter() bool { return m.seek }
func (m *memBoundaries) HeadTick(context.Context) (sim.Tick, error) {
	if len(m.all) == 0 {
		return 0, nil
	}
	return m.all[len(m.all)-1].Tick, nil
}
func (m *memBoundaries) Next(_ context.Context, max int, _ time.Duration) ([]tickloop.Boundary, error) {
	n := min(max, len(m.all)-m.next)
	out := m.all[m.next : m.next+n]
	m.next += n
	return out, nil
}

// world is a live Engine, its log, and a store with a round at tick `round`.
type world struct {
	owned    []sim.ZoneID
	fs       *store.FS
	bounds   []tickloop.Boundary
	records  simtest.MemorySource
	finalH   [32]byte
	finalTik sim.Tick
}

func newWorld(t *testing.T, ticks, round int) *world {
	t.Helper()
	e, err := simtest.NewEngine(11)
	if err != nil {
		t.Fatal(err)
	}
	w := &world{fs: store.NewFS(t.TempDir()), owned: e.State().SortedZoneIDs()}
	all := simtest.Script(ticks * 2)
	remaining := map[int32][]sim.Record{}
	w.records = simtest.MemorySource{}
	for p, rs := range all {
		remaining[p] = append([]sim.Record(nil), rs...)
		w.records[p] = rs
	}
	for i := 1; i <= ticks; i++ {
		res, err := e.Step(simtest.Batch(remaining, 2))
		if err != nil {
			t.Fatal(err)
		}
		w.bounds = append(w.bounds, tickloop.Boundary{TickCompleted: res.Completed})
		if i == round {
			snaps := e.SnapshotAll(1)
			for j := range snaps {
				body, err := snaps[j].Encode()
				if err != nil {
					t.Fatal(err)
				}
				if err := w.fs.Put(context.Background(), snaps[j].Key(), body); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	w.finalH, w.finalTik = e.StateHash(), e.Tick()
	return w
}

func (w *world) opts(t *testing.T) recovery.Options {
	t.Helper()
	topo, err := simtest.World()
	if err != nil {
		t.Fatal(err)
	}
	reg, err := simtest.Templates()
	if err != nil {
		t.Fatal(err)
	}
	return recovery.Options{
		Store:      w.fs,
		Listed:     w.owned,
		Boundaries: &memBoundaries{all: w.bounds},
		OpenRecords: func(_ context.Context, _ map[int32]int64) (sim.RecordSource, error) {
			return w.records, nil
		},
		Config:  sim.Config{Seed: 11, Partitions: simtest.AllPartitions(), Handlers: simtest.Handlers(reg)},
		Prepare: func(map[string]uint64) (sim.Topology, error) { return sim.Topology{World: topo, Templates: reg}, nil },
		Poll:    time.Millisecond,
		Metrics: recovery.NewMetrics(nil),
	}
}

func TestRecoversFromTheNewestRoundToTheHead(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 12, 5)
	e, rep, err := recovery.Recover(context.Background(), w.opts(t))
	if err != nil {
		t.Fatal(err)
	}
	if e.StateHash() != w.finalH || e.Tick() != w.finalTik || !rep.Match {
		t.Fatalf("recovered tick %d hash %x, want %d %x (match=%v)", e.Tick(), e.StateHash(), w.finalTik, w.finalH, rep.Match)
	}
	if rep.Round.Tick != 5 || rep.Replayed != 7 {
		t.Fatalf("round %d replayed %d, want 5 and 7", rep.Round.Tick, rep.Replayed)
	}
	for _, ph := range []string{recovery.PhaseLoad, recovery.PhaseSeek, recovery.PhaseReplay, recovery.PhaseVerify, recovery.PhaseTotal} {
		if _, ok := rep.Phases[ph]; !ok {
			t.Errorf("no %q phase in %v", ph, rep.Phases)
		}
	}
}

func TestReplayBatchDoesNotChangeTheHashes(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 12, 5)
	for _, batch := range []int{1, 3, 4096} {
		o := w.opts(t)
		o.ReplayBatch = batch
		var seen []sim.Tick
		var hashes [][32]byte
		o.After = func(r sim.StepResult) error {
			seen, hashes = append(seen, r.Tick), append(hashes, r.Completed.StateHash)
			return nil
		}
		e, _, err := recovery.Recover(context.Background(), o)
		if err != nil || e.StateHash() != w.finalH {
			t.Fatalf("batch %d: err %v", batch, err)
		}
		for i, tk := range seen {
			if hashes[i] != w.bounds[tk-1].StateHash {
				t.Fatalf("batch %d: tick %d hash differs from the log", batch, tk)
			}
		}
	}
}

func TestColdStartReplaysFromZero(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 8, 0)
	e, rep, err := recovery.Recover(context.Background(), w.opts(t))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Round.Tick != 0 || rep.Replayed != 8 || e.StateHash() != w.finalH {
		t.Fatalf("cold start: round %d replayed %d", rep.Round.Tick, rep.Replayed)
	}
}

func TestRequireSnapshotRefusesAColdStartWithExit7(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 8, 0)
	o := w.opts(t)
	o.RequireSnapshot = true
	_, _, err := recovery.Recover(context.Background(), o)
	if got := recovery.ExitCode(err); got != recovery.ExitRound {
		t.Fatalf("exit %d (%v), want %d", got, err, recovery.ExitRound)
	}
}

func TestAHashMismatchAtATailBoundaryIsExit8NamingTheTick(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 12, 5)
	w.bounds[8].StateHash[0] ^= 0xff // tick 9
	o := w.opts(t)
	reg := prometheus.NewRegistry()
	o.Metrics = recovery.NewMetrics(reg)
	if n := testutil.CollectAndCount(reg, "andara_recovery_state_hash_match"); n != 0 {
		t.Fatalf("the gauge has %d samples before recovery sets it, want 0", n)
	}
	_, rep, err := recovery.Recover(context.Background(), o)
	var hm *recovery.HashMismatchError
	if !errors.As(err, &hm) || hm.Tick != 9 || hm.Round != 5 {
		t.Fatalf("err %v, want a hash mismatch at tick 9 from round 5", err)
	}
	if recovery.ExitCode(err) != recovery.ExitHashMismatch || rep.Match || rep.MismatchTick != 9 {
		t.Fatalf("exit %d match %v tick %d", recovery.ExitCode(err), rep.Match, rep.MismatchTick)
	}
	if testutil.ToFloat64(o.Metrics.HashMatch()) != 0 {
		t.Fatal("andara_recovery_state_hash_match is not 0")
	}
	if v := testutil.ToFloat64(o.Metrics.Failures.WithLabelValues(recovery.ReasonHash)); v != 1 {
		t.Fatalf("failures{hash} = %v, want 1", v)
	}
}

func TestTheGaugeIsOneAfterASuccessfulRecovery(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 6, 3)
	reg := prometheus.NewRegistry()
	o := w.opts(t)
	o.Metrics = recovery.NewMetrics(reg)
	if _, _, err := recovery.Recover(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if n := testutil.CollectAndCount(reg, "andara_recovery_state_hash_match"); n != 1 || testutil.ToFloat64(o.Metrics.HashMatch()) != 1 {
		t.Fatalf("samples %d, want 1 sample at 1", n)
	}
}

func TestARestoreThatDoesNotReproduceItsTickIsExit6(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 8, 4)
	w.bounds[3].StateHash[0] ^= 0xff // the round's own tick
	o := w.opts(t)
	_, _, err := recovery.Recover(context.Background(), o)
	if got := recovery.ExitCode(err); got != recovery.ExitRestore {
		t.Fatalf("exit %d (%v), want 6", got, err)
	}
	if f := recovery.Classify(err); f.Restore != recovery.RestoreHash || f.Reason != recovery.ReasonRestore {
		t.Fatalf("failure %+v", f)
	}
	if v := testutil.ToFloat64(o.Metrics.Restores.WithLabelValues(recovery.CallerRecovery, "hash_mismatch")); v != 1 {
		t.Fatalf("restore_total{recovery,hash_mismatch} = %v", v)
	}
	if testutil.ToFloat64(o.Metrics.HashMatch()) != 0 {
		t.Fatal("hash match gauge not 0")
	}
}

func TestANamedRoundThatIsNotCompleteIsExit7AndNothingElseIsTried(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 8, 4)
	tick := sim.Tick(6) // no object at all
	o := w.opts(t)
	o.Round = &tick
	_, _, err := recovery.Recover(context.Background(), o)
	var ri *sim.ErrRoundIncomplete
	if !errors.As(err, &ri) || ri.Cause != sim.RoundMissing || len(ri.Zones) != len(w.owned) {
		t.Fatalf("err %v, want missing with every owned Zone", err)
	}
	if recovery.ExitCode(err) != recovery.ExitRound {
		t.Fatalf("exit %d", recovery.ExitCode(err))
	}
	if v := testutil.ToFloat64(o.Metrics.Failures.WithLabelValues(recovery.ReasonRound)); v != 1 {
		t.Fatalf("failures{round} = %v", v)
	}
	if o.Metrics.HashMatch() != nil {
		t.Fatal("exit 7 must not set the hash-match gauge")
	}
}

func TestALogThatNoLongerHasTheRoundIsExit3(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 8, 4)
	w.bounds = w.bounds[5:] // retention dropped ticks 1..5
	_, _, err := recovery.Recover(context.Background(), w.opts(t))
	if got := recovery.ExitCode(err); got != recovery.ExitLogGap {
		t.Fatalf("exit %d (%v), want 3", got, err)
	}
}

func TestExitCodeMapsEveryError(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		err  error
		want int
	}{
		{nil, 0},
		{errors.New("store down"), recovery.ExitConfig},
		{&tickloop.LogGapError{}, recovery.ExitLogGap},
		{&sim.ErrStateVersion{Have: 3, Want: 2}, recovery.ExitStateVersion},
		{&sim.RestoreMismatch{}, recovery.ExitRestore},
		{&sim.SeedMismatch{}, recovery.ExitRestore},
		{&sim.ContentDigestError{}, recovery.ExitRestore},
		{&sim.ErrRoundZoneUnknown{}, recovery.ExitRestore},
		{&sim.ErrRoundZoneDuplicate{}, recovery.ExitRound},
		{&sim.ErrRoundIncomplete{}, recovery.ExitRound},
		{&sim.HashMismatchError{}, recovery.ExitHashMismatch},
		{&recovery.HashMismatchError{}, recovery.ExitHashMismatch},
		{sim.ErrBoundaryGap, recovery.ExitLogGap},
	} {
		if got := recovery.ExitCode(c.err); got != c.want {
			t.Errorf("%T: exit %d, want %d", c.err, got, c.want)
		}
	}
}

// capped is a log whose end moves while it is read: HeadTick reports where it
// was when recovery asked, though boundaries past it are already there.
type capped struct {
	*memBoundaries
	head sim.Tick
}

func (c capped) HeadTick(context.Context) (sim.Tick, error) { return c.head, nil }

func TestReplayEndsAtTheHeadItStartedWith(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 12, 5)
	o := w.opts(t)
	o.Boundaries = capped{o.Boundaries.(*memBoundaries), 8}
	e, rep, err := recovery.Recover(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if e.Tick() != 8 || rep.Replayed != 3 || e.StateHash() != w.bounds[7].StateHash {
		t.Fatalf("tick %d replayed %d, want 8 and 3", e.Tick(), rep.Replayed)
	}
}

// A verify's restore is counted under caller=verify, boot's under recovery.
func TestRestoreIsCountedUnderItsCaller(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		verify bool
		caller string
		other  string
	}{{false, recovery.CallerRecovery, recovery.CallerVerify}, {true, recovery.CallerVerify, recovery.CallerRecovery}} {
		w := newWorld(t, 8, 4)
		o := w.opts(t)
		o.Verify = c.verify
		if _, _, err := recovery.Recover(context.Background(), o); err != nil {
			t.Fatal(err)
		}
		if testutil.ToFloat64(o.Metrics.Restores.WithLabelValues(c.caller, "ok")) != 1 || testutil.ToFloat64(o.Metrics.Restores.WithLabelValues(c.other, "ok")) != 0 {
			t.Errorf("verify=%v: restore_total not under %s", c.verify, c.caller)
		}
	}
}

type closeSource struct {
	simtest.MemorySource
	closed *bool
}

func (c closeSource) Close() { *c.closed = true }

// The Command source's client is closed when recovery ends, whichever Close
// signature it has (tickloop.CommandSource's returns nothing).
func TestTheCommandSourceIsClosed(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 6, 3)
	o := w.opts(t)
	closed := false
	o.OpenRecords = func(context.Context, map[int32]int64) (sim.RecordSource, error) {
		return closeSource{w.records, &closed}, nil
	}
	if _, _, err := recovery.Recover(context.Background(), o); err != nil || !closed {
		t.Fatalf("err %v, closed %v", err, closed)
	}
}

// A separate Restores instrument takes the count in place of Metrics' own.
func TestRestoresCanBeCountedElsewhere(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 6, 3)
	o := w.opts(t)
	live := recovery.NewMetrics(nil)
	o.Verify, o.Restores = true, live.Restores
	if _, _, err := recovery.Recover(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if testutil.ToFloat64(live.Restores.WithLabelValues(recovery.CallerVerify, "ok")) != 1 || testutil.ToFloat64(o.Metrics.Restores.WithLabelValues(recovery.CallerVerify, "ok")) != 0 {
		t.Fatal("the restore was not counted on the override alone")
	}
}
