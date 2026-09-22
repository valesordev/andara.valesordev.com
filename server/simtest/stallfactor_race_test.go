// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

//go:build race

package simtest_test

// stallFactor scales AC-1's wall-clock assertion for the build it runs in.
//
// Eight, here, because `make test` — and therefore CI — runs -race, and the
// race detector instruments every memory access: the same copy that takes
// 2.7 ms without it takes 12.6 ms with it, measured on the same machine.
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
