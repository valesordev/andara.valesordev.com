// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

//go:build race

package simtest_test

// stallFactor scales TestContentSwapStaysInsideHalfTheTickBudget's wall-clock
// assertion for the build it runs in. Eight under -race, which instruments
// every memory access (AW-SRV-006 measured the 25,000-Entity snapshot copy at
// 34.7 ms with it against 7.4–8.7 ms without, 2026-09-22). That test's limit is
// unchanged by #172; the snapshot guard has its own factor below.
const stallFactor = 8

// snapshotStallFactor scales TestSnapshotCopyStaysInsideTheStallBudget's
// thread-CPU limit: 14 x the 15 ms snapshot.max_stall_ms = 210 ms.
//
// A limit set from a quiet reading is wrong for where `make check` runs the
// guard, so this one is set from the worst reading inside the full suite
// (docs/specs/testing/timing-assertions.md §2, ruling of 2026-10-03). Under
// the suite's own parallel package binaries the thread's CPU time reads
// 2.5–3x its alone figure: cache, memory-bandwidth and SMT contention are
// charged to the thread.
//   - Alone at load average 1.3–1.7 (the lowest this shared workstation showed
//     that day, not an idle machine): 36.1–50.7 ms over 10 runs.
//   - In the full suite, `go test -race -count=1 -v ./server/...`, 20 runs on
//     2026-10-03 cycling 0–18 busy processes, load average 3.5–23.7 at start
//     (go1.27.1-X:nodwarf5, 24 cores): 62.9–96.6 ms.
//   - The worst in-suite reading seen on this change, outside those 20 runs, is
//     134.6 ms (`make test`, load average 17), after 122.6 ms in another run.
//     The limit is 1.56x that.
//
// CI runs -race, so this is the build that counts. The bound only moves by an
// architecture ruling, and only when the allocation count at that commit is
// within 1.0x of its recorded value, so a re-measurement on a tree whose copy
// has crept can't raise the limit and hide it.
//
// The alternative to a factor that moves with the build was a //go:build !race
// on the whole test; but CI runs `make check` and nothing else, and `make test`
// is the only Go test target in it, so a !race test never runs anywhere but a
// developer's laptop.
//
// This limit is loose, so the structural gate is the allocation bound below.
// The CPU limit catches work that doesn't allocate. It is a regression guard,
// not a measurement: the failure it exists to catch is an encode creeping back
// inside the tick, which cost 24.7 ms uninstrumented against a 5 ms budget.
// The benchmark is the number a human should read.
const snapshotStallFactor = 14

// The allocation bound's recorded values for this build (allocbound_test.go),
// at the sizing fixture, 2026-10-03: the minimum over 3 testing.AllocsPerRun
// calls (allocations) and over 5 rounds (bytes). The count is identical with
// and without -race, and across quiet and 24-busy-process runs.
const (
	buildTag       = "race"
	recordedGo     = "go1.27.1-X:nodwarf5"
	recordedAllocs = 74119
	recordedBytes  = 8408136
)
