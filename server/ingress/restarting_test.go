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

	logv1 "github.com/valesordev/andara/gen/go/andara/log/v1"
	"github.com/valesordev/andara/server/command"
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

// heldLog holds every produce until release is closed.
type heldLog struct {
	inner   command.Producer
	entered chan struct{}
	release chan struct{}
}

func (h *heldLog) Produce(ctx context.Context, cmd *logv1.LoggedCommand) (command.Accepted, error) {
	select {
	case h.entered <- struct{}{}:
	default:
	}
	<-h.release
	return h.inner.Produce(ctx, cmd)
}

// WaitAdmitted returns once the Submits admitted before the flag return, and
// reports false when its deadline comes first.
func TestWaitAdmittedWaitsForSubmitsAlreadyAdmitted(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	f := newFixture(t, func(o *Options) {
		o.Pipeline.Log = &heldLog{inner: o.Pipeline.Log, entered: entered, release: release}
	})
	// A real Submit admitted before the flag, held inside its produce.
	done := make(chan error, 1)
	go func() { _, err := f.submit(context.Background(), "s-alice", "look"); done <- err }()
	<-entered
	f.in.RefuseRestarting()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if f.in.WaitAdmitted(ctx) {
		t.Fatal("WaitAdmitted returned true with a Submit still in flight")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("the admitted Submit: %v", err)
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	if !f.in.WaitAdmitted(ctx2) {
		t.Fatal("WaitAdmitted did not return once the admitted Submit did")
	}
}
