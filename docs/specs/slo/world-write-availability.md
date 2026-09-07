# SLO — World write availability

> **Status: proposed targets, 2026-09-07.** `AW-INF-005` validates against first measurement.

ADR-0002 introduced a failure mode the earlier design did not have: **Kafka availability bounds World
availability.** If `andara.commands.v1` is unreachable, no Command can be accepted, and the World goes
read-only — Sessions stay connected, Events keep flowing, and submits are refused with a typed error
(`AW-SRV-010`).

This SLO is about the one thing players cannot do in that state: act.

## SLI

**Definition:** the fraction of time the World accepts Commands.

```promql
avg_over_time((andara_ingress_degraded == bool 0)[5m:])
```

| | |
|---|---|
| **Target** | 99.9% |
| **Window** | rolling 28 days |
| **Error budget** | **40.3 minutes per 28 days** |

## The deploy tension — read this before setting a deploy cadence

Until ADR-0001's sharding is activated, a deploy is a full-World restart (`AW-INF-007`). Every deploy
therefore spends from this budget:

```
budget           40.3 min / 28 days
deploy cost      60 s  (the Phase 1 RTO target)
                 ───────────────────────────────
deploys          ~40 per 28 days  ≈  1.4 per day
```

**Forty deploys per month, and the error budget is gone before a single unplanned outage.**

That number is worth internalizing, because it reframes something. Sharding has been discussed purely as
a response to load — ADR-0001's revisit triggers are all tick-duration signals. But the first thing that
will actually make sharding urgent is almost certainly **deploy cadence**, not concurrency. A team
shipping twice a day cannot hold a 99.9% write-availability target on a single-process World, regardless
of how fast its ticks are.

Three ways out, in increasing cost:

1. **Accept a lower target during early development.** 99.5% gives 3.4 hours per 28 days, which is ~200
   deploys. This is the right answer before there are players.
2. **Batch deploys.** Fewer, larger releases. Cheap, and unpleasant for exactly the reasons it is always
   unpleasant.
3. **Activate sharding**, which lets partitions drain and recover independently so a rolling deploy is a
   partial rather than total interruption. This is the real fix, and it is a replica count change rather
   than a project.

### Recommendation

| Phase | Target | Budget | Implied deploys |
|-------|-------:|-------:|----------------:|
| Pre-launch (M0–M3) | **99.5%** | 3.4 h | ~200 / 28 days |
| Closed launch (M4 onward) | **99.9%** | 40 min | ~40 / 28 days |

Adopt 99.5% now, and treat the tightening to 99.9% at launch as the forcing function that decides whether
sharding is activated before or after the world opens. Deciding that deliberately at M4 is much better
than discovering it from a burn-rate alert two weeks after launch.

## Alert

**`WorldReadOnly`** — `andara_ingress_degraded == 1` for 30 seconds.

Runbook: `docs/runbooks/world-read-only.md` (`AW-SRV-010`, completed by `AW-INF-005`). Diagnostic order:

1. Broker reachability and ISR state — is this `min.insync.replicas` being unsatisfiable?
2. `andara_ingress_produce_duration_seconds` — degrading, or hard-failed?
3. Whether the simulation is still ticking. It should be: read-only means writes are refused, not that
   the World stopped.

The third check is the one that keeps an operator calm. In read-only mode the World is still alive and
still visible to every connected player; what has failed is the ability to change it.

## Excluded from the budget

**Planned deploys are counted, not excluded.** Excluding them would hide precisely the tension this
document exists to make visible. A player interrupted by a deploy is as interrupted as one hit by a
broker failure.

## Error budget exhaustion policy

1. Deploys pause except for fixes to whatever exhausted the budget.
2. If the burn is deploy-driven rather than incident-driven, ADR-0001's sharding activation is evaluated
   at that point — with the deploy-cadence data as the argument, not tick duration.
3. If the burn is incident-driven, the broker configuration contract in `AW-INF-005` is re-verified
   before anything else, because `acks=all` with `min.insync.replicas=2` should make broker-caused
   read-only rare.
