// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package ingress

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/protobuf/proto"

	gamev1 "github.com/valesordev/andara/gen/go/andara/game/v1"
	logv1 "github.com/valesordev/andara/gen/go/andara/log/v1"
	"github.com/valesordev/andara/internal/eventually"
	"github.com/valesordev/andara/server/auth"
	"github.com/valesordev/andara/server/command"
	"github.com/valesordev/andara/server/recordlog"
	"github.com/valesordev/andara/server/sim"
)

// fakeLog is command.Producer over a slice: per-Partition offsets, an
// injectable error, and a gate that holds produces open so ordering can
// be observed.
type fakeLog struct {
	mu      sync.Mutex
	next    map[int32]int64
	records []*logv1.LoggedCommand
	fail    error
	gate    chan struct{} // when set, Produce waits on it
	// unsettled, when set, makes Produce answer ErrDeadline with the
	// record's fate pending; the Unsettled is sent here for the test to
	// settle (AW-SRV-031).
	unsettled chan *Unsettled
}

func (f *fakeLog) Produce(ctx context.Context, cmd *logv1.LoggedCommand) (command.Accepted, error) {
	if f.gate != nil {
		select {
		case <-f.gate:
		case <-ctx.Done():
			return command.Accepted{}, ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return command.Accepted{}, f.fail
	}
	if f.unsettled != nil {
		u := newUnsettled(ErrDeadline)
		f.unsettled <- u
		return command.Accepted{}, u
	}
	if f.next == nil {
		f.next = map[int32]int64{}
	}
	p := sim.PartitionFor(sim.ZoneID(cmd.GetZoneId()))
	off := f.next[p]
	f.next[p]++
	f.records = append(f.records, proto.Clone(cmd).(*logv1.LoggedCommand))
	return command.Accepted{Partition: p, Offset: off}, nil
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) step(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type fixture struct {
	in       *Ingress
	log      *fakeLog
	bindings *Bindings
	audit    *recordlog.Memory
	clock    *clock
	reg      *prometheus.Registry
	refs     atomic.Uint64
}

var (
	player = auth.Principal{AccountID: "acct-alice", Roles: []auth.Role{auth.RolePlayer}}
	agent  = auth.Principal{AccountID: "acct-agent", Roles: []auth.Role{auth.RoleAgent}}
)

func newFixture(t *testing.T, mutate func(*Options)) *fixture {
	t.Helper()
	reg := prometheus.NewRegistry()
	metrics := NewMetrics(reg)
	c := &clock{t: time.Unix(1_700_000_000, 0)}
	f := &fixture{log: &fakeLog{}, audit: recordlog.NewMemory(), clock: c, reg: reg}
	f.bindings = NewBindings(2*time.Second, c.now, metrics.Held)
	table := command.Builtin()
	o := Options{
		Pipeline: &command.Pipeline{
			Table:      table,
			MaxBytes:   4096,
			Authorizer: &auth.Authorizer{Table: table.Roles(), Audit: auth.NewAuditor(f.audit, nil, auth.NewMetrics(nil), c.now)},
			Bindings:   f.bindings,
			Log:        f.log,
			Metrics:    command.NewMetrics(reg, nil),
		},
		Bindings:       f.bindings,
		RateLimit:      auth.RateLimit{N: 20, Period: time.Second},
		AgentRateLimit: auth.RateLimit{N: 100, Period: time.Second},
		Burst:          40,
		MaxPending:     4,
		Metrics:        metrics,
		Now:            c.now,
	}
	if mutate != nil {
		mutate(&o)
	}
	f.in = New(o)
	f.bindings.Bind("s-alice", command.Binding{Actor: "alice", Zone: "town"})
	return f
}

// submit sends raw with a fresh client_ref: each call is its own
// Command. submitRef is the retry: the same client_ref again.
func (f *fixture) submit(ctx context.Context, session, raw string) (*gamev1.SubmitResponse, error) {
	return f.submitRef(ctx, session, raw, fmt.Sprintf("ref-%d", f.refs.Add(1)))
}

func (f *fixture) submitRef(ctx context.Context, session, raw, ref string) (*gamev1.SubmitResponse, error) {
	p := player
	if session == "s-agent" {
		p = agent
	}
	return f.in.submit(ctx, session, p, nil, &gamev1.SubmitRequest{SessionId: session, Raw: raw, ClientRef: ref})
}

func (f *fixture) outcome(o string) float64 {
	return testutil.ToFloat64(f.in.Metrics().Submits.WithLabelValues(o))
}

func codeOf(t *testing.T, err error) connect.Code {
	t.Helper()
	var ce *connect.Error
	if !errors.As(err, &ce) {
		t.Fatalf("not a connect error: %v", err)
	}
	return ce.Code()
}

func infoOf(t *testing.T, err error) *errdetails.ErrorInfo {
	t.Helper()
	var ce *connect.Error
	if !errors.As(err, &ce) {
		t.Fatalf("not a connect error: %v", err)
	}
	for _, d := range ce.Details() {
		if v, err := d.Value(); err == nil {
			if info, ok := v.(*errdetails.ErrorInfo); ok {
				return info
			}
		}
	}
	t.Fatalf("no ErrorInfo on %v", err)
	return nil
}

// AC-1: a bound Session's valid Intent is produced on its Zone's Partition
// and the response carries where it landed.
func TestSubmit_Produced(t *testing.T) {
	f := newFixture(t, nil)
	resp, err := f.submit(context.Background(), "s-alice", "look")
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetPartition() != sim.PartitionFor("town") || resp.GetAcceptedOffset() != 0 {
		t.Fatalf("resp = %v", resp)
	}
	resp, _ = f.submit(context.Background(), "s-alice", "north")
	if resp.GetAcceptedOffset() != 1 {
		t.Fatalf("second offset = %d", resp.GetAcceptedOffset())
	}
	if f.outcome(OutcomeProduced) != 2 {
		t.Fatalf("produced = %v", f.outcome(OutcomeProduced))
	}
}

// Inherited from AW-SRV-003's review: the produced record is Parse's
// output plus the Gateway's correlation fields and nothing else. A
// client-supplied record never reaches the log.
func TestSubmit_ProducesExactlyWhatParseReturned(t *testing.T) {
	f := newFixture(t, nil)
	if _, err := f.submitRef(context.Background(), "s-alice", "move north", "ref-move-north"); err != nil {
		t.Fatal(err)
	}
	got := f.log.records[0]
	want, _, err := command.Parse(command.Intent{SessionID: "s-alice", Raw: "move north", ClientRef: "ref-move-north"}, command.Builtin(), 4096)
	if err != nil {
		t.Fatal(err)
	}
	want.ZoneId, want.ActorId = "town", "alice"
	want.AcceptedAtUnixNano, want.TraceId = got.GetAcceptedAtUnixNano(), got.GetTraceId()
	if want.AcceptedAtUnixNano == 0 {
		t.Fatal("accepted_at not stamped")
	}
	a, _ := canonical.Marshal(got)
	b, _ := canonical.Marshal(want)
	if string(a) != string(b) {
		t.Fatalf("record differs from Parse's output:\n got %v\nwant %v", got, want)
	}
}

// AC-3, AC-4, and the error taxonomy row by row.
func TestSubmit_ErrorMapping(t *testing.T) {
	f := newFixture(t, nil)

	// parse → INVALID_ARGUMENT, typed, nothing produced.
	_, err := f.submit(context.Background(), "s-alice", "frobnicate")
	if codeOf(t, err) != connect.CodeInvalidArgument || infoOf(t, err).GetReason() != command.CodeUnknownVerb {
		t.Fatalf("parse failure: %v", err)
	}
	if info := infoOf(t, err); info.GetDomain() != ErrorDomain || info.GetMetadata()["stage"] != "parse" || info.GetMetadata()["pre_log"] != "true" {
		t.Fatalf("ErrorInfo = %v", infoOf(t, err))
	}
	_, err = f.submit(context.Background(), "s-alice", "move sideways")
	if codeOf(t, err) != connect.CodeInvalidArgument || infoOf(t, err).GetMetadata()["arg"] != "direction" {
		t.Fatalf("invalid argument: %v %v", err, infoOf(t, err))
	}
	// authorize (unbound) → PERMISSION_DENIED, one audit record.
	_, err = f.submit(context.Background(), "s-nobody", "look")
	if codeOf(t, err) != connect.CodePermissionDenied || infoOf(t, err).GetReason() != command.CodeNotAuthorized {
		t.Fatalf("unbound: %v", err)
	}
	if f.audit.Len() != 1 {
		t.Fatalf("audit records = %d", f.audit.Len())
	}
	if len(f.log.records) != 0 {
		t.Fatalf("a rejection reached the log: %v", f.log.records)
	}
	if f.outcome(OutcomeRejectedParse) != 2 || f.outcome(OutcomeRejectedAuthz) != 1 {
		t.Fatalf("outcomes parse=%v authz=%v", f.outcome(OutcomeRejectedParse), f.outcome(OutcomeRejectedAuthz))
	}

	// broker unreachable → UNAVAILABLE with RetryInfo.
	f.log.fail = ErrUnavailable
	_, err = f.submit(context.Background(), "s-alice", "look")
	if codeOf(t, err) != connect.CodeUnavailable || infoOf(t, err).GetReason() != ReasonReadOnly {
		t.Fatalf("unavailable: %v", err)
	}
	var ce *connect.Error
	errors.As(err, &ce)
	retry := false
	for _, d := range ce.Details() {
		if v, _ := d.Value(); v != nil {
			if _, ok := v.(*errdetails.RetryInfo); ok {
				retry = true
			}
		}
	}
	if !retry || ce.Message() != ReadOnlyMessage {
		t.Fatalf("no retryable indication: %v", err)
	}
	// deadline → DEADLINE_EXCEEDED.
	f.log.fail = ErrDeadline
	_, err = f.submit(context.Background(), "s-alice", "look")
	if codeOf(t, err) != connect.CodeDeadlineExceeded || infoOf(t, err).GetReason() != ReasonDeadline {
		t.Fatalf("deadline: %v", err)
	}
	f.log.fail = ErrPendingFull
	_, err = f.submit(context.Background(), "s-alice", "look")
	if codeOf(t, err) != connect.CodeResourceExhausted {
		t.Fatalf("pending full: %v", err)
	}
	if f.outcome(OutcomeUnavailable) != 1 || f.outcome(OutcomeDeadline) != 1 || f.outcome(OutcomePendingFull) != 1 {
		t.Fatalf("outcomes: unavailable=%v deadline=%v pending=%v", f.outcome(OutcomeUnavailable), f.outcome(OutcomeDeadline), f.outcome(OutcomePendingFull))
	}
	// Outside the taxonomy → INTERNAL, so a bug is reported as one.
	f.log.fail = errors.New("what")
	_, err = f.submit(context.Background(), "s-alice", "look")
	if codeOf(t, err) != connect.CodeInternal {
		t.Fatalf("unknown error: %v", err)
	}
}

// AC-8: past ingress.burst the Session is refused, survives, and produces
// nothing; the bucket refills at the rate; agents have their own rate.
func TestSubmit_RateLimit(t *testing.T) {
	f := newFixture(t, nil)
	for i := range 40 {
		if _, err := f.submit(context.Background(), "s-alice", "look"); err != nil {
			t.Fatalf("submit %d within burst: %v", i, err)
		}
	}
	_, err := f.submit(context.Background(), "s-alice", "look")
	if codeOf(t, err) != connect.CodeResourceExhausted || infoOf(t, err).GetReason() != ReasonRateLimited {
		t.Fatalf("41st: %v", err)
	}
	if f.outcome(OutcomeRateLimited) != 1 || len(f.log.records) != 40 {
		t.Fatalf("rate_limited=%v records=%d", f.outcome(OutcomeRateLimited), len(f.log.records))
	}
	// 20/s: after 100ms, two tokens.
	f.clock.step(100 * time.Millisecond)
	for i := range 2 {
		if _, err := f.submit(context.Background(), "s-alice", "look"); err != nil {
			t.Fatalf("refill %d: %v", i, err)
		}
	}
	if _, err := f.submit(context.Background(), "s-alice", "look"); codeOf(t, err) != connect.CodeResourceExhausted {
		t.Fatalf("third after refill: %v", err)
	}
	// An unrelated Session is not affected; an agent gets 100/s.
	f.bindings.Bind("s-agent", command.Binding{Actor: "npc-1", Zone: "town"})
	for i := range 40 {
		if _, err := f.submit(context.Background(), "s-agent", "look"); err != nil {
			t.Fatalf("agent %d: %v", i, err)
		}
	}
	f.clock.step(100 * time.Millisecond) // 10 tokens at 100/s
	for i := range 10 {
		if _, err := f.submit(context.Background(), "s-agent", "look"); err != nil {
			t.Fatalf("agent refill %d: %v", i, err)
		}
	}
	if _, err := f.submit(context.Background(), "s-agent", "look"); codeOf(t, err) != connect.CodeResourceExhausted {
		t.Fatalf("agent 11th after refill: %v", err)
	}
}

// AC-2 and the pending bound: Submits of one Session reach the log in
// the order they arrived, whatever goroutine each ran on, and past
// ingress.max_pending the excess is refused rather than queued.
func TestSubmit_OrderedAndBounded(t *testing.T) {
	f := newFixture(t, nil)
	f.log.gate = make(chan struct{})
	const n = 4 // == MaxPending
	var wg sync.WaitGroup
	results := make([]*gamev1.SubmitResponse, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Serialize the arrival order so it is known: each waits for
			// the previous to be queued.
			until(func() bool { return testutil.ToFloat64(f.in.Metrics().Pending) >= float64(i) })
			results[i], _ = f.submit(context.Background(), "s-alice", fmt.Sprintf("look %d", i))
		}()
	}
	waitFor(t, func() bool { return testutil.ToFloat64(f.in.Metrics().Pending) == n }, "all pending")
	// The fifth is refused while four are in flight.
	_, err := f.submit(context.Background(), "s-alice", "look")
	if codeOf(t, err) != connect.CodeResourceExhausted || infoOf(t, err).GetReason() != ReasonPendingFull {
		t.Fatalf("fifth: %v", err)
	}
	close(f.log.gate)
	wg.Wait()
	for i := range n {
		if results[i] == nil || results[i].GetAcceptedOffset() != int64(i) {
			t.Fatalf("submit %d landed at %v", i, results[i])
		}
	}
	if testutil.ToFloat64(f.in.Metrics().Pending) != 0 {
		t.Fatal("pending not released")
	}
}

// Inherited item 3: a Session whose Character is between Zones has its
// Intents held, then released in order to the new Zone's Partition on
// arrival; a stuck handoff surfaces as in_transit at the hold, and after
// that at once.
func TestSubmit_TransitHold(t *testing.T) {
	f := newFixture(t, nil)
	left := sim.Event{Type: sim.EvCharacterLeft, Scope: sim.ScopeRoom("town", "hall").With("alice"),
		Envelope: &gamev1.EventEnvelope{Payload: &gamev1.EventEnvelope_CharacterLeft{CharacterLeft: &gamev1.CharacterLeft{ZoneId: "town", RoomId: "hall", CharacterName: "alice", ToDirection: "north"}}}}
	arrived := sim.Event{Type: sim.EvCharacterArrived, Scope: sim.ScopeRoom("docks", "pier").With("alice"),
		Envelope: &gamev1.EventEnvelope{Payload: &gamev1.EventEnvelope_CharacterArrived{CharacterArrived: &gamev1.CharacterArrived{ZoneId: "docks", RoomId: "pier", CharacterName: "alice", FromDirection: "south"}}}}
	f.bindings.Publish(left)
	if _, _, inTransit := f.bindings.Lookup("s-alice"); !inTransit {
		t.Fatal("not in transit after CharacterLeft")
	}

	type result struct {
		resp *gamev1.SubmitResponse
		err  error
	}
	results := make([]result, 2)
	var wg sync.WaitGroup
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			until(func() bool { return testutil.ToFloat64(f.in.Metrics().Pending) >= float64(i) })
			r, err := f.submit(context.Background(), "s-alice", "look")
			results[i] = result{r, err}
		}()
	}
	// Both queued before the arrival: the first in the hold, the second
	// behind it — otherwise the second could go looking for a Pending of
	// one after the first had already left.
	waitFor(t, func() bool {
		return testutil.ToFloat64(f.in.Metrics().Held) == 1 && testutil.ToFloat64(f.in.Metrics().Pending) == 2
	}, "one intent held, the second queued behind it")
	if len(f.log.records) != 0 {
		t.Fatal("produced during transit")
	}
	f.bindings.Publish(arrived)
	wg.Wait()
	for i, r := range results {
		if r.err != nil || r.resp.GetPartition() != sim.PartitionFor("docks") || r.resp.GetAcceptedOffset() != int64(i) {
			t.Fatalf("held intent %d: %v %v", i, r.resp, r.err)
		}
	}
	if f.log.records[0].GetZoneId() != "docks" {
		t.Fatalf("released to %q", f.log.records[0].GetZoneId())
	}

	// A stuck handoff: the hold runs out from the CharacterLeft, not from
	// the Submit; then no more waiting.
	f.bindings.Publish(left)
	f.clock.step(3 * time.Second)
	began := time.Now()
	_, err := f.submit(context.Background(), "s-alice", "look")
	if codeOf(t, err) != connect.CodeUnavailable || infoOf(t, err).GetReason() != command.CodeInTransit {
		t.Fatalf("expired hold: %v", err)
	}
	if time.Since(began) > time.Second {
		t.Fatal("waited past the expired hold")
	}
	if f.outcome(OutcomeInTransit) != 1 {
		t.Fatalf("in_transit = %v", f.outcome(OutcomeInTransit))
	}
	// Arrival clears it.
	f.bindings.Publish(arrived)
	if _, err := f.submit(context.Background(), "s-alice", "look"); err != nil {
		t.Fatalf("after arrival: %v", err)
	}
}

// The hold also ends with the caller: a client that gives up mid-hold is
// canceled, not rejected, and nothing is counted against the Intent.
func TestSubmit_TransitHoldCanceled(t *testing.T) {
	f := newFixture(t, nil)
	f.bindings.Publish(sim.Event{Type: sim.EvCharacterLeft, Scope: sim.ScopeEntities("alice"),
		Envelope: &gamev1.EventEnvelope{Payload: &gamev1.EventEnvelope_CharacterLeft{CharacterLeft: &gamev1.CharacterLeft{ZoneId: "town", CharacterName: "alice"}}}})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		until(func() bool { return testutil.ToFloat64(f.in.Metrics().Held) == 1 })
		cancel()
	}()
	_, err := f.submit(ctx, "s-alice", "look")
	if codeOf(t, err) != connect.CodeCanceled {
		t.Fatalf("canceled hold: %v", err)
	}
	if f.outcome(OutcomeCanceled) != 1 || testutil.ToFloat64(f.in.Metrics().Held) != 0 {
		t.Fatalf("canceled=%v held=%v", f.outcome(OutcomeCanceled), testutil.ToFloat64(f.in.Metrics().Held))
	}
	if got := testutil.ToFloat64(f.in.Metrics().Submits.WithLabelValues(OutcomeInTransit)); got != 0 {
		t.Fatalf("a cancel was counted as a rejection: %v", got)
	}
}

// A Session's end drops its queue, its bucket, and its binding.
func TestSubmit_SessionCleanup(t *testing.T) {
	f := newFixture(t, nil)
	ended := make(chan struct{})
	if _, err := f.in.submit(context.Background(), "s-alice", player, ended, &gamev1.SubmitRequest{Raw: "look"}); err != nil {
		t.Fatal(err)
	}
	close(ended)
	waitFor(t, func() bool { _, bound, _ := f.bindings.Lookup("s-alice"); return !bound }, "binding dropped")
	f.in.mu.Lock()
	_, kept := f.in.sessions["s-alice"]
	f.in.mu.Unlock()
	if kept {
		t.Fatal("session state kept after its context ended")
	}
	_, err := f.submit(context.Background(), "s-alice", "look")
	if codeOf(t, err) != connect.CodePermissionDenied {
		t.Fatalf("after cleanup: %v", err)
	}
}

// Bind replaces: a Character bound to a second Session leaves the first.
func TestBindings_Replace(t *testing.T) {
	b := NewBindings(time.Second, nil, nil)
	b.Bind("s1", command.Binding{Actor: "alice", Zone: "town"})
	b.Bind("s2", command.Binding{Actor: "alice", Zone: "town"})
	if _, bound, _ := b.Lookup("s1"); bound {
		t.Fatal("s1 still bound")
	}
	if got, _ := b.Binding(context.Background(), "s2"); got.Actor != "alice" {
		t.Fatalf("s2 = %v", got)
	}
	// Unbind during transit releases the waiter as unbound.
	b.Publish(sim.Event{Type: sim.EvCharacterLeft, Scope: sim.ScopeEntities("alice"),
		Envelope: &gamev1.EventEnvelope{Payload: &gamev1.EventEnvelope_CharacterLeft{CharacterLeft: &gamev1.CharacterLeft{CharacterName: "alice"}}}})
	done := make(chan error, 1)
	go func() { _, err := b.Binding(context.Background(), "s2"); done <- err }()
	time.Sleep(10 * time.Millisecond)
	b.Unbind("s2")
	if err := <-done; !errors.Is(err, command.ErrNoBinding) {
		t.Fatalf("after unbind: %v", err)
	}
}

// The partitioner: two Rooms of one Zone select one Partition, a Zone
// always the same one, and the choice is FNV-1a of the ZoneID — not a
// library default.
func TestPartition_ByZone(t *testing.T) {
	for _, zone := range []sim.ZoneID{"town", "docks", "forest-of-teeth"} {
		p := sim.PartitionFor(zone)
		if p < 0 || p >= sim.PartitionCount || p != sim.PartitionFor(zone) {
			t.Fatalf("%s → %d", zone, p)
		}
	}
	f := newFixture(t, nil)
	f.bindings.Bind("s-a", command.Binding{Actor: "a", Zone: "town"})
	f.bindings.Bind("s-b", command.Binding{Actor: "b", Zone: "town"})
	ra, _ := f.in.submit(context.Background(), "s-a", player, nil, &gamev1.SubmitRequest{Raw: "look"})
	rb, _ := f.in.submit(context.Background(), "s-b", player, nil, &gamev1.SubmitRequest{Raw: "look"})
	if ra.GetPartition() != rb.GetPartition() || ra.GetPartition() != sim.PartitionFor("town") {
		t.Fatalf("partitions %d %d", ra.GetPartition(), rb.GetPartition())
	}
}

// until spins on cond from a goroutine that may not fail the test.
func until(cond func() bool) {
	for !cond() {
		time.Sleep(time.Millisecond)
	}
}

// waitFor is eventually.True with this package's in-process deadline.
func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	eventually.True(t, 5*time.Second, what, cond)
}
