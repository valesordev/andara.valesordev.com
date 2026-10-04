// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

//go:build !race

package simtest_test

// stallFactor scales TestContentSwapStaysInsideHalfTheTickBudget's wall-clock
// assertion for the build it runs in: two without -race (see
// stallfactor_race_test.go). That test's limit is unchanged by #172; the
// snapshot guard has its own factor below.
const stallFactor = 2

// snapshotStallFactor scales TestSnapshotCopyStaysInsideTheStallBudget's
// thread-CPU limit: 4 x the 15 ms snapshot.max_stall_ms = 60 ms.
//
// Set from the worst reading inside the full suite, as the ruling says
// (docs/specs/testing/timing-assertions.md §2); see stallfactor_race_test.go
// for why a quiet reading is the wrong base. AW-SRV-006 measured the
// 25,000-Entity copy at 7.4–8.7 ms wall-clock uncontended (2026-09-22). The
// guard's own thread-CPU readings run higher than that benchmark.
//   - Alone at load average 1.3–1.7 (the lowest this shared workstation showed
//     that day, not an idle machine): 10.7–15.6 ms over 10 runs.
//   - In the full suite, `go test -count=1 -v ./server/...`, 20 runs on
//     2026-10-03 cycling 0–18 busy processes, load average 20–28 at start
//     (Go go1.27.1-X:nodwarf5, 24 cores): 16.4–21.3 ms.
//
// The limit is 2.8x the worst of those. The in-suite race build read up to
// 1.39x its 20-run worst when it was seen at its worst (134.6 ms against
// 96.6 ms), which puts the same tail near 30 ms here, and a factor of 3 would
// leave 1.5x on that. CI runs -race, so this is the build only a plain
// `go test` meets. If it flakes, raise the factor by a new ruling, not retries.
const snapshotStallFactor = 4

// The allocation bound's recorded values for this build (allocbound_test.go),
// at the sizing fixture, 2026-10-03: the minimum over 3 testing.AllocsPerRun
// calls (allocations) and over 5 rounds (bytes). The count is identical with
// and without -race, and across quiet and 24-busy-process runs.
const (
	buildTag       = "norace"
	recordedGo     = "go1.27.1-X:nodwarf5"
	recordedAllocs = 74119
	recordedBytes  = 8408136
)
