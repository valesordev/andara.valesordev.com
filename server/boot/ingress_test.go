// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package boot

import (
	"context"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	gamev1 "github.com/valesordev/andara/gen/go/andara/game/v1"
	"github.com/valesordev/andara/internal/eventually"
	"github.com/valesordev/andara/server/auth"
	"github.com/valesordev/andara/server/command"
	"github.com/valesordev/andara/server/config"
	"github.com/valesordev/andara/server/events"
	"github.com/valesordev/andara/server/gateway"
	"github.com/valesordev/andara/server/sim"
)

// AW-SRV-010 at the boot layer, sim.source=memory: a Submit through the
// ingress lands on the in-memory source the tick loop consumes, and its
// outcome comes back through the Event hub — the whole pipeline in one
// process with no broker.
func TestStartIngress_MemoryLoopback(t *testing.T) {
	rt, _, _ := recordingRuntime(t, fixture(t, "valid"), false)
	rt.Cfg.SimSource = "memory"
	rt.Cfg.SimTickRate = 50
	rt.Cfg.SimTickBudget = 10 * time.Millisecond
	rt.Cfg.SimMaxPerTick = config.DefaultSimMaxPerTick
	rt.Cfg.SimDrainTimeout = time.Second
	rt.Cfg.SimPartitions = allPartitionsForTest()
	rt.Cfg.SimCheckpointEveryTicks = 10
	rt.Cfg.SubscriberBuffer = 64
	rt.Cfg.MaxSubscribers = 10
	rt.Cfg.MaxIntentBytes = 4096
	rt.Cfg.IngressRateLimit = config.DefaultIngressRateLimit
	rt.Cfg.IngressAgentRateLimit = config.DefaultIngressAgentRateLimit
	rt.Cfg.IngressBurst = config.DefaultIngressBurst
	rt.Cfg.IngressMaxPending = config.DefaultIngressMaxPending
	rt.Cfg.IngressProduceDeadline = config.DefaultIngressProduceDeadline
	rt.Cfg.IngressTransitHold = config.DefaultIngressTransitHold
	ctx := context.Background()
	if code := rt.LoadContent(ctx); code != ExitOK {
		t.Fatalf("load content: exit %d", code)
	}
	if err := rt.LoadVerbs(ctx); err != nil {
		t.Fatal(err)
	}
	if err := rt.StartIngress(ctx); err != nil {
		t.Fatal(err)
	}
	if rt.Ingress == nil || rt.Bindings == nil || rt.memSource == nil {
		t.Fatal("ingress not built")
	}
	loop, err := rt.StartTickLoop(ctx)
	if err != nil {
		t.Fatal(err)
	}
	loopCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- loop.Run(loopCtx) }()
	if code := rt.ReconcileContent(ctx); code != ExitOK {
		t.Fatalf("reconcile: exit %d", code)
	}

	// A Session bound to a Character the World does not hold: the
	// Command is accepted and ordered, and the sim's answer is the Event.
	rt.Bindings.Bind("s-1", command.Binding{Actor: "ghost", Zone: "town"})
	sub, err := rt.Events.Subscribe(ctx, events.Subscriber{Observer: events.Observer{Entity: "ghost"}, SessionID: "s-1"})
	if err != nil {
		t.Fatal(err)
	}
	principal := auth.Principal{AccountID: "acct", Roles: []auth.Role{auth.RolePlayer}}
	resp, err := rt.Ingress.Submit(ctx, sessionFor("s-1", principal), &gamev1.SubmitRequest{SessionId: "s-1", Raw: "look", ClientRef: "c1"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetPartition() != sim.PartitionFor("town") || resp.GetAcceptedOffset() != 0 {
		t.Fatalf("resp = %v", resp)
	}
	select {
	case d := <-sub.Events():
		if d.Type != sim.EvCommandRejected || d.Envelope.GetCommandRejected().GetCode() != "actor_not_found" || d.Envelope.GetClientRef() != "c1" {
			t.Fatalf("delivery = %v", d.Envelope)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the Submit never came back through the tick")
	}
	if got := testutil.ToFloat64(rt.Ingress.Metrics().Submits.WithLabelValues("produced")); got != 1 {
		t.Fatalf("produced = %v", got)
	}
	stop()
	if err := <-done; err != nil {
		t.Fatalf("loop: %v", err)
	}
	rt.Events.Close()
}

// sessionFor is a Session with no connection behind it: enough for the
// ingress, which reads its ID, Principal, and lifetime.
func sessionFor(id string, p auth.Principal) *gateway.Session {
	return &gateway.Session{ID: id, Principal: p}
}

func allPartitionsForTest() []int32 {
	out := make([]int32, sim.PartitionCount)
	for i := range out {
		out[i] = int32(i)
	}
	return out
}

// AW-SRV-011 at the boot layer: the egress built over the fan-out
// streams the Submit's outcome to the Session that sent it, perceiving
// from where the routing table put its Character.
func TestStartEgress_MemoryLoopback(t *testing.T) {
	rt, _, _ := recordingRuntime(t, fixture(t, "valid"), false)
	rt.Cfg.SimSource = "memory"
	rt.Cfg.SimTickRate = 50
	rt.Cfg.SimTickBudget = 10 * time.Millisecond
	rt.Cfg.SimMaxPerTick = config.DefaultSimMaxPerTick
	rt.Cfg.SimDrainTimeout = time.Second
	rt.Cfg.SimPartitions = allPartitionsForTest()
	rt.Cfg.SimCheckpointEveryTicks = 10
	rt.Cfg.SubscriberBuffer = 64
	rt.Cfg.MaxSubscribers = 10
	rt.Cfg.MaxIntentBytes = 4096
	rt.Cfg.IngressRateLimit = config.DefaultIngressRateLimit
	rt.Cfg.IngressAgentRateLimit = config.DefaultIngressAgentRateLimit
	rt.Cfg.IngressBurst = config.DefaultIngressBurst
	rt.Cfg.IngressMaxPending = config.DefaultIngressMaxPending
	rt.Cfg.IngressProduceDeadline = config.DefaultIngressProduceDeadline
	rt.Cfg.IngressTransitHold = config.DefaultIngressTransitHold
	rt.Cfg.EgressBuffer = 16
	rt.Cfg.EgressResumeWindow = 32
	rt.Cfg.HeartbeatInterval = time.Hour
	ctx := context.Background()
	if code := rt.LoadContent(ctx); code != ExitOK {
		t.Fatalf("load content: exit %d", code)
	}
	if err := rt.LoadVerbs(ctx); err != nil {
		t.Fatal(err)
	}
	rt.StartEvents()
	if err := rt.StartIngress(ctx); err != nil {
		t.Fatal(err)
	}
	rt.StartEgress(ctx)
	if rt.Egress == nil || rt.Bindings.OnChange == nil {
		t.Fatal("egress not built or not wired to the routing table")
	}
	loop, err := rt.StartTickLoop(ctx)
	if err != nil {
		t.Fatal(err)
	}
	loopCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- loop.Run(loopCtx) }()
	if code := rt.ReconcileContent(ctx); code != ExitOK {
		t.Fatalf("reconcile: exit %d", code)
	}

	principal := auth.Principal{AccountID: "acct", Roles: []auth.Role{auth.RolePlayer}}
	sess := sessionFor("s-1", principal)
	recv := make(chan *gamev1.EventEnvelope, 16)
	streamDone := make(chan error, 1)
	streamCtx, cancelStream := context.WithCancel(ctx)
	go func() {
		streamDone <- rt.Egress.SubscribeWith(streamCtx, sess, &gamev1.SubscribeRequest{SessionId: "s-1"}, sendFunc(func(env *gamev1.EventEnvelope) error { recv <- env; return nil }))
	}()
	waitFor(t, func() bool { return testutil.ToFloat64(rt.Egress.Metrics().Streams) == 1 }, "subscribed")
	// The stream opens with Attached (AW-SRV-011 AC-11); the rebind below
	// sends no second one.
	select {
	case env := <-recv:
		if env.GetAttached() == nil {
			t.Fatalf("first frame = %v, want Attached", env)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no Attached")
	}
	// Binding after subscribing: the routing table tells the egress, which
	// re-reads where the Session perceives from.
	rt.Bindings.Bind("s-1", command.Binding{Actor: "ghost", Zone: "town"})
	waitFor(t, func() bool {
		return testutil.ToFloat64(rt.Events.Metrics().Drops.WithLabelValues(events.ReasonUnsubscribed)) == 1
	}, "resubscribed on bind")
	if _, err := rt.Ingress.Submit(ctx, sess, &gamev1.SubmitRequest{SessionId: "s-1", Raw: "look", ClientRef: "c1"}); err != nil {
		t.Fatal(err)
	}
	select {
	case env := <-recv:
		if env.GetCommandRejected().GetCode() != "actor_not_found" || env.GetClientRef() != "c1" {
			t.Fatalf("stream got %v", env)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the Submit's outcome never reached the stream")
	}
	cancelStream()
	<-streamDone
	if rt.Egress.LastTick() == 0 {
		t.Error("the loop's ticks never reached the egress; a heartbeat would report tick 0")
	}
	stop()
	if err := <-done; err != nil {
		t.Fatalf("loop: %v", err)
	}
	rt.Events.Close()
}

type sendFunc func(*gamev1.EventEnvelope) error

func (f sendFunc) Send(env *gamev1.EventEnvelope) error { return f(env) }

// waitFor is eventually.True with this package's in-process deadline.
func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	eventually.True(t, 5*time.Second, what, cond)
}

// AW-SRV-028: the first retry of a lost Arrive should land inside the
// Gateway's hold for the crossing (ingress.transit_hold). Startup warns, never
// refuses, when sim.handoff_retry_ticks at sim.tick_rate is not below a
// non-zero hold.
func TestHandoffRetryOutsideHold(t *testing.T) {
	for _, c := range []struct {
		name        string
		ticks, rate int
		hold        time.Duration
		wantFirst   time.Duration
		wantOutside bool
	}{
		{"the defaults: 10 ticks at 10 Hz is 1 s, inside the 2 s hold", 10, 10, 2 * time.Second, time.Second, false},
		{"20 ticks at 10 Hz is the hold exactly: not below it", 20, 10, 2 * time.Second, 2 * time.Second, true},
		{"a faster tick rate shrinks the same ticks", 10, 100, 2 * time.Second, 100 * time.Millisecond, false},
		{"a slower tick rate stretches them past the hold", 10, 2, 2 * time.Second, 5 * time.Second, true},
		{"a zero hold holds nothing, so there is nothing to be outside", 100, 1, 0, 0, false},
	} {
		first, _, outside := handoffRetryOutsideHold(c.ticks, c.rate, c.hold)
		if outside != c.wantOutside || (c.hold > 0 && first != c.wantFirst) {
			t.Errorf("%s: first %s outside %v, want %s %v", c.name, first, outside, c.wantFirst, c.wantOutside)
		}
	}
}

// The three handoff retry keys reach the Engine's config: a key dropped from
// the wiring would run the defaults silently (AW-SRV-028).
func TestEngineConfigCarriesTheHandoffRetryKeys(t *testing.T) {
	cfg := config.Config{SimSeed: 5, SimPartitions: []int32{1, 2}, SimHandoffRetryTicks: 4, SimHandoffRetryMaxTicks: 40, SimHandoffRetryBatch: 9}
	got := engineConfig(cfg, nil)
	if got.HandoffRetryTicks != 4 || got.HandoffRetryMaxTicks != 40 || got.HandoffRetryBatch != 9 {
		t.Fatalf("engine config retry keys = %d %d %d, want 4 40 9", got.HandoffRetryTicks, got.HandoffRetryMaxTicks, got.HandoffRetryBatch)
	}
	if got.Seed != 5 || len(got.Partitions) != 2 || got.Handlers == nil {
		t.Fatalf("the rest of the engine config was lost: %+v", got)
	}
}
