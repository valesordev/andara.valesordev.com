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

	"github.com/prometheus/client_golang/prometheus"
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
	// HeadTick is the tick of the log's last boundary now: where replay ends.
	HeadTick(ctx context.Context) (sim.Tick, error)
}

// Options is everything Recover reads, so that nothing in it reaches for a
// broker or a clock of its own.
type Options struct {
	// Store holds the snapshot rounds; nil means there are none.
	Store sim.WorldStore
	// Listed is the Zones discovery lists: the loaded content's. A round is
	// judged complete against the Zones of the content it records, which
	// ZonesAt resolves (AC-16).
	Listed []sim.ZoneID
	// ZonesAt resolves a round's recorded content to its Zones. Nil builds it
	// on Prepare (or sim.PrepareContent over Content), caching per versions set.
	ZonesAt store.ZonesAt
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
	// OnEngine is called with the Engine as soon as it exists, restored or
	// fresh, before replay applies anything to it: what the replay's
	// per-tick hook reads the content in effect from.
	OnEngine func(*sim.Engine)
	// OnRestored is called with the content a restored round had in effect.
	OnRestored func([]sim.SwapApplied)

	// Restores counts the restore under andara_restore_total; nil uses
	// Metrics.Restores. A verify inside a running server counts on the live
	// instrument while every other recovery instrument stays its own.
	Restores *prometheus.CounterVec
	Metrics  *Metrics
	Log      *slog.Logger
	Tracer   trace.Tracer
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
	// TraceID is the recovery.run trace: what the boot summary line carries.
	TraceID string
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
	rep.TraceID = traceID

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
	// Each object read starts and ends its own span around the read itself.
	ctx = store.WithReadObserver(ctx, func(zone sim.ZoneID, key string) func(int, error) {
		_, sp := o.Tracer.Start(ctx, "recovery.load_snapshot", trace.WithAttributes(attribute.String("zone_id", string(zone)), attribute.String("key", key)))
		return func(n int, err error) {
			sp.SetAttributes(attribute.Int("bytes", n))
			if err != nil {
				sp.SetStatus(codes.Error, err.Error())
			}
			sp.End()
		}
	})
	round, state, found, err := o.selectRound(ctx)
	if err != nil {
		o.logSelection(ctx, err, traceID)
		return fail(err)
	}
	var eng *sim.Engine
	var begin map[int32]int64
	cfg := o.Config
	if found {
		rep.Round = round
		o.Log.InfoContext(ctx, "recovery starting from a snapshot round", "tick", uint64(round.Tick), "zones", len(round.Zones),
			"offsets", offsetsString(state.Offsets), "trace_id", traceID)
		prepare := o.Prepare
		if prepare == nil {
			prepare = func(v map[string]uint64) (sim.Topology, error) { return sim.PrepareContent(o.Content, v) }
		}
		topo, err := prepare(state.Content)
		if err != nil {
			var unknown *sim.ErrContentVersionUnknown
			if errors.As(err, &unknown) {
				// A version the source doesn't have: the round doesn't restore
				// onto it, which is exit 6 (AC-16), where a source that failed
				// to answer is exit 1 and must not set the gauge.
				err = &sim.ErrRoundContent{Tick: round.Tick, Versions: state.Content, Unknown: unknown}
			} else {
				err = fmt.Errorf("snapshot round at tick %d: rebuild its content: %w", round.Tick, err)
			}
			o.logSelection(ctx, err, traceID)
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
			restores := o.Restores
			if restores == nil {
				restores = o.Metrics.Restores
			}
			restores.WithLabelValues(caller, oc).Inc()
			rspan.SetAttributes(attribute.String("outcome", oc))
		}
		rspan.End()
		if err != nil {
			o.logRestoreMismatch(ctx, err, round.Tick, traceID)
			o.Metrics.SetHashMatch(false)
			return fail(err)
		}
		if o.OnEngine != nil {
			o.OnEngine(eng)
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
		if o.OnEngine != nil {
			o.OnEngine(eng)
		}
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
	switch c := src.(type) {
	case io.Closer:
		defer func() { _ = c.Close() }()
	case interface{ Close() }:
		defer c.Close()
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
			err = &HashMismatchError{Tick: hm.Tick, Expected: hm.Recorded, Actual: hm.Replayed, Round: rep.Round.Tick, cause: hm}
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

// zonesAt is ZonesAt, or the one built on the content source: it prepares the
// topology the recorded versions build and reads its Zones, once per set.
func (o *Options) zonesAt() store.ZonesAt {
	if o.ZonesAt != nil {
		return o.ZonesAt
	}
	prepare := o.Prepare
	if prepare == nil {
		prepare = func(v map[string]uint64) (sim.Topology, error) { return sim.PrepareContent(o.Content, v) }
	}
	return NewZonesAt(prepare)
}

// NewZonesAt is store.ZonesAt over prepare: the Zones of the topology a set of
// recorded versions builds, resolved once per set.
func NewZonesAt(prepare func(map[string]uint64) (sim.Topology, error)) store.ZonesAt {
	cache := map[string][]sim.ZoneID{}
	return func(versions map[string]uint64) ([]sim.ZoneID, error) {
		key := fmt.Sprint(versions)
		if zs, ok := cache[key]; ok {
			return zs, nil
		}
		topo, err := prepare(versions)
		if err != nil {
			return nil, err
		}
		zs := []sim.ZoneID{}
		if topo.World != nil {
			for id := range topo.World.Zones {
				zs = append(zs, id)
			}
		}
		sort.Slice(zs, func(i, j int) bool { return zs[i] < zs[j] })
		cache[key] = zs
		return zs, nil
	}
}

// selectRound chooses the round to restore: the named one, else the newest
// complete. found is false for a cold start.
func (o *Options) selectRound(ctx context.Context) (store.Round, sim.RoundState, bool, error) {
	if o.Round != nil {
		if o.Store == nil {
			return store.Round{}, sim.RoundState{}, false, fmt.Errorf("round %d is named and no snapshot store is configured", *o.Round)
		}
		r, st, err := store.RoundAt(ctx, o.Store, o.Listed, o.zonesAt(), *o.Round)
		if err != nil {
			return store.Round{}, sim.RoundState{}, false, err
		}
		return r, st, true, nil
	}
	if o.Store != nil {
		// A round written by a newer binary is a refusal naming both versions,
		// not a silent fall back to an older round the operator did not choose.
		v, err := store.StateVersionOf(ctx, o.Store, o.Listed)
		if err != nil {
			return store.Round{}, sim.RoundState{}, false, fmt.Errorf("snapshot store: %w", err)
		}
		if v > sim.StateVersion {
			return store.Round{}, sim.RoundState{}, false, &sim.ErrStateVersion{Have: v, Want: sim.StateVersion}
		}
		r, st, ok, err := store.NewestComplete(ctx, o.Store, o.Listed, o.zonesAt())
		if err != nil {
			return store.Round{}, sim.RoundState{}, false, fmt.Errorf("snapshot store: %w", err)
		}
		if ok {
			return r, st, true, nil
		}
	}
	if o.RequireSnapshot {
		return store.Round{}, sim.RoundState{}, false, &sim.ErrRoundIncomplete{Cause: sim.RoundMissing, Zones: o.Listed}
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

// replay streams the boundaries after the Engine's tick up to the log's head as
// it is when replay begins, ReplayBatch at a time, never holding the topic. The
// head is fixed up front: a log still being written has an end that moves, and a
// replay that chased it would never finish.
func (o *Options) replay(ctx context.Context, e *sim.Engine, src sim.RecordSource, rep *Report, records *int64) error {
	head, err := o.Boundaries.HeadTick(ctx)
	if err != nil {
		return fmt.Errorf("boundary head: %w", err)
	}
	counting := countingSource{src: src, n: records}
	for e.Tick() < head {
		bs, err := o.Boundaries.Next(ctx, o.ReplayBatch, o.Poll)
		if err != nil {
			return err
		}
		tcs := make([]sim.TickCompleted, 0, len(bs))
		for _, b := range bs {
			if b.Tick > e.Tick() && b.Tick <= head {
				tcs = append(tcs, b.TickCompleted)
			}
		}
		if len(tcs) == 0 {
			continue
		}
		if err := e.ReplayEach(tcs, counting, o.After); err != nil {
			return err
		}
		rep.Replayed += uint64(len(tcs))
	}
	return nil
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

// logSelection logs a refusal made choosing or rebuilding a round. A round that
// doesn't restore onto the content it records (AC-16) is a restore mismatch:
// the `recovery restore mismatch` line, and the hash-match gauge at 0. Anything
// else is `recovery refused`.
func (o *Options) logSelection(ctx context.Context, err error, traceID string) {
	var (
		rc *sim.ErrRoundContent
		zu *sim.ErrRoundZoneUnknown
	)
	switch {
	case errors.As(err, &rc):
		o.logRestoreMismatch(ctx, err, rc.Tick, traceID)
	case errors.As(err, &zu):
		o.logRestoreMismatch(ctx, err, zu.Tick, traceID)
	default:
		o.logRefusal(ctx, err, traceID)
		return
	}
	o.Metrics.SetHashMatch(false)
}
