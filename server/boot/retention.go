// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package boot

import (
	"context"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/store"
)

// Retained-round kinds: andara_snapshot_rounds_retained's `kind` label.
const (
	retainedUntagged = "untagged"
	retainedTagged   = "tagged"
)

// RetentionMetrics is the retention sweep's instrumentation (AW-SRV-055 §7).
type RetentionMetrics struct {
	// Retained is the rounds the last sweep left, by kind.
	Retained *prometheus.GaugeVec
}

// NewRetentionMetrics builds the sweep's instruments with both kinds
// initialised, and registers them on reg when it isn't nil.
func NewRetentionMetrics(reg prometheus.Registerer) *RetentionMetrics {
	m := &RetentionMetrics{
		Retained: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "andara_snapshot_rounds_retained",
			Help: "Snapshot rounds the last retention sweep kept, by whether any of their objects carries a round tag.",
		}, []string{"kind"}),
	}
	for _, k := range []string{retainedUntagged, retainedTagged} {
		m.Retained.WithLabelValues(k)
	}
	if reg != nil {
		reg.MustRegister(m.Retained)
	}
	return m
}

// snapshotSweeper is the one Sweeper the retention loop and
// Admin.TagSnapshotRound share: its lock keeps a tag from landing on a round
// a sweep has already decided to delete.
func (rt *Runtime) snapshotSweeper() *store.Sweeper {
	rt.sweeperOnce.Do(func() { rt.sweeper = store.NewSweeper() })
	return rt.sweeper
}

// retentionPolicy is the snapshot.keep_* and max_round_age keys, and the
// round recovery.pin_round names.
func (rt *Runtime) retentionPolicy() store.Retention {
	return store.Retention{
		KeepRounds: rt.Cfg.SnapshotKeepRounds,
		KeepTagged: rt.Cfg.SnapshotKeepDeployRounds,
		MaxAge:     rt.Cfg.SnapshotMaxRoundAge,
		Pin:        sim.Tick(rt.Cfg.RecoveryPinRound),
	}
}

// RunSnapshotRetention sweeps the snapshot store once now and then every
// snapshot.interval until ctx ends (AW-SRV-055). A process with snapshots off
// takes no rounds, so it sweeps nothing; its gauge reads zero.
func (rt *Runtime) RunSnapshotRetention(ctx context.Context) {
	m := NewRetentionMetrics(rt.Tel.Reg)
	if rt.Cfg.SnapshotInterval <= 0 {
		return
	}
	ws, err := rt.openSnapshotStore()
	if err != nil {
		rt.Tel.Log.LogAttrs(ctx, slog.LevelError, "snapshot retention: store", slog.String("detail", err.Error()))
		return
	}
	ts, ok := ws.(store.TagStore)
	if !ok {
		rt.Tel.Log.LogAttrs(ctx, slog.LevelError, "snapshot retention: the store cannot delete; no sweep runs")
		return
	}
	t := time.NewTicker(rt.Cfg.SnapshotInterval)
	defer t.Stop()
	for {
		rt.sweepSnapshots(ctx, ts, m, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// sweepSnapshots runs one sweep and reports it: an info line with what it did,
// or an error line for a store error, after which the next sweep tries again.
func (rt *Runtime) sweepSnapshots(ctx context.Context, ts store.TagStore, m *RetentionMetrics, now time.Time) {
	sw := rt.snapshotSweeper()
	sw.Lock()
	res, err := sw.Sweep(ctx, ts, rt.listedZones(), rt.zonesAt(), rt.retentionPolicy(), now)
	sw.Unlock()
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		rt.Tel.Log.LogAttrs(ctx, slog.LevelError, "snapshot retention sweep failed",
			slog.Int("deleted", res.Deleted), slog.String("detail", err.Error()))
		return
	}
	m.Retained.WithLabelValues(retainedUntagged).Set(float64(res.KeptUntagged))
	m.Retained.WithLabelValues(retainedTagged).Set(float64(res.KeptTagged))
	rt.Tel.Log.LogAttrs(ctx, slog.LevelInfo, "snapshot retention sweep",
		slog.Int("deleted", res.Deleted),
		slog.Int("kept_tagged", res.KeptTagged),
		slog.Int("kept_newest", res.KeptUntagged),
		slog.Uint64("newest_complete", uint64(res.NewestComplete)))
}
