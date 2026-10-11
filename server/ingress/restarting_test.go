// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package ingress

import (
	"context"
	"strconv"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// AW-SRV-054 AC-3: once the draining flag is set, Submit is UNAVAILABLE with
// reason server_restarting and nothing is produced, and the flag is not the
// degraded state: andara_ingress_degraded stays 0 on every Partition.
func TestRefuseRestartingIsNotTheDegradedState(t *testing.T) {
	f := newFixture(t, nil)
	if _, err := f.submit(context.Background(), "s-alice", "look"); err != nil {
		t.Fatalf("before the flag: %v", err)
	}
	f.in.RefuseRestarting()
	_, err := f.submit(context.Background(), "s-alice", "look")
	if codeOf(t, err) != connect.CodeUnavailable || infoOf(t, err).GetReason() != ReasonServerRestarting {
		t.Fatalf("Submit while restarting = %v, want UNAVAILABLE server_restarting", err)
	}
	if n := len(f.log.records); n != 1 {
		t.Fatalf("%d records produced, want only the one before the flag", n)
	}
	if !f.in.Restarting() {
		t.Fatal("Restarting() is false after RefuseRestarting")
	}
	m := f.in.Metrics()
	for p := 0; p < 64; p++ {
		if got := testutil.ToFloat64(m.Degraded.WithLabelValues(strconv.Itoa(p))); got != 0 {
			t.Fatalf("andara_ingress_degraded{partition=%d} = %v, want 0", p, got)
		}
	}
}

// WaitAdmitted returns once the Submits admitted before the flag return, and
// reports false when its deadline comes first.
func TestWaitAdmittedWaitsForSubmitsAlreadyAdmitted(t *testing.T) {
	f := newFixture(t, nil)
	// A Submit admitted before the flag and not yet returned.
	f.in.admitted.Add(1)
	f.in.RefuseRestarting()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if f.in.WaitAdmitted(ctx) {
		t.Fatal("WaitAdmitted returned true with a Submit still in flight")
	}
	f.in.admitted.Done()
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	if !f.in.WaitAdmitted(ctx2) {
		t.Fatal("WaitAdmitted did not return once the admitted Submit did")
	}
}
