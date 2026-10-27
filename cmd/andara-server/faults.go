// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

//go:build !fault

package main

import (
	"log/slog"

	"github.com/valesordev/andara/server/boot"
	"github.com/valesordev/andara/server/config"
)

// buildTags is andara_build_info's build_tags: "" for the production binary.
const buildTags = ""

// installFaults does nothing in the production binary: no fault package is
// compiled in, and no fault variable is read (AW-SRV-066 AC-8).
func installFaults(*boot.Runtime, config.EnvLookup, *slog.Logger) error { return nil }
