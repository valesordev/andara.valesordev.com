// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package recovery

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/store"
	"github.com/valesordev/andara/server/tickloop"
)

// Boundaries is the Tick Boundary Record stream recovery reads:
// *tickloop.BoundaryReader on the broker, a fake in a unit test.
type Boundaries interface {
	SeekAfter(ctx context.Context, tick sim.Tick) (int64, error)
	BoundaryAfter() bool
	Next(ctx context.Context, max int, wait time.Duration) ([]tickloop.Boundary, error)
	AtHead() bool
}

// Options is everything Recover reads, so that nothing in it reaches for a
// broker or a clock of its own.
type Options struct {
	// Store holds the snapshot rounds; nil means there are none.
	Store sim.WorldStore
	// Owned is the Zones a complete round must carry one object for.
	Owned []sim.ZoneID
	// Boundaries is the boundary stream, positioned by Recover.
	Boundaries Boundaries
	// OpenRecords opens the Command source for the Partitions at their start
	// offsets. It is called once, after the round is loaded, because the
	// offsets are the round's. A source that is an io.Closer is closed.
	OpenRecords func(ctx context.Context, start map[int32]int64) (sim.RecordSource, error)
	// Config is the Engine's: Seed, Handlers, Content, and, for a replay from
	// offset zero, the Partitions it owns. A restored Engine owns the round's.
	Config sim.Config
	// Content prepares the topology a round recorded, as the Engine's own
	// ContentSource does; nil only when no round records content.
	Content sim.ContentSource
	// Prepare rebuilds the topology a round's recorded content builds. Nil
	// means sim.PrepareContent over Content; a test supplies a fixed World,
	// which also starts a replay from offset zero (Prepare(nil)) instead of
	// the empty World the log's ContentSwaps build from.
	Prepare func(versions map[string]uint64) (sim.Topology, error)

	// Round names the round to recover from: nil recovers from the newest
	// complete one. A named round that isn't complete is refused, and no other
	// is tried (AC-15).
	Round *sim.Tick
	// RequireSnapshot refuses a recovery with no complete round (exit 7).
	RequireSnapshot bool
	// Verify is `recover --verify` and Admin.VerifySnapshotRound: the restore
	// is counted under caller=verify, and the caller never serves the Engine.
	Verify bool
	// ReplayBatch is how many boundaries are replayed at a time. It bounds
	// memory only: the State Hash sequence is the same at any value (AC-3).
	ReplayBatch int
	// Poll is how long one boundary read waits for the log.
	Poll time.Duration
	// After is called with each replayed tick once its hash is verified: how
	// the content source learns the swaps recovery applied (AW-SRV-012).
	After func(sim.StepResult) error
	// OnRestored is called with the content a restored round had in effect.
	OnRestored func([]sim.SwapApplied)

	Metrics *Metrics
	Log     *slog.Logger
	Tracer  trace.Tracer
	// Now is the clock the phases are measured with.
	Now func() time.Time
}

// Report is what a recovery did: where each phase's time went, the round it
// restored from, and what replay reached.
type Report struct {
	Phases map[string]time.Duration // load, seek, replay, verify, total
	// Round is the round restored from; its zero value is a replay from
	// offset zero.
	Round store.Round
	// Replayed is the ticks replayed past the round.
	Replayed uint64
	// Tick is the tick the Engine reached.
	Tick sim.Tick
	// Match is true when every recorded boundary's State Hash was reproduced.
	Match bool
	// Expected and Actual are the State Hash the log recorded at the first
	// mismatching tick and the one replay produced there, set with Match false;
	// Head is the last verified hash otherwise.
	Expected, Actual [32]byte
	// MismatchTick is the tick of the first mismatching boundary.
	MismatchTick sim.Tick
}

func (o *Options) defaults() {
	if o.Log == nil {
		o.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if o.Tracer == nil {
		o.Tracer = noop.NewTracerProvider().Tracer("recovery")
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Metrics == nil {
		o.Metrics = NewMetrics(nil)
	}
	if o.ReplayBatch <= 0 {
		o.ReplayBatch = 4096
	}
	if o.Poll <= 0 {
		o.Poll = 500 * time.Millisecond
	}
}

// Recover is the boot-time path: select a round, load it, seek the log to it,
// replay the recorded boundaries to the head, and prove the State Hash at every
// one. It returns an Engine at the log's head or a *Failure, never a World that
// didn't reproduce its history, and never falls back from the round it chose to
// another.
func Recover(ctx context.Context, o Options) (*sim.Engine, Report, error) {
	o.defaults()
	rep := Report{Phases: map[string]time.Duration{}}
	start := o.Now()
	ctx, span := o.Tracer.Start(ctx, "recovery.run")
	defer span.End()
	traceID := span.SpanContext().TraceID().String()

	finish := func() {
		rep.Phases[PhaseTotal] = o.Now().Sub(start)
		for ph, d := range rep.Phases {
			o.Metrics.Duration.WithLabelValues(ph).Observe(d.Seconds())
		}
	}
	fail := func(err error) (*sim.Engine, Report, error) {
		f := Classify(err)
		finish()
		o.Metrics.Failures.WithLabelValues(f.Reason).Inc()
		span.SetStatus(codes.Error, f.Error())
		span.SetAttributes(attribute.Int("exit_code", f.Exit), attribute.String("reason", f.Reason))
		return nil, rep, f
	}
	phase := func(name string, since time.Time) time.Time {
		now := o.Now()
		rep.Phases[name] += now.Sub(since)
		return now
	}

	// Select and load the round: Phase load.
	t0 := o.Now()
	round, state, found, err := o.selectRound(ctx)
	if err != nil {
		o.logRefusal(ctx, err, traceID)
		return fail(err)
	}
	var eng *sim.Engine
	var begin map[int32]int64
	cfg := o.Config
	if found {
		rep.Round = round
		o.Log.InfoContext(ctx, "recovery starting from a snapshot round", "tick", uint64(round.Tick), "zones", len(round.Zones),
			"offsets", offsetsString(state.Offsets), "trace_id", traceID)
		for _, z := range round.Zones {
			_, zs := o.Tracer.Start(ctx, "recovery.load_snapshot", trace.WithAttributes(attribute.String("zone_id", string(z.Zone)), attribute.String("key", z.Key)))
			zs.End()
		}
		prepare := o.Prepare
		if prepare == nil {
			prepare = func(v map[string]uint64) (sim.Topology, error) { return sim.PrepareContent(o.Content, v) }
		}
		topo, err := prepare(state.Content)
		if err != nil {
			err = fmt.Errorf("snapshot round at tick %d: rebuild its content: %w", round.Tick, err)
			o.logRefusal(ctx, err, traceID)
			return fail(err)
		}
		t1 := phase(PhaseLoad, t0)
		// Seek: the round's own boundary is the hash the restore must
		// reproduce, and replay starts after it.
		tc, err := o.roundBoundary(ctx, round.Tick)
		if err != nil {
			o.logRefusal(ctx, err, traceID)
			return fail(err)
		}
		state.RecordedHash = tc.StateHash[:]
		t0 = phase(PhaseSeek, t1)
		_, rspan := o.Tracer.Start(ctx, "restore.verify", trace.WithAttributes(attribute.Int64("round_tick", int64(round.Tick))))
		eng, err = sim.RestoreEngine(topo.World, topo.Templates, cfg, state)
		caller := CallerRecovery
		if o.Verify {
			caller = CallerVerify
		}
		if oc := sim.RestoreOutcome(err); oc != "" {
			o.Metrics.Restores.WithLabelValues(caller, oc).Inc()
			rspan.SetAttributes(attribute.String("outcome", oc))
		}
		rspan.End()
		if err != nil {
			o.logRestoreMismatch(ctx, err, round.Tick, traceID)
			o.Metrics.SetHashMatch(false)
			return fail(err)
		}
		if o.OnRestored != nil && len(state.Content) > 0 {
			restored := make([]sim.SwapApplied, 0, len(state.Content))
			for p, v := range state.Content {
				restored = append(restored, sim.SwapApplied{Pack: p, Version: v})
			}
			sort.Slice(restored, func(i, j int) bool { return restored[i].Pack < restored[j].Pack })
			o.OnRestored(restored)
		}
		begin = make(map[int32]int64, len(state.Offsets))
		for _, po := range state.Offsets {
			begin[po.Partition] = po.Offset
		}
		phase(PhaseLoad, t0) // the restore's own time is load
	} else {
		o.Log.InfoContext(ctx, "recovery found no complete snapshot round: replaying from offset zero", "trace_id", traceID)
		var topo sim.Topology
		if o.Prepare != nil {
			var err error
			if topo, err = o.Prepare(nil); err != nil {
				o.logRefusal(ctx, err, traceID)
				return fail(err)
			}
		} else {
			topo = sim.Topology{World: sim.EmptyWorld()}
		}
		eng = sim.NewEngine(topo.World, topo.Templates, cfg)
		begin = map[int32]int64{}
		for p := range eng.State().Offsets {
			begin[p] = 0
		}
		phase(PhaseLoad, t0)
	}
	o.Metrics.RoundTick.Set(float64(rep.Round.Tick))

	// Replay.
	rt := o.Now()
	_, rsp := o.Tracer.Start(ctx, "recovery.replay")
	src, err := o.OpenRecords(ctx, begin)
	if err != nil {
		rsp.End()
		o.logRefusal(ctx, err, traceID)
		return fail(err)
	}
	if c, ok := src.(io.Closer); ok {
		defer func() { _ = c.Close() }()
	}
	records := int64(0)
	err = o.replay(ctx, eng, src, &rep, &records)
	rsp.SetAttributes(attribute.Int64("ticks", int64(rep.Replayed)), attribute.Int64("records", records))
	rsp.End()
	phase(PhaseReplay, rt)
	if err != nil {
		var hm *sim.HashMismatchError
		var cd *sim.ContentDigestError
		switch {
		case errors.As(err, &hm):
			err = &HashMismatchError{Tick: hm.Tick, Expected: hm.Recorded, Actual: hm.Replayed, Round: rep.Round.Tick}
			rep.MismatchTick, rep.Expected, rep.Actual = hm.Tick, hm.Recorded, hm.Replayed
			o.Metrics.SetHashMatch(false)
			o.Log.ErrorContext(ctx, "recovery state hash mismatch", "tick", uint64(hm.Tick),
				"recorded_hash", fmt.Sprintf("%x", hm.Recorded), "replayed_hash", fmt.Sprintf("%x", hm.Replayed),
				"round_tick", uint64(rep.Round.Tick), "trace_id", traceID)
		case errors.As(err, &cd):
			// A swap replayed onto content that no longer builds the recorded
			// digest halts as a State Hash mismatch does (AW-SRV-012).
			o.Metrics.SetHashMatch(false)
			o.logRefusal(ctx, err, traceID)
			return fail(&Failure{Err: err, Exit: ExitHashMismatch, Reason: ReasonHash})
		default:
			o.logRefusal(ctx, err, traceID)
		}
		return fail(err)
	}

	// Verify: the head is what the log last recorded.
	vt := o.Now()
	_, vsp := o.Tracer.Start(ctx, "recovery.verify")
	rep.Tick = eng.Tick()
	rep.Match = true
	rep.Actual = eng.StateHash()
	rep.Expected = rep.Actual
	vsp.End()
	phase(PhaseVerify, vt)
	o.Metrics.SetHashMatch(true)
	o.Metrics.ReplayedTicks.Set(float64(rep.Replayed))
	finish()
	o.Log.InfoContext(ctx, "recovery complete", "tick", uint64(rep.Tick), "round_tick", uint64(rep.Round.Tick),
		"replayed_ticks", rep.Replayed, "load_ms", rep.Phases[PhaseLoad].Milliseconds(), "seek_ms", rep.Phases[PhaseSeek].Milliseconds(),
		"replay_ms", rep.Phases[PhaseReplay].Milliseconds(), "verify_ms", rep.Phases[PhaseVerify].Milliseconds(),
		"total_ms", rep.Phases[PhaseTotal].Milliseconds(), "state_hash", fmt.Sprintf("%x", rep.Actual), "trace_id", traceID)
	return eng, rep, nil
}

// selectRound chooses the round to restore: the named one, else the newest
// complete. found is false for a cold start.
func (o *Options) selectRound(ctx context.Context) (store.Round, sim.RoundState, bool, error) {
	if o.Round != nil {
		if o.Store == nil {
			return store.Round{}, sim.RoundState{}, false, fmt.Errorf("round %d is named and no snapshot store is configured", *o.Round)
		}
		r, st, err := store.RoundAt(ctx, o.Store, o.Owned, *o.Round)
		if err != nil {
			return store.Round{}, sim.RoundState{}, false, err
		}
		return r, st, true, nil
	}
	if o.Store != nil {
		// A round written by a newer binary is a refusal naming both versions,
		// not a silent fall back to an older round the operator did not choose.
		v, err := store.StateVersionOf(ctx, o.Store, o.Owned)
		if err != nil {
			return store.Round{}, sim.RoundState{}, false, fmt.Errorf("snapshot store: %w", err)
		}
		if v > sim.StateVersion {
			return store.Round{}, sim.RoundState{}, false, &sim.ErrStateVersion{Have: v, Want: sim.StateVersion}
		}
		r, st, ok, err := store.NewestComplete(ctx, o.Store, o.Owned)
		if err != nil {
			return store.Round{}, sim.RoundState{}, false, fmt.Errorf("snapshot store: %w", err)
		}
		if ok {
			return r, st, true, nil
		}
	}
	if o.RequireSnapshot {
		return store.Round{}, sim.RoundState{}, false, &sim.ErrRoundIncomplete{Cause: sim.RoundMissing, Zones: o.Owned}
	}
	return store.Round{}, sim.RoundState{}, false, nil
}

// roundBoundary positions the reader at the round's own tick and reads that
// tick's Tick Boundary Record, leaving the reader at the tick after it. A log
// that no longer has it is a *tickloop.LogGapError.
func (o *Options) roundBoundary(ctx context.Context, round sim.Tick) (sim.TickCompleted, error) {
	if round == 0 {
		return sim.TickCompleted{}, fmt.Errorf("snapshot round at tick 0: no Tick Boundary Record records a tick 0")
	}
	_, span := o.Tracer.Start(ctx, "recovery.seek")
	defer span.End()
	at, err := o.Boundaries.SeekAfter(ctx, round-1)
	if err != nil {
		return sim.TickCompleted{}, fmt.Errorf("boundaries: %w", err)
	}
	span.SetAttributes(attribute.Int64("offset", at))
	if !o.Boundaries.BoundaryAfter() {
		return sim.TickCompleted{}, &tickloop.LogGapError{Topic: tickloop.EventsTopic, Partition: tickloop.BoundaryPartition, Need: int64(round), Have: -1}
	}
	for {
		b, err := o.Boundaries.Next(ctx, 1, o.Poll)
		if err != nil {
			return sim.TickCompleted{}, err
		}
		switch {
		case len(b) == 0, b[0].Tick < round:
		case b[0].Tick == round:
			return b[0].TickCompleted, nil
		default:
			return sim.TickCompleted{}, &tickloop.LogGapError{Topic: tickloop.EventsTopic, Partition: tickloop.BoundaryPartition, Need: int64(round), Have: int64(b[0].Tick)}
		}
	}
}

// replay streams the boundaries after the Engine's tick to the log's head,
// ReplayBatch at a time, never holding the topic.
func (o *Options) replay(ctx context.Context, e *sim.Engine, src sim.RecordSource, rep *Report, records *int64) error {
	idle := 0
	counting := countingSource{src: src, n: records}
	for {
		bs, err := o.Boundaries.Next(ctx, o.ReplayBatch, o.Poll)
		if err != nil {
			return err
		}
		tcs := make([]sim.TickCompleted, 0, len(bs))
		for _, b := range bs {
			if b.Tick > e.Tick() {
				tcs = append(tcs, b.TickCompleted)
			}
		}
		if len(tcs) == 0 {
			// A quiet read at the head, twice in a row: an empty read before
			// the first fetch lands reports a head that isn't one.
			if len(bs) == 0 && o.Boundaries.AtHead() {
				if idle++; idle >= 2 {
					return nil
				}
			}
			continue
		}
		idle = 0
		if err := e.ReplayEach(tcs, counting, o.After); err != nil {
			return err
		}
		rep.Replayed += uint64(len(tcs))
	}
}

type countingSource struct {
	src sim.RecordSource
	n   *int64
}

func (c countingSource) Fetch(p int32, from, to int64) ([]sim.Record, error) {
	r, err := c.src.Fetch(p, from, to)
	*c.n += int64(len(r))
	return r, err
}

func offsetsString(po []sim.PartitionOffset) string {
	s := ""
	for i, p := range po {
		if i > 0 {
			s += ","
		}
		s += fmt.Sprintf("%d:%d", p.Partition, p.Offset)
	}
	return s
}
