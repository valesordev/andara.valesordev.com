// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package recovery_test

import (
	"context"
	"testing"
	"time"

	"github.com/valesordev/andara/server/recovery"
)

// stamp gives each boundary a CreateTime one second apart, the head's last.
func stamp(w *world) time.Time {
	base := time.Unix(1_760_000_000, 0)
	for i := range w.bounds {
		w.bounds[i].ProducedAt = base.Add(time.Duration(i) * time.Second)
	}
	return w.bounds[len(w.bounds)-1].ProducedAt
}

// AW-SRV-054 AC-8: Report.HeadProducedAt is the head Tick Boundary Record's
// CreateTime, whether replay reached the head, the restored round already
// was the head, or there was no round at all.
func TestHeadProducedAtIsTheHeadBoundarysTime(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name         string
		ticks, round int
	}{
		{"replayed to the head", 12, 5},
		{"round at the head", 12, 12},
		{"cold start", 8, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			w := newWorld(t, c.ticks, c.round)
			head := stamp(w)
			_, rep, err := recovery.Recover(context.Background(), w.opts(t))
			if err != nil {
				t.Fatal(err)
			}
			if !rep.HeadProducedAt.Equal(head) {
				t.Fatalf("HeadProducedAt = %v, want the head boundary's %v (round %d, replayed %d)", rep.HeadProducedAt, head, rep.Round.Tick, rep.Replayed)
			}
		})
	}
}

// A log with no boundary has no head: nothing to measure from.
func TestHeadProducedAtIsZeroWithNoBoundary(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 0, 0)
	_, rep, err := recovery.Recover(context.Background(), w.opts(t))
	if err != nil {
		t.Fatal(err)
	}
	if !rep.HeadProducedAt.IsZero() {
		t.Fatalf("HeadProducedAt = %v with no boundary", rep.HeadProducedAt)
	}
}
