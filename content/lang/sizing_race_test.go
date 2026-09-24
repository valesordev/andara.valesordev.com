// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

//go:build race

package lang_test

// compileFactor scales AC-7's wall-clock assertion for the build it runs in.
//
// Three, here, because `make test` — and therefore CI — runs -race, and the
// race detector instruments every memory access. The same reasoning as
// server/simtest/stallfactor_race_test.go: a //go:build !race on the whole
// test would be a test that never runs anywhere but a developer's laptop,
// because CI runs `make check` and `make test` is the only Go test target in
// it.
//
// It is a regression guard, not a measurement. The number a human should read
// is the one the test logs, and the failure it exists to catch is an
// accidental quadratic in name resolution — which at 2,000 Rooms would be far
// past this, not marginally over it.
const compileFactor = 3
