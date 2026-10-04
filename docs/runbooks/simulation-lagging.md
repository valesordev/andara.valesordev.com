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
| 7 *(ships with `AW-SRV-028`)* | `andara_handoffs_in_transit` above 0 for more than a few minutes, **and** `rate(andara_handoff_retries_total[5m])` above 0 | Cross-Zone moves are waiting for an acknowledgement that isn't coming: the broker, or the target Zone's Partition. This is not itself lag. A handoff whose `Arrive` the target hasn't applied within `sim.handoff_retry_ticks` (1 s at the defaults) is retried, backing off to `sim.handoff_retry_max_ticks` (10 s), so a handoff that is merely in flight doesn't retry. The count above 0 with no retries is ordinary traffic, not a fault. Retries rising point back to steps 4 and 5 (the broker) or to a faulted target Zone: `andara_tick_zone_faults_total` rising, and the `zone_faulted` rejections. The players in transit can't act and are refused `in_transit` until the handoff settles. |

`rate(andara_ingress_produced_total{partition}[5m])` (AW-SRV-010) joins this table when ingress exists.

## How to mitigate

| Cause | Action |
|-------|--------|
| One Zone dominating | Nothing safe to do live. If it is content, roll the pack back (`AW-SRV-013`); if it is scale, this is the trigger to activate sharding (ADR-0001) — a replica count increase and a rebalance, not an emergency. |
| Input faster than `max_per_tick` | Raise `sim.max_per_tick` if tick duration has headroom under the 50 ms budget; otherwise this is the same conversation as above. |
| Broker unreachable | Restore the broker. The loop recovers on its own; nothing to restart. |
| Loop stalled | Restart the pod. Recovery replays from the recorded boundaries and resumes at the last recorded tick; expect a boot proportional to the log's age until snapshots exist (`AW-SRV-006`). |
| An Entity stuck in transit *(ships with `AW-SRV-028`)* | **There is no operator release before `AW-SRV-027`.** An Entity in transit to a faulted Zone stays in `Transit` and keeps retrying, with its player refused `in_transit`, because nothing else may place or restore it. If the broker caused the retries, restoring it settles them. If a faulted Zone did, nothing documented clears the fault: rolling the pack back (`AW-SRV-013`) stops further faults but isn't a way to un-fault a Zone, and whether a restart or a content reload does is `AW-SRV-027`'s to settle. A restart replays straight back into the same Transit, so don't. The only release today is on `dev`: `make world-reset ENV=dev CONFIRM=andara-dev`, which destroys every Character and Account, so confirm with Brian first (a local stack is `make down VOLUMES=1`). Never on `prod`. |

## The handoff marks *(ships with `AW-SRV-028`)*

Each Zone keeps, for good, a mark for every Entity that has ever arrived in it by handoff: the
highest handoff sequence it decided. Marks are what make a retried `Arrive` harmless, and nothing
prunes them. They're hashed Zone state and part of every snapshot body, so they cost the same
things Entities do: snapshot copy time inside the tick (budget `snapshot.max_stall_ms`, 15 ms) and
State Hash work. `andara_handoff_placed_entries` is the count, summed over Zones, and it only goes up
while the World runs. The marks survive a restart (they're in the snapshot body) and nothing prunes
them, so a count that falls or reads 0 after one is wrong, not a sign of pruning. It grows with the
number of distinct Entities that cross Zones: Entity IDs are never reused, and Item Instances
(`AW-SRV-047`) take a new ID each time they're placed.

Two thresholds, chosen from the snapshot sizing fixture (`server/simtest/sizing.go`: 16 Zones and
25,000 Entities) and **neither measured against marks**. Against the 15 ms `snapshot.max_stall_ms`
itself, that fixture's in-tick copy already takes about 12 ms without `-race` (11.7, 12.8 and 13.2 ms
in three runs of `TestSnapshotCopyStaysInsideTheStallBudget` on 2026-10-04, CPU time), and marks are
added to those Entities, so they draw on 2 to 3 ms of headroom. The test fails only far past that: a
plain `go test` at 4 times the budget (60 ms), and CI, which runs `go test -race`, at 14 times (210 ms,
where the copy took about 51 ms in one run the same day). A green run says nothing about marks.

| `andara_handoff_placed_entries` | Means | Do |
|---------------------------------|-------|----|
| 25,000 or more | The start-measuring line, chosen because it's the sizing fixture's Entity count and unmeasured, not because it's safe. A mark (an Entity ID and a sequence) is cheaper than an Entity, but by an unknown ratio, and the headroom is 2 to 3 ms | Plan the pruning story (`AW-SRV-028`'s Out of scope names it, a `HandoffClosed` record), and estimate the runway: `delta(andara_handoff_placed_entries[7d])` against 100,000 |
| 100,000 or more | Four times the sizing fixture's population, where an unmeasured cost could already pass the headroom | Pruning is wanted before the next release. Re-run `TestSnapshotCopyStaysInsideTheStallBudget` with marks added to its fixture to learn the real cost, and watch `andara_snapshot_tick_stall_seconds` (the in-tick copy, the part players feel) against the stall budget, and `andara_snapshot_bytes{zone}` for the size |

These are a dashboard line and a ticket, not an alert: no SLO covers the marks yet, and an alert
needs one (CLAUDE.md §7). Replace both numbers with measured ones once the sizing fixture carries
marks.

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
