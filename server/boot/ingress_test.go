// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package boot

import (
	"context"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	gamev1 "github.com/valesordev/andara/gen/go/andara/game/v1"
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
