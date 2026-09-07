---
id: ADR-0008
title: Tick rate and the player latency budget
status: proposed
date: 2026-09-07
deciders: [brian]
gates: []
---

## Context

The Tick Rate has been the largest `[NEEDS BRIAN]` in the repo since the first server story. It sets the
tick budget, the tick-health SLO target, the floor on player-perceived command latency, the cost of
ADR-0001's one-tick cross-Zone delay, the reaction time of ADR-0005's Behavior Agents, and what the
Phase 2 client has to work with. Brian has asked for a recommendation.

The relevant fact is that this is now a *latency* decision rather than a *throughput* one. ADR-0002 took
the durability write off the tick's critical path — the tick consumes records that are already durable —
so the tick interval is not competing with a disk or a broker for budget. What it is competing with is a
human's sense that the world responded.

### What a player actually waits for

```
keystroke ──▶ gRPC ──▶ parse+authorize ──▶ Kafka produce (acks=all) ──▶ ack
                                                   │
                                                   ▼
                              wait for next tick boundary  (0 … one interval)
                                                   │
                                                   ▼
                                     validate + apply + emit ──▶ stream ──▶ screen
```

Fixed costs, at MUD scale on a healthy cluster: gRPC and parse are sub-millisecond; the produce ack is
5–20 ms; apply is sub-millisecond; fan-out and stream write are low single-digit milliseconds. Call the
fixed portion **~15 ms typical, ~30 ms at p99**.

The variable cost is the wait for the next tick boundary, which averages half an interval and worst-cases
at a full one. So perceived latency ≈ `15 ms + (0 … interval)`.

| Tick rate | Interval | Typical | Worst case | Cross-Zone (one extra tick + produce) |
|-----------|---------:|--------:|-----------:|--------------------------------------:|
| 1 Hz | 1000 ms | 515 ms | 1015 ms | ~2.0 s |
| 4 Hz | 250 ms | 140 ms | 265 ms | ~530 ms |
| **10 Hz** | **100 ms** | **65 ms** | **115 ms** | **~230 ms** |
| 20 Hz | 50 ms | 40 ms | 65 ms | ~130 ms |
| 60 Hz | 17 ms | 24 ms | 32 ms | ~50 ms |

The threshold that matters: below roughly 100 ms a response reads as immediate; by 200 ms it reads as
responsive; past about 300 ms it reads as sluggish, and a player starts waiting for the world rather than
acting on it.

## Options considered

### Option A — 1 Hz, the classic MUD pulse

**Good:** Traditional, trivially within budget, minimal `TickCompleted` volume (86k records/day), cheap
replay.
**Bad:** Half-second typical latency, one-second worst case, two-second cross-Zone traversal. Every
command feels like it is being considered. It also effectively forecloses smooth Phase 2 movement without
client-side prediction — and prediction is precisely the thing a server-authoritative design exists to
avoid needing.
**Costs us:** The Phase 2 client would have to lie to the player about where they are, which is where
authority leaks back into the client.

### Option B — 10 Hz

**Good:** 65 ms typical latency reads as instant. Cross-Zone traversal at ~230 ms is noticeable only if
you are looking for it. Ten authoritative updates per second is ample for a rendered client to interpolate
between — this is the range most MMOs run their server tick at, for the same reason. Comfortably within a
single process's budget at MUD scale, with room to spare.
**Bad:** 864k `TickCompleted` records/day on `andara.events.v1`. Real, but small next to Event volume from
an active world, and it compacts out of `andara.state.v1` entirely.
**Costs us:** Ten times the tick bookkeeping of Option A for a game whose lineage did fine at 1 Hz.

### Option C — 20 Hz or faster

**Good:** 40 ms typical. Required if combat is ever action-oriented rather than round-based.
**Bad:** Halves the tick budget to 50 ms for the whole World in one process, which brings ADR-0001's
sharding trigger much closer. At 60 Hz the Kafka produce ack (5–20 ms) becomes a visible fraction of the
interval, which is exactly the regime ADR-0002 says its trade-offs stop working in.
**Costs us:** Sharding sooner, for latency nobody asked for in a text-first world.

## Decision

**10 Hz. A 100 ms tick interval, with the overrun threshold — the tick budget — set at 50 ms.**

Two numbers, deliberately different:

| Name | Value | Meaning |
|------|------:|---------|
| `sim.tick_rate` | 10 Hz | ticks per second; the interval is 100 ms |
| `sim.tick_budget_ms` | **50 ms** | the overrun threshold and the tick-health SLI boundary |

Setting the budget at half the interval buys a warning band. A tick taking 60 ms is not yet causing lag —
the World is keeping up — but it is running at 60% of capacity, and that shows up as overruns and as SLO
burn long before players feel anything. A budget equal to the interval would give zero warning: the first
symptom would be accruing lag.

### Combat rounds are decoupled from the tick

Stated here because it is the mistake the MUD lineage invites: classic MUDs conflate the engine pulse with
the combat round, and the two then cannot be tuned independently. **A combat round is a number of Ticks, a
design-level constant.** At 10 Hz, a 2-second round is 20 ticks. Brian can retune combat pacing without
touching the engine, and the engine can change rate without rebalancing combat.

The same applies to every other periodic mechanic — regeneration, spawns, weather, hunger. None of them
should be "every tick."

## Consequences

- The tick-health SLO becomes concrete: *99.9% of ticks complete within 50 ms, over a rolling 28 days.*
  See `docs/specs/slo/tick-health.md`.
- ADR-0001's sharding trigger becomes measurable in absolute terms: revisit when p50 tick duration exceeds
  25 ms, which is half the budget and a quarter of the interval.
- Behavior Agents (ADR-0005) react in 100 ms plus their own think time. For an NPC, that is a feature —
  instant reaction reads as robotic.
- Reactive effects being one tick late costs 100 ms, which is why not building the in-tick Reflex Rule
  layer is now clearly the right call rather than merely the cheaper one.
- Phase 2 gets 10 authoritative updates/second, enough to interpolate movement without the client holding
  any authority.
- `andara.events.v1` carries ~864k `TickCompleted` records/day at idle. Retention sizing must account for
  it, and a future optimization is to omit the record for ticks that applied nothing — deliberately not
  done now, because a gap in the tick record sequence is exactly the kind of special case that breaks
  replay.

## Revisit when

- p50 tick duration exceeds 25 ms at expected peak concurrency (also the ADR-0001 sharding signal).
- Combat design turns action-oriented, which would argue for 20 Hz and for sharding sooner.
- Measured produce-ack p99 exceeds 25 ms, at which point the fixed cost is competing with the interval and
  ADR-0002 wants revisiting before this ADR does.
