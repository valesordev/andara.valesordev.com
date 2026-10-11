// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package boot

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/valesordev/andara/server/lifecycle"
	"github.com/valesordev/andara/server/recovery"
	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/store"
	"github.com/valesordev/andara/server/tickloop"
)

// The deploy lifecycle's server half (AW-SRV-054, docs/specs/deploy/
// lifecycle.md): the Lifecycle service pre-stop calls, and the new pod's
// measurement of the interruption.

// InterruptionBuckets are andara_deploy_interruption_seconds's, bracketing the
// RTOs (120 s at M2, 60 s at Phase 1 exit) and the grace period.
var InterruptionBuckets = []float64{5, 10, 20, 30, 60, 120, 300}

// interruption is what the boot learned from recovery for the measurement:
// when the old World last ticked, and the deploy it was, if the restored round
// says. Observed once, when the process first reports serving.
type interruption struct {
	once     sync.Once
	hist     prometheus.Histogram
	start    time.Time // the head Tick Boundary Record's CreateTime; zero: nothing to observe
	deployID string    // `<image tag>@<tick>` when the restored round carries a deploy: tag
	round    sim.Tick
}

// deployMetrics registers andara_deploy_interruption_seconds once.
func (rt *Runtime) deployMetrics() *interruption {
	rt.deployOnce.Do(func() {
		h := prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "andara_deploy_interruption_seconds",
			Help:    "Wall time from the head Tick Boundary Record a boot recovered to, the old World's last tick, to this process first reporting serving. Observed once per boot.",
			Buckets: InterruptionBuckets,
		})
		if rt.Tel.Reg != nil {
			// A registry another Runtime already registered it on (a test's)
			// keeps the one it has.
			if err := rt.Tel.Reg.Register(h); err != nil {
				if are, ok := err.(prometheus.AlreadyRegisteredError); ok {
					h = are.ExistingCollector.(prometheus.Histogram)
				} else {
					panic(err)
				}
			}
		}
		rt.deploy = &interruption{hist: h}
	})
	return rt.deploy
}

// noteRecovered keeps what the interruption is measured from: the head
// boundary's timestamp, and the deploy_id of a restored round tagged
// `deploy:<v>`. A boot whose log had no boundary keeps nothing.
func (rt *Runtime) noteRecovered(ctx context.Context, ws sim.WorldStore, rep recovery.Report) {
	d := rt.deployMetrics()
	d.start, d.round = rep.HeadProducedAt, rep.Round.Tick
	if rep.Round.Tick == 0 || ws == nil {
		return
	}
	keys := make([]string, len(rep.Round.Zones))
	for i, z := range rep.Round.Zones {
		keys[i] = z.Key
	}
	tags, err := store.RoundTags(ctx, ws, keys)
	if err != nil {
		// The measurement is unaffected; only the correlation is lost.
		rt.Tel.Log.LogAttrs(ctx, slog.LevelWarn, "deploy: the restored round's tags could not be read",
			slog.Uint64("round_tick", uint64(rep.Round.Tick)), slog.String("detail", err.Error()))
		return
	}
	for _, t := range tags {
		if v, ok := strings.CutPrefix(t, store.DeployTagPrefix); ok {
			d.deployID = lifecycle.DeployID(v, rep.Round.Tick)
			return
		}
	}
}

// observeServing is called when the process reports serving: the first call
// observes andara_deploy_interruption_seconds and logs `deploy interruption`
// under a deploy.recovery span (AC-7). Every later call, and a boot that
// recovered from no boundary, does nothing.
func (rt *Runtime) observeServing() {
	d := rt.deploy
	if d == nil {
		return
	}
	d.once.Do(func() {
		if d.start.IsZero() {
			return
		}
		now := rt.clock()
		secs := now.Sub(d.start).Seconds()
		d.hist.Observe(secs)
		// The span covers the interruption itself: from the old World's last
		// tick to serving, on the new process's trace. deploy_id is how a query
		// finds the old pod's deploy.prestop_snapshot (ruling 5).
		attrs := []attribute.KeyValue{attribute.Float64("seconds", secs), attribute.Int64("round_tick", int64(d.round))}
		if d.deployID != "" {
			attrs = append(attrs, attribute.String("deploy_id", d.deployID))
		}
		ctx, span := rt.Tel.Tracer.Start(context.Background(), "deploy.recovery",
			trace.WithNewRoot(), trace.WithTimestamp(d.start), trace.WithAttributes(attrs...))
		traceID := ""
		if sc := span.SpanContext(); sc.HasTraceID() {
			traceID = sc.TraceID().String()
		}
		rt.Tel.Log.LogAttrs(ctx, slog.LevelInfo, "deploy interruption",
			slog.Float64("seconds", secs), slog.Uint64("round_tick", uint64(d.round)),
			slog.String("deploy_id", d.deployID), slog.String("trace_id", traceID))
		span.End(trace.WithTimestamp(now))
	})
}

// WatchServing observes the interruption the moment the process first
// reports serving, whether or not a probe asks: /readyz calls observeServing
// too, and whichever is first counts. Returns when it has, or ctx ends.
func (rt *Runtime) WatchServing(ctx context.Context) {
	t := time.NewTicker(50 * time.Millisecond)
	defer t.Stop()
	for {
		if rt.Ready() {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (rt *Runtime) clock() time.Time {
	if rt.now != nil {
		return rt.now()
	}
	return time.Now()
}

// StartLifecycle serves the Lifecycle service on deploy.control_socket for the
// running loop (ruling 1). The caller closes it.
func (rt *Runtime) StartLifecycle(loop *tickloop.Loop) (*lifecycle.Server, error) {
	cfg := rt.Cfg
	snap := loop.Snapshotter()
	svc := lifecycle.New(lifecycle.Options{
		Announcer:     rt.Egress,
		Refuser:       rt.Ingress,
		Rounds:        snap,
		Store:         snap.Store(),
		Committed:     loop.CommittedThrough,
		NoticeLead:    cfg.DeployNoticeLead,
		AdmitWait:     cfg.DeployAdmitWait,
		ExpectedBack:  cfg.DeployExpectedBack,
		ImageTag:      cfg.DeployImageTag,
		UploadTimeout: cfg.SnapshotUploadTimeout,
		TickInterval:  loop.Interval(),
		Log:           rt.Tel.Log,
		Tracer:        rt.Tel.Tracer,
	})
	srv, err := lifecycle.Listen(cfg.DeployControlSocket, svc)
	if err != nil {
		return nil, err
	}
	rt.Tel.Log.LogAttrs(context.Background(), slog.LevelInfo, "lifecycle listen", slog.String("socket", cfg.DeployControlSocket))
	return srv, nil
}
