// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package tickloop

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/valesordev/andara/server/sim"
)

// wedgeHarness runs a loop on the stepped clock whose BeforeTick blocks at
// the wedge-th tick until release is closed: a World frozen mid-schedule.
type wedgeHarness struct {
	*harness
	reg     *prometheus.Registry
	entered chan struct{}
	release chan struct{}
	cancel  context.CancelFunc
	done    chan error
	// ignoreCtx makes the wedged tick deaf to the loop's context, as a real
	// stuck handler is: only release ends it.
	ignoreCtx bool
}

func newWedgeHarness(t *testing.T, wedge int) *wedgeHarness {
	t.Helper()
	w := &wedgeHarness{reg: prometheus.NewRegistry(), entered: make(chan struct{}), release: make(chan struct{}), done: make(chan error, 1)}
	var calls atomic.Int64
	w.harness = newHarness(t, func(o *Options) {
		o.Registry = w.reg
		o.BeforeTick = func(ctx context.Context, _ time.Time) {
			if calls.Add(1) != int64(wedge) {
				return
			}
			close(w.entered)
			if w.ignoreCtx {
				<-w.release
				return
			}
			select {
			case <-w.release:
			case <-ctx.Done():
			}
		}
	})
	return w
}

func (w *wedgeHarness) start() {
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	go func() { w.done <- w.loop.Run(ctx) }()
	select {
	case <-w.entered:
	case <-time.After(10 * time.Second):
		w.t.Fatal("the loop never reached the wedged tick")
	}
	w.t.Cleanup(func() {
		w.cancel()
		<-w.done
	})
}

// lag is andara_simulation_lag_seconds as a scrape reads it.
func (w *wedgeHarness) lag() float64 {
	w.t.Helper()
	return gatherLag(w.t, w.reg)
}

func gatherLag(t *testing.T, reg *prometheus.Registry) float64 {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() == "andara_simulation_lag_seconds" {
			return mf.GetMetric()[0].GetGauge().GetValue()
		}
	}
	t.Fatal("andara_simulation_lag_seconds not gathered")
	return 0
}

func (w *wedgeHarness) count(msg string) int {
	return strings.Count(w.logs.String(), `"msg":"`+msg+`"`)
}

// AC-4: before the loop runs, it is never wedged, whatever the clock says,
// and the lag reads what was set.
func TestLiveness_NotStartedIsNeverWedged(t *testing.T) {
	w := newWedgeHarness(t, 1)
	w.clock.Advance(time.Hour)
	if w.loop.Wedged() {
		t.Fatal("a loop that has not begun is wedged")
	}
	if got := w.lag(); got != 0 {
		t.Errorf("lag before start = %v, want the set value 0", got)
	}
}

// AC-1, AC-2, AC-3, AC-9: the wedge is found past tick_budget × 100 and not
// before, logged once, seen as lag rising at scrape, and ended by the next
// completed tick with one resumed line carrying how long the World stood still.
func TestLiveness_WedgeAndResume(t *testing.T) {
	w := newWedgeHarness(t, 5)
	w.start()
	if w.loop.Wedged() {
		t.Fatal("wedged at the tick it blocked on")
	}
	// The harness's budget is 50 ms: the threshold is 5 s since the last
	// completed tick, which was at most one interval ago.
	w.clock.Advance(4800 * time.Millisecond)
	if w.loop.Wedged() {
		t.Fatal("wedged before tick_budget × 100")
	}
	w.clock.Advance(300 * time.Millisecond)
	for range 3 {
		if !w.loop.Wedged() {
			t.Fatal("not wedged past tick_budget × 100")
		}
	}
	if n := w.count("tick loop wedged"); n != 1 {
		t.Fatalf("%d wedged lines for one wedge, want 1", n)
	}
	line := findLog(t, w.logs, "tick loop wedged")
	if line["level"] != "WARN" || line["last_tick"] != float64(4) || line["since_seconds"].(float64) <= 5 {
		t.Errorf("wedged line %v, want warn, last_tick 4, since_seconds > 5", line)
	}

	first := w.lag()
	if first <= 0.5 {
		t.Errorf("lag while wedged = %v, want above 0.5", first)
	}
	w.clock.Advance(time.Second)
	if next := w.lag(); next <= first {
		t.Errorf("lag at the next scrape = %v, want above %v", next, first)
	}

	close(w.release)
	deadline := time.Now().Add(10 * time.Second)
	for w.count("tick loop resumed") == 0 {
		if time.Now().After(deadline) {
			t.Fatal("no resumed line after the tick completed")
		}
		time.Sleep(time.Millisecond)
	}
	if w.loop.Wedged() {
		t.Error("still wedged after a tick completed")
	}
	resumed := findLog(t, w.logs, "tick loop resumed")
	if resumed["level"] != "INFO" || resumed["last_tick"] != float64(5) || resumed["wedged_seconds"].(float64) < 6 {
		t.Errorf("resumed line %v, want info, last_tick 5, wedged_seconds ≥ 6", resumed)
	}
	if n := w.count("tick loop resumed"); n != 1 {
		t.Errorf("%d resumed lines, want 1", n)
	}
}

// AC-5, AC-9: a loop the boot has told it's draining is never wedged, and its
// lag reads the set value.
func TestLiveness_DrainingIsNeverWedged(t *testing.T) {
	w := newWedgeHarness(t, 5)
	w.start()
	w.clock.Advance(time.Minute)
	set := gaugeValue(w.loop.Metrics().Lag)
	w.loop.Draining()
	if w.loop.Wedged() {
		t.Fatal("a draining loop is wedged")
	}
	if got := w.lag(); got != set {
		t.Errorf("lag while draining = %v, want the set value %v", got, set)
	}
	if n := w.count("tick loop wedged"); n != 0 {
		t.Errorf("%d wedged lines while draining", n)
	}
}

// AC-5: once the loop's context ends, the drain waiting on a tick that won't
// finish is not a wedge, though no tick completes and nothing reaches stop.
func TestLiveness_StoppingIsNeverWedged(t *testing.T) {
	w := newWedgeHarness(t, 5)
	w.ignoreCtx = true
	w.start()
	w.cancel()
	deadline := time.Now().Add(5 * time.Second)
	for !w.loop.stopping.Load() {
		if time.Now().After(deadline) {
			t.Fatal("Run never saw its context end")
		}
		time.Sleep(time.Millisecond)
	}
	w.clock.Advance(time.Minute)
	wedged := w.loop.Wedged()
	close(w.release)
	<-w.done
	w.done <- nil // for the cleanup
	if wedged {
		t.Fatal("wedged while the drain waits on a stuck tick")
	}
}

// AC-6: a loop whose broker is unreachable still completes ticks, starved, and
// is never wedged: liveness never depends on Kafka.
func TestLiveness_StarvedTicksAreLive(t *testing.T) {
	var wedged atomic.Bool
	var h *harness
	h = newHarness(t, func(o *Options) {
		o.Source = lostPasswordSource{NewMemorySource()}
		o.OnTick = func(sim.StepResult, time.Duration) {
			if h.loop.Wedged() {
				wedged.Store(true)
			}
		}
	})
	if err := h.runFor(30 * time.Second); err != nil {
		t.Fatal(err)
	}
	if counter(h.loop.Metrics().InputStarved) == 0 {
		t.Fatal("no starved tick: the broker was reachable")
	}
	if wedged.Load() {
		t.Error("a loop ticking starved was found wedged")
	}
}

// AC-9: asleep between ticks, after a tick that took 20 ms of its budget, the
// next tick isn't due yet, so the lag at scrape is the loop's own value.
func TestLiveness_LagBetweenTicksIsTheSetValue(t *testing.T) {
	reg := prometheus.NewRegistry()
	var h *harness
	h = newHarness(t, func(o *Options) {
		o.Registry = reg
		o.OnTick = func(sim.StepResult, time.Duration) { h.clock.Advance(20 * time.Millisecond) }
	})
	asleep, hold := make(chan struct{}), make(chan struct{})
	var sleeps atomic.Int64
	h.clock.OnSleep = func(time.Time) bool {
		if sleeps.Add(1) == 3 {
			close(asleep)
			<-hold
		}
		return true
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.loop.Run(ctx) }()
	select {
	case <-asleep:
	case <-time.After(10 * time.Second):
		t.Fatal("the loop never slept")
	}
	got, set := gatherLag(t, reg), gaugeValue(h.loop.Metrics().Lag)
	cancel()
	close(hold)
	<-done
	if got != set {
		t.Errorf("lag asleep between ticks = %v, want the set value %v", got, set)
	}
}
