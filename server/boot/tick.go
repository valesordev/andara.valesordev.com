// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package boot

import (
	"context"
	"fmt"
	"log/slog"

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
		// Verb handlers arrive with AW-SRV-003; until then every logged
		// Command is rejected with unsupported_command and the offsets
		// advance, which is what keeps the World replayable through a
		// binary that cannot yet act.
		Handlers: map[sim.CommandKind]sim.Apply{},
	}
	engine := sim.NewEngine(rt.World, rt.Templates, engineCfg)

	var (
		source    tickloop.Source
		publisher tickloop.Publisher
		err       error
	)
	switch cfg.SimSource {
	case "memory":
		rt.Tel.Log.LogAttrs(ctx, slog.LevelWarn, "sim.source=memory: the World ticks with no Command input and nothing is published")
		source, publisher = tickloop.NewMemorySource(), &tickloop.MemoryPublisher{}
	case "kafka":
		source, err = tickloop.NewKafkaSource(ctx, tickloop.KafkaSourceOptions{
			Brokers:  cfg.KafkaBrokers,
			Group:    "andara-sim-" + cfg.Environment,
			ClientID: cfg.ServiceName,
			Start:    engine.State().Offsets,
		})
		if err != nil {
			return nil, err
		}
		publisher, err = tickloop.NewKafkaPublisher(ctx, cfg.KafkaBrokers, cfg.ServiceName)
		if err != nil {
			_ = source.Close()
			return nil, err
		}
	default:
		return nil, fmt.Errorf("sim.source %q is not kafka or memory", cfg.SimSource)
	}

	loop, err := tickloop.New(tickloop.Options{
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
	})
	if err != nil {
		_ = source.Close()
		_ = publisher.Close()
		return nil, err
	}
	// The loop times Zones for the engine; the engine was built before the
	// loop existed, so attach it now.
	engine.SetZoneTimer(loop)
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
