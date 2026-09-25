// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package boot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	logv1 "github.com/valesordev/andara/gen/go/andara/log/v1"
	"github.com/valesordev/andara/server/command"
	"github.com/valesordev/andara/server/config"
	"github.com/valesordev/andara/server/content"
	"github.com/valesordev/andara/server/ingress"
	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/store"
	"github.com/valesordev/andara/server/telemetry"
	"github.com/valesordev/andara/server/tickloop"
)

// StartTickLoop builds the engine and the loop over the configured source,
// recovers, and returns the loop ready to Run.
//
// The engine starts with no content (sim.EmptyWorld): the log is the source
// of the content in effect (AW-SRV-012), so recovery builds it only from the
// ContentSwaps it replays, preparing each through the content source, and
// ReconcileContent brings in whatever the source names that the log does not
// yet have. The engine starts at tick 0 and the consumer at offset 0 on every
// assigned Partition: AW-SRV-006 writes snapshots, and AW-SRV-007 is what
// recovers from one. Until then a restart replays the log from its
// beginning, which is correct and slow.
func (rt *Runtime) StartTickLoop(ctx context.Context) (*tickloop.Loop, error) {
	cfg := rt.Cfg
	if rt.Content == nil {
		return nil, fmt.Errorf("tick loop: no content source")
	}
	engineCfg := sim.Config{
		Seed:       cfg.SimSeed,
		Partitions: cfg.SimPartitions,
		// The verb handlers (AW-SRV-003). A logged arm with no handler is
		// rejected with unsupported_command and the offsets advance, which
		// is what keeps the World replayable through a binary behind its
		// content.
		Handlers: sim.Handlers(),
		Content:  rt.Content,
	}
	engine := sim.NewEngine(sim.EmptyWorld(), nil, engineCfg)
	// Before recovery, so the content applied while replaying reaches the
	// gauges it moves.
	rt.Engine = engine

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
		replayed, err := tickloop.Recover(rctx, cfg.KafkaBrokers, tickloop.CommandsTopic, tickloop.EventsTopic, engine, rt.recovered())
		rspan.End()
		if err != nil {
			return nil, fmt.Errorf("recovery: %w", RecoveryError(err, engine))
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

	// Where the loop starts on the World Partition, for worldBarrier.
	rt.worldNext.Store(engine.State().Offsets[sim.WorldPartition])

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
		OnSwap:          rt.onSwap(),
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
	eg := rt.Egress
	return func(res sim.StepResult, _ time.Duration) {
		if len(res.Swaps) > 0 {
			rt.contentApplied(res.Swaps)
		}
		if len(res.SwapsRefused) > 0 && rt.Content != nil {
			rt.Content.Refused(res.SwapsRefused)
		}
		rt.worldNext.Store(res.Completed.Offsets[sim.WorldPartition])
		if eg != nil {
			eg.ObserveTick(uint64(res.Tick))
		}
		if res.Completed.CommandsApplied > 0 {
			rt.observeCharacters(rt.Engine)
		}
	}
}

// ErrPreRuleLog: the command log applied Commands before any ContentSwap, so
// it predates the rule that the log records the content in effect
// (AW-SRV-012, 2026-09-25). Its content cannot be known, and boot refuses it
// rather than guess.
var ErrPreRuleLog = errors.New("the command log predates AW-SRV-012: it applied Commands before any ContentSwap recorded the content in effect; recover onto a fresh log (make down VOLUMES=1 locally, fresh topics for dev)")

// recovered is Recover's per-tick hook: the content source learns each
// replayed swap and refusal, and a log that applied Commands while no content
// was in effect is refused.
func (rt *Runtime) recovered() func(sim.StepResult) error {
	inEffect := false
	return func(res sim.StepResult) error {
		swaps := uint64(len(res.Swaps) + len(res.SwapsRefused))
		if !inEffect && res.Completed.CommandsApplied > swaps {
			return fmt.Errorf("tick %d: %w", res.Tick, ErrPreRuleLog)
		}
		if len(res.Swaps) > 0 {
			inEffect = true
			rt.contentApplied(res.Swaps)
		}
		if len(res.SwapsRefused) > 0 && rt.Content != nil {
			rt.Content.Refused(res.SwapsRefused)
		}
		return nil
	}
}

// RecoveryError reports a pre-rule log as ErrPreRuleLog whichever way it
// shows itself. A log written before content was recorded in it usually fails
// its State Hash at tick 1, because replay runs on an empty topology, before
// any Command could be seen applying with no content in effect: a hash
// mismatch while no content is in effect is that log, not corruption (review
// of #88, reproduced live).
func RecoveryError(err error, e *sim.Engine) error {
	var hm *sim.HashMismatchError
	if versions, _ := e.Content(); errors.As(err, &hm) && len(versions) == 0 {
		return fmt.Errorf("tick %d, before any content was in effect: %w (%v)", hm.Tick, ErrPreRuleLog, err)
	}
	return err
}

// contentApplied tells the content source what a tick applied, and keeps the
// AW-SRV-001 topology gauges on the content in effect. On the loop's
// goroutine, or recovery's before the loop starts.
func (rt *Runtime) contentApplied(swaps []sim.SwapApplied) {
	if rt.Content != nil {
		rt.Content.Applied(swaps)
	}
	if rt.Engine == nil {
		return
	}
	w := rt.Engine.World()
	rt.Tel.Metrics.ZonesLoaded.Set(float64(len(w.Zones)))
	// Only the Zones in effect: a label from content no longer in effect
	// would report Rooms nobody can stand in.
	rt.Tel.Metrics.RoomsLoaded.Reset()
	for id, z := range w.Zones {
		rt.Tel.Metrics.RoomsLoaded.WithLabelValues(string(id)).Set(float64(len(z.Rooms)))
	}
}

// onSwap observes a swap's in-tick cost (AC-9).
func (rt *Runtime) onSwap() func(time.Duration) {
	return func(d time.Duration) {
		if rt.ContentMetrics == nil {
			return
		}
		rt.ContentMetrics.ReloadStall.Observe(d.Seconds())
		rt.ContentMetrics.LoadDuration.WithLabelValues(content.PhaseSwap).Observe(d.Seconds())
	}
}

// ReconcileContent brings the World to what the content source names, through
// the log, and marks the process ready once it has content in effect. On an
// empty log that is genesis — one ContentSwap per followed pack, andara.core
// first — and after a restart it is whatever moved while the process was
// down. The loop must be running: a swap is in effect when its tick applies
// it. A boot that ends with nothing in effect exits 1: there is no World to
// serve and no previous version to retain.
func (rt *Runtime) ReconcileContent(ctx context.Context) int {
	if rt.Content == nil || rt.Engine == nil {
		rt.Tel.Log.LogAttrs(ctx, slog.LevelError, "content: the tick loop must be started first")
		return ExitFail
	}
	ctx, span := rt.Tel.Tracer.Start(ctx, "content.reconcile")
	defer span.End()
	rt.Content.SetProducer(swapProducer{rt.commandLog})
	rt.Content.SetBarrier(rt.worldBarrier)
	rejects, err := rt.Content.Reconcile(ctx)
	if err != nil {
		rt.Tel.Log.LogAttrs(ctx, slog.LevelError, "content could not be brought into effect",
			slog.String("detail", err.Error()), slog.String("trace_id", telemetry.TraceID(ctx)))
		return ExitFail
	}
	if len(rt.Engine.World().Zones) == 0 {
		for _, r := range rejects {
			rt.Tel.Log.LogAttrs(ctx, slog.LevelError, "no content in effect: refused",
				slog.String("pack", r.Pack), slog.Uint64("version", r.Version), slog.String("reason", r.Reason))
		}
		rt.Tel.Log.LogAttrs(ctx, slog.LevelError, "no content in effect: the World has no Zones and there is no previous version to retain",
			slog.String("trace_id", telemetry.TraceID(ctx)))
		return ExitFail
	}
	rt.World, rt.Templates = rt.Engine.World(), rt.Engine.Templates()
	versions, digest := rt.Content.InEffect()
	rt.Tel.Log.LogAttrs(ctx, slog.LevelInfo, "content in effect",
		slog.Any("versions", versions), slog.String("world_digest", fmt.Sprintf("%x", digest[:8])))
	return ExitOK
}

// worldBarrier waits until the tick loop has consumed the World Partition
// past its end as of now: every ContentSwap already in the log has applied or
// been refused. What reconcile runs first — a swap a previous process
// produced may lie past the last boundary — and what a swap whose produce had
// an unknown outcome waits for.
func (rt *Runtime) worldBarrier(ctx context.Context) error {
	end, err := rt.worldEnd(ctx)
	if err != nil {
		return err
	}
	t := time.NewTicker(10 * time.Millisecond)
	defer t.Stop()
	for rt.worldNext.Load() < end {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
	return nil
}

// worldEnd is the World Partition's end offset: the broker's, or the memory
// source's.
func (rt *Runtime) worldEnd(ctx context.Context) (int64, error) {
	if rt.memSource != nil {
		return rt.memSource.End(sim.WorldPartition), nil
	}
	return tickloop.EndOffset(ctx, rt.Cfg.KafkaBrokers, tickloop.CommandsTopic, sim.WorldPartition)
}

// FollowContent applies Active Pointer moves until ctx ends. The Loader logs
// every rejection itself.
func (rt *Runtime) FollowContent(ctx context.Context) error {
	if rt.Content == nil {
		return nil
	}
	return rt.Content.Follow(ctx, nil)
}

// swapProducer writes a ContentSwap through the Gateway's producer, which puts
// a World-scoped Command on sim.WorldPartition.
type swapProducer struct{ p command.Producer }

func (s swapProducer) ProduceSwap(ctx context.Context, cmd *logv1.LoggedCommand) error {
	if s.p == nil {
		return errors.New("no command producer: StartIngress must run first")
	}
	_, err := s.p.Produce(ctx, cmd)
	var u *ingress.Unsettled
	if errors.As(err, &u) {
		// The produce's wait ended with the record possibly live: the Loader
		// waits for it to settle, and never counts it as not written first.
		return &content.SwapPending{Settled: u.Settled(), Outcome: func() error {
			_, oerr := u.Outcome()
			switch {
			case oerr == nil:
				return nil
			case errors.Is(oerr, ingress.ErrOutcomeUnknown):
				return content.ErrSwapOutcomeUnknown
			default:
				return fmt.Errorf("%w: %w", content.ErrSwapNotWritten, oerr)
			}
		}}
	}
	return err
}
