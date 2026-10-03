// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package boot

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/valesordev/andara/server/sim"
)

// #287: content is never reported in effect ahead of its gauges. When
// ReconcileContent returns, contentApplied has already set
// andara_content_rooms_loaded for every Zone in effect.
//
// One read, deliberately (live-assertions.md rule 2's exception): the claim
// is an ordering inside contentApplied — the gauges are set on the loop's
// goroutine before Content.Applied, which is what lets ReconcileContent
// return — so a poll would let the broken order pass.
//
// LoadContent sets the same series for the World it validated, so they are
// cleared first: otherwise they are already there when ReconcileContent
// returns, whatever the order (pre-PR review of #287). Shown by widening
// the window: Applied first, then a 50 ms sleep, then the gauges, fails
// this every run.
func TestReconcileContent_GaugesBeforeInEffect(t *testing.T) {
	rt, logs := runtime(t, fixture(t, "valid"), false)
	if code := rt.LoadContent(context.Background()); code != ExitOK {
		t.Fatalf("load: %s", logs.String())
	}
	rt.Tel.Metrics.RoomsLoaded.Reset()
	rt.roomsLabeled = nil
	// At the moment the content source is told, on the loop's goroutine:
	// a plain reorder fails this with no window to widen.
	// Each call records the series and the Zones in effect: the World is
	// the loop goroutine's to read, and this runs on it.
	var atApplied [][2]int
	rt.beforeApplied = func() {
		atApplied = append(atApplied, [2]int{
			testutil.CollectAndCount(rt.Tel.Metrics.RoomsLoaded, "andara_content_rooms_loaded"),
			len(rt.Engine.World().Zones),
		})
	}
	serveMemory(t, rt)
	n := testutil.CollectAndCount(rt.Tel.Metrics.RoomsLoaded, "andara_content_rooms_loaded")
	if n != len(rt.Engine.World().Zones) || n == 0 {
		t.Fatalf("rooms_loaded has %d series when ReconcileContent returns; %d Zones are in effect", n, len(rt.Engine.World().Zones))
	}
	// Read after ReconcileContent returned, which Applied's report ordered.
	if len(atApplied) == 0 {
		t.Fatal("the content source was never told")
	}
	for i, got := range atApplied {
		if got[0] != got[1] {
			t.Fatalf("swap %d: %d rooms_loaded series when the content source was told, with %d Zones in effect", i, got[0], got[1])
		}
	}
}

// #287: a scrape never sees the rooms_loaded family empty while the Zones in
// effect change. A Zone still in effect keeps its series throughout, and a
// departed Zone's series is gone once the update returns.
//
// Shown by widening the window: with the old Reset-then-refill and a sleep
// between them, the scraper sees an empty family every run.
func TestSetTopologyGauges_NeverEmpty(t *testing.T) {
	rt, _ := runtime(t, t.TempDir(), false)
	world := func(zones ...sim.ZoneID) *sim.World {
		w := &sim.World{Zones: map[sim.ZoneID]*sim.Zone{}}
		for _, z := range zones {
			w.Zones[z] = &sim.Zone{ID: z, Rooms: map[sim.RoomID]*sim.Room{"a": {ID: "a"}}}
		}
		return w
	}
	a, b := world("town", "wilds"), world("wilds", "docks")
	rt.setTopologyGauges(a)

	var stop atomic.Bool
	var wg sync.WaitGroup
	var scrapes atomic.Int64
	failure := make(chan string, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for !stop.Load() {
			scrapes.Add(1)
			fams, err := rt.Tel.Reg.Gather()
			if err != nil {
				failure <- err.Error()
				return
			}
			wilds := false
			for _, f := range fams {
				if f.GetName() != "andara_content_rooms_loaded" {
					continue
				}
				for _, m := range f.GetMetric() {
					for _, l := range m.GetLabel() {
						if l.GetName() == "zone" && l.GetValue() == "wilds" {
							wilds = true
						}
					}
				}
			}
			if !wilds {
				select {
				case failure <- fmt.Sprintf("scrape %d: no rooms_loaded{zone=wilds}, which is in effect throughout", scrapes.Load()):
				default:
				}
				return
			}
		}
	}()
	// Update until the scraper has looked at least 50 times, so a loaded
	// runner can't finish every update before it has run at all.
	deadline := time.Now().Add(10 * time.Second)
	for i := 0; scrapes.Load() < 50 && time.Now().Before(deadline); i++ {
		w := a
		if i%2 == 0 {
			w = b
		}
		rt.setTopologyGauges(w)
	}
	rt.setTopologyGauges(a)
	stop.Store(true)
	wg.Wait()
	select {
	case f := <-failure:
		t.Fatal(f)
	default:
	}
	if n := scrapes.Load(); n < 50 {
		t.Fatalf("%d scrapes in 10 s, want 50", n)
	}
	// The last update was a: docks departed.
	if n := testutil.CollectAndCount(rt.Tel.Metrics.RoomsLoaded, "andara_content_rooms_loaded"); n != 2 {
		t.Fatalf("%d series after moving to town and wilds, want 2", n)
	}
}
