// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package ingress

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/prometheus/client_golang/prometheus/testutil"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/valesordev/andara/server/command"
	"github.com/valesordev/andara/server/sim"
)

func (f *fixture) keys() float64 { return testutil.ToFloat64(f.in.Metrics().IdempotencyKeys) }

// AC-1: a retry of a produced Submit is answered with the same offset,
// produces nothing, and is counted deduplicated.
func TestIdempotency_ProducedRetry(t *testing.T) {
	f := newFixture(t, nil)
	first, err := f.submitRef(context.Background(), "s-alice", "north", "r1")
	if err != nil {
		t.Fatal(err)
	}
	again, err := f.submitRef(context.Background(), "s-alice", "north", "r1")
	if err != nil {
		t.Fatal(err)
	}
	if again.GetAcceptedOffset() != first.GetAcceptedOffset() || again.GetPartition() != first.GetPartition() {
		t.Fatalf("retry = %v, original %v", again, first)
	}
	if len(f.log.records) != 1 {
		t.Fatalf("records = %d: the retry produced", len(f.log.records))
	}
	if f.outcome(OutcomeProduced) != 1 || f.outcome(OutcomeDeduplicated) != 1 {
		t.Fatalf("produced=%v deduplicated=%v", f.outcome(OutcomeProduced), f.outcome(OutcomeDeduplicated))
	}
	if f.keys() != 1 {
		t.Fatalf("keys gauge = %v", f.keys())
	}
	// A different ref is a new Command.
	next, err := f.submitRef(context.Background(), "s-alice", "north", "r2")
	if err != nil || next.GetAcceptedOffset() != first.GetAcceptedOffset()+1 {
		t.Fatalf("new ref: %v %v", next, err)
	}
}

// AC-4: a rejected Submit's retry is the same rejection, without
// re-parsing, counted deduplicated.
func TestIdempotency_RejectionRetry(t *testing.T) {
	f := newFixture(t, nil)
	_, err := f.submitRef(context.Background(), "s-alice", "frobnicate", "r1")
	if codeOf(t, err) != connect.CodeInvalidArgument {
		t.Fatalf("first: %v", err)
	}
	parsed := testutil.ToFloat64(f.in.Metrics().Submits.WithLabelValues(OutcomeRejectedParse))
	_, err = f.submitRef(context.Background(), "s-alice", "frobnicate", "r1")
	if codeOf(t, err) != connect.CodeInvalidArgument || infoOf(t, err).GetReason() != command.CodeUnknownVerb {
		t.Fatalf("retry: %v", err)
	}
	if got := testutil.ToFloat64(f.in.Metrics().Submits.WithLabelValues(OutcomeRejectedParse)); got != parsed {
		t.Fatalf("the retry was parsed again: rejected_parse %v → %v", parsed, got)
	}
	if f.outcome(OutcomeDeduplicated) != 1 {
		t.Fatalf("deduplicated = %v", f.outcome(OutcomeDeduplicated))
	}
	// An authorize rejection is the Command's fate too: one audit record
	// for two calls.
	_, err = f.submitRef(context.Background(), "s-nobody", "look", "r1")
	if codeOf(t, err) != connect.CodePermissionDenied {
		t.Fatalf("unbound: %v", err)
	}
	_, err = f.submitRef(context.Background(), "s-nobody", "look", "r1")
	if codeOf(t, err) != connect.CodePermissionDenied || f.audit.Len() != 1 {
		t.Fatalf("unbound retry: %v, audit records %d", err, f.audit.Len())
	}
}

// AC-5: a retry arriving while the original is in flight waits for it;
// both return the same outcome and one record lands. The retry does not
// take a place in the Session's queue.
func TestIdempotency_InFlightRetry(t *testing.T) {
	f := newFixture(t, nil)
	f.log.gate = make(chan struct{})
	var wg sync.WaitGroup
	results := make([]int64, 3)
	errs := make([]error, 3)
	call := func(i int) {
		defer wg.Done()
		resp, err := f.submitRef(context.Background(), "s-alice", "look", "r1")
		errs[i] = err
		if err == nil {
			results[i] = resp.GetAcceptedOffset()
		}
	}
	wg.Add(1)
	go call(0)
	waitFor(t, func() bool { return testutil.ToFloat64(f.in.Metrics().Pending) == 1 }, "original pending")
	for i := 1; i < 3; i++ {
		wg.Add(1)
		go call(i)
	}
	// Give the retries time to arrive: still only one pending.
	time.Sleep(20 * time.Millisecond)
	if testutil.ToFloat64(f.in.Metrics().Pending) != 1 {
		t.Fatalf("pending = %v: a retry queued", testutil.ToFloat64(f.in.Metrics().Pending))
	}
	close(f.log.gate)
	wg.Wait()
	for i := range 3 {
		if errs[i] != nil || results[i] != 0 {
			t.Fatalf("call %d: %v %v", i, results[i], errs[i])
		}
	}
	if len(f.log.records) != 1 || f.outcome(OutcomeProduced) != 1 || f.outcome(OutcomeDeduplicated) != 2 {
		t.Fatalf("records=%d produced=%v deduplicated=%v", len(f.log.records), f.outcome(OutcomeProduced), f.outcome(OutcomeDeduplicated))
	}
}

// AC-6: the same client_ref with different text is a client bug:
// INVALID_ARGUMENT duplicate_client_ref, nothing produced, the first
// Intent's outcome untouched.
func TestIdempotency_DuplicateClientRef(t *testing.T) {
	f := newFixture(t, nil)
	if _, err := f.submitRef(context.Background(), "s-alice", "north", "r1"); err != nil {
		t.Fatal(err)
	}
	_, err := f.submitRef(context.Background(), "s-alice", "south", "r1")
	if codeOf(t, err) != connect.CodeInvalidArgument || infoOf(t, err).GetReason() != ReasonDuplicateClientRef || infoOf(t, err).GetDomain() != ErrorDomain {
		t.Fatalf("different text: %v", err)
	}
	if len(f.log.records) != 1 || f.outcome(OutcomeRejectedRef) != 1 {
		t.Fatalf("records=%d rejected_ref=%v", len(f.log.records), f.outcome(OutcomeRejectedRef))
	}
	// The original's outcome is still there for its own retry.
	if resp, err := f.submitRef(context.Background(), "s-alice", "north", "r1"); err != nil || resp.GetAcceptedOffset() != 0 {
		t.Fatalf("retry after the bug: %v %v", resp, err)
	}
}

// AC-7: past ingress.idempotency_window a key is forgotten and the retry
// is a new Command; at ingress.max_pending keys the oldest is evicted
// first, so a Session's table has a ceiling.
func TestIdempotency_WindowAndEviction(t *testing.T) {
	f := newFixture(t, func(o *Options) { o.IdempotencyWindow = 30 * time.Second; o.MaxPending = 4 })
	if _, err := f.submitRef(context.Background(), "s-alice", "look", "old"); err != nil {
		t.Fatal(err)
	}
	f.clock.step(29 * time.Second)
	if resp, err := f.submitRef(context.Background(), "s-alice", "look", "old"); err != nil || resp.GetAcceptedOffset() != 0 {
		t.Fatalf("inside the window: %v %v", resp, err)
	}
	f.clock.step(2 * time.Second)
	if resp, err := f.submitRef(context.Background(), "s-alice", "look", "old"); err != nil || resp.GetAcceptedOffset() != 1 {
		t.Fatalf("past the window: %v %v (a new Command lands at 1)", resp, err)
	}
	if f.keys() != 1 {
		t.Fatalf("keys = %v after the sweep", f.keys())
	}
	// Four keys is the ceiling: the fifth evicts the oldest, "old".
	for i := range 3 {
		if _, err := f.submitRef(context.Background(), "s-alice", "look", fmt.Sprintf("k%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if f.keys() != 4 {
		t.Fatalf("keys = %v", f.keys())
	}
	if _, err := f.submitRef(context.Background(), "s-alice", "look", "k3"); err != nil {
		t.Fatal(err)
	}
	if f.keys() != 4 {
		t.Fatalf("keys = %v past the ceiling", f.keys())
	}
	before := len(f.log.records)
	if _, err := f.submitRef(context.Background(), "s-alice", "look", "old"); err != nil {
		t.Fatal(err)
	}
	if len(f.log.records) != before+1 {
		t.Fatal("the evicted key was still remembered")
	}
	// k1 was next-oldest after k0 went for "old": still there.
	if _, err := f.submitRef(context.Background(), "s-alice", "look", "k1"); err != nil || len(f.log.records) != before+1 {
		t.Fatalf("k1: %v, records %d", err, len(f.log.records))
	}
	// The Session's end drops its keys.
	f.in.forget("s-alice")
	if f.keys() != 0 {
		t.Fatalf("keys = %v after forget", f.keys())
	}
}

// AC-8: an empty client_ref is not deduplicated.
func TestIdempotency_EmptyRef(t *testing.T) {
	f := newFixture(t, nil)
	for range 2 {
		if _, err := f.submitRef(context.Background(), "s-alice", "look", ""); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.log.records) != 2 || f.keys() != 0 {
		t.Fatalf("records=%d keys=%v", len(f.log.records), f.keys())
	}
}

// A transient refusal — the log read-only, the queue full — is not the
// Command's fate and is not remembered: the retry runs it. A retry that
// was waiting on such an original runs it too.
func TestIdempotency_TransientNotRemembered(t *testing.T) {
	f := newFixture(t, nil)
	f.log.fail = ErrUnavailable
	if _, err := f.submitRef(context.Background(), "s-alice", "look", "r1"); codeOf(t, err) != connect.CodeUnavailable {
		t.Fatalf("read-only: %v", err)
	}
	if f.keys() != 0 {
		t.Fatalf("keys = %v: a transient outcome was kept", f.keys())
	}
	f.log.fail = nil
	if resp, err := f.submitRef(context.Background(), "s-alice", "look", "r1"); err != nil || resp.GetAcceptedOffset() != 0 {
		t.Fatalf("retry after read-only: %v %v", resp, err)
	}

	// In flight and refused: the waiter runs the Command itself.
	f.log.fail = ErrUnavailable
	f.log.gate = make(chan struct{})
	done := make(chan error, 1)
	go func() { _, err := f.submitRef(context.Background(), "s-alice", "look", "r2"); done <- err }()
	waitFor(t, func() bool { return testutil.ToFloat64(f.in.Metrics().Pending) == 1 }, "original pending")
	waiter := make(chan error, 1)
	go func() { _, err := f.submitRef(context.Background(), "s-alice", "look", "r2"); waiter <- err }()
	time.Sleep(20 * time.Millisecond)
	close(f.log.gate)
	if err := <-done; codeOf(t, err) != connect.CodeUnavailable {
		t.Fatalf("original: %v", err)
	}
	// The waiter ran it: with the log still read-only it is refused the
	// same way, having tried.
	if err := <-waiter; codeOf(t, err) != connect.CodeUnavailable {
		t.Fatalf("waiter: %v", err)
	}
	if f.outcome(OutcomeUnavailable) != 3 || f.outcome(OutcomeDeduplicated) != 0 {
		t.Fatalf("unavailable=%v deduplicated=%v", f.outcome(OutcomeUnavailable), f.outcome(OutcomeDeduplicated))
	}
}

// AC-2, AC-3 at the seam: a Submit answered DEADLINE_EXCEEDED keeps its
// key open until the producer settles the record's fate. Landed: the
// retry gets the offset. Not written: the retry is a new Command. Still
// unknown: the retry inside the window is told DEADLINE_EXCEEDED again
// rather than becoming a second record.
func TestIdempotency_UnsettledFate(t *testing.T) {
	f := newFixture(t, nil)
	f.log.unsettled = make(chan *Unsettled, 1)
	p := sim.PartitionFor("town")

	// Landed.
	_, err := f.submitRef(context.Background(), "s-alice", "north", "r1")
	if codeOf(t, err) != connect.CodeDeadlineExceeded || infoOf(t, err).GetReason() != ReasonDeadline {
		t.Fatalf("deadline: %v", err)
	}
	u := <-f.log.unsettled
	if f.keys() != 1 {
		t.Fatalf("keys = %v: the open key was dropped", f.keys())
	}
	// A retry before settlement waits for it.
	retried := make(chan struct{})
	var resp *connectResp
	go func() {
		defer close(retried)
		r, err := f.submitRef(context.Background(), "s-alice", "north", "r1")
		resp = &connectResp{offset: r.GetAcceptedOffset(), partition: r.GetPartition(), err: err}
	}()
	select {
	case <-retried:
		t.Fatal("the retry returned before the record's fate was known")
	case <-time.After(20 * time.Millisecond):
	}
	u.settle(command.Accepted{Partition: p, Offset: 7}, nil)
	<-retried
	if resp.err != nil || resp.offset != 7 || resp.partition != p {
		t.Fatalf("retry after landing: %+v", resp)
	}
	if f.outcome(OutcomeDeduplicated) != 1 || len(f.log.records) != 0 {
		t.Fatalf("deduplicated=%v records=%d", f.outcome(OutcomeDeduplicated), len(f.log.records))
	}

	// Not written: the retry is a new produce.
	_, err = f.submitRef(context.Background(), "s-alice", "north", "r2")
	if codeOf(t, err) != connect.CodeDeadlineExceeded {
		t.Fatalf("deadline: %v", err)
	}
	u = <-f.log.unsettled
	u.settle(command.Accepted{}, fmt.Errorf("%w: %w", ErrNotWritten, errors.New("client closed")))
	waitFor(t, func() bool { return f.keys() == 1 }, "the not-written key dropped")
	f.log.unsettled = nil
	r, err := f.submitRef(context.Background(), "s-alice", "north", "r2")
	if err != nil || r.GetAcceptedOffset() != 0 || len(f.log.records) != 1 {
		t.Fatalf("retry after not-written: %v %v, records %d", r, err, len(f.log.records))
	}

	// Unknown: the retry is told so, terminally — outcome_unknown, not
	// the produce_deadline it retried on — and no record.
	f.log.unsettled = make(chan *Unsettled, 1)
	_, err = f.submitRef(context.Background(), "s-alice", "north", "r3")
	if codeOf(t, err) != connect.CodeDeadlineExceeded || infoOf(t, err).GetReason() != ReasonDeadline {
		t.Fatalf("deadline: %v", err)
	}
	u = <-f.log.unsettled
	u.settle(command.Accepted{}, fmt.Errorf("%w: %w", ErrOutcomeUnknown, errors.New("records have timed out")))
	f.log.unsettled = nil
	_, err = f.submitRef(context.Background(), "s-alice", "north", "r3")
	if codeOf(t, err) != connect.CodeDeadlineExceeded || infoOf(t, err).GetReason() != ReasonOutcomeUnknown || infoOf(t, err).GetDomain() != ErrorDomain {
		t.Fatalf("retry after unknown: %v", err)
	}
	if len(f.log.records) != 1 || f.outcome(OutcomeDeduplicated) != 2 {
		t.Fatalf("records=%d deduplicated=%v: an unknown fate became a second record", len(f.log.records), f.outcome(OutcomeDeduplicated))
	}
}

type connectResp struct {
	offset    int64
	partition int32
	err       error
}

// A dedup hit's trace is one command.execute span carrying
// deduplicated=true and no children.
func TestIdempotency_TraceShape(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	tracer := tp.Tracer("test")
	f := newFixture(t, func(o *Options) { o.Tracer = tracer; o.Pipeline.Tracer = tracer })
	if _, err := f.submitRef(context.Background(), "s-alice", "look", "r1"); err != nil {
		t.Fatal(err)
	}
	seen := len(rec.Ended())
	if _, err := f.submitRef(context.Background(), "s-alice", "look", "r1"); err != nil {
		t.Fatal(err)
	}
	spans := rec.Ended()[seen:]
	if len(spans) != 1 || spans[0].Name() != "command.execute" {
		names := make([]string, 0, len(spans))
		for _, s := range spans {
			names = append(names, s.Name())
		}
		t.Fatalf("dedup hit spans = %v", names)
	}
	var dedup bool
	for _, a := range spans[0].Attributes() {
		if string(a.Key) == "deduplicated" && a.Value.AsBool() {
			dedup = true
		}
	}
	if !dedup {
		t.Fatalf("attributes = %v", spans[0].Attributes())
	}
}

// A key still in flight is never evicted to make room — its retry must
// find it — so with every key in flight the Session is at its pending
// ceiling and a new ref is refused pending_full, while the retry of an
// in-flight key still joins it.
func TestIdempotency_InFlightKeysAreNotEvicted(t *testing.T) {
	f := newFixture(t, func(o *Options) { o.MaxPending = 2 })
	f.log.gate = make(chan struct{})
	var wg sync.WaitGroup
	for _, ref := range []string{"r1", "r2"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := f.submitRef(context.Background(), "s-alice", "look", ref); err != nil {
				t.Errorf("%s: %v", ref, err)
			}
		}()
	}
	waitFor(t, func() bool { return testutil.ToFloat64(f.in.Metrics().Pending) == 2 }, "two in flight")
	_, err := f.submitRef(context.Background(), "s-alice", "look", "r3")
	if codeOf(t, err) != connect.CodeResourceExhausted || infoOf(t, err).GetReason() != ReasonPendingFull {
		t.Fatalf("third ref with both in flight: %v", err)
	}
	if f.keys() != 2 {
		t.Fatalf("keys = %v: an in-flight key was evicted", f.keys())
	}
	retried := make(chan error, 1)
	go func() { _, err := f.submitRef(context.Background(), "s-alice", "look", "r1"); retried <- err }()
	time.Sleep(20 * time.Millisecond)
	close(f.log.gate)
	wg.Wait()
	if err := <-retried; err != nil {
		t.Fatalf("retry of an in-flight key: %v", err)
	}
	if f.outcome(OutcomeDeduplicated) != 1 || len(f.log.records) != 2 {
		t.Fatalf("deduplicated=%v records=%d", f.outcome(OutcomeDeduplicated), len(f.log.records))
	}
	// Resolved now: a new ref evicts the oldest.
	if _, err := f.submitRef(context.Background(), "s-alice", "look", "r3"); err != nil || f.keys() != 2 {
		t.Fatalf("after resolution: %v, keys %v", err, f.keys())
	}
}

// A key in flight does not expire: however long the queue wait, the
// hold, and the produce took, the retry that arrives past the window
// must find the entry and wait on it, not run the Command beside it.
func TestIdempotency_InFlightKeyOutlivesTheWindow(t *testing.T) {
	// At the table.
	var tb table
	t0 := time.Unix(1_700_000_000, 0)
	e, hit, _, err := tb.lookup("r1", "look", t0, 30*time.Second, 4)
	if err != nil || hit {
		t.Fatalf("first lookup: hit=%v err=%v", hit, err)
	}
	e2, hit, n, err := tb.lookup("r1", "look", t0.Add(31*time.Second), 30*time.Second, 4)
	if err != nil || !hit || e2 != e || n != 0 {
		t.Fatalf("past the window, unresolved: hit=%v same=%v n=%d err=%v", hit, e2 == e, n, err)
	}
	tb.resolve(e, nil, nil, true, t0.Add(31*time.Second))
	if _, hit, _, _ := tb.lookup("r1", "look", t0.Add(62*time.Second), 30*time.Second, 4); hit {
		t.Fatal("resolved and past the window: still a hit")
	}

	// Through the ingress: the original is gated in the log while the
	// clock steps past the window; the retry waits, and one record lands.
	f := newFixture(t, func(o *Options) { o.IdempotencyWindow = 30 * time.Second })
	f.log.gate = make(chan struct{})
	done := make(chan error, 1)
	go func() { _, err := f.submitRef(context.Background(), "s-alice", "look", "r1"); done <- err }()
	waitFor(t, func() bool { return testutil.ToFloat64(f.in.Metrics().Pending) == 1 }, "original pending")
	f.clock.step(31 * time.Second)
	retried := make(chan error, 1)
	go func() { _, err := f.submitRef(context.Background(), "s-alice", "look", "r1"); retried <- err }()
	select {
	case err := <-retried:
		t.Fatalf("the retry returned while the original was in flight: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	if testutil.ToFloat64(f.in.Metrics().Pending) != 1 || f.keys() != 1 {
		t.Fatalf("pending=%v keys=%v: the retry ran beside the original", testutil.ToFloat64(f.in.Metrics().Pending), f.keys())
	}
	close(f.log.gate)
	if err := <-done; err != nil {
		t.Fatalf("original: %v", err)
	}
	if err := <-retried; err != nil {
		t.Fatalf("retry: %v", err)
	}
	if len(f.log.records) != 1 || f.outcome(OutcomeDeduplicated) != 1 {
		t.Fatalf("records=%d deduplicated=%v", len(f.log.records), f.outcome(OutcomeDeduplicated))
	}
}
