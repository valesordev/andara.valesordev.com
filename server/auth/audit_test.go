// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package auth

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/valesordev/andara/server/recordlog"
)

// blockingLog is a Log whose Append waits until released.
type blockingLog struct {
	release chan error
}

func (b *blockingLog) Append(context.Context, string, []byte) error { return <-b.release }
func (b *blockingLog) Replay(context.Context, func(recordlog.Record) error) error {
	return nil
}
func (b *blockingLog) Close() error { return nil }

// AW-SRV-010 AC-5: a Record whose write the broker cannot take returns
// within the write timeout with the write still in flight; the record is
// not dropped, and a failure after that is still counted.
func TestAuditor_BoundedWait(t *testing.T) {
	var logs syncBuffer
	log := &blockingLog{release: make(chan error, 1)}
	metrics := NewMetrics(nil)
	a := NewAuditor(log, slog.New(slog.NewJSONHandler(&logs, nil)), metrics, nil)
	a.WriteTimeout = 20 * time.Millisecond

	began := time.Now()
	a.Record(context.Background(), Entry{Actor: Principal{AccountID: "acct"}, Action: ActionAuthorize, Target: "look", Outcome: AuditDenied})
	if took := time.Since(began); took > time.Second {
		t.Fatalf("Record waited %s on a stuck write", took)
	}
	if !strings.Contains(logs.String(), "write pending past its timeout") {
		t.Fatalf("no pending warning:\n%s", logs.String())
	}
	if got := testutil.ToFloat64(metrics.AuditWriteFailures); got != 0 {
		t.Fatalf("a late write was counted as failed: %v", got)
	}
	// The write eventually fails: counted and logged then.
	log.release <- errors.New("broker gone")
	deadline := time.Now().Add(2 * time.Second)
	for testutil.ToFloat64(metrics.AuditWriteFailures) != 1 || !strings.Contains(logs.String(), "audit record not written") {
		if time.Now().After(deadline) {
			t.Fatalf("background failure never counted and logged:\n%s", logs.String())
		}
		time.Sleep(time.Millisecond)
	}

	// A write that completes in time is silent.
	logs.reset()
	log.release <- nil
	a.Record(context.Background(), Entry{Actor: Principal{AccountID: "acct"}, Action: ActionAuthorize, Target: "look", Outcome: AuditDenied})
	if strings.Contains(logs.String(), "pending") || testutil.ToFloat64(metrics.AuditWriteFailures) != 1 {
		t.Fatalf("prompt write mis-reported:\n%s", logs.String())
	}
}
