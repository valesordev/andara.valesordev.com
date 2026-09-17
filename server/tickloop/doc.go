// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

// Package tickloop schedules the simulation (AW-SRV-002). It owns everything
// the core may not: the clock, the consumer that feeds records, the
// publisher that carries Events and Tick Boundary Records out, offset
// checkpoints, and the SLIs. The core is handed a TickInput and returns a
// StepResult; nothing in server/sim knows this package exists.
package tickloop
