// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

//go:build !linux

package simtest_test

import (
	"testing"
	"time"
)

// threadCPU is the wall-clock fallback where getrusage(RUSAGE_THREAD) doesn't
// exist. These platforms aren't in CI; a developer who hits the limit here
// under load reruns the test alone (docs/specs/testing/timing-assertions.md §1).
func threadCPU(_ testing.TB, f func()) (time.Duration, string) {
	start := time.Now()
	f()
	return time.Since(start), "wall-clock fallback (no RUSAGE_THREAD on this platform)"
}
