// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package tickloop

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	logv1 "github.com/valesordev/andara/gen/go/andara/log/v1"
	"github.com/valesordev/andara/server/sim"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// SnapshotKey is the record key of a SnapshotWritten on andara.events.v1.
// A control record is told apart by its key, the way TickCompleted is.
const SnapshotKey = "snapshot-written"

// ManifestPublisher writes SnapshotWritten records. Separate from Publisher
// rather than a method on it: a manifest is audit and tooling, nothing in the
// recovery path reads it, and a process with no broker still takes snapshots.
// A nil ManifestPublisher writes no manifest and fails no round.
type ManifestPublisher interface {
	ProduceSnapshots(ctx context.Context, records []*logv1.SnapshotWritten) error
}

// SnapshotMetrics is AW-SRV-006's observability contract.
type SnapshotMetrics struct {
	Duration    prometheus.Histogram   // andara_snapshot_duration_seconds — the whole round
	TickStall   prometheus.Histogram   // andara_snapshot_tick_stall_seconds — the in-tick copy
	Bytes       *prometheus.GaugeVec   // {zone}
	LastTick    prometheus.Gauge       //
	Age         prometheus.GaugeFunc   // derived, so it rises without a round running
	Failures    *prometheus.CounterVec // {reason}: store, encode, timeout, stall
	Rounds      *prometheus.CounterVec // {outcome}: complete, incomplete
	lastRoundAt atomic.Int64           // unix nanos of the last complete round; 0 = none yet
	startedAt   time.Time
}

// SnapshotBuckets bracket the 5 ms stall budget and the 30 s upload timeout,
// so both thresholds are measured rather than interpolated.
var SnapshotBuckets = []float64{0.0005, 0.001, 0.0025, 0.005, 0.01, 0.05, 0.25, 1, 5, 15, 30, 60}

// NewSnapshotMetrics registers the snapshot metrics on reg (nil registers
// nothing).
func NewSnapshotMetrics(reg prometheus.Registerer) *SnapshotMetrics {
	m := &SnapshotMetrics{startedAt: time.Now()}
	m.Duration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name: "andara_snapshot_duration_seconds", Help: "Time for a whole snapshot round, copy through the last Put.", Buckets: SnapshotBuckets,
	})
	// A separate series from the round's duration, and the one that matters
	// for players: this is the part inside the tick.
	m.TickStall = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name: "andara_snapshot_tick_stall_seconds", Help: "Time the snapshot copy held the tick — the part players can feel.", Buckets: SnapshotBuckets,
	})
	m.Bytes = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "andara_snapshot_bytes", Help: "Encoded size of the last snapshot written per Zone. Cardinality: Zones, bounded by content.",
	}, []string{"zone"})
	m.LastTick = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "andara_snapshot_last_tick", Help: "Tick of the newest complete snapshot round.",
	})
	// A GaugeFunc, not a Gauge: SnapshotStale fires when rounds have stopped
	// happening, and a value only written by a completing round would freeze
	// at its last value exactly when it needed to rise. Before the first
	// round it measures from process start, so a process that has never
	// snapshotted still alerts.
	m.Age = prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "andara_snapshot_age_seconds", Help: "Seconds since the newest complete snapshot round, or since start if there has been none.",
	}, func() float64 {
		if ns := m.lastRoundAt.Load(); ns != 0 {
			return time.Since(time.Unix(0, ns)).Seconds()
		}
		return time.Since(m.startedAt).Seconds()
	})
	m.Failures = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "andara_snapshot_failures_total", Help: "Snapshot failures by reason. store, encode, and timeout count once per Zone that failed; stall counts once per round.",
	}, []string{"reason"})
	m.Rounds = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "andara_snapshot_rounds_total", Help: "Snapshot rounds by outcome.",
	}, []string{"outcome"})
	// Pre-create every label value, so a dashboard and an alert see a zero
	// rather than a missing series before the first failure (CLAUDE.md §7).
	for _, r := range []string{"store", "encode", "timeout", "stall"} {
		m.Failures.WithLabelValues(r)
	}
	for _, o := range []string{"complete", "incomplete"} {
		m.Rounds.WithLabelValues(o)
	}
	if reg != nil {
		reg.MustRegister(m.Duration, m.TickStall, m.Bytes, m.LastTick, m.Age, m.Failures, m.Rounds)
	}
	return m
}

// SnapshotOptions configures the round runner.
type SnapshotOptions struct {
	Store sim.WorldStore
	// Interval is snapshot.interval. Zero disables snapshots entirely.
	Interval time.Duration
	// MaxStall is snapshot.max_stall_ms — a warning threshold, not a refusal.
	MaxStall time.Duration
	// UploadTimeout is snapshot.upload_timeout: a round past it is failed,
	// not queued behind the next.
	UploadTimeout time.Duration
	// Manifest is optional; nil writes no SnapshotWritten records.
	Manifest ManifestPublisher

	Log      *slog.Logger
	Tracer   trace.Tracer
	Registry prometheus.Registerer
	// Now is the clock, for tests. Nil means time.Now.
	Now func() time.Time
	// OnRound, if set, is called when a round finishes, on the round's
	// goroutine. Tests use it; nothing in production does.
	OnRound func(tick sim.Tick, err error)
}

// Snapshotter runs snapshot rounds: the copy on the tick goroutine at a
// boundary, and everything after it off the tick.
//
// The split is the whole design. A round costs the tick a deep copy and
// nothing else — encode, hash, and upload happen on a goroutine that owns an
// immutable body — so a slow store cannot become a slow World. It also means a
// round outlives the tick that started it, which is why the runner is
// single-flight: see Maybe.
type Snapshotter struct {
	opts    SnapshotOptions
	log     *slog.Logger
	tracer  trace.Tracer
	metrics *SnapshotMetrics
	now     func() time.Time

	inFlight atomic.Bool
	// lastRound is when the last round *started*, so the cadence does not
	// drift with how long a round takes.
	mu        sync.Mutex
	lastRound time.Time
	wg        sync.WaitGroup
}

// NewSnapshotter builds a round runner. A zero Interval returns a runner that
// never takes a round, so the loop needs no nil check.
func NewSnapshotter(o SnapshotOptions) (*Snapshotter, error) {
	if o.Interval > 0 && o.Store == nil {
		return nil, errors.New("tickloop: snapshot.interval is set but no store was given")
	}
	if o.Interval > 0 && o.UploadTimeout <= 0 {
		return nil, errors.New("tickloop: snapshot.upload_timeout must be positive")
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Tracer == nil {
		o.Tracer = noop.NewTracerProvider().Tracer("andara-server")
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	s := &Snapshotter{opts: o, log: o.Log, tracer: o.Tracer, metrics: NewSnapshotMetrics(o.Registry), now: o.Now}
	s.lastRound = o.Now()
	return s, nil
}

// Metrics exposes the snapshot instruments, for tests and the boot.
func (s *Snapshotter) Metrics() *SnapshotMetrics { return s.metrics }

// Enabled reports whether snapshots are configured.
func (s *Snapshotter) Enabled() bool { return s != nil && s.opts.Interval > 0 }

// Maybe takes a round if the interval has elapsed, and returns immediately
// otherwise. Called by the loop at a tick boundary and nowhere else (AC-8):
// the round starts on a boundary, never mid-tick, and the tick it names is the
// one whose TickCompleted was just emitted.
//
// Single-flight. A round still running when the next interval elapses does not
// get a second one started behind it: the story's rule is that a round past
// snapshot.upload_timeout is failed rather than queued, and the running round
// fails itself on its own deadline. Starting a second would mean two rounds
// writing the same keys with different state. The configuration validation
// keeps upload_timeout at or under interval, so a skip here means the store is
// already failing and the in-flight round is about to count it.
//
// The copy happens on the caller's goroutine — this is the in-tick work — and
// everything after it is handed to a goroutine.
func (s *Snapshotter) Maybe(ctx context.Context, e *sim.Engine) {
	if !s.Enabled() {
		return
	}
	// The tick comes from the engine rather than from the caller. They are
	// the same number — the loop calls this right after Step, so the engine
	// is at the tick whose TickCompleted was just published — and taking it
	// from the engine means AC-8 holds structurally instead of depending on
	// every caller passing the right one.
	tick := e.Tick()
	now := s.now()
	s.mu.Lock()
	due := now.Sub(s.lastRound) >= s.opts.Interval
	if due {
		s.lastRound = now
	}
	s.mu.Unlock()
	if !due {
		return
	}
	if !s.inFlight.CompareAndSwap(false, true) {
		s.log.LogAttrs(ctx, slog.LevelWarn, "snapshot round skipped: the previous one is still running",
			slog.Uint64("tick", uint64(tick)), slog.String("trace_id", traceID(ctx)))
		return
	}

	// The round's own root span, explicitly not inside sim.tick: a round
	// outlives the tick, and hanging it under the tick would make every
	// tick trace carry a span that ends after it.
	// Linked to the tick that started it rather than parented to it: a link
	// keeps the round correlatable without making a tick's trace contain a
	// span that ends after the tick does. The link also survives the tick
	// span being dropped, which most of them are — telemetry.SpanFilter
	// keeps one tick in a hundred, and every round.
	roundCtx, round := s.tracer.Start(context.WithoutCancel(ctx), "persistence.snapshot",
		trace.WithNewRoot(),
		trace.WithLinks(trace.LinkFromContext(ctx)),
		trace.WithAttributes(attribute.Int64("tick", int64(tick))))

	// The copy: in-tick, but parented to the round.
	_, copySpan := s.tracer.Start(roundCtx, "snapshot.copy")
	// Wall clock for the record the snapshot carries, monotonic for the
	// measurement: s.now is injectable so a test can control the *cadence*,
	// and a duration measured against a clock a test holds still would be
	// zero no matter how long the copy took.
	began, startedMono := s.now(), time.Now()
	snaps := e.SnapshotAll(began.UnixNano())
	stall := time.Since(startedMono)
	copySpan.SetAttributes(attribute.Int("zones", len(snaps)), attribute.Float64("stall_ms", float64(stall.Microseconds())/1000))
	copySpan.End()

	s.metrics.TickStall.Observe(stall.Seconds())
	if s.opts.MaxStall >= 0 && stall > s.opts.MaxStall {
		// A warning, not a refusal: the tick has already paid this cost, and
		// abandoning the round would add a recovery-time problem to a
		// latency one.
		s.metrics.Failures.WithLabelValues("stall").Inc()
		stallErr := &sim.ErrSnapshotStall{Tick: tick, Elapsed: stall.Seconds(), Budget: s.opts.MaxStall.Seconds()}
		s.log.LogAttrs(roundCtx, slog.LevelWarn, "snapshot copy exceeded its stall budget",
			slog.Uint64("tick", uint64(tick)),
			slog.Float64("stall_ms", float64(stall.Microseconds())/1000),
			slog.Float64("budget_ms", float64(s.opts.MaxStall.Microseconds())/1000),
			slog.String("detail", stallErr.Error()),
			slog.String("trace_id", traceID(roundCtx)))
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer s.inFlight.Store(false)
		defer round.End()
		err := s.run(roundCtx, tick, snaps, startedMono)
		if err != nil {
			round.SetStatus(codes.Error, err.Error())
		}
		if s.opts.OnRound != nil {
			s.opts.OnRound(tick, err)
		}
	}()
}

// run is everything after the copy: encode, upload, manifest. Off the tick.
func (s *Snapshotter) run(ctx context.Context, tick sim.Tick, snaps []sim.Snapshot, startedMono time.Time) error {
	// The round's deadline. A round past it is failed, not queued behind the
	// next one, so the cadence holds even when the store does not.
	ctx, cancel := context.WithTimeout(ctx, s.opts.UploadTimeout)
	defer cancel()

	var (
		missing   []sim.ZoneID
		manifests []*logv1.SnapshotWritten
		total     uint64
		firstErr  error
	)
	// By index: a Snapshot carries a lazily computed hash, and ranging by
	// value would copy the struct and throw that cache away on every read.
	for i := range snaps {
		snap := &snaps[i]
		key := snap.Key()
		envelope, err := s.encode(ctx, snap)
		if err != nil {
			s.metrics.Failures.WithLabelValues("encode").Inc()
			missing = append(missing, snap.Zone)
			firstErr = errors.Join(firstErr, err)
			s.warn(ctx, tick, snap.Zone, key, "snapshot not encoded", err)
			continue
		}
		if err := s.put(ctx, key, envelope, snap); err != nil {
			reason := "store"
			if errors.Is(err, context.DeadlineExceeded) {
				reason = "timeout"
			}
			s.metrics.Failures.WithLabelValues(reason).Inc()
			missing = append(missing, snap.Zone)
			firstErr = errors.Join(firstErr, err)
			s.warn(ctx, tick, snap.Zone, key, "snapshot not written", err)
			continue
		}
		size := uint64(len(envelope))
		total += size
		s.metrics.Bytes.WithLabelValues(string(snap.Zone)).Set(float64(size))
		manifests = append(manifests, manifestFor(snap, key, size))
	}

	duration := time.Since(startedMono)
	s.metrics.Duration.Observe(duration.Seconds())

	if len(missing) > 0 {
		// The Zones that did succeed are left where they are: they are valid
		// objects, and "not selectable" is a property of the round that
		// AW-SRV-007 decides by listing, not something to enforce by
		// deleting. The previous complete round stays the newest selectable
		// one.
		s.metrics.Rounds.WithLabelValues("incomplete").Inc()
		incomplete := &sim.ErrRoundIncomplete{Tick: tick, Missing: missing}
		s.log.LogAttrs(ctx, slog.LevelWarn, "snapshot round incomplete",
			slog.Uint64("tick", uint64(tick)), slog.Int("zones", len(snaps)),
			slog.Int("missing", len(missing)), slog.String("detail", incomplete.Error()),
			slog.Float64("duration_ms", float64(duration.Microseconds())/1000),
			slog.String("trace_id", traceID(ctx)))
		return errors.Join(incomplete, firstErr)
	}

	s.metrics.Rounds.WithLabelValues("complete").Inc()
	s.metrics.LastTick.Set(float64(tick))
	s.metrics.lastRoundAt.Store(s.now().UnixNano())

	// The manifest is written after every object is durable, and its failure
	// does not fail the round: nothing in the recovery path reads it.
	if s.opts.Manifest != nil && len(manifests) > 0 {
		if err := s.opts.Manifest.ProduceSnapshots(ctx, manifests); err != nil {
			s.log.LogAttrs(ctx, slog.LevelWarn, "snapshot manifest not produced",
				slog.Uint64("tick", uint64(tick)), slog.String("detail", err.Error()),
				slog.String("trace_id", traceID(ctx)))
		}
	}

	s.log.LogAttrs(ctx, slog.LevelInfo, "snapshot round complete",
		slog.Uint64("tick", uint64(tick)),
		slog.Int("zones", len(snaps)),
		slog.Uint64("bytes", total),
		slog.Float64("duration_ms", float64(duration.Microseconds())/1000),
		slog.String("trace_id", traceID(ctx)))
	return nil
}

func (s *Snapshotter) encode(ctx context.Context, snap *sim.Snapshot) ([]byte, error) {
	_, span := s.tracer.Start(ctx, "snapshot.encode", trace.WithAttributes(attribute.String("zone", string(snap.Zone))))
	defer span.End()
	b, err := snap.Encode()
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		return nil, err
	}
	span.SetAttributes(attribute.Int("bytes", len(b)))
	return b, nil
}

func (s *Snapshotter) put(ctx context.Context, key string, envelope []byte, snap *sim.Snapshot) error {
	putCtx, span := s.tracer.Start(ctx, "snapshot.put", trace.WithAttributes(
		attribute.String("zone", string(snap.Zone)),
		attribute.String("key", key),
		attribute.Int("bytes", len(envelope)),
	))
	defer span.End()
	if err := s.opts.Store.Put(putCtx, key, envelope); err != nil {
		span.SetStatus(codes.Error, err.Error())
		return fmt.Errorf("zone %s: %w", snap.Zone, err)
	}
	return nil
}

func (s *Snapshotter) warn(ctx context.Context, tick sim.Tick, zone sim.ZoneID, key, msg string, err error) {
	s.log.LogAttrs(ctx, slog.LevelWarn, msg,
		slog.Uint64("tick", uint64(tick)),
		slog.String("zone_id", string(zone)),
		slog.String("key", key),
		slog.String("detail", err.Error()),
		slog.String("trace_id", traceID(ctx)))
}

func manifestFor(snap *sim.Snapshot, key string, size uint64) *logv1.SnapshotWritten {
	hash := snap.StateHash()
	rec := &logv1.SnapshotWritten{
		ZoneId:       string(snap.Zone),
		StateVersion: snap.StateVersion,
		Tick:         uint64(snap.Tick),
		StateHash:    hash[:],
		Key:          key,
		SizeBytes:    size,
	}
	for _, po := range snap.Offsets {
		rec.Offsets = append(rec.Offsets, &logv1.PartitionOffset{Partition: po.Partition, Offset: po.Offset})
	}
	return rec
}

// Wait blocks until any in-flight round has finished. The drain calls it, so a
// shutdown does not abandon a round whose objects are half written — the
// previous complete round would still be the newest, but the partial one's
// Zones would be a puzzle for whoever looked next.
func (s *Snapshotter) Wait() {
	if s == nil {
		return
	}
	s.wg.Wait()
}
