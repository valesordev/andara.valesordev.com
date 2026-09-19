// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package boot

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	logv1 "github.com/valesordev/andara/gen/go/andara/log/v1"
	"github.com/valesordev/andara/server/auth"
	"github.com/valesordev/andara/server/events"
	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/tickloop"
)

// StartTickLoop builds the engine over the loaded World and Templates and
// the loop over the configured source, and returns the loop ready to Run.
// Without a snapshot (AW-SRV-006) the engine starts at tick 0 and the
// consumer at offset 0 on every assigned Partition: a restart replays the
// log from its beginning, which is correct and slow.
func (rt *Runtime) StartTickLoop(ctx context.Context) (*tickloop.Loop, error) {
	cfg := rt.Cfg
	if rt.World == nil {
		return nil, fmt.Errorf("tick loop: no World loaded")
	}
	engineCfg := sim.Config{
		Seed:       cfg.SimSeed,
		Partitions: cfg.SimPartitions,
		// The verb handlers (AW-SRV-003). A logged arm with no handler is
		// rejected with unsupported_command and the offsets advance, which
		// is what keeps the World replayable through a binary behind its
		// content.
		Handlers: sim.Handlers(),
	}
	engine := sim.NewEngine(rt.World, rt.Templates, engineCfg)

	// The fan-out is the Engine's one sink (AW-SRV-004): Publish is one
	// enqueue from the tick; scoping, redaction form, and delivery happen
	// on the Hub's goroutine behind bounded per-subscriber buffers.
	var audit *auth.Auditor
	if rt.Accounts != nil {
		audit = rt.Accounts.Auditor()
	}
	rt.Events = events.New(events.Options{
		Buffer:         cfg.SubscriberBuffer,
		MaxSubscribers: cfg.MaxSubscribers,
		Audit:          audit,
		Log:            rt.Tel.Log,
		Tracer:         rt.Tel.Tracer,
		Registry:       rt.Tel.Reg,
	})

	var (
		source    tickloop.Source
		publisher tickloop.Publisher
		loop      *tickloop.Loop
		err       error
	)
	switch cfg.SimSource {
	case "memory":
		rt.Tel.Log.LogAttrs(ctx, slog.LevelWarn, "sim.source=memory: the World ticks with no Command input and nothing is published")
		// The source StartIngress built, so Submits land on it; cross-Zone
		// Commands loop back into it too, so an arrival resolves on a later
		// tick here as it would through the broker.
		ms := rt.memSource
		if ms == nil {
			ms = tickloop.NewMemorySource()
		}
		source, publisher = ms, &tickloop.MemoryPublisher{OnProduce: func(c *logv1.LoggedCommand) { ms.Push(c) }}
	case "kafka":
		// Recovery (ADR-0002 §4): replay the recorded boundaries before
		// going live, so the World that starts ticking is the one that was
		// running, verified tick by tick against its own hashes.
		rctx, rspan := rt.Tel.Tracer.Start(ctx, "sim.recover")
		replayed, err := tickloop.Recover(rctx, cfg.KafkaBrokers, tickloop.CommandsTopic, tickloop.EventsTopic, engine)
		rspan.End()
		if err != nil {
			return nil, fmt.Errorf("recovery: %w", err)
		}
		rt.Tel.Log.LogAttrs(ctx, slog.LevelInfo, "recovered from the log",
			slog.Int("ticks_replayed", replayed),
			slog.Uint64("tick", uint64(engine.Tick())),
		)
		source, err = tickloop.NewKafkaSource(ctx, tickloop.KafkaSourceOptions{
			Brokers:  cfg.KafkaBrokers,
			Group:    "andara-sim-" + cfg.Environment,
			ClientID: cfg.ServiceName,
			Start:    engine.State().Offsets,
		})
		if err != nil {
			return nil, err
		}
		kp, err := tickloop.NewKafkaPublisher(ctx, cfg.KafkaBrokers, cfg.ServiceName)
		if err != nil {
			_ = source.Close()
			return nil, err
		}
		publisher = kp
		defer func() {
			// Delivery failures arrive after Publish returned; count them
			// against the loop's metric by topic.
			if loop != nil {
				kp.OnBoundaryLost = func(tick sim.Tick, err error) {
					loop.Metrics().PublishFailures.WithLabelValues("boundary").Inc()
					rt.Tel.Log.LogAttrs(ctx, slog.LevelError, "tick boundary lost: this process publishes no more boundaries; the next restart recovers exactly to the last delivered one and re-batches after it",
						slog.Uint64("tick", uint64(tick)), slog.String("detail", err.Error()))
				}
				kp.OnBoundaryAcked = func(_ sim.Tick, lag time.Duration) {
					rt.Events.Metrics().PublishLag.Set(lag.Seconds())
				}
				kp.OnFailure = func(topic string, err error) {
					kind := "events"
					if topic == tickloop.CommandsTopic {
						kind = "commands"
					}
					loop.Metrics().PublishFailures.WithLabelValues(kind).Inc()
					rt.Tel.Log.LogAttrs(ctx, slog.LevelWarn, "record never acknowledged", slog.String("topic", topic), slog.String("detail", err.Error()))
				}
			}
		}()
	default:
		return nil, fmt.Errorf("sim.source %q is not kafka or memory", cfg.SimSource)
	}

	// Subscribed after recovery, so replayed Events — history, already
	// delivered by the process that first emitted them — are not fanned
	// out or counted again. The routing table follows the same Events
	// (AW-SRV-010): a Character's Session moves Partition when it does.
	engine.Subscribe(rt.Events)
	if rt.Bindings != nil {
		engine.Subscribe(rt.Bindings)
	}

	loop, err = tickloop.New(tickloop.Options{
		Engine:          engine,
		Source:          source,
		Publisher:       publisher,
		TickRate:        cfg.SimTickRate,
		TickBudget:      cfg.SimTickBudget,
		MaxPerTick:      cfg.SimMaxPerTick,
		DrainTimeout:    cfg.SimDrainTimeout,
		CheckpointEvery: cfg.SimCheckpointEveryTicks,
		Log:             rt.Tel.Log,
		Tracer:          rt.Tel.Tracer,
		Registry:        rt.Tel.Reg,
		Commands:        rt.Commands,
	})
	if err != nil {
		_ = source.Close()
		_ = publisher.Close()
		return nil, err
	}
	// The loop times Zones for the engine; the engine was built before the
	// loop existed, so attach it now.
	engine.SetObserver(loop)
	rt.Engine = engine
	rt.Tel.Log.LogAttrs(ctx, slog.LevelInfo, "tick loop configured",
		slog.String("source", cfg.SimSource),
		slog.Int("tick_rate", cfg.SimTickRate),
		slog.String("tick_budget", cfg.SimTickBudget.String()),
		slog.Int("partitions", len(cfg.SimPartitions)),
		slog.Uint64("seed", engine.State().Seed),
	)
	return loop, nil
}
