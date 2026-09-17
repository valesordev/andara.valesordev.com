// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package tickloop

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/valesordev/andara/server/sim"
)

// Options configures a Loop. Every duration maps to a sim.* key.
type Options struct {
	Engine    *sim.Engine
	Source    Source
	Publisher Publisher
	Clock     Clock

	TickRate        int           // sim.tick_rate, Hz
	TickBudget      time.Duration // sim.tick_budget_ms
	MaxPerTick      int           // sim.max_per_tick
	DrainTimeout    time.Duration // sim.drain_timeout_ms
	CheckpointEvery int           // sim.checkpoint_every_ticks

	Log      *slog.Logger
	Tracer   trace.Tracer
	Registry prometheus.Registerer
	// OnTick, if set, is called after every tick with its result. Tests use
	// it; production leaves it nil.
	OnTick func(sim.StepResult, time.Duration)
}

// Loop runs the tick loop.
type Loop struct {
	opts    Options
	log     *slog.Logger
	tracer  trace.Tracer
	metrics *Metrics
	clock   Clock

	interval time.Duration

	mu            sync.Mutex
	inflight      sim.Tick
	lastCommitted sim.Tick

	zoneTimes map[sim.ZoneID]time.Duration
}

// DrainTimeoutError is what Run returns when shutdown outlasted
// sim.drain_timeout_ms: exit 1, naming the tick that would not complete.
type DrainTimeoutError struct {
	Tick    sim.Tick
	Timeout time.Duration
}

func (e *DrainTimeoutError) Error() string {
	return fmt.Sprintf("tickloop: drain exceeded %s; tick %d did not complete", e.Timeout, e.Tick)
}

// New validates the options and builds a Loop.
func New(o Options) (*Loop, error) {
	if o.Engine == nil || o.Source == nil || o.Publisher == nil {
		return nil, errors.New("tickloop: engine, source, and publisher are required")
	}
	if o.TickRate < 1 || o.TickRate > 1000 {
		return nil, fmt.Errorf("tickloop: sim.tick_rate must be 1..1000, got %d", o.TickRate)
	}
	if o.TickBudget <= 0 || o.MaxPerTick < 1 || o.CheckpointEvery < 1 || o.DrainTimeout < 0 {
		return nil, errors.New("tickloop: tick_budget, max_per_tick, and checkpoint_every must be positive; drain_timeout non-negative")
	}
	if o.Clock == nil {
		o.Clock = RealClock{}
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Tracer == nil {
		o.Tracer = noop.NewTracerProvider().Tracer("andara-server")
	}
	l := &Loop{
		opts:      o,
		log:       o.Log,
		tracer:    o.Tracer,
		metrics:   NewMetrics(o.Registry),
		clock:     o.Clock,
		interval:  time.Second / time.Duration(o.TickRate),
		zoneTimes: map[sim.ZoneID]time.Duration{},
	}
	return l, nil
}

// Metrics exposes the loop's instruments, for tests.
func (l *Loop) Metrics() *Metrics { return l.metrics }

// Interval is the tick interval.
func (l *Loop) Interval() time.Duration { return l.interval }

// Run ticks until ctx is done, then drains: the in-flight tick completes,
// offsets are checkpointed, SimulationStopped is emitted, and Run returns
// nil (AC-15). If that outlasts DrainTimeout, Run returns a
// DrainTimeoutError naming the tick; the loop goroutine is abandoned, since
// the process exits 1 behind it. A record-source error the loop cannot
// continue past — an offset gap — is returned as is.
func (l *Loop) Run(ctx context.Context) error {
	done := make(chan error, 1)
	go func() { done <- l.run(ctx) }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
	}
	if l.opts.DrainTimeout == 0 {
		return <-done
	}
	// Drain is measured on the real clock even under a stepped one: the
	// stepped clock is for scheduling, and a wedged handler does not
	// advance it.
	timer := time.NewTimer(l.opts.DrainTimeout)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-timer.C:
		l.mu.Lock()
		tick := l.inflight
		l.mu.Unlock()
		return &DrainTimeoutError{Tick: tick, Timeout: l.opts.DrainTimeout}
	}
}

func (l *Loop) run(ctx context.Context) error {
	e := l.opts.Engine
	start := l.clock.Now()
	lastLog := start
	l.log.LogAttrs(ctx, slog.LevelInfo, "tick loop started",
		slog.Uint64("tick", uint64(e.Tick())),
		slog.String("interval", l.interval.String()),
		slog.String("budget", l.opts.TickBudget.String()),
	)
	// The schedule is anchored at start: the first tick is due now and the
	// n-th after it at start + n·interval. A late tick is not skipped and
	// the anchor never moves, so lag is the honest distance behind schedule
	// rather than a number the loop can reset by falling behind (AC-6, AC-8).
	base := e.Tick()
	for {
		if ctx.Err() != nil {
			return l.drain(e)
		}
		tick := e.Tick() + 1
		due := start.Add(time.Duration(tick-base-1) * l.interval)
		now := l.clock.Now()
		lag := now.Sub(due)
		if lag < 0 {
			lag = 0
		}
		l.metrics.Lag.Set(lag.Seconds())

		l.mu.Lock()
		l.inflight = tick
		l.mu.Unlock()
		if err := l.tick(ctx, tick, lag); err != nil {
			return err
		}

		if now = l.clock.Now(); now.Sub(lastLog) >= time.Second {
			lastLog = now
			l.summary(ctx, e.Tick(), lag)
		}
		next := start.Add(time.Duration(tick-base) * l.interval)
		if wait := next.Sub(l.clock.Now()); wait > 0 {
			if !l.clock.Sleep(ctx, wait) {
				return l.drain(e)
			}
		}
	}
}

// tick runs one tick end to end.
func (l *Loop) tick(ctx context.Context, tick sim.Tick, lag time.Duration) error {
	e := l.opts.Engine
	tctx, span := l.tracer.Start(ctx, "sim.tick", trace.WithAttributes(attribute.Int64("tick", int64(tick))))
	defer span.End()

	records, err := l.opts.Source.Poll(e.FaultedPartitions(), l.opts.MaxPerTick)
	starved := false
	switch {
	case errors.Is(err, ErrStarved):
		starved = true
		l.metrics.InputStarved.Inc()
		l.log.LogAttrs(tctx, slog.LevelWarn, "tick input starved: applying nothing",
			slog.Uint64("tick", uint64(tick)), slog.String("detail", err.Error()), slog.String("trace_id", traceID(tctx)))
	case errors.Is(err, sim.ErrOffsetGap):
		span.SetStatus(codes.Error, err.Error())
		return err
	case err != nil:
		return fmt.Errorf("tickloop: poll: %w", err)
	}

	began := l.clock.Now()
	res, err := e.Step(sim.TickInput{Records: records})
	if err != nil {
		// A gap or an unowned Partition is the source's bug, and applying
		// past it would skip history; refuse and exit 1.
		span.SetStatus(codes.Error, err.Error())
		return err
	}
	duration := l.clock.Now().Sub(began)
	if len(res.Unapplied) > 0 {
		l.opts.Source.Requeue(res.Unapplied)
	}
	for _, f := range res.Faults {
		l.metrics.ZoneFaults.WithLabelValues(string(f.Zone)).Inc()
		l.log.LogAttrs(tctx, slog.LevelError, "zone faulted: partition frozen",
			slog.Uint64("tick", uint64(tick)), slog.String("zone", string(f.Zone)),
			slog.Int64("partition", int64(f.Partition)), slog.Int64("offset", f.Offset),
			slog.String("panic", f.Panic), slog.String("trace_id", traceID(tctx)))
	}

	if err := l.opts.Publisher.Publish(tctx, res.Events, res.Completed); err != nil {
		l.metrics.PublishFailures.WithLabelValues("events").Inc()
		l.log.LogAttrs(tctx, slog.LevelWarn, "events not published", slog.Uint64("tick", uint64(tick)), slog.String("detail", err.Error()), slog.String("trace_id", traceID(tctx)))
	}
	if len(res.Outbound) > 0 {
		if err := l.opts.Publisher.Produce(tctx, res.Outbound); err != nil {
			l.metrics.PublishFailures.WithLabelValues("commands").Inc()
			l.log.LogAttrs(tctx, slog.LevelWarn, "cross-zone commands not produced", slog.Uint64("tick", uint64(tick)), slog.Int("count", len(res.Outbound)), slog.String("detail", err.Error()), slog.String("trace_id", traceID(tctx)))
		}
	}

	l.metrics.Ticks.Inc()
	l.metrics.TickDuration.Observe(duration.Seconds())
	l.metrics.AppliedRecords.Add(float64(res.Completed.CommandsApplied))
	l.metrics.DeferredRecords.Set(float64(l.opts.Source.Pending()))
	for p, lagRecs := range l.opts.Source.Lag() {
		l.metrics.ConsumerLag.WithLabelValues(strconv.Itoa(int(p))).Set(float64(lagRecs))
	}
	overrun := duration > l.opts.TickBudget
	if overrun {
		l.metrics.Overruns.Inc()
		l.log.LogAttrs(tctx, slog.LevelWarn, "tick overran its budget",
			slog.Uint64("tick", uint64(tick)),
			slog.Float64("duration_ms", float64(duration.Microseconds())/1000),
			slog.Float64("budget_ms", float64(l.opts.TickBudget.Microseconds())/1000),
			slog.String("zone", l.slowestZone()),
			slog.String("trace_id", traceID(tctx)))
	}
	l.observeZones(tctx, tick)
	span.SetAttributes(
		attribute.Int("record_count", len(records)),
		attribute.Int("event_count", len(res.Events)),
		attribute.Bool("overrun", overrun),
		attribute.Bool("starved", starved),
		attribute.Float64("lag_seconds", lag.Seconds()),
	)

	if tick%sim.Tick(l.opts.CheckpointEvery) == 0 {
		l.checkpoint(tctx, res.Completed)
	}
	l.mu.Lock()
	l.metrics.CheckpointAge.Set(float64(tick - l.lastCommitted))
	l.mu.Unlock()
	if l.opts.OnTick != nil {
		l.opts.OnTick(res, duration)
	}
	return nil
}

func (l *Loop) checkpoint(ctx context.Context, tc sim.TickCompleted) {
	if err := l.opts.Source.Commit(ctx, tc.Offsets); err != nil {
		l.metrics.PublishFailures.WithLabelValues("checkpoint").Inc()
		l.log.LogAttrs(ctx, slog.LevelWarn, "offsets not checkpointed", slog.Uint64("tick", uint64(tc.Tick)), slog.String("detail", err.Error()), slog.String("trace_id", traceID(ctx)))
		return
	}
	l.mu.Lock()
	l.lastCommitted = tc.Tick
	l.mu.Unlock()
}

// drain finishes the loop: checkpoint what the last tick left, emit
// SimulationStopped, close the seams.
func (l *Loop) drain(e *sim.Engine) error {
	ctx := context.Background()
	l.log.LogAttrs(ctx, slog.LevelInfo, "tick loop draining", slog.Uint64("tick", uint64(e.Tick())))
	l.checkpoint(ctx, sim.TickCompleted{Tick: e.Tick(), Offsets: copyOffsets(e.State().Offsets)})
	ev := e.Stop("draining")
	if err := l.opts.Publisher.Publish(ctx, []sim.Event{ev}, sim.TickCompleted{}); err != nil {
		l.metrics.PublishFailures.WithLabelValues("events").Inc()
	}
	l.log.LogAttrs(ctx, slog.LevelInfo, "tick loop stopped", slog.Uint64("tick", uint64(e.Tick())))
	return nil
}

func copyOffsets(in map[int32]int64) map[int32]int64 {
	out := make(map[int32]int64, len(in))
	for p, o := range in {
		out[p] = o
	}
	return out
}

// summary is the once-per-second info line.
func (l *Loop) summary(ctx context.Context, tick sim.Tick, lag time.Duration) {
	var consumerLag int64
	for _, v := range l.opts.Source.Lag() {
		consumerLag += v
	}
	l.mu.Lock()
	age := tick - l.lastCommitted
	l.mu.Unlock()
	l.log.LogAttrs(ctx, slog.LevelInfo, "tick",
		slog.Uint64("tick", uint64(tick)),
		slog.Float64("lag_ms", float64(lag.Microseconds())/1000),
		slog.Int64("consumer_lag", consumerLag),
		slog.Int("deferred", l.opts.Source.Pending()),
		slog.Uint64("checkpoint_age_ticks", uint64(age)),
		slog.Any("applied_offsets", offsetsForLog(l.opts.Engine.State().Offsets)),
	)
}

// offsetsForLog renders only the Partitions that have moved.
func offsetsForLog(in map[int32]int64) map[string]int64 {
	out := map[string]int64{}
	for p, o := range in {
		if o > 0 {
			out[strconv.Itoa(int(p))] = o
		}
	}
	return out
}

// --- per-Zone timing -------------------------------------------------------

// Begin implements sim.ZoneTimer: the loop attributes handler time to
// Zones without the core reading a clock.
func (l *Loop) Begin(zone sim.ZoneID) func() {
	start := l.clock.Now()
	return func() {
		l.zoneTimes[zone] += l.clock.Now().Sub(start)
	}
}

// observeZones flushes the per-Zone accumulators into the histogram and a
// sim.zone_tick span each, then clears them. Per-Entity spans are not
// emitted.
func (l *Loop) observeZones(ctx context.Context, tick sim.Tick) {
	for zone, d := range l.zoneTimes {
		l.metrics.ZoneTickDuration.WithLabelValues(string(zone)).Observe(d.Seconds())
		_, span := l.tracer.Start(ctx, "sim.zone_tick", trace.WithAttributes(
			attribute.Int64("tick", int64(tick)), attribute.String("zone", string(zone)),
			attribute.Float64("duration_ms", float64(d.Microseconds())/1000)))
		span.End()
		delete(l.zoneTimes, zone)
	}
}

// slowestZone names the Zone that took the most of this tick, for the
// overrun line, or "" when no Zone was applied to.
func (l *Loop) slowestZone() string {
	var best sim.ZoneID
	var max time.Duration
	for zone, d := range l.zoneTimes {
		if d > max || (d == max && zone < best) {
			best, max = zone, d
		}
	}
	return string(best)
}

func traceID(ctx context.Context) string {
	sc := trace.SpanFromContext(ctx).SpanContext()
	if !sc.HasTraceID() {
		return ""
	}
	return sc.TraceID().String()
}
