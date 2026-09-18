// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

// Package command is the pre-log half of the Command Pipeline (AW-SRV-003):
//
//	Gateway:  parse ──▶ authorize ──▶ [ produce to andara.commands.v1 ] ──▶ ack "accepted"
//	Tick:     [ consume ] ──▶ validate ──▶ apply ──▶ emit events
//
// Parse is stateless and knows only the verb table. Authorize reads Session
// and Account state — the Principal and the Character binding — and never
// World state. Both run before the produce, so the log holds only Commands
// that parsed and were authorized: a record of legitimate intent, not of
// everything anyone typed. Validate and apply need authoritative World state
// and live in server/sim, reachable only through a consumed Record.
//
// The Gateway imports this package; sim never does. The produce itself is
// AW-SRV-010's — here it is a seam, Producer, and the harness that stands in
// for the Gateway (andara-cli sim repl) fills it with a fake log.
package command
