// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package boot

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	logv1 "github.com/valesordev/andara/gen/go/andara/log/v1"
	"github.com/valesordev/andara/server/config"
	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/store"
	"github.com/valesordev/andara/server/tickloop"
)

// StartTickLoop builds the engine over the loaded World and Templates and
// the loop over the configured source, and returns the loop ready to Run.
// The engine still starts at tick 0 and the consumer at offset 0 on every
// assigned Partition: AW-SRV-006 writes snapshots, and AW-SRV-007 is what
// recovers from one. Until then a restart replays the log from its
// beginning, which is correct and slow.
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

	// The fan-out is the Engine's one sink (AW-SRV-004). Built by
	// StartEvents before the gateway, which streams from it (AW-SRV-011);
	// here for a boot that has no gateway.
	if rt.Events == nil {
		rt.StartEvents()
	}

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

	// Snapshots (AW-SRV-006). Built after the source and publisher so a
	// failure here closes them, and given the publisher as its manifest
	// writer when there is one — the memory source has none, and a round
	// without a manifest is a round that wrote its objects and skipped an
	// audit record.
	snapshotter, err := newSnapshotter(cfg, rt, publisher)
	if err != nil {
		_ = source.Close()
		_ = publisher.Close()
		return nil, err
	}

	loop, err = tickloop.New(tickloop.Options{
		Engine:          engine,
		Source:          source,
		Publisher:       publisher,
		Snapshotter:     snapshotter,
		TickRate:        cfg.SimTickRate,
		TickBudget:      cfg.SimTickBudget,
		MaxPerTick:      cfg.SimMaxPerTick,
		DrainTimeout:    cfg.SimDrainTimeout,
		CheckpointEvery: cfg.SimCheckpointEveryTicks,
		Log:             rt.Tel.Log,
		Tracer:          rt.Tel.Tracer,
		Registry:        rt.Tel.Reg,
		Commands:        rt.Commands,
		OnTick:          rt.onTick(),
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
	// The bodies as recovery left them; the loop keeps the gauge current.
	rt.observeCharacters(engine)
	rt.Tel.Log.LogAttrs(ctx, slog.LevelInfo, "tick loop configured",
		slog.String("source", cfg.SimSource),
		slog.Int("tick_rate", cfg.SimTickRate),
		slog.String("tick_budget", cfg.SimTickBudget.String()),
		slog.Int("partitions", len(cfg.SimPartitions)),
		slog.Uint64("seed", engine.State().Seed),
	)
	rt.Tel.Log.LogAttrs(ctx, slog.LevelInfo, "snapshots configured",
		slog.Bool("enabled", snapshotter.Enabled()),
		slog.String("store", cfg.SnapshotStore),
		slog.String("interval", cfg.SnapshotInterval.String()),
	)
	return loop, nil
}

// newSnapshotter builds the round runner from the snapshot.* configuration. A
// zero snapshot.interval yields a runner that never takes a round and needs no
// store, which is what sim.source=memory and a development process want.
func newSnapshotter(cfg config.Config, rt *Runtime, publisher tickloop.Publisher) (*tickloop.Snapshotter, error) {
	o := tickloop.SnapshotOptions{
		Interval:      cfg.SnapshotInterval,
		MaxStall:      cfg.SnapshotMaxStall,
		UploadTimeout: cfg.SnapshotUploadTimeout,
		Log:           rt.Tel.Log,
		Tracer:        rt.Tel.Tracer,
		Registry:      rt.Tel.Reg,
	}
	if cfg.SnapshotInterval > 0 {
		ws, err := store.Open(store.Options{
			Kind:       cfg.SnapshotStore,
			FSPath:     cfg.SnapshotFSPath,
			S3Bucket:   cfg.SnapshotS3Bucket,
			S3Endpoint: cfg.SnapshotS3Endpoint,
		})
		if err != nil {
			return nil, err
		}
		o.Store = ws
		if mp, ok := publisher.(tickloop.ManifestPublisher); ok {
			o.Manifest = mp
		}
	}
	return tickloop.NewSnapshotter(o)
}

// onTick is what the loop tells after every tick: the egress, so a
// Heartbeat on a quiet World carries the Tick that just completed, and
// the roster's body gauge, on a tick that applied something (nothing
// else moves a body). Nil when there is neither.
func (rt *Runtime) onTick() func(sim.StepResult, time.Duration) {
	if rt.Egress == nil && rt.Roster == nil {
		return nil
	}
	eg := rt.Egress
	return func(res sim.StepResult, _ time.Duration) {
		if eg != nil {
			eg.ObserveTick(uint64(res.Tick))
		}
		if res.Completed.CommandsApplied > 0 {
			rt.observeCharacters(rt.Engine)
		}
	}
}
