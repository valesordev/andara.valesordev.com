// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

//go:build !sizing

package main

import (
	"context"
	"log/slog"

	"github.com/valesordev/andara/server/boot"
	"github.com/valesordev/andara/server/config"
)

// guardBuild refuses nothing outside a sizing build.
func guardBuild(*config.Config, *slog.Logger) error { return nil }

// installSeed seeds nothing outside a sizing build: no seeding package is
// compiled in, and no flag, env var or config key reaches one (AW-SRV-065
// AC-3).
func installSeed(*boot.Runtime, *slog.Logger) func(context.Context, <-chan struct{}) error {
	return func(context.Context, <-chan struct{}) error { return nil }
}
