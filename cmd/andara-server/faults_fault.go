// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

//go:build fault

package main

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/valesordev/andara/server/boot"
	"github.com/valesordev/andara/server/config"
	"github.com/valesordev/andara/server/fault"
)

// buildTags is andara_build_info's build_tags: a fault build is identifiable
// from the scrape (AW-SRV-066).
const buildTags = "fault"

// envWedgeTickAfter wedges the tick loop once that much time has run. It is
// read only here, so it is never a chart key.
const envWedgeTickAfter = "ANDARA_FAULT_WEDGE_TICK_AFTER"

// installFaults wires the fault injectors the environment asks for. A value
// that isn't a positive duration is an error: the boot exits 1 rather than run
// a fault build that silently injects nothing.
func installFaults(rt *boot.Runtime, env config.EnvLookup, log *slog.Logger) error {
	v, ok := env(envWedgeTickAfter)
	if !ok || v == "" {
		return nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return fmt.Errorf("%s=%q: want a positive duration", envWedgeTickAfter, v)
	}
	rt.BeforeTick = fault.WedgeTickAfter(d, log)
	log.Warn("fault build: the tick loop will wedge", "variable", envWedgeTickAfter, "after", d.String())
	return nil
}
