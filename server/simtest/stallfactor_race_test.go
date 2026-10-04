// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

//go:build race

package simtest_test

// stallFactor scales AC-1's CPU-time assertion for the build it runs in.
//
// Eight, here, because `make test` — and therefore CI — runs -race, and the
// race detector instruments every memory access: AW-SRV-006 measured the same
// copy at 25,000 Entities at 34.7 ms with it against 7.4–8.7 ms without
// (2026-09-22), so the 120 ms limit is about 3.5x that. Thread CPU time of the
// guard on 2026-10-03, on a workstation shared with other sessions (load
// average 7–30, so not quiet): 44.1–56.2 ms over 10 runs; 46–67 ms with
// 2 x nproc busy processes running, worst 89.8 ms once at load 7.
//
// The alternative was a //go:build !race on the whole test, which is what the
// numbers first suggested. But CI runs `make check` and nothing else, and
// `make test` is the only Go test target in it — so a !race test is a test
// that never runs anywhere but a developer's laptop, and the Definition of
// Done asks for the test plan to run in CI. A threshold that moves with the
// build keeps the assertion where it is useful.
//
// It is still a regression guard, not a measurement: the failure it exists to
// catch is an encode creeping back inside the tick, which cost 24.7 ms
// unin­strumented and would be far past this. The benchmark is the number a
// human should read.
const stallFactor = 8
