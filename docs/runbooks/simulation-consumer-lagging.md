# SimulationConsumerLagging

**Alert:** `max by (namespace) (andara_consumer_lag) > 400` for 2 m (`files/alerts.yaml`).
**Severity:** page. **SLO:** `docs/specs/slo/kafka-availability.md`, which is also where the 400 comes from.
**Ships with:** `AW-INF-039`.

## What fired, and what the player is experiencing

On at least one Partition, the simulation has been more than 400 offsets behind the Command log for two
minutes. At a Partition's fair share of the per-tick cap, that is more than 2.5 s of accepted Commands not yet
applied. Players in that Partition's Zones act, are acknowledged, and then see nothing happen for seconds.
Nothing is lost: every Command is durable on the log and will be applied in order. It's late, not gone.

This is **not** the World being read-only (`world-read-only.md`): Submits are accepted. And it's not
necessarily the loop being slow (`simulation-lagging.md`). The tick can run on schedule while its input
arrives late.

## First: is the loop slow, or is the input late?

The two symptoms have different causes, so tell them apart before anything else. For the paged
`namespace`:

```
max by (namespace) (andara_simulation_lag_seconds{namespace="andara-<env>"})
histogram_quantile(0.99, sum by (namespace, le) (rate(andara_tick_duration_seconds_bucket{namespace="andara-<env>"}[5m])))
```

Or the `andara-tick-health` dashboard: *Simulation lag*, *Tick duration — p50 / p99*, and *Per-partition
consumer lag*.

| Simulation lag | Tick p99 | It is | Go to |
|---|---|---|---|
| over 0.5 s, and `SimulationLagging` is firing too | near or over 50 ms | **The loop is slow.** It can't apply the input as fast as it arrives, so consumer lag is a consequence. | `simulation-lagging.md`. Its mitigations fix this alert too. |
| under 0.5 s | well under 50 ms | **The input is late or capped.** The loop ticks on time but doesn't receive, or isn't allowed to apply, enough per tick. | *Diagnose* below. |

## How to confirm

```
curl -s http://<server>:8080/metrics | grep -E '^andara_(consumer_lag|tick_deferred_records|tick_input_starved_total|tick_applied_records_total)'
```

Note which Partitions are behind: `andara_consumer_lag > 400` by `partition`. One or two Partitions behind is
a broker or a hot Zone. All 64 behind is the fetch path or the per-tick cap.

## Diagnose, in this order

| Step | Look at | Means |
|---|---|---|
| 1 | `andara_tick_deferred_records` above 0 and steady | The World receives more than `sim.max_per_tick` (1024) per tick and defers the rest, round-robin across Partitions. The lag is a backlog the cap is draining. Check the commands rate: `sum(rate(andara_ingress_produced_total{namespace="andara-<env>"}[5m]))` against `sim.max_per_tick × 10`. |
| 2 | One or two Partitions behind, the rest at 0 | A hot Zone (its players produce more than a fair share), or that Partition's leader sits on a struggling broker. `kafka-topics.sh --describe --topic <env>.andara.commands.v1` from a broker shows the leader per Partition. Compare the lagging Partitions' leaders. |
| 3 | `andara_tick_input_starved_total` rising | Fetches return nothing although the log has records: the broker serves fetches slowly or not at all. Broker health next (step 4). |
| 4 | The brokers: `make shared-check`, Strimzi's `Ready` on `kafka/andara-log`, and the broker series in `solo7dev` (`namespace="andara-shared"`, `environment="unknown"`): `kafka_server_replicamanager_underminisrpartitioncount`, `kafka_server_replicamanager_isrshrinkspersec_total`, `kafka_controller_kafkacontroller_activecontrollercount` (exactly one broker reports 1) | A broker restarting, an ISR shrinking, or a controller election slows the fetch path for the Partitions it leads. **Don't read `atminisrpartitioncount`**: every `local.` Partition is RF 1, so it reads ~74 per broker at rest (`kafka-availability.md`). |
| 5 | `andara_checkpoint_age_ticks` climbing with the lag | The loop's offset checkpoint isn't advancing either. That points back at the loop, not the broker: re-read *First*. |

## How to mitigate

| Cause | Action |
|---|---|
| Backlog over the per-tick cap (step 1) | If tick p99 has headroom under the 50 ms budget, raise `sim.max_per_tick`, then **recompute the threshold** in `kafka-availability.md`, which is derived from it. Otherwise this is the sharding conversation (ADR-0001) in `simulation-lagging.md`. |
| A hot Zone (step 2) | Nothing safe live. The backlog drains at the cap once the burst ends. A Zone that is hot all the time is a content or sharding question, raised as an issue. |
| A broker (steps 3–4) | Restore the broker: `world-read-only.md`'s broker rows. The loop catches up on its own at up to `sim.max_per_tick` per tick. Don't restart the server: it would replay recovery and come back to the same broker. |
| Loop stalled (step 5) | `simulation-lagging.md`'s *Loop stalled* row. |

## When to escalate

If the lag isn't falling 10 minutes after the broker is healthy and the per-tick cap isn't saturated, open
an issue with the per-Partition lag, `andara_tick_deferred_records`, and the leader layout from step 2. If the
budget in `kafka-availability.md` is exhausted, its exhaustion policy applies.
