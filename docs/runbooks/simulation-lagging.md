# SimulationLagging

**Alert:** `andara_simulation_lag_seconds > 0.5` for 2 m.
**Severity:** page. **SLO:** `docs/specs/slo/tick-health.md`.
**Ships with:** `AW-SRV-002`.

## What fired, and what the player is experiencing

The tick loop has been more than five tick intervals behind its schedule for two minutes. A
command that should resolve in ~65 ms is resolving in 500 ms or more: the World is responding
late, and it has stopped feeling instant. Nothing is lost — every Command is in the log and will
be applied — but everything is arriving late, and the gap is growing if the cause is sustained.

Lag is the distance behind a schedule that never moves: the loop never skips a tick to catch up,
so lag falls only when ticks finish under their interval again.

## How to confirm

```
curl -s http://<server>:8080/metrics | grep -E '^andara_(simulation_lag_seconds|tick_overruns_total|ticks_total|tick_deferred_records|tick_input_starved_total) '
```

Or the dashboard: `andara-tick-health`, panels *Simulation lag*, *Tick duration p50/p99*,
*Overruns*, *Consumer lag by partition*, *Deferred records*. Lag rising with overruns is the loop
being slow; lag rising without overruns is the loop being *stalled* — see the last row below.

## Diagnose, in this order

Each is a cause, and none of them alerts on its own (CLAUDE.md §7). Walk them in order; stop at
the first that explains the lag.

| Step | Look at | Means |
|------|---------|-------|
| 1 | `andara_zone_tick_duration_seconds{zone}` — p99 per Zone | One Zone dominating the tick is ADR-0001's sharding signal: the World has structurally outgrown one process, or one Zone's content is pathological. The `tick overran its budget` warn line names the slowest Zone. |
| 2 | `andara_consumer_lag{partition}` | The loop is behind on *input* rather than slow to *process*: records are arriving faster than `max_per_tick` allows, or the broker is delivering them late. |
| 3 | `andara_tick_deferred_records` | The World is receiving more than `sim.max_per_tick` per tick and deferring the rest. Deferral is round-robin across Partitions, so one busy Zone cannot starve another — but the backlog is the lag. |
| 4 | `andara_tick_input_starved_total` rising | The broker is unreachable. The loop keeps its schedule applying nothing; lag from this is small and stops when the broker returns. If lag is large *and* starvation is rising, something else is also wrong. |
| 5 | `andara_tick_publish_failures_total{kind}` | The producer's buffer is full or the broker refused; the tick does not wait for either, so this is not itself lag — but it says the broker is unhealthy. |
| 6 | `andara_tick_duration_seconds` p99 low, lag rising anyway | The loop goroutine is stalled outside `Step` — a wedged handler, a checkpoint commit past its two-second bound, GC. Take a goroutine dump (`kill -QUIT`) and look for the tick goroutine. |

`andara_ingress_partition_skew{partition}` (AW-SRV-010) joins this table when ingress exists.

## How to mitigate

| Cause | Action |
|-------|--------|
| One Zone dominating | Nothing safe to do live. If it is content, roll the pack back (`AW-SRV-013`); if it is scale, this is the trigger to activate sharding (ADR-0001) — a replica count increase and a rebalance, not an emergency. |
| Input faster than `max_per_tick` | Raise `sim.max_per_tick` if tick duration has headroom under the 50 ms budget; otherwise this is the same conversation as above. |
| Broker unreachable | Restore the broker. The loop recovers on its own; nothing to restart. |
| Loop stalled | Restart the pod. Recovery replays from the recorded boundaries and resumes at the last recorded tick; expect a boot proportional to the log's age until snapshots exist (`AW-SRV-006`). |

## What not to do

- Do not raise `sim.tick_budget_ms` to silence overruns. The budget is half the interval so that
  overruns warn *before* lag accrues; a budget at the interval hides the warning and changes
  nothing about the lag.
- Do not lower `sim.tick_rate` live. Every periodic mechanic is a number of ticks (ADR-0008), so a
  slower tick is a slower game, not a cheaper one.
- Do not restart to clear a Zone fault. A faulted Zone's Partition is frozen on purpose; the
  fault's `panic` line names the record, and a restart replays straight back into it.

## After

If the budget in `docs/specs/slo/tick-health.md` is exhausted, feature work on `server/sim`
stops until the next change is a tick-performance change — that is the policy, not a suggestion.
