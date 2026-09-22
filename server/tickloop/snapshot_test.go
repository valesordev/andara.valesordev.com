// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package tickloop_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	logv1 "github.com/valesordev/andara/gen/go/andara/log/v1"
	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/simtest"
	"github.com/valesordev/andara/server/store"
	"github.com/valesordev/andara/server/tickloop"
)

// failingStore wraps a store and fails Put for named Zones, so AC-5 and AC-6
// can be asserted without breaking a real filesystem.
type failingStore struct {
	inner sim.WorldStore
	mu    sync.Mutex
	fail  map[string]error
	delay time.Duration
	puts  int
}

func (f *failingStore) Put(ctx context.Context, key string, b []byte) error {
	f.mu.Lock()
	f.puts++
	var err error
	for zone, e := range f.fail {
		if len(key) >= len(zone) && key[:len(zone)] == zone {
			err = e
		}
	}
	delay := f.delay
	f.mu.Unlock()
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if err != nil {
		return err
	}
	return f.inner.Put(ctx, key, b)
}

func (f *failingStore) Get(ctx context.Context, key string) ([]byte, error) {
	return f.inner.Get(ctx, key)
}

func (f *failingStore) List(ctx context.Context, z sim.ZoneID) ([]string, error) {
	return f.inner.List(ctx, z)
}

type recordingManifest struct {
	mu      sync.Mutex
	records []*logv1.SnapshotWritten
	err     error
}

func (r *recordingManifest) ProduceSnapshots(_ context.Context, recs []*logv1.SnapshotWritten) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return r.err
	}
	r.records = append(r.records, recs...)
	return nil
}

func (r *recordingManifest) all() []*logv1.SnapshotWritten {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*logv1.SnapshotWritten(nil), r.records...)
}

// snapshotEngine is a three-Zone World with one Entity, advanced to tick 42 so
// the assertions have a boundary tick to name. Maybe reads the tick from the
// engine, which is what makes AC-8 structural.
func snapshotEngine(t *testing.T) *sim.Engine {
	t.Helper()
	e, err := simtest.NewEngine(1)
	if err != nil {
		t.Fatalf("simtest.NewEngine: %v", err)
	}
	simtest.Place(e, "hero", "town", "plaza")
	for e.Tick() < 42 {
		if _, err := e.Step(sim.TickInput{}); err != nil {
			t.Fatalf("Step: %v", err)
		}
	}
	return e
}

// roundHarness drives Maybe with a controllable clock and waits for the round.
type roundHarness struct {
	s    *tickloop.Snapshotter
	fs   *store.FS
	fail *failingStore
	man  *recordingManifest
	now  time.Time
	mu   sync.Mutex
	done chan error
}

func newRoundHarness(t *testing.T, mutate func(*tickloop.SnapshotOptions)) *roundHarness {
	t.Helper()
	fs := store.NewFS(t.TempDir())
	h := &roundHarness{
		fs:   fs,
		fail: &failingStore{inner: fs, fail: map[string]error{}},
		man:  &recordingManifest{},
		now:  time.Unix(1758500000, 0),
		done: make(chan error, 8),
	}
	o := tickloop.SnapshotOptions{
		Store:         h.fail,
		Interval:      60 * time.Second,
		MaxStall:      5 * time.Millisecond,
		UploadTimeout: 30 * time.Second,
		Manifest:      h.man,
		Now: func() time.Time {
			h.mu.Lock()
			defer h.mu.Unlock()
			return h.now
		},
		OnRound: func(_ sim.Tick, err error) { h.done <- err },
	}
	if mutate != nil {
		mutate(&o)
	}
	s, err := tickloop.NewSnapshotter(o)
	if err != nil {
		t.Fatalf("NewSnapshotter: %v", err)
	}
	h.s = s
	return h
}

func (h *roundHarness) advance(d time.Duration) {
	h.mu.Lock()
	h.now = h.now.Add(d)
	h.mu.Unlock()
}

func (h *roundHarness) await(t *testing.T) error {
	t.Helper()
	select {
	case err := <-h.done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("snapshot round did not finish")
		return nil
	}
}

// AC-8: the round starts on a boundary once the interval has elapsed, and the
// envelope's tick is the boundary's.
func TestRoundRunsOnTheBoundaryAfterTheInterval(t *testing.T) {
	t.Parallel()
	h := newRoundHarness(t, nil)
	e := snapshotEngine(t)

	// Before the interval: nothing.
	h.s.Maybe(context.Background(), e)
	select {
	case <-h.done:
		t.Fatal("a round ran before snapshot.interval elapsed")
	case <-time.After(50 * time.Millisecond):
	}

	h.advance(60 * time.Second)
	h.s.Maybe(context.Background(), e)
	if err := h.await(t); err != nil {
		t.Fatalf("round: %v", err)
	}

	keys, err := h.fs.List(context.Background(), "town")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("List = %v, want one object", keys)
	}
	b, err := h.fs.Get(context.Background(), keys[0])
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	env, zone, err := store.Decode(b)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if env.GetTick() != 42 {
		t.Errorf("envelope tick = %d, want the boundary's 42", env.GetTick())
	}
	if zone.ID != "town" {
		t.Errorf("zone = %q, want town", zone.ID)
	}
	if _, ok := zone.Entities["hero"]; !ok {
		t.Error("the snapshot does not carry the Entity that was in the Zone")
	}
}

// AC-7: every Zone's manifest names a key that resolves to an object whose
// envelope hash matches the record's.
func TestManifestResolvesToTheObjectItNames(t *testing.T) {
	t.Parallel()
	h := newRoundHarness(t, nil)
	h.advance(60 * time.Second)
	h.s.Maybe(context.Background(), snapshotEngine(t))
	if err := h.await(t); err != nil {
		t.Fatalf("round: %v", err)
	}

	recs := h.man.all()
	if len(recs) != len(simtest.Zones) {
		t.Fatalf("manifest has %d records, want one per Zone (%d)", len(recs), len(simtest.Zones))
	}
	for _, r := range recs {
		b, err := h.fs.Get(context.Background(), r.GetKey())
		if err != nil {
			t.Fatalf("zone %s: the key the manifest names does not resolve: %v", r.GetZoneId(), err)
		}
		env, zone, err := store.Decode(b)
		if err != nil {
			t.Fatalf("zone %s: Decode: %v", r.GetZoneId(), err)
		}
		if string(env.GetStateHash()) != string(r.GetStateHash()) {
			t.Errorf("zone %s: manifest hash %x, envelope hash %x", r.GetZoneId(), r.GetStateHash(), env.GetStateHash())
		}
		if got := sim.HashZone(zone); string(got[:]) != string(r.GetStateHash()) {
			t.Errorf("zone %s: the object's Zone does not hash to what the manifest claims", r.GetZoneId())
		}
		if r.GetTick() != 42 || r.GetStateVersion() != sim.StateVersion {
			t.Errorf("zone %s: tick %d, state_version %d", r.GetZoneId(), r.GetTick(), r.GetStateVersion())
		}
		if r.GetSizeBytes() != uint64(len(b)) {
			t.Errorf("zone %s: size_bytes %d, object is %d bytes", r.GetZoneId(), r.GetSizeBytes(), len(b))
		}
	}
}

// AC-5 and AC-6: one Zone's write fails, the round is reported incomplete and
// counted, and the Zones that succeeded are left alone.
func TestOneZoneFailingMakesTheRoundIncomplete(t *testing.T) {
	t.Parallel()
	h := newRoundHarness(t, nil)
	h.fail.fail["docks"] = fmt.Errorf("%w: disk on fire", sim.ErrStoreUnavailable)
	h.advance(60 * time.Second)
	h.s.Maybe(context.Background(), snapshotEngine(t))

	err := h.await(t)
	if err == nil {
		t.Fatal("a round with a failed Zone reported success")
	}
	var incomplete *sim.ErrRoundIncomplete
	if !errors.As(err, &incomplete) {
		t.Fatalf("round error is %T (%v), want *sim.ErrRoundIncomplete", err, err)
	}
	if len(incomplete.Missing) != 1 || incomplete.Missing[0] != "docks" {
		t.Errorf("Missing = %v, want [docks]", incomplete.Missing)
	}
	if incomplete.Tick != 42 {
		t.Errorf("Tick = %d, want 42", incomplete.Tick)
	}

	// The Zones that succeeded are still there: "not selectable" is a
	// property of the round, decided by AW-SRV-007 at list time.
	for _, zone := range []sim.ZoneID{"town", "wilds"} {
		keys, err := h.fs.List(context.Background(), zone)
		if err != nil || len(keys) != 1 {
			t.Errorf("zone %s: List = %v (err %v), want the object that succeeded", zone, keys, err)
		}
	}
	if keys, _ := h.fs.List(context.Background(), "docks"); len(keys) != 0 {
		t.Errorf("the failed Zone left an object behind: %v", keys)
	}
	// And no manifest for an incomplete round.
	if recs := h.man.all(); len(recs) != 0 {
		t.Errorf("an incomplete round produced %d manifest records", len(recs))
	}
	assertCounter(t, h.s, "incomplete", 1)
	assertFailure(t, h.s, "store", 1)
}

// A round still running when the next interval elapses does not get a second
// one started behind it.
func TestRoundsAreSingleFlight(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	h := newRoundHarness(t, nil)
	h.fail.inner = blockingStore{inner: h.fs, release: release}
	e := snapshotEngine(t)

	h.advance(60 * time.Second)
	h.s.Maybe(context.Background(), e)

	// Second interval elapses while the first round is blocked in Put.
	h.advance(60 * time.Second)
	h.s.Maybe(context.Background(), e)

	close(release)
	if err := h.await(t); err != nil {
		t.Fatalf("first round: %v", err)
	}
	select {
	case <-h.done:
		t.Fatal("a second round started while the first was in flight")
	case <-time.After(100 * time.Millisecond):
	}
	assertCounter(t, h.s, "complete", 1)
}

// A round that cannot finish inside snapshot.upload_timeout is failed with
// reason=timeout, not queued behind the next one.
func TestRoundPastItsUploadTimeoutIsFailed(t *testing.T) {
	t.Parallel()
	h := newRoundHarness(t, func(o *tickloop.SnapshotOptions) {
		o.UploadTimeout = 20 * time.Millisecond
	})
	h.fail.delay = 2 * time.Second
	h.advance(60 * time.Second)
	h.s.Maybe(context.Background(), snapshotEngine(t))

	err := h.await(t)
	if err == nil {
		t.Fatal("a round past its upload timeout reported success")
	}
	var incomplete *sim.ErrRoundIncomplete
	if !errors.As(err, &incomplete) {
		t.Fatalf("round error is %T (%v), want *sim.ErrRoundIncomplete", err, err)
	}
	// Once per Zone whose write timed out, which is all of them: the
	// deadline is the round's, and every Put in it is past it.
	assertFailure(t, h.s, "timeout", float64(len(simtest.Zones)))
}

// The stall is a warning, not a refusal: over budget, the round still
// completes and the failure is counted under reason=stall.
func TestAStallOverBudgetWarnsButCompletesTheRound(t *testing.T) {
	t.Parallel()
	h := newRoundHarness(t, func(o *tickloop.SnapshotOptions) {
		// Any real copy exceeds a zero budget.
		o.MaxStall = 0
	})
	h.advance(60 * time.Second)
	h.s.Maybe(context.Background(), snapshotEngine(t))
	if err := h.await(t); err != nil {
		t.Fatalf("a stall failed the round: %v", err)
	}
	assertFailure(t, h.s, "stall", 1)
	assertCounter(t, h.s, "complete", 1)
}

// A manifest that cannot be produced does not fail the round: nothing in the
// recovery path reads it.
func TestAFailedManifestDoesNotFailTheRound(t *testing.T) {
	t.Parallel()
	h := newRoundHarness(t, nil)
	h.man.err = errors.New("broker unreachable")
	h.advance(60 * time.Second)
	h.s.Maybe(context.Background(), snapshotEngine(t))
	if err := h.await(t); err != nil {
		t.Fatalf("a failed manifest failed the round: %v", err)
	}
	assertCounter(t, h.s, "complete", 1)
}

// snapshot.interval of zero disables snapshots and needs no store.
func TestZeroIntervalDisablesSnapshots(t *testing.T) {
	t.Parallel()
	s, err := tickloop.NewSnapshotter(tickloop.SnapshotOptions{})
	if err != nil {
		t.Fatalf("NewSnapshotter: %v", err)
	}
	if s.Enabled() {
		t.Fatal("a zero interval reported Enabled")
	}
	s.Maybe(context.Background(), snapshotEngine(t)) // must not panic
	s.Wait()
}

// A nil Snapshotter is what the loop holds when snapshots are off, so every
// method must tolerate it.
func TestNilSnapshotterIsInert(t *testing.T) {
	t.Parallel()
	var s *tickloop.Snapshotter
	if s.Enabled() {
		t.Fatal("a nil Snapshotter reported Enabled")
	}
	s.Maybe(context.Background(), snapshotEngine(t))
	s.Wait()
}

func TestSnapshotterRefusesAnIntervalWithNoStore(t *testing.T) {
	t.Parallel()
	if _, err := tickloop.NewSnapshotter(tickloop.SnapshotOptions{Interval: time.Second, UploadTimeout: time.Second}); err == nil {
		t.Fatal("NewSnapshotter accepted an interval with no store")
	}
}

type blockingStore struct {
	inner   sim.WorldStore
	release chan struct{}
}

func (b blockingStore) Put(ctx context.Context, key string, v []byte) error {
	select {
	case <-b.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	return b.inner.Put(ctx, key, v)
}
func (b blockingStore) Get(ctx context.Context, k string) ([]byte, error) { return b.inner.Get(ctx, k) }
func (b blockingStore) List(ctx context.Context, z sim.ZoneID) ([]string, error) {
	return b.inner.List(ctx, z)
}

// assertCounter reads andara_snapshot_rounds_total{outcome}.
func assertCounter(t *testing.T, s *tickloop.Snapshotter, outcome string, want float64) {
	t.Helper()
	if got := counterValue(t, s.Metrics().Rounds, "outcome", outcome); got != want {
		t.Errorf("andara_snapshot_rounds_total{outcome=%q} = %v, want %v", outcome, got, want)
	}
}

// assertFailure reads andara_snapshot_failures_total{reason}.
func assertFailure(t *testing.T, s *tickloop.Snapshotter, reason string, want float64) {
	t.Helper()
	if got := counterValue(t, s.Metrics().Failures, "reason", reason); got != want {
		t.Errorf("andara_snapshot_failures_total{reason=%q} = %v, want %v", reason, got, want)
	}
}

func counterValue(t *testing.T, v *prometheus.CounterVec, label, value string) float64 {
	t.Helper()
	c, err := v.GetMetricWith(prometheus.Labels{label: value})
	if err != nil {
		t.Fatal(err)
	}
	var m dto.Metric
	if err := c.(prometheus.Metric).Write(&m); err != nil {
		t.Fatal(err)
	}
	return m.GetCounter().GetValue()
}
