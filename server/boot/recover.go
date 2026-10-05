// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package boot

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"time"

	"github.com/valesordev/andara/server/recovery"
	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/store"
	"github.com/valesordev/andara/server/tickloop"
)

// recoveryClientID names recovery's Kafka clients.
const recoveryClientID = "andara-server-recovery"

// StartExit is the exit code of a StartTickLoop error: recovery's own for a
// refusal it classified (3, 4, 6, 7, 8), ExitFail otherwise.
func StartExit(err error) int {
	var f *recovery.Failure
	if errors.As(err, &f) {
		return f.Exit
	}
	return ExitFail
}

// Lingers reports whether err is a refusal that holds /metrics for the
// mismatch to be scraped (AC-14): a hash mismatch, exit 8, or a restore that
// doesn't reproduce its round, exit 6.
func Lingers(err error) bool {
	var f *recovery.Failure
	return errors.As(err, &f) && (f.Exit == recovery.ExitHashMismatch || f.Exit == recovery.ExitRestore)
}

// RecoveryMetrics is the process's recovery instrument set, registered once.
func (rt *Runtime) RecoveryMetrics() *recovery.Metrics {
	if rt.recMetrics == nil {
		rt.recMetrics = recovery.NewMetrics(rt.Tel.Reg)
	}
	return rt.recMetrics
}

// ownedZones is the Zones a complete round must carry: those of the content
// loaded. Nil when none has loaded.
func (rt *Runtime) ownedZones() []sim.ZoneID {
	if rt.World == nil {
		return nil
	}
	owned := make([]sim.ZoneID, 0, len(rt.World.Zones))
	for id := range rt.World.Zones {
		owned = append(owned, id)
	}
	sort.Slice(owned, func(i, j int) bool { return owned[i] < owned[j] })
	return owned
}

// openSnapshotStore opens the snapshot store recovery reads rounds from, nil
// when snapshots are off and nothing names a round.
func (rt *Runtime) openSnapshotStore() (sim.WorldStore, error) {
	cfg := rt.Cfg
	if cfg.SnapshotInterval <= 0 && cfg.RecoveryPinRound == 0 && !cfg.RecoveryRequireSnapshot {
		return nil, nil
	}
	return store.Open(store.Options{
		Kind:       cfg.SnapshotStore,
		FSPath:     cfg.SnapshotFSPath,
		S3Bucket:   cfg.SnapshotS3Bucket,
		S3Endpoint: cfg.SnapshotS3Endpoint,
	})
}

// RecoverOptions is the Options boot recovery runs with, over the broker or,
// when a test set rt.replay, over that log. The returned func releases what
// it opened.
func (rt *Runtime) RecoverOptions(ctx context.Context, engineCfg sim.Config, inEffect *bool) (recovery.Options, func(), error) {
	cfg := rt.Cfg
	ws, err := rt.openSnapshotStore()
	if err != nil {
		return recovery.Options{}, nil, &recovery.Failure{Err: fmt.Errorf("snapshot store: %w", err), Exit: recovery.ExitConfig, Reason: recovery.ReasonStore}
	}
	o := recovery.Options{
		Store:           ws,
		Owned:           rt.ownedZones(),
		Config:          engineCfg,
		Content:         rt.Content,
		RequireSnapshot: cfg.RecoveryRequireSnapshot,
		ReplayBatch:     cfg.RecoveryReplayBatch,
		After:           rt.recovered(inEffect),
		OnEngine:        func(e *sim.Engine) { rt.Engine = e },
		OnRestored: func(swaps []sim.SwapApplied) {
			*inEffect = true
			rt.contentApplied(swaps)
		},
		Metrics: rt.RecoveryMetrics(),
		Log:     rt.Tel.Log,
		Tracer:  rt.Tel.Tracer,
	}
	if cfg.RecoveryPinRound > 0 {
		r := sim.Tick(cfg.RecoveryPinRound)
		o.Round = &r
	}
	if rt.replay != nil {
		bs, err := rt.replay.Boundaries(ctx)
		if err != nil {
			return recovery.Options{}, nil, fmt.Errorf("recovery: read boundaries: %w", err)
		}
		o.Boundaries = &sliceBoundaries{all: bs}
		o.OpenRecords = func(context.Context, map[int32]int64) (sim.RecordSource, error) { return rt.replay, nil }
		return o, func() {}, nil
	}
	br, err := tickloop.NewBoundaryReader(ctx, cfg.KafkaBrokers, tickloop.EventsTopic, recoveryClientID)
	if err != nil {
		return recovery.Options{}, nil, fmt.Errorf("recovery: boundary reader: %w", err)
	}
	o.Boundaries = br
	o.OpenRecords = func(ctx context.Context, start map[int32]int64) (sim.RecordSource, error) {
		return tickloop.NewCommandSource(ctx, cfg.KafkaBrokers, tickloop.CommandsTopic, recoveryClientID, start)
	}
	return o, br.Close, nil
}

// sliceBoundaries serves recovery boundaries already in hand, as the broker's
// reader would: the test seam rt.replay stands in for the log with.
type sliceBoundaries struct {
	all  []sim.TickCompleted
	next int
	seek bool
}

func (s *sliceBoundaries) SeekAfter(_ context.Context, tick sim.Tick) (int64, error) {
	s.next, s.seek = len(s.all), false
	for i, b := range s.all {
		if b.Tick > tick {
			s.next, s.seek = i, true
			break
		}
	}
	return int64(s.next), nil
}
func (s *sliceBoundaries) BoundaryAfter() bool { return s.seek }
func (s *sliceBoundaries) HeadTick(context.Context) (sim.Tick, error) {
	if len(s.all) == 0 {
		return 0, nil
	}
	return s.all[len(s.all)-1].Tick, nil
}
func (s *sliceBoundaries) Next(_ context.Context, max int, _ time.Duration) ([]tickloop.Boundary, error) {
	n := min(max, len(s.all)-s.next)
	out := make([]tickloop.Boundary, n)
	for i := range out {
		out[i] = tickloop.Boundary{TickCompleted: s.all[s.next+i]}
	}
	s.next += n
	return out, nil
}

// HoldMismatch serves the operator surface on ln for linger after a boot that
// ended in a hash or restore mismatch, so that /metrics is scraped with
// andara_recovery_state_hash_match at 0 (AC-14): /metrics and /livez answer
// 200, /readyz and /startedz 503, and grpc.listen is never bound. A canceled
// ctx (SIGTERM, SIGINT) ends it at once. wait is the clock, time.After in a
// process and a stepped channel in a test.
func (rt *Runtime) HoldMismatch(ctx context.Context, ln net.Listener, linger time.Duration, wait func(time.Duration) <-chan time.Time) {
	srv := &http.Server{Handler: rt.Handler(), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	rt.Tel.Log.LogAttrs(ctx, slog.LevelError, "holding /metrics for the hash mismatch to be scraped", slog.Duration("for", linger))
	select {
	case <-wait(linger):
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(sctx)
}

var _ = io.Discard

// EngineConfig is the Engine's configuration for this process: what recovery
// builds and restores with.
func (rt *Runtime) EngineConfig() sim.Config { return engineConfig(rt.Cfg, rt.Content) }
