# SLO — World write availability

> **Status: proposed targets, 2026-09-07; SLI and alert settled 2026-10-24 (`AW-INF-039`).** The targets are
> Brian's to set. The 28-day validation against the measurement table below is a dated review PM schedules
> once `WorldReadOnly` is live.

ADR-0002 introduced a failure mode the earlier design did not have: **Kafka availability bounds World
availability.** If `andara.commands.v1` is unreachable, no Command can be accepted, and the World goes
read-only — Sessions stay connected, Events keep flowing, and submits are refused with a typed error
(`AW-SRV-010`).

This SLO is about the one thing players cannot do in that state: act.

## SLI

**Definition:** the fraction of Partition-time each environment accepts Commands (`AW-INF-039` ruling 2).

```promql
sum by (namespace) (
  sum_over_time((1 - max by (namespace, partition) (
    max_over_time(andara_ingress_degraded{cluster!="ci"}[1m])
  ))[28d:1m])
) / (64 * 40320)
```

Healthy Partition-minutes, divided by every Partition-minute in the window: 64 Partitions × 40,320 minutes.

- **Per Partition-minute, over a fixed denominator.** `andara_ingress_degraded{partition}` is 1 while that Partition refuses
  writes, with 64 series per environment. A read-only Partition-minute weighs 1/64 of a whole-log minute:
  one Partition read-only for the whole window takes 1/64 (1.6%) off the SLI, and the whole log takes all of it. The page (below) is binary on purpose: one degraded
  Partition's players can't act. The page is for responsiveness, and the SLI measures proportion.
- **`max_over_time(…[1m])` inside** (ruling 3). Under a persistent produce fault the gauge saw-tooths: 1 for
  `ingress.degraded_hold`, then 0 for up to `ingress.produce_deadline` until the next Submit rediscovers the
  fault. Taking each minute's max means the gap never reads as health.
- **A minute with no sample counts against the budget.** While the server is down, restarting or
  recovering it has no Ready pod, so nothing is scraped. An average over the samples that exist would read
  that time as available and hide exactly the deploy cost this document is about. The fixed denominator
  charges it. The cost: a namespace younger than 28 days reads low until it has a full window, which is the
  safe direction. `scripts/tests/test_slo_report.py` evaluates this expression with `promtool` over a gap
  and over two series for one Partition.
- **One series per Partition** (`max by (namespace, partition)`). Every server process registers all 64
  Partitions' series, so two replicas, a restart whose new pod overlaps the old one's last scrape, or two
  scrape paths would otherwise count one Partition-minute twice and push the SLI past 1.
- **`local` is stack uptime, not availability.** `local` runs only while someone is working, so against a
  fixed denominator its number is how long the stack was up. `make slo-report` prints it and marks the budget
  `n/a`. The target and the exhaustion policy below hold `dev` (and prod, once it exists), not `local`.
  **[proposed, Brian's to set]**
- **`cluster!="ci"`**: CI ships to `solo7dev` as `namespace="andara-local"` on cluster `ci` (`ADR-0014`
  A3). Its short runs aren't `local`'s availability. Compose series carry no `cluster` label and still match.
- **Per `namespace`** (ruling 4). `local` and `dev` share brokers but not budgets. Once `AW-INF-047`'s edit
  lands, the grouping is `by (environment, namespace)`.

| | Pre-launch (now) | Closed launch (M4 on) |
|---|---|---|
| **Target** | **99.5%** | **99.9%** |
| **Window** | rolling 28 days | rolling 28 days |
| **Error budget** | **3.4 h** of whole-log read-only per 28 days | **40.3 minutes** per 28 days |

The target in force is the pre-launch one, per this document's own recommendation below. Brian sets it,
and the tightening at M4 is the decision point the deploy-tension section describes.

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

**`WorldReadOnly`**: `max by (namespace) (andara_ingress_degraded) == 1` for 30 s, with `keep_firing_for: 2m`
over the sawtooth's gap (ruling 3). Severity `page`, `slo: world-write-availability`. The `for:` and
`ingress.degraded_hold` change together: `make check` fails if the hold's default or minimum is below
`2 × for + 30 s` (`docs/specs/kafka/client-contract.md`; `scripts/degraded_hold.py`).

Runbook: `docs/runbooks/world-read-only.md` (`AW-SRV-010`, completed by `AW-INF-039`). Its first step reads
`sum by (namespace) (andara_ingress_degraded)`: 64 is the log unreachable, a few is a Partition fault. Then:

1. Broker reachability and ISR state — is this `min.insync.replicas` being unsatisfiable?
2. `andara_ingress_produce_duration_seconds` — degrading, or hard-failed?
3. Whether the simulation is still ticking. It should be: read-only means writes are refused, not that
   the World stopped.

The third check is the one that keeps an operator calm. In read-only mode the World is still alive and
still visible to every connected player; what has failed is the ability to change it.

## Measurement

`make slo-report` renders the 28-day SLI into this table through `gcx` (AW-INF-075), reading `solo7dev`.

_Includes drill minutes; drill exclusion not yet applied (AW-INF-081)._

| Measured (UTC) | Environment | SLI (28 d) | Budget left | Drill windows named |
|---|---|---|---|---|

## Excluded from the budget

**Planned deploys are counted, not excluded.** Excluding them would hide precisely the tension this
document exists to make visible. A player interrupted by a deploy is as interrupted as one hit by a
broker failure.

## Error budget exhaustion policy

1. Deploys pause except for fixes to whatever exhausted the budget.
2. If the burn is deploy-driven rather than incident-driven, ADR-0001's sharding activation is evaluated
   at that point — with the deploy-cadence data as the argument, not tick duration.
3. If the burn is incident-driven, the broker contract is re-verified before anything else
   (`make broker-assert ENV=<env>` once `AW-INF-038` lands; until then `make shared-check` and Strimzi's `Ready` on `kafka/andara-log`), because `acks=all` with `min.insync.replicas=2` should
   make broker-caused read-only rare.
4. A budget exhausted only by drill minutes is recorded in the table with the windows named and doesn't
   trigger 1–3 (Brian's decision, `AW-INF-039` ruling 5). Until AW-INF-081 lands, the windows come from the
   drill's own `drill: start/end` terminal output, and the table says so.
