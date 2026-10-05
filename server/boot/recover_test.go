// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package boot

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/valesordev/andara/server/recovery"
	"github.com/valesordev/andara/server/sim"
)

func TestStartExit_MapsRecoveryRefusals(t *testing.T) {
	for _, c := range []struct {
		err  error
		want int
	}{
		{errors.New("broker down"), ExitFail},
		{fmt.Errorf("recovery: %w", recovery.Classify(&sim.HashMismatchError{})), 8},
		{fmt.Errorf("recovery: %w", recovery.Classify(&sim.RestoreMismatch{})), 6},
		{fmt.Errorf("recovery: %w", recovery.Classify(&sim.ErrRoundIncomplete{})), 7},
		{fmt.Errorf("recovery: %w", recovery.Classify(&sim.ErrStateVersion{})), 4},
	} {
		if got := StartExit(c.err); got != c.want {
			t.Errorf("%v: exit %d, want %d", c.err, got, c.want)
		}
	}
	if !Lingers(recovery.Classify(&sim.HashMismatchError{})) || !Lingers(recovery.Classify(&sim.SeedMismatch{})) {
		t.Error("exits 8 and 6 linger")
	}
	if Lingers(recovery.Classify(&sim.ErrRoundIncomplete{})) || Lingers(errors.New("x")) {
		t.Error("exit 7 and a plain error must not linger")
	}
}

func httpStatus(t *testing.T, base, path string) int {
	t.Helper()
	resp, err := http.Get(base + path)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// AC-14: during the linger /metrics and /livez answer 200 and /readyz and
// /startedz 503; the clock ends it; a signal ends it at once.
func TestHoldMismatch_ServesOperatorHTTPThenReturns(t *testing.T) {
	rt, _ := runtime(t, fixture(t, "valid"), false)
	for _, c := range []struct {
		name string
		end  func(tick chan time.Time, cancel context.CancelFunc)
	}{
		{"the clock", func(tick chan time.Time, _ context.CancelFunc) { tick <- time.Now() }},
		{"a signal", func(_ chan time.Time, cancel context.CancelFunc) { cancel() }},
	} {
		t.Run(c.name, func(t *testing.T) {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			tick := make(chan time.Time)
			var asked time.Duration
			done := make(chan struct{})
			go func() {
				rt.HoldMismatch(ctx, ln, 60*time.Second, func(d time.Duration) <-chan time.Time { asked = d; return tick })
				close(done)
			}()
			base := "http://" + ln.Addr().String()
			deadline := time.Now().Add(5 * time.Second)
			for httpStatus(t, base, "/livez") != 200 && time.Now().Before(deadline) {
				time.Sleep(5 * time.Millisecond)
			}
			for path, want := range map[string]int{"/livez": 200, "/metrics": 200, "/readyz": 503, "/startedz": 503} {
				if got := httpStatus(t, base, path); got != want {
					t.Errorf("%s = %d, want %d", path, got, want)
				}
			}
			select {
			case <-done:
				t.Fatal("returned before the linger ended")
			default:
			}
			c.end(tick, cancel)
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("did not return")
			}
			if asked != 60*time.Second {
				t.Errorf("waited %s, want 60s", asked)
			}
		})
	}
}
