// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package projector

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/store"
)

// Exit codes (AW-SRV-019).
const (
	ExitOK           = 0 // clean stop
	ExitConfig       = 1 // configuration, store, or broker
	ExitDivergence   = 2 // digest divergence
	ExitLogGap       = 3 // the log no longer has what the replica needs
	ExitStateVersion = 4 // state written by a newer binary
)

// ExitCode maps an error from Run to the process's exit code.
func ExitCode(err error) int {
	var d *Divergence
	var sv *sim.ErrStateVersion
	switch {
	case err == nil:
		return ExitOK
	case errors.As(err, &d):
		return ExitDivergence
	case errors.As(err, &sv):
		return ExitStateVersion
	case errors.Is(err, ErrLogGap), errors.Is(err, sim.ErrBoundaryGap), errors.Is(err, sim.ErrOffsetGap):
		return ExitLogGap
	}
	return ExitConfig
}

// RunOptions configures Run.
type RunOptions struct {
	Brokers []string
	// Group is andara-projector-state-<env>.
	Group string
	// Topics; empty means the production names. Tests use throwaway ones.
	CommandsTopic, EventsTopic, StateTopic string

	// The replica is built exactly as the server builds its Engine: from no
	// content, with the content in effect built only from the ContentSwaps
	// it replays through Content (AW-SRV-012), and the same seed. The
	// handler table is not an option: it is always sim.Handlers(), the
	// server's own, so no caller can make the replica a second
	// implementation (TestReplicaHasNoApplyOfItsOwn).
	//
	// World is the candidate topology — what the content source names now —
	// and only scopes which Zones a complete snapshot round must carry.
	World   *sim.World
	Content sim.ContentSource
	// OnSwaps is told every swap the replica applies, and the content a
	// round was restored with, so ContentVersion follows the content in
	// effect.
	OnSwaps func([]sim.SwapApplied)
	Seed    uint64

	// Store is where snapshot rounds are read, read-only; nil means there
	// are none and bootstrap is from offset zero.
	Store sim.WorldStore

	Rebuild    bool
	FromZero   bool
	BatchTicks int
	// Poll is how long one read of the boundary Partition waits on a quiet
	// log; zero means 500ms.
	Poll time.Duration

	ContentVersion func(sim.ZoneID) string
	Metrics        *Metrics
	Log            *slog.Logger
	Tracer         trace.Tracer
	// OnReady is called once, when bootstrap is done and the replica has
	// caught up to the log head.
	OnReady func()
	Now     func() time.Time
}

// Run is `andara-projector state`: bootstrap the replica, then follow the log
// until ctx ends (nil: a clean stop) or the replica cannot go on (an error
// ExitCode classifies).
func Run(ctx context.Context, o RunOptions) error {
	o = o.withDefaults()
	log := o.Log

	cm, err := NewCommitter(o.Brokers, o.Group, o.CommandsTopic)
	if err != nil {
		return err
	}
	defer cm.Close()
	cp, committed, err := cm.Last(ctx)
	if err != nil {
		return err
	}
	if d := cp.Diverged; committed && d != nil {
		// An unresolved divergence halts every start, whatever rounds exist
		// (feedback §2): bootstrapping from a round newer than it would
		// replay past the tick and lose it. --rebuild is the operator saying
		// the divergence has been dealt with.
		if !o.Rebuild {
			o.Metrics.DigestMismatches.Inc()
			log.Error("state projector diverged earlier and it is unresolved; run with --rebuild once it has been dealt with",
				"tick", uint64(d.Tick), "recorded_hash", fmt.Sprintf("%x", d.Recorded), "replayed_hash", fmt.Sprintf("%x", d.Replayed),
				"last_good_offsets", offsetsString(d.LastGood))
			return d
		}
		log.Info("--rebuild discards an unresolved divergence", "tick", uint64(d.Tick),
			"recorded_hash", fmt.Sprintf("%x", d.Recorded), "replayed_hash", fmt.Sprintf("%x", d.Replayed))
	}
	if o.Rebuild {
		if err := cm.Wipe(ctx); err != nil {
			return fmt.Errorf("--rebuild: %w", err)
		}
		log.Info("consumer group wiped for a rebuild", "group", o.Group)
		cp, committed = Checkpoint{}, false
	}

	boundaries, err := NewBoundaryReader(ctx, o.Brokers, o.EventsTopic)
	if err != nil {
		return fmt.Errorf("boundaries: %w", err)
	}
	defer boundaries.Close()

	bootStart := o.Now()
	eng, round, err := o.bootstrapEngine(ctx, boundaries)
	if err != nil {
		return err
	}
	if eng == nil {
		return nil // ctx ended before the World produced a first boundary
	}
	p := New(eng, Options{ContentVersion: o.ContentVersion, OnSwaps: o.OnSwaps})

	prod, err := NewProducer(ctx, o.Brokers, o.StateTopic)
	if err != nil {
		return fmt.Errorf("producer: %w", err)
	}
	defer prod.Close()

	// Where production starts. Behind a committed checkpoint the topic
	// already holds everything through it, so replay to it is silent. With
	// no checkpoint, or one older than the state loaded, the topic is made
	// equal to the replica by a dump and a reconcile, and committed.
	silentThrough := sim.Tick(0)
	if committed && cp.Tick >= eng.Tick() {
		silentThrough = cp.Tick
	} else {
		if err := o.dump(ctx, p, prod, cm); err != nil {
			return err
		}
	}
	o.Metrics.RebuildDuration.WithLabelValues("bootstrap").Observe(o.Now().Sub(bootStart).Seconds())
	log.Info("state projector started", "round_tick", uint64(round), "tick", uint64(eng.Tick()),
		"committed", committed, "committed_tick", uint64(cp.Tick), "silent_through", uint64(silentThrough),
		"rebuild", o.Rebuild, "from_zero", o.FromZero)

	src, err := NewCommandSource(ctx, o.Brokers, o.CommandsTopic, copyOffsets(eng.State().Offsets))
	if err != nil {
		return err
	}
	defer src.Close()

	replayStart := o.Now()
	ready := false
	var lastVerified Boundary
	for {
		batch, err := boundaries.Next(ctx, o.BatchTicks, o.Poll)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			return err
		}
		batch = after(batch, eng.Tick())
		if len(batch) > 0 {
			if err := o.step(ctx, p, src, prod, cm, batch, silentThrough, &lastVerified); err != nil {
				var d *Divergence
				if errors.As(err, &d) {
					o.Metrics.DigestMismatches.Inc()
					log.Error("state projector diverged", "tick", uint64(d.Tick),
						"recorded_hash", fmt.Sprintf("%x", d.Recorded), "replayed_hash", fmt.Sprintf("%x", d.Replayed),
						"last_good_offsets", offsetsString(d.LastGood), "detail", err.Error())
					// The halt is recorded in the checkpoint, at T-1, so it
					// survives the restart. The halt stands if the commit
					// fails; the next start then finds no record of it,
					// which the log line above is the fallback for.
					halt := Checkpoint{Tick: d.Tick - 1, Offsets: d.LastGood, Diverged: d}
					if cerr := cm.Commit(context.WithoutCancel(ctx), halt); cerr != nil {
						log.Error("the divergence was not recorded in the checkpoint; a restart will not see it", "tick", uint64(d.Tick), "detail", cerr.Error())
					}
				}
				return err
			}
		} else {
			o.observeLag(lastVerified)
		}
		// Caught up is a position: every boundary on the log, as of the last
		// fetch, verified. Not an empty read — a World ticking at 10 Hz never
		// leaves the boundary Partition quiet for a whole poll.
		if !ready && boundaries.AtHead() {
			ready = true
			o.Metrics.RebuildDuration.WithLabelValues("replay").Observe(o.Now().Sub(replayStart).Seconds())
			log.Info("state projector caught up", "tick", uint64(eng.Tick()))
			if o.OnReady != nil {
				o.OnReady()
			}
		}
	}
}

// step replays one batch, produces what it rendered, and commits through its
// last verified boundary. On a divergence, the ticks before it are still
// produced and committed — nothing past T-1 is (AC-3).
func (o RunOptions) step(ctx context.Context, p *Projector, src *CommandSource, prod *Producer, cm *Committer,
	batch []Boundary, silentThrough sim.Tick, lastVerified *Boundary) error {
	ctx, span := o.Tracer.Start(ctx, "state.replay")
	defer span.End()
	tcs := make([]sim.TickCompleted, len(batch))
	for i, b := range batch {
		tcs[i] = b.TickCompleted
	}
	var (
		out       []Out
		last      *Boundary
		recordsIn uint64
		i         int
	)
	replayErr := p.Replay(tcs, src, func(b sim.TickCompleted, recs []Out) error {
		_, vspan := o.Tracer.Start(ctx, "state.verify", trace.WithAttributes(attribute.Int64("tick", int64(b.Tick))))
		vspan.End()
		bd := batch[i]
		i++
		recordsIn += b.CommandsApplied
		*lastVerified = bd
		o.Metrics.Tick.Set(float64(b.Tick))
		if b.Tick <= silentThrough {
			return nil
		}
		out = append(out, recs...)
		last = &bd
		return nil
	})
	span.SetAttributes(attribute.Int("ticks", i), attribute.Int64("records_in", int64(recordsIn)), attribute.Int("records_out", len(out)))
	if last != nil {
		if err := o.produce(ctx, prod, out); err != nil {
			return err
		}
		if err := cm.Commit(ctx, Checkpoint{Tick: last.Tick, Offsets: last.Offsets}); err != nil {
			return err
		}
	}
	o.observeLag(*lastVerified)
	if replayErr == nil {
		o.Log.Debug("state batch", "tick", uint64(lastVerified.Tick), "ticks", i, "records_out", len(out))
	}
	return replayErr
}

// bootstrapEngine builds the replica: from the newest complete round unless
// --from-zero or there is none, else at tick 0 over the Partitions the first
// boundary names — the server's own set, which the State Hash covers.
func (o RunOptions) bootstrapEngine(ctx context.Context, boundaries *BoundaryReader) (*sim.Engine, sim.Tick, error) {
	cfg := sim.Config{Seed: o.Seed, Handlers: sim.Handlers(), Content: o.Content}
	var zones map[sim.ZoneID]*sim.Zone
	if o.World != nil {
		// Nil when the content the pointers name did not load: the replica
		// still replays what the log recorded, with no round to scope.
		zones = o.World.Zones
	}
	owned := make([]sim.ZoneID, 0, len(zones))
	for id := range zones {
		owned = append(owned, id)
	}
	sort.Slice(owned, func(i, j int) bool { return owned[i] < owned[j] })

	if o.Store != nil && !o.FromZero {
		// AC-10: a round written by a newer binary is a refusal naming both
		// versions, as AW-SRV-007 does, not a silent fall-back to an older
		// round the operator did not choose.
		if v, err := store.StateVersionOf(ctx, o.Store, owned); err != nil {
			return nil, 0, fmt.Errorf("snapshot store: %w", err)
		} else if v > sim.StateVersion {
			return nil, 0, &sim.ErrStateVersion{Have: v, Want: sim.StateVersion}
		}
		round, state, ok, err := store.NewestComplete(ctx, o.Store, owned)
		if err != nil {
			return nil, 0, fmt.Errorf("snapshot store: %w", err)
		}
		if ok {
			if len(state.Content) == 0 {
				return nil, 0, fmt.Errorf("snapshot round at tick %d records no content in effect: it predates AW-SRV-012; bootstrap with --from-zero", round.Tick)
			}
			topo, err := sim.PrepareContent(o.Content, state.Content)
			if err != nil {
				return nil, 0, fmt.Errorf("snapshot round at tick %d: rebuild its content: %w", round.Tick, err)
			}
			eng, err := sim.RestoreEngine(topo.World, topo.Templates, cfg, state)
			if err != nil {
				return nil, 0, err
			}
			if o.OnSwaps != nil {
				restored := make([]sim.SwapApplied, 0, len(state.Content))
				for p, v := range state.Content {
					restored = append(restored, sim.SwapApplied{Pack: p, Version: v})
				}
				sort.Slice(restored, func(i, j int) bool { return restored[i].Pack < restored[j].Pack })
				o.OnSwaps(restored)
			}
			o.Log.Info("bootstrap from snapshot round", "tick", uint64(round.Tick), "zones", len(round.Zones))
			return eng, round.Tick, nil
		}
		o.Log.Info("no complete snapshot round; bootstrapping from offset zero")
	}
	// From zero. The server's Partition set is read off its first boundary
	// rather than configured: a replica that owned a different set would
	// hash differently at tick 1.
	for {
		first, err := boundaries.Next(ctx, 1, o.Poll)
		if ctx.Err() != nil {
			return nil, 0, nil
		}
		if err != nil {
			return nil, 0, err
		}
		if len(first) == 0 {
			continue
		}
		if first[0].Tick != 1 {
			return nil, 0, fmt.Errorf("%w: the first Tick Boundary Record on the log is for tick %d, and there is no snapshot round to start before it", ErrLogGap, first[0].Tick)
		}
		parts := make([]int32, 0, len(first[0].Offsets))
		for p := range first[0].Offsets {
			parts = append(parts, p)
		}
		cfg.Partitions = parts
		boundaries.buf = append(first, boundaries.buf...)
		return sim.NewEngine(sim.EmptyWorld(), nil, cfg), 0, nil
	}
}

// dump makes the topic equal to the replica's state: every aggregate, then a
// tombstone for every live key the state does not produce, then a commit at
// the replica's tick.
func (o RunOptions) dump(ctx context.Context, p *Projector, prod *Producer, cm *Committer) error {
	recs, err := p.Dump()
	if err != nil {
		return err
	}
	keys, err := TopicKeys(ctx, o.Brokers, o.StateTopic)
	if err != nil {
		return fmt.Errorf("read %s keys: %w", o.StateTopic, err)
	}
	stale := p.Reconcile(keys)
	recs = append(recs, stale...)
	if err := o.produce(ctx, prod, recs); err != nil {
		return err
	}
	eng := p.Engine()
	o.Log.Info("state dump written", "tick", uint64(eng.Tick()), "records", len(recs), "stale_tombstones", len(stale))
	return cm.Commit(ctx, Checkpoint{Tick: eng.Tick(), Offsets: copyOffsets(eng.State().Offsets)})
}

func (o RunOptions) produce(ctx context.Context, prod *Producer, recs []Out) error {
	if err := prod.Produce(ctx, recs); err != nil {
		return fmt.Errorf("produce %s: %w", o.StateTopic, err)
	}
	for _, r := range recs {
		o.Metrics.RecordsProduced.WithLabelValues(KindLabel(r.Kind)).Inc()
		if r.Tombstone() {
			o.Metrics.Tombstones.Inc()
		}
	}
	return nil
}

func (o RunOptions) observeLag(b Boundary) {
	if b.ProducedAt.IsZero() {
		return
	}
	o.Metrics.LagSeconds.Set(o.Now().Sub(b.ProducedAt).Seconds())
}

func (o RunOptions) withDefaults() RunOptions {
	if o.BatchTicks < 1 {
		o.BatchTicks = 10
	}
	if o.Poll <= 0 {
		o.Poll = 500 * time.Millisecond
	}
	if o.Metrics == nil {
		o.Metrics = NewMetrics(nil)
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Tracer == nil {
		o.Tracer = noop.NewTracerProvider().Tracer("projector")
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.StateTopic == "" {
		o.StateTopic = StateTopic
	}
	return o
}

// after drops boundaries at or before tick: ones the replica already holds.
func after(b []Boundary, tick sim.Tick) []Boundary {
	i := 0
	for i < len(b) && b[i].Tick <= tick {
		i++
	}
	return b[i:]
}

func offsetsString(m map[int32]int64) string {
	return (&Divergence{LastGood: m}).lastGood()
}
