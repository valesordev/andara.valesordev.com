// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package simtest_test

// The allocation bound's headroom (docs/specs/testing/timing-assertions.md §2).
// The count is deterministic, so 1.05x catches one more allocation per Entity;
// the bytes headroom is for toolchain drift.
const (
	allocBoundCount = 1.05
	allocBoundBytes = 1.25
)
