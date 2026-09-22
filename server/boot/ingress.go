// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package boot

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/valesordev/andara/server/auth"
	"github.com/valesordev/andara/server/command"
	"github.com/valesordev/andara/server/ingress"
	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/tickloop"

	logv1 "github.com/valesordev/andara/gen/go/andara/log/v1"
)

// StartIngress builds the Submit path (AW-SRV-010): the routing table,
// the producer for the configured source, and the pipeline over them.
// It runs after LoadVerbs and OpenAccounts and before the gateway is
// built, which takes it as its Ingress seam. The producer is closed by
// CloseIngress after the gateway has drained.
func (rt *Runtime) StartIngress(ctx context.Context) error {
	cfg := rt.Cfg
	if rt.Verbs == nil {
		return fmt.Errorf("ingress: verb table not loaded")
	}
	rate, err := auth.ParseRateLimit(cfg.IngressRateLimit)
	if err != nil {
		return fmt.Errorf("ingress.rate_limit: %w", err)
	}
	agentRate, err := auth.ParseRateLimit(cfg.IngressAgentRateLimit)
	if err != nil {
		return fmt.Errorf("ingress.agent_rate_limit: %w", err)
	}
	metrics := ingress.NewMetrics(rt.Tel.Reg)
	rt.Bindings = ingress.NewBindings(cfg.IngressTransitHold, nil, metrics.Held)

	var producer command.Producer
	switch cfg.SimSource {
	case "kafka":
		kp, err := ingress.NewKafkaProducer(ingress.ProducerOptions{
			Brokers:  cfg.KafkaBrokers,
			ClientID: cfg.ServiceName,
			Deadline: cfg.IngressProduceDeadline,
			Metrics:  metrics,
			Log:      rt.Tel.Log,
			Tracer:   rt.Tel.Tracer,
		})
		if err != nil {
			return err
		}
		rt.producer = kp
		producer = kp
	case "memory":
		// The same loopback the tick's cross-Zone Commands use: a Submit
		// lands on the in-memory source the loop will consume.
		rt.memSource = tickloop.NewMemorySource()
		producer = &memoryProducer{source: rt.memSource}
	default:
		return fmt.Errorf("sim.source %q is not kafka or memory", cfg.SimSource)
	}

	rt.commandLog = producer

	var audit *auth.Auditor
	if rt.Accounts != nil {
		audit = rt.Accounts.Auditor()
		// One deadline for a write to the log, whichever topic: an audit
		// record behind a refused Submit waits as long as a produce would.
		audit.WriteTimeout = cfg.IngressProduceDeadline
	}
	rt.Ingress = ingress.New(ingress.Options{
		Pipeline: &command.Pipeline{
			Table:      rt.Verbs,
			MaxBytes:   cfg.MaxIntentBytes,
			Authorizer: &auth.Authorizer{Table: rt.Verbs.Roles(), Audit: audit},
			Bindings:   rt.Bindings,
			Log:        producer,
			Metrics:    rt.Commands,
			Tracer:     rt.Tel.Tracer,
			Logger:     rt.Tel.Log,
		},
		Bindings:          rt.Bindings,
		RateLimit:         rate,
		AgentRateLimit:    agentRate,
		Burst:             cfg.IngressBurst,
		MaxPending:        cfg.IngressMaxPending,
		IdempotencyWindow: cfg.IngressIdempotencyWindow,
		Metrics:           metrics,
		Log:               rt.Tel.Log,
		Tracer:            rt.Tel.Tracer,
	})
	rt.Tel.Log.LogAttrs(ctx, slog.LevelInfo, "command ingress configured",
		slog.String("source", cfg.SimSource),
		slog.String("rate_limit", rate.String()), slog.String("agent_rate_limit", agentRate.String()),
		slog.Int("burst", cfg.IngressBurst), slog.Int("max_pending", cfg.IngressMaxPending),
		slog.String("produce_deadline", cfg.IngressProduceDeadline.String()),
		slog.String("transit_hold", cfg.IngressTransitHold.String()),
		slog.String("idempotency_window", cfg.IngressIdempotencyWindow.String()),
	)
	return nil
}

// CloseIngress flushes and closes the producer. After the gateway drain:
// nothing produces once the Protocol is down — except the teardowns the
// drain started, whose UnbindCharacters are waited for first (AW-SRV-014).
func (rt *Runtime) CloseIngress() error {
	if rt.Roster != nil {
		rt.Roster.Wait()
	}
	if rt.producer == nil {
		return nil
	}
	return rt.producer.Close()
}

// memoryProducer is command.Producer over the in-memory source for
// sim.source=memory: Partition by sim.PartitionFor, offset from the
// source, no broker.
type memoryProducer struct {
	mu     sync.Mutex
	source *tickloop.MemorySource
}

func (m *memoryProducer) Produce(_ context.Context, cmd *logv1.LoggedCommand) (command.Accepted, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.source.Push(cmd)
	return command.Accepted{Partition: sim.PartitionFor(sim.ZoneID(cmd.GetZoneId())), Offset: r.Offset}, nil
}
