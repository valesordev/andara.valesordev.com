// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package boot

import (
	"errors"
	"fmt"
	"testing"

	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/tickloop"
)

// AW-SRV-026 AC-1: a lost boundary exits 5, however the loop's error reaches
// main; the drain timeout and every other stop exit 1.
func TestLoopExit(t *testing.T) {
	lost := &tickloop.BoundaryLostError{Lost: 11, LastDelivered: 10, Err: errors.New("broker gone")}
	for _, tc := range []struct {
		name string
		err  error
		want int
	}{
		{"boundary lost", lost, 5},
		{"boundary lost, wrapped", fmt.Errorf("tick loop: %w", lost), 5},
		{"drain timeout", &tickloop.DrainTimeoutError{Tick: 3}, 1},
		{"offset gap", sim.ErrOffsetGap, 1},
		{"bare ErrBoundaryLost from a publish", tickloop.ErrBoundaryLost, 1},
	} {
		if got := LoopExit(tc.err); got != tc.want {
			t.Errorf("%s: exit %d, want %d", tc.name, got, tc.want)
		}
	}
	if ExitBoundaryLost != 5 || ExitFail != 1 {
		t.Fatalf("ExitBoundaryLost %d, ExitFail %d: the contract's exit codes are 5 and 1", ExitBoundaryLost, ExitFail)
	}
}
