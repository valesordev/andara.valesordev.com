// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

// Package sizing is the seed a `-tags sizing` build of andara-server
// (andara-server-sizing) wires in (AW-SRV-065): simtest's sizing fixture's
// Entities, placed into the booted World once its content is in effect, so a
// measurement of the tick reads a loaded World. Only that build imports it,
// and through it server/simtest: the production binary's dependencies never
// include either, which a test in cmd/andara-server holds.
package sizing

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/valesordev/andara/server/config"
	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/simtest"
)

// GuardError is a setting the sizing build refuses to boot with (AC-6).
type GuardError struct{ Key, Value, Want string }

func (e *GuardError) Error() string {
	return fmt.Sprintf("sizing build: %s=%q, want %q", e.Key, e.Value, e.Want)
}

// Guard refuses every configuration but a local one on the memory Command
// source with snapshots off, logging the first setting that differs at error.
// That is what keeps the seeded World off the log and out of the snapshot
// store: it runs before anything is built, so nothing is produced and no
// Snapshot is written.
func Guard(cfg *config.Config, log *slog.Logger) error {
	var err *GuardError
	switch {
	case cfg.Environment != "local":
		err = &GuardError{Key: "ANDARA_ENV", Value: cfg.Environment, Want: "local"}
	case cfg.SimSource != "memory":
		err = &GuardError{Key: "sim.source", Value: cfg.SimSource, Want: "memory"}
	case cfg.SnapshotInterval != 0:
		err = &GuardError{Key: "snapshot.interval", Value: cfg.SnapshotInterval.String(), Want: "0"}
	default:
		return nil
	}
	log.Error("sizing build: refusing to boot", "key", err.Key, "value", err.Value, "required", err.Want)
	return err
}

// Seeder places the fixture on the loop's goroutine. Its BeforeTick is the
// tick loop's (tickloop.Options.BeforeTick); Seed asks for the seed and waits
// for it, so the boot can hold the Gateway until the World is loaded.
type Seeder struct {
	engine     func() *sim.Engine
	contentDir string
	log        *slog.Logger
	// Observe, if set, is called on the loop's goroutine before every tick
	// once the World is seeded: how a test reads the seeded World's State
	// Hash between ticks.
	Observe func(*sim.Engine)

	requested atomic.Bool
	once      sync.Once
	done      chan struct{}
	seeded    simtest.Seeded
	err       error
}

// NewSeeder seeds the Engine engine returns, reporting a missing Template
// against contentDir.
func NewSeeder(engine func() *sim.Engine, contentDir string, log *slog.Logger) *Seeder {
	return &Seeder{engine: engine, contentDir: contentDir, log: log, done: make(chan struct{})}
}

// BeforeTick seeds the World before the first tick after Seed asks for it,
// once.
func (s *Seeder) BeforeTick(context.Context, time.Time) {
	if !s.requested.Load() {
		return
	}
	s.once.Do(func() {
		s.seeded, s.err = simtest.SeedSizing(s.engine())
		close(s.done)
	})
	if s.err == nil && s.Observe != nil {
		s.Observe(s.engine())
	}
}

// ErrLoopStopped is Seed's error when the loop stopped before it seeded.
var ErrLoopStopped = errors.New("sizing build: the tick loop stopped before the World was seeded")

// Seed asks for the seed, waits for the loop to place it, and logs the
// result: one warn naming what was seeded (AC-4), or an error naming the
// missing Template and the content dir (AC-2). It returns early, with
// ctx's error or ErrLoopStopped, if ctx ends or stopped closes first.
func (s *Seeder) Seed(ctx context.Context, stopped <-chan struct{}) error {
	s.requested.Store(true)
	select {
	case <-s.done:
	case <-ctx.Done():
		return ctx.Err()
	case <-stopped:
		return ErrLoopStopped
	}
	var missing *simtest.MissingTemplateError
	switch {
	case errors.As(s.err, &missing):
		s.log.Error("sizing build: the content has no Template the fixture needs", "template", string(missing.Ref), "content_dir", s.contentDir)
		return s.err
	case s.err != nil:
		s.log.Error("sizing build: seed", "detail", s.err.Error(), "content_dir", s.contentDir)
		return s.err
	}
	s.log.Warn("sizing build: seeded World, not for serving players", "entities", s.seeded.Entities, "zones", s.seeded.Zones, "rooms", s.seeded.Rooms)
	return nil
}
