// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package simtest_test

import (
	"runtime"
	"syscall"
	"testing"
	"time"
)

// threadCPU runs f on one pinned OS thread and returns the CPU time that
// thread spent in it, user plus system, from getrusage(RUSAGE_THREAD). Time
// the thread spends descheduled is not charged to it, and descheduling is what
// other processes' load does (docs/specs/testing/timing-assertions.md §1).
//
// It measures only the calling goroutine's work: use it for code that spawns
// no goroutines. The thread's time excludes Go's background GC workers, so a
// regression it is meant to catch has to be mutator CPU.
func threadCPU(t testing.TB, f func()) (time.Duration, string) {
	t.Helper()
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	var before, after syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_THREAD, &before); err != nil {
		t.Fatalf("getrusage(RUSAGE_THREAD): %v", err)
	}
	f()
	if err := syscall.Getrusage(syscall.RUSAGE_THREAD, &after); err != nil {
		t.Fatalf("getrusage(RUSAGE_THREAD): %v", err)
	}
	return tvDuration(after.Utime) + tvDuration(after.Stime) - tvDuration(before.Utime) - tvDuration(before.Stime), "thread CPU time"
}

func tvDuration(tv syscall.Timeval) time.Duration {
	return time.Duration(tv.Sec)*time.Second + time.Duration(tv.Usec)*time.Microsecond
}
