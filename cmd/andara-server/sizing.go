// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

//go:build sizing

package main

import (
	"context"
	"log/slog"

	"github.com/valesordev/andara/server/boot"
	"github.com/valesordev/andara/server/config"
	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/sizing"
)

// buildTags is andara_build_info's build_tags: a sizing build is identifiable
// from the scrape, and measure_tick.sh records only from one (AW-SRV-065).
const buildTags = "sizing"

// installFaults does nothing in a sizing build: fault and sizing are
// mutually exclusive (AW-SRV-065 AC-7).
func installFaults(*boot.Runtime, config.EnvLookup, *slog.Logger) error { return nil }

// guardBuild refuses to boot anywhere but local, on the memory source, with
// snapshots off (AW-SRV-065 AC-6).
func guardBuild(cfg *config.Config, log *slog.Logger) error { return sizing.Guard(cfg, log) }

// observeSeeded is the Seeder's Observe: a test reads the seeded World
// through it.
var observeSeeded func(*sim.Engine)

// installSeed hands the tick loop the seeder's BeforeTick, and returns what
// asks for the seed once content is in effect and waits for it.
func installSeed(rt *boot.Runtime, log *slog.Logger) func(context.Context, <-chan struct{}) error {
	s := sizing.NewSeeder(func() *sim.Engine { return rt.Engine }, rt.Cfg.ContentPath, log)
	s.Observe = observeSeeded
	rt.BeforeTick = s.BeforeTick
	return s.Seed
}
