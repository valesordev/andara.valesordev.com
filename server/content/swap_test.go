// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package content

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	logv1 "github.com/valesordev/andara/gen/go/andara/log/v1"
	"github.com/valesordev/andara/server/sim"
)

// harnessLoader is testLoader with its engine harness in hand.
func harnessLoader(t *testing.T, s Store, now func() time.Time, packs ...string) (*Loader, *Metrics, *engineHarness) {
	t.Helper()
	m := NewMetrics(nil)
	l := NewLoader(LoaderOptions{Store: s, Packs: packs, Metrics: m, Now: now})
	return l, m, attachEngine(l)
}

func fallbackless(id, room string) string {
	return `{"formatVersion":1,"id":"` + id + `","name":"Zone","rooms":[{"id":"` + room + `","title":"T","description":"d"}]}`
}

// Serving means applied (AW-SRV-012): a version the Loader accepted is not
// serving until the Engine applies the ContentSwap it produced, and the swap
// carries the digest of the whole World it builds.
func TestLoader_ServingMeansApplied(t *testing.T) {
	s := newFakeStore()
	s.publish("andara.core", 1, 0, map[string]string{"core.json": zoneJSON("core", "void")})
	l := NewLoader(LoaderOptions{Store: s, Packs: []string{"andara.core"}})
	var produced []*logv1.LoggedCommand
	var mu sync.Mutex
	l.SetProducer(producerFunc(func(cmd *logv1.LoggedCommand) error {
		mu.Lock()
		produced = append(produced, cmd)
		mu.Unlock()
		return nil
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	// Nothing applies the swap, so the load waits until its context ends.
	if rejects, err := l.LoadAll(ctx); err != nil || len(rejects) != 0 {
		t.Fatalf("rejects %v err %v", rejects, err)
	}
	if v := l.Versions(); len(v) != 0 {
		t.Fatalf("serving %v before any swap applied", v)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(produced) != 1 || produced[0].GetZoneId() != "" || sim.CommandPartition(produced[0]) != sim.WorldPartition {
		t.Fatalf("produced %v; want one World-scoped swap for Partition 0", produced)
	}
	cs := produced[0].GetContentSwap()
	if cs.GetPackId() != "andara.core" || cs.GetVersion() != 1 || len(cs.GetWorldDigest()) != 32 {
		t.Fatalf("swap %v", cs)
	}
	// What the Engine would prepare is the staged build, and it digests to
	// what the swap says.
	topo, err := l.Prepare(map[string]uint64{}, cs)
	if err != nil {
		t.Fatal(err)
	}
	if d := sim.ContentDigest(topo); string(d[:]) != string(cs.GetWorldDigest()) {
		t.Fatal("the staged topology does not digest to the swap's world_digest")
	}
}

type producerFunc func(*logv1.LoggedCommand) error

func (f producerFunc) ProduceSwap(_ context.Context, cmd *logv1.LoggedCommand) error { return f(cmd) }

// Replay: a swap this process did not stage is rebuilt from the store, against
// exactly the versions in effect it is handed, and a version that no longer
// resolves is an error the Engine refuses the tick on.
func TestLoader_PrepareRebuildsAnUnstagedSwapFromTheStore(t *testing.T) {
	s := newFakeStore()
	s.publish("andara.core", 1, 0, map[string]string{"core.json": zoneJSON("core", "void")})
	s.publish("town", 7, 1, map[string]string{"town.json": zoneJSON("town", "square")})
	fresh := NewLoader(LoaderOptions{Store: s, Packs: []string{AllPacks}})
	topo, err := fresh.Prepare(map[string]uint64{"andara.core": 1}, &logv1.ContentSwap{PackId: "town", Version: 7})
	if err != nil {
		t.Fatal(err)
	}
	if len(topo.World.Zones) != 2 || topo.World.Zones["town"] == nil || topo.World.Zones["core"] == nil {
		t.Fatalf("zones %v", topo.World.Zones)
	}
	if _, err := fresh.Prepare(nil, &logv1.ContentSwap{PackId: "town", Version: 99}); err == nil {
		t.Fatal("a version the store lacks prepared")
	}
}

// AC-10 through the Loader: a version whose Zones lack a fallback is refused
// with reason fallback_missing, not validation, and the previous version keeps
// serving.
func TestLoader_FallbackMissingHasItsOwnReason(t *testing.T) {
	s := newFakeStore()
	s.publish("andara.core", 1, 0, map[string]string{"core.json": zoneJSON("core", "void")})
	l, m := testLoader(t, s, "andara.core")
	if rejects, err := l.LoadAll(context.Background()); err != nil || len(rejects) != 0 {
		t.Fatalf("boot: %v %v", rejects, err)
	}
	s.publish("andara.core", 2, 0, map[string]string{"core.json": fallbackless("core", "void")})
	rejects := l.Apply(context.Background(), PointerMove{Pack: "andara.core", Version: 2})
	if len(rejects) != 1 || rejects[0].Reason != ReasonFallbackRoom {
		t.Fatalf("rejects %+v", rejects)
	}
	var fm *ErrFallbackMissing
	if !errors.As(rejects[0].Err, &fm) || fm.Zone != "core" {
		t.Fatalf("err %v", rejects[0].Err)
	}
	if got := serving(t, l, "andara.core"); got != 1 {
		t.Fatalf("serving %d", got)
	}
	if n := testutil.ToFloat64(m.LoadFailures.WithLabelValues(ReasonFallbackRoom)); n != 1 {
		t.Errorf("load_failures{fallback_missing} = %v", n)
	}
}

// §9b: a core rollback that would strand serving packs is refused naming every
// one of them.
func TestLoader_CoreRollbackNamesEveryPackHoldingIt(t *testing.T) {
	s := newFakeStore()
	s.publish("andara.core", 3, 0, map[string]string{"core.json": zoneJSON("core", "void")})
	s.publish("andara.core", 4, 0, map[string]string{"core.json": zoneJSON("core", "void")})
	s.publish("abbey", 1, 4, map[string]string{"abbey.json": zoneJSON("abbey", "nave")})
	s.publish("town", 1, 4, map[string]string{"town.json": zoneJSON("town", "square")})
	l, m := testLoader(t, s, AllPacks)
	if rejects, err := l.LoadAll(context.Background()); err != nil || len(rejects) != 0 {
		t.Fatalf("boot: %v %v", rejects, err)
	}
	rejects := l.Apply(context.Background(), PointerMove{Pack: "andara.core", Version: 3})
	var cr *ErrCoreRollback
	if len(rejects) != 1 || !errors.As(rejects[0].Err, &cr) || len(cr.Holding) != 2 || cr.Holding[0].Pack != "abbey" || cr.Holding[1].Pack != "town" {
		t.Fatalf("rejects %+v", rejects)
	}
	if msg := rejects[0].Err.Error(); !strings.Contains(msg, "abbey (andara.core@4)") || !strings.Contains(msg, "town (andara.core@4)") {
		t.Errorf("message %q does not name both packs", msg)
	}
	if n := testutil.ToFloat64(m.LoadFailures.WithLabelValues(ReasonCoreVersion)); n != 1 {
		t.Errorf("core_version failures = %v", n)
	}
}

// A swap that cannot be written to the log is store_unavailable — the
// platform, not the Builder — and nothing is serving because of it.
func TestLoader_AProduceFailureIsAStoreFault(t *testing.T) {
	s := newFakeStore()
	s.publish("andara.core", 1, 0, map[string]string{"core.json": zoneJSON("core", "void")})
	l, _, h := harnessLoader(t, s, nil, "andara.core")
	h.fail = errors.New("broker unreachable")
	rejects, err := l.LoadAll(context.Background())
	if err != nil || len(rejects) != 1 || rejects[0].Reason != ReasonStoreUnavailable {
		t.Fatalf("rejects %+v err %v", rejects, err)
	}
	var pe *ErrProduce
	if !errors.As(rejects[0].Err, &pe) {
		t.Fatalf("err %v", rejects[0].Err)
	}
	if v := l.Versions(); len(v) != 0 {
		t.Fatalf("serving %v", v)
	}
}

// §5: a load the store could not serve is retried with backoff until it
// loads, without the pointer moving again.
func TestLoader_FollowRetriesAStoreFault(t *testing.T) {
	prevI, prevM := retryInitial, retryMax
	retryInitial, retryMax = 10*time.Millisecond, 40*time.Millisecond
	t.Cleanup(func() { retryInitial, retryMax = prevI, prevM })

	s := newFakeStore()
	s.publish("andara.core", 1, 0, map[string]string{"core.json": zoneJSON("core", "void")})
	l, m, h := harnessLoader(t, s, nil, "andara.core")
	if _, err := l.LoadAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.publish("andara.core", 2, 0, map[string]string{"core.json": zoneJSON("core", "void")})
	h.mu.Lock()
	h.fail = errors.New("broker unreachable")
	h.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := &fakeWatcher{ch: make(chan PointerMove, 1)}
	done := make(chan struct{})
	go func() { defer close(done); _ = l.Follow(ctx, w, 5*time.Millisecond, nil) }()
	w.ch <- PointerMove{Pack: "andara.core", Version: 2}

	waitUntil(t, func() bool { return testutil.ToFloat64(m.LoadFailures.WithLabelValues(ReasonStoreUnavailable)) >= 2 }, "two failed attempts")
	h.mu.Lock()
	h.fail = nil
	h.mu.Unlock()
	waitUntil(t, func() bool { return l.Versions()["andara.core"] == 2 }, "the retry to load core@2")
	cancel()
	<-done
}

// andara_content_pending_seconds: counts from the pointer move while the
// version is neither in effect nor refused for a Builder's reason; a Builder's
// refusal is not pending; a platform fault keeps counting; the version coming
// into effect stops it.
func TestLoader_PendingSeconds(t *testing.T) {
	var mu sync.Mutex
	now := time.Unix(1000, 0)
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	advance := func(d time.Duration) { mu.Lock(); now = now.Add(d); mu.Unlock() }

	s := newFakeStore()
	s.publish("andara.core", 1, 0, map[string]string{"core.json": zoneJSON("core", "void")})
	l, m, h := harnessLoader(t, s, clock, "andara.core")
	reg := prometheus.NewRegistry()
	reg.MustRegister(pendingCollector{m})
	pending := func() float64 {
		t.Helper()
		return testutil.ToFloat64(pendingGauge{reg})
	}
	if _, err := l.LoadAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if p := pending(); p != 0 {
		t.Fatalf("pending %v with the pointer in effect", p)
	}

	// A Builder's mistake: refused, not pending.
	s.publish("andara.core", 2, 0, map[string]string{"core.json": fallbackless("core", "void")})
	l.Apply(context.Background(), PointerMove{Pack: "andara.core", Version: 2})
	advance(time.Minute)
	if p := pending(); p != 0 {
		t.Fatalf("pending %v for a version refused for fallback_missing", p)
	}

	// The platform failing to write the swap: pending, and a newer move keeps
	// the older start.
	s.publish("andara.core", 3, 0, map[string]string{"core.json": zoneJSON("core", "void")})
	h.mu.Lock()
	h.fail = errors.New("broker unreachable")
	h.mu.Unlock()
	l.Apply(context.Background(), PointerMove{Pack: "andara.core", Version: 3})
	advance(30 * time.Second)
	s.publish("andara.core", 4, 0, map[string]string{"core.json": zoneJSON("core", "void")})
	l.Apply(context.Background(), PointerMove{Pack: "andara.core", Version: 4})
	advance(30 * time.Second)
	if p := pending(); p != 60 {
		t.Fatalf("pending %v, want 60 counted from the first unserved move", p)
	}

	h.mu.Lock()
	h.fail = nil
	h.mu.Unlock()
	l.Apply(context.Background(), PointerMove{Pack: "andara.core", Version: 4})
	if p := pending(); p != 0 {
		t.Fatalf("pending %v once core@4 is in effect", p)
	}
}

// pendingGauge collects the one andara_content_pending_seconds series.
type pendingGauge struct{ reg *prometheus.Registry }

func (g pendingGauge) Describe(chan<- *prometheus.Desc) {}
func (g pendingGauge) Collect(ch chan<- prometheus.Metric) {
	mfs, _ := g.reg.Gather()
	for _, mf := range mfs {
		for _, mm := range mf.GetMetric() {
			ch <- prometheus.MustNewConstMetric(prometheus.NewDesc(mf.GetName(), "", nil, nil), prometheus.GaugeValue, mm.GetGauge().GetValue())
		}
	}
}

func waitUntil(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
