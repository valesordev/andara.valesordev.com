# SLO — Kafka availability (the read side)

> **Status: proposed target, 2026-10-24** (`AW-INF-039`). The target is Brian's to set. The number below is
> SRE's proposal, reasoned from the tick loop and not yet checked against a running system. The 28-day
> validation is a dated review PM schedules once the alert is live (`AW-INF-039`, out of scope).

ADR-0002 makes the Command log the ordering authority, so Kafka bounds the World in both directions.
`world-write-availability.md` covers the write side: whether a Command is *accepted*. This document covers
the read side: whether an accepted Command is *applied in time*. A broker that acknowledges writes but serves
fetches slowly, a Partition whose leader moved to an overloaded broker, or a consumer that fell behind all
look the same to a player. The Command was accepted, and nothing happens for seconds.

## The signal

`andara_consumer_lag{partition}` is the broker's end offset minus the tick loop's next-to-read offset, per
Partition. It's a gauge with 64 series per environment, bounded by `deploy/kafka/topics.yaml`'s Partition
count. It is measured in **offsets, not time**. A lag of 400 offsets means 400 accepted Commands on that
Partition that the World hasn't applied yet.

This is not `SimulationLagging` (`tick-health.md`). That alert measures the loop falling behind its own
schedule. This one measures the input falling behind the log while the loop may tick on time. The two
have different causes, and the runbook tells them apart first.

## Derived: the threshold, in offsets

**Threshold: 400 offsets.** This is the one place it is defined. `files/alerts.yaml` and the runbook
cite it.

```
lag budget        sim.tick_budget_ms × 50  =  50 ms × 50  =  2.5 s     (a player notices an action this late)
ticks in it       2.5 s / 100 ms tick interval              =  25 ticks
per-tick share    sim.max_per_tick / Partitions  =  1024 / 64  =  16 Commands per tick per Partition
threshold         25 ticks × 16                               =  400 offsets
```

At its fair share of the per-tick application cap, a Partition 400 offsets behind takes 2.5 s to drain.
Below that, a burst drains before anyone notices. Above it for 2 minutes, the lag isn't a burst.

**Change it together:** `sim.tick_budget_ms` (default 50), `sim.max_per_tick` (default 1024) and the
Partition count (64) are the inputs. A change to any of them recomputes the threshold here first, then the
rule.

## SLI

**Definition:** the fraction of Partition-time each environment's simulation is within the threshold of the
log.

```promql
1 - sum by (namespace) (
      sum_over_time((max by (namespace, partition) (
        max_over_time(andara_consumer_lag{cluster!="ci"}[1m])
      ) > bool 400)[28d:1m])
    )
  / sum by (namespace) (
      count_over_time(max by (namespace, partition) (
        max_over_time(andara_consumer_lag{cluster!="ci"}[1m])
      )[28d:1m])
    )
```

- **Lagging Partition-minutes over observed Partition-minutes** (the shape of ruling 2 in
  `world-write-availability.md`): a lagging Partition-minute weighs 1/64 of a whole-log minute, so
  one Partition behind for the whole window takes 1/64 (1.6%) off the SLI, and the whole log takes all of it. The
  budget is a fraction of *observed* minutes, so it shrinks with them.
- **One series per Partition** (`max by (namespace, partition)`, before the subquery). A pod replacement
  gives a Partition a new `instance` series. Collapsed, the old and new pods' minutes are weighted by how
  long each was observed. Averaged separately, a two-minute lagging pod would weigh as much as its 28-day
  healthy replacement.
- **A minute with no series is left out**, unlike the write SLI's fixed denominator: while the process is
  down or recovering nothing consumes, and that time is charged to write availability.
  `scripts/tests/test_slo_report.py` evaluates this expression with `promtool`.
- **`max_over_time(…[1m])` inside** means a Partition that crosses the threshold between 15 s scrapes still
  counts for that minute. Sampling can't read a lag spike as health.
- **`cluster!="ci"`**: CI ships to `solo7dev` as `namespace="andara-local"` on cluster `ci` (`ADR-0014`
  A3). Its short runs aren't `local`'s availability. Compose series carry no `cluster` label and still match.
- **Per `namespace`** (ruling 4): `local` and `dev` share brokers but not budgets. Once `AW-INF-047`'s edit
  lands, the grouping is `by (environment, namespace)`.

| | |
|---|---|
| **Target** | **99.9%** (proposed, Brian's to set) |
| **Window** | rolling 28 days |
| **Error budget** | **40.3 Partition-minutes per Partition per 28 days**: 40 minutes of the whole log behind, or one Partition behind for 43 hours |

Why 99.9% and not tighter: under normal play the lag is near zero, and the budget exists for broker
maintenance and leader elections. A full-World restart spends little of it. The gauge is set only by the live
tick loop (`server/tickloop/loop.go`), so a process that is down or still recovering has no series, and the
ingress is down with it, so no backlog builds behind the restart. The restart is charged to write
availability, where `world-write-availability.md`'s deploy tension belongs. **[verify at the 28-day review]**
that the first live ticks after a restart read under the threshold.

## Alert

**`SimulationConsumerLagging`**: `max by (namespace) (andara_consumer_lag) > 400` for 2 m. Severity
`page`, `slo: kafka-availability`. It is never per Partition: the `max` pages the environment once.

Runbook: `docs/runbooks/simulation-consumer-lagging.md`.

## What is not alerted

- **Under-replicated or at-min-ISR Partitions.** These are causes, and they are diagnostic steps in both
  runbooks. They are also unusable as a symptom on the shared broker: every `local.` Partition is RF 1 with
  `min.insync.replicas` 1, so `kafka_server_replicamanager_atminisrpartitioncount` reads 73–75 per broker at
  rest (read 2026-10-24, AW-INF-051's §8 record), and per-topic broker series aren't shipped (`ADR-0014` §6).
  `underminisrpartitioncount` (0 at rest) is the one that means something in a runbook.
- **The broker itself.** `andara-shared`'s series carry `environment="unknown"`. A shared-service alert
  needs its own SLO and an explicit route first (`ADR-0014`).

## Measurement

`make slo-report` renders the 28-day SLI into this table through `gcx` (AW-INF-075), reading
`solo7dev`.

_Includes drill minutes; drill exclusion not yet applied (AW-INF-081)._

| Measured (UTC) | Environment | SLI (28 d) | Budget left | Drill windows named |
|---|---|---|---|---|

## Error budget exhaustion policy

1. Deploys pause except for fixes to whatever exhausted the budget.
2. The broker contract is re-verified first (`make broker-assert ENV=<env>` once `AW-INF-038` lands; until then `make shared-check` and Strimzi's `Ready` on `kafka/andara-log`): a fetch-side
   regression after a broker setting drifted is the cheapest cause to rule out.
3. If the lag is load, not broker, meaning the loop applies at `sim.max_per_tick` every tick and still falls
   behind, that is ADR-0001's sharding signal, with this measurement as the argument.
4. A budget exhausted only by drill minutes is recorded in the table with the windows named and doesn't
   trigger 1–3 (Brian's decision, `AW-INF-039` ruling 5). Until AW-INF-081 lands, the windows come from the
   drill's own `drill: start/end` terminal output, and the table says so.
