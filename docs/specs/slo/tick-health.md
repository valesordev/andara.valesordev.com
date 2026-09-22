# SLO — Tick health

> **Status: targets confirmed against first measurement, 2026-09-17** (`AW-SRV-002`). At the
> idle floor — `testdata/content/valid`, no Commands, memory source — p99 tick duration is
> under 1 ms, a burst of 20,000 rejected Commands at 1,024 per tick stays under 1 ms per tick,
> and lag holds under 1 ms; through a ten-second broker outage the loop kept 10 Hz applying
> nothing. Both targets hold with two orders of magnitude of margin at the floor. They are to be
> **re-validated on the sizing fixture** (2,000 Rooms / 25,000 Entities / 500 Characters,
> `AW-SRV-006`) once verb handlers exist (`AW-SRV-003`), which is where the budget will first
> be spent. The Entity count was 10,000 when this was written and is 25,000 as of 2026-09-22
> (Brian); `server/simtest/sizing.go` is the one place it lives. The alert rule and runbook ship with `AW-SRV-002`.

Tick duration, tick overrun, and simulation lag are first-class SLIs from the first server story onward,
not retrofitted (CLAUDE.md §7). This document is what the alert hangs from.

## The three signals, and which one alerts

| Signal | What it is | Alerts? |
|--------|------------|---------|
| Tick duration | how long a tick took to process | no — a cause |
| Tick overrun | a tick exceeded the 50 ms budget | no — a cause |
| **Simulation lag** | how far behind schedule the loop is | **yes — the symptom** |

Players do not experience overruns. They experience the world responding late, which is lag. Alerting on
overruns would be alerting on a cause, which CLAUDE.md §7 rejects — but overruns are the leading
indicator, which is why the budget is set at half the interval (ADR-0008) so they appear well before lag
does.

## SLI 1 — Tick budget adherence

**Definition:** the fraction of ticks completing within the tick budget.

```promql
sum(rate(andara_tick_duration_seconds_bucket{le="0.05"}[5m]))
/
sum(rate(andara_tick_duration_seconds_count[5m]))
```

The `le="0.05"` bucket boundary is the 50 ms tick budget from ADR-0008, and the histogram buckets are
chosen so that the budget falls exactly on a boundary rather than being interpolated.

| | |
|---|---|
| **Target** | 99.9% |
| **Window** | rolling 28 days |
| **Error budget** | 0.1% of ticks = ~2,419 ticks/day at 10 Hz |

At 10 Hz there are 864,000 ticks per day, so the budget is roughly 2,400 overrunning ticks daily. That is
deliberately generous for an occasional GC pause or a content reload, and tight enough that a systemic
regression burns it in hours.

## SLI 2 — Simulation lag

**Definition:** the fraction of time simulation lag stays under 500 ms — five tick intervals.

```promql
avg_over_time((andara_simulation_lag_seconds < bool 0.5)[5m:])
```

500 ms is the threshold at which the world starts feeling late rather than merely imprecise: a command
that should have resolved in 65 ms typical (ADR-0008) resolving in 565 ms crosses from "instant" to
"sluggish."

| | |
|---|---|
| **Target** | 99.9% |
| **Window** | rolling 28 days |
| **Error budget** | ~40 minutes per 28 days |

## Alert

**`SimulationLagging`** — `andara_simulation_lag_seconds > 0.5` sustained for 2 minutes.

Runbook: `docs/runbooks/simulation-lagging.md`, which ships in `AW-SRV-002`. Diagnostic order, from the
metrics this SLO's stories emit:

1. `andara_zone_tick_duration_seconds{zone}` — is one Zone dominating? (ADR-0001's sharding signal)
2. `andara_consumer_lag{partition}` — is the loop behind on input rather than slow to process?
3. `andara_tick_deferred_records` — is the World simply receiving more than `max_per_tick` allows?
4. `topk(5, rate(andara_ingress_produced_total{partition}[5m]))` — is one Zone hot?

No alert fires on any of these four. They are the runbook's diagnostic path, not independent alerts.

## Error budget exhaustion policy

When either budget is exhausted within its window:

1. **Feature work on the simulation core stops.** The next change to `server/sim` is a tick-performance
   change.
2. The per-Zone tick duration breakdown determines whether the answer is optimization or ADR-0001's
   sharding activation. The distinction matters: sharding is a real operational step, not a performance
   fix to reach for first.
3. If sharding is the answer, that is not an emergency — it is a replica count increase and a
   consumer-group rebalance, which is the property ADR-0001 was chosen for.

## The sharding trigger

Separate from the alert, and measured continuously rather than on burn:

> Revisit ADR-0001 when **p50 tick duration exceeds 25 ms** — half the budget, a quarter of the interval —
> at expected peak concurrency.

p50 rather than p99, because the question is whether the World has structurally outgrown one process, not
whether it occasionally hiccups.
