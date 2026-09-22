// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package eventually

import (
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

// A predicate that holds on its third sample returns before the deadline; one
// that never holds fails naming what, after the deadline, having sampled once
// more past it.
func TestTrue(t *testing.T) {
	var calls atomic.Int32
	True(t, time.Second, "the third sample", func() bool { return calls.Add(1) >= 3 })
	if got := calls.Load(); got != 3 {
		t.Fatalf("sampled %d times, want 3", got)
	}

	ft := &fakeT{}
	began := time.Now()
	calls.Store(0)
	True(ft, 20*time.Millisecond, "a signal that never lands", func() bool { calls.Add(1); return false })
	if !ft.failed || ft.msg != "timed out after 20ms waiting for a signal that never lands" {
		t.Fatalf("failed=%v msg=%q", ft.failed, ft.msg)
	}
	if took := time.Since(began); took < 20*time.Millisecond {
		t.Fatalf("gave up after %s, before the deadline", took)
	}
	if calls.Load() < 2 {
		t.Fatalf("sampled %d times; want at least one sample past the deadline", calls.Load())
	}
}

func TestInterval(t *testing.T) {
	for _, c := range []struct{ d, want time.Duration }{
		{100 * time.Millisecond, time.Millisecond},
		{5 * time.Second, 5 * time.Millisecond},
		{30 * time.Second, 30 * time.Millisecond},
		{90 * time.Second, 90 * time.Millisecond},
		{10 * time.Minute, 100 * time.Millisecond},
	} {
		if got := interval(c.d); got != c.want {
			t.Errorf("interval(%s) = %s, want %s", c.d, got, c.want)
		}
	}
}

// fakeT records the failure True reports instead of ending the test.
type fakeT struct {
	testing.TB
	failed bool
	msg    string
}

func (f *fakeT) Helper() {}
func (f *fakeT) Fatalf(format string, args ...any) {
	f.failed = true
	f.msg = sprintf(format, args...)
}

func sprintf(format string, args ...any) string { return fmt.Sprintf(format, args...) }
