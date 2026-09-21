// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package boot

import (
	"context"
	"log/slog"

	"github.com/valesordev/andara/server/auth"
	"github.com/valesordev/andara/server/egress"
	"github.com/valesordev/andara/server/events"
	"github.com/valesordev/andara/server/sim"
)

// StartEvents builds the Event fan-out (AW-SRV-004): Publish is one
// enqueue from the tick; scoping, redaction form, and delivery happen on
// the Hub's goroutine behind bounded per-subscriber buffers. It runs
// after OpenAccounts, whose auditor records a World-scope subscription,
// and before the gateway, whose Subscribe streams from it. StartTickLoop
// makes the Engine publish to it.
func (rt *Runtime) StartEvents() {
	var audit *auth.Auditor
	if rt.Accounts != nil {
		audit = rt.Accounts.Auditor()
	}
	rt.Events = events.New(events.Options{
		Buffer:         rt.Cfg.SubscriberBuffer,
		MaxSubscribers: rt.Cfg.MaxSubscribers,
		Audit:          audit,
		Log:            rt.Tel.Log,
		Tracer:         rt.Tel.Tracer,
		Registry:       rt.Tel.Reg,
	})
}

// StartEgress builds the Subscribe path (AW-SRV-011) over the fan-out:
// per-Session retained history for a resume, the stream's unsent bound,
// heartbeats. It runs after StartEvents and StartIngress — a stream
// perceives from where the routing table says the Session's Character
// is — and before the gateway, which takes it as its Egress seam.
func (rt *Runtime) StartEgress(ctx context.Context) {
	cfg := rt.Cfg
	if rt.Events == nil {
		rt.StartEvents()
	}
	var observers egress.Observers
	if rt.Bindings != nil {
		bindings := rt.Bindings
		observers = egress.ObserverFunc(func(sessionID string) (events.Observer, bool) {
			b, bound, _ := bindings.Lookup(sessionID)
			if !bound {
				return events.Observer{}, false
			}
			obs := events.Observer{Entity: b.Actor}
			if b.Room != "" {
				obs.Room = sim.RoomRef{Zone: b.Zone, Room: b.Room}
			}
			return obs, true
		})
	}
	rt.Egress = egress.New(egress.Options{
		Hub:               rt.Events,
		Observers:         observers,
		Buffer:            cfg.EgressBuffer,
		ResumeWindow:      cfg.EgressResumeWindow,
		HeartbeatInterval: cfg.HeartbeatInterval,
		Log:               rt.Tel.Log,
		Tracer:            rt.Tel.Tracer,
		Registry:          rt.Tel.Reg,
	})
	if rt.Bindings != nil {
		// A Character bound or unbound moves where the Session's stream
		// perceives from.
		rt.Bindings.OnChange = rt.Egress.Rebind
	}
	rt.Tel.Log.LogAttrs(ctx, slog.LevelInfo, "event egress configured",
		slog.Int("buffer", cfg.EgressBuffer), slog.Int("resume_window", cfg.EgressResumeWindow),
		slog.String("heartbeat_interval", cfg.HeartbeatInterval.String()),
		slog.Int("subscriber_buffer", cfg.SubscriberBuffer), slog.Int("max_subscribers", cfg.MaxSubscribers),
	)
}
