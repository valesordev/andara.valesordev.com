<!--
SPDX-FileCopyrightText: 2026 Valesor Development
SPDX-License-Identifier: CC-BY-SA-4.0
-->

# SLO — Projection freshness and integrity

> **Status: proposed target, 2026-09-24.** Written with `AW-SRV-019`, which ships the first
> projection and its two alerts, because CLAUDE.md §7 has an SLO precede an alert. The SLI and its
> measurement follow from the story. The target is Claude Code's proposal and is `[NEEDS BRIAN]`:
> nobody has measured the projector under load yet, and a target set before measurement is a
> hypothesis with a number on it.

## What this covers, and what it does not

Projections are the indexes built from the World's history: `andara.state.v1` (`AW-SRV-019`), and
the Redis and Postgres indexes read from it (`AW-SRV-017`, `AW-SRV-018`). The Builder and operator
tooling reads them. **No player reads them.** The in-game read path is the simulation's memory
(ADR-0002 §5.5), and nothing may read a projection to make a game decision. A stale or broken
projection shows an operator an old World; it does not show a player a wrong one.

That is why the alerts this SLO backs are tickets, not pages. It is also why integrity is not a
budget: a projection that describes a World nobody was in is wrong, not late.

## SLI 1 — freshness

`andara_state_projector_lag_seconds`: now minus when the Tick Boundary Record of the last tick the
projector *verified* was produced (the record's broker timestamp). It measures the projector after
verification, so a projector that is producing records but has stopped verifying cannot look fresh.

**Good minute:** lag ≤ `projector.state.lag_budget` (default 5 s, exported as
`andara_state_projector_lag_budget_seconds`).

**Target (proposed):** 99 % of minutes good over 28 days, measured only while the World is ticking.
A World that is not ticking produces no boundaries. Its projector's lag then grows without anything
being stale, and the rule has to tell the two apart (see *Known gaps*).

**Error budget:** 1 % of 28 days ≈ 6.7 hours. **Policy when exhausted:** schema changes to a
projection, and rebuilds that are not repairs, wait until the budget recovers. A rebuild consumes
freshness by construction, and spending budget on a planned one while it is already gone turns a
routine change into an incident.

**Alert:** `ProjectionStale{projection="state"}`, lag over budget for 5 m, severity `ticket`.
Runbook: `docs/runbooks/projection-stale.md`.

## SLI 2 — integrity

`andara_state_digest_mismatches_total`: ticks whose replayed State Hash differed from the one the
live server recorded. **Must remain 0; there is no error budget.** A non-zero value means the
indexes no longer describe the World. If a rebuild reproduces it, the *server* is
non-deterministic, which is a simulation bug with a blast radius far past the projection.

**Alert:** `StateProjectorDiverged`, any non-zero value, severity `ticket`. The projector stops
producing at the tick before the divergence, so the indexes are frozen, not corrupted, and no player
is affected. Runbook: `docs/runbooks/state-projector-diverged.md`.

## Known gaps

- **A projector that is not running exports no lag.** `ProjectionStale` cannot fire for a projector
  that is down, crash-looping, or disabled. For crash-looping, the runbook's first step is the pod's
  restart count and last exit code (`2` divergence, `3` log gap, `4` state_version). An
  absence-based rule has to be careful the way `AndaraServerUnavailable` is (one per environment,
  never `absent()` over every namespace). It is deferred until the chart enables the projector
  anywhere.
- **Lag grows while the World is quiet.** No new boundary means the lag gauge climbs even though
  there is nothing to project. The server ticks continuously today (an idle tick still writes a
  boundary), so this does not happen in practice. If idle ticks ever stop writing boundaries, this
  SLI needs a "no boundary to project" exclusion.

## Revisit when

- The first production measurement of `andara_state_projector_lag_seconds` under load exists: set
  the target from it.
- `AW-SRV-017`/`AW-SRV-018` land: each adds its own `ProjectionStale{projection=…}` against its own
  budget, under this document.
