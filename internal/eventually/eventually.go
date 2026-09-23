// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

// Package eventually is the one polling helper for live assertions — an
// assertion whose subject is produced by machinery the test does not drive
// directly: a registry counter, a Prometheus series, a projection, an Event
// on a stream (docs/specs/testing/live-assertions.md).
//
// A test that reads such a subject once asserts that the effect had landed by
// the moment it looked, which is a claim about scheduling. True polls the
// exact expression the test is about to assert on until it holds or the
// deadline passes, so the wait and the assertion are the same predicate and a
// wait on a proxy signal — the mistake that looks deliberate — has nowhere to
// hide.
//
// One helper rather than one per package, so every wait has the same shape,
// the same sampling rule, and the same failure. A package's own wrapper is
// fine when it fixes the deadline; it delegates here rather than looping.
package eventually

import (
	"testing"
	"time"
)

// True polls want until it returns true or d passes, then fails the test
// naming what. what names the assertion, not the wait: "both unsubscribes
// counted", not "the drop". A predicate that closes over the value it reads
// can record it for the failure; True itself only knows the verdict.
//
// Deadlines are generous on purpose: a long one costs only when the test is
// failing anyway, a short one costs at random forever. 5–10 s suits an
// in-process signal, 90 s anything behind a Prometheus scrape.
//
// Sampling scales with the deadline — a thousandth of it, between 1 ms and
// 100 ms — so an in-process wait notices within milliseconds while a 30 s
// wait on a scraped /metrics does not hammer the endpoint. The predicate is
// evaluated once more after the deadline, so a signal that lands on it is
// still seen.
func True(t testing.TB, d time.Duration, what string, want func() bool) {
	t.Helper()
	Observed(t, d, what, func() (bool, string) { return want(), "" })
}

// Observed is True for a predicate that also reports what it saw. The
// report from the last sample is part of the failure, so the failure says
// what the value was, not only that it was wrong: "present=1 bound=2"
// rather than "the gauges never settled". An empty report is left out.
func Observed(t testing.TB, d time.Duration, what string, want func() (ok bool, saw string)) {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		ok, saw := want()
		if ok {
			return
		}
		if time.Now().After(deadline) {
			if saw != "" {
				t.Fatalf("timed out after %s waiting for %s; last saw %s", d, what, saw)
			} else {
				t.Fatalf("timed out after %s waiting for %s", d, what)
			}
			return
		}
		time.Sleep(interval(d))
	}
}

// interval is how often True samples for a deadline of d.
func interval(d time.Duration) time.Duration {
	i := d / 1000
	if i < time.Millisecond {
		return time.Millisecond
	}
	if i > 100*time.Millisecond {
		return 100 * time.Millisecond
	}
	return i
}
