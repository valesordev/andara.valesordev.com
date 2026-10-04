// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

//go:build !race

package simtest_test

// stallFactor scales AC-1's CPU-time assertion for the build it runs in.
//
// Two, here: AW-SRV-006 measured the copy at the 25,000-Entity sizing
// fixture at 7.4–8.7 ms wall-clock uncontended (2026-09-22), so the 30 ms
// limit is about 3.5x that benchmark figure. The guard's own thread-CPU
// readings run higher than the benchmark, so the margin is thinner than that
// suggests. On 2026-10-03 at load average 1.3–1.7 (the lowest this shared
// workstation showed that day, not an idle machine), worst-of-5 was 10.7–15.6
// ms over 10 runs, so the limit is 1.9x the worst low-load reading. At load average 7–30 it was 11.6–17.5 ms in six
// runs and 29.4 ms once. CI runs -race, where the limit is 120 ms; only a plain
// `go test` is this tight. If it flakes, raise the factor from a new
// measurement; do not add retries. See stallfactor_race_test.go for why this
// is not one number, and docs/specs/testing/timing-assertions.md for why it is
// CPU time.
const stallFactor = 2
