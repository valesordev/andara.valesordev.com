// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

//go:build !race

package simtest_test

// stallFactor scales AC-1's CPU-time assertion for the build it runs in.
//
// Two, here: AW-SRV-006 measured the copy at the 25,000-Entity sizing
// fixture at 7.4–8.7 ms uncontended (2026-09-22), so the 30 ms limit is about
// 3.5x that. Thread CPU time of the guard on 2026-10-03, on a workstation
// shared with other sessions (load average 7–30, so not quiet): 15.7–29.4 ms
// over 10 runs. See stallfactor_race_test.go for why this is not one number,
// and docs/specs/testing/timing-assertions.md for why it is CPU time.
const stallFactor = 2
