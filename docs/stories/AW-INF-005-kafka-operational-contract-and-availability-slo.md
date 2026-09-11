---
id: AW-INF-005
title: Kafka operational contract, degradation mode, and availability SLO
epic: EPIC-10
component: infra
type: infra
status: ready
size: M
depends_on: [AW-INF-004, AW-SRV-010]
blocks: []
lane: architecture
risk: high
---

## Context

ADR-0002 names this as the significant new cost of the Kafka decision: **Kafka availability now bounds
World availability.** If the log is unreachable, no Command can be accepted. That did not exist in the
in-process alternative, and it must be a designed, documented, exercised degradation rather than a
discovered one.

`AW-SRV-010` implements the read-only mode and emits `andara_ingress_degraded`. This story owns the
operational contract around it: the SLO the alert hangs from, the runbook, the cluster configuration
that makes the mode rare, and the rehearsal that proves the zero-RPO claim on the cluster we actually
run — which, per the 2026-09-10 decision, is a three-broker Redpanda on one kind box, so the rehearsal
proves broker-failure semantics and cannot prove disk-failure semantics. Said here so nobody reads a
green rehearsal as more than it is.

## User story

As an operator, I want the world's behavior when the log is unavailable to be documented, alerted, and
rehearsed, so that the first time it happens is not the first time we think about it.

## Scope

### In scope
- `docs/specs/kafka/broker-contract.md` and `client-contract.md`: every broker and client setting the
  durability and ordering claims depend on, with the claim each one carries.
- `scripts/broker_assert.py` (`make broker-assert ENV=<env>`): reads live broker and topic configs and
  fails on any deviation; runs in CI against the local stack and as a scheduled job against the cluster.
- A client-config self-test in the server: `andara-server config-assert` prints the producer/consumer
  settings in effect and exits non-zero if they differ from the contract (AC-4).
- `docs/specs/slo/kafka-availability.md`; validation of `world-write-availability.md`'s proposed
  targets against the first 28 days of measurement, recorded in that document.
- Alerts `WorldReadOnly` and `SimulationConsumerLagging`, runbooks `world-read-only.md` (completing
  `AW-SRV-010`'s) and `simulation-consumer-lagging.md`.
- `make kafka-rehearsal ENV=<env>`: the degradation and zero-RPO rehearsal, results written to
  `docs/specs/kafka/rehearsals/<date>.md`.
- Retention decision recorded: `andara.commands.v1` infinite, `andara.events.v1` 30 d; tiered storage
  when `andara_kafka_log_bytes` says so.

### Out of scope
- Topic definitions — `AW-INF-004`. The degradation implementation — `AW-SRV-010`.
- Tiered storage configuration; a follow-up when disk demands it.
- Managed Kafka. Self-hosted Redpanda on the box (2026-09-10); the client half is unchanged if that
  moves.

## Acceptance criteria

1. **Given** the three-broker cluster **when** one broker is killed **then** the World keeps accepting
   Commands with `andara_ingress_degraded = 0`, because `min.insync.replicas=2` still holds; produce
   p99 stays under `ingress.produce_deadline / 4`.
2. **Given** two brokers killed **when** a Command is submitted **then** the World is read-only within
   `ingress.produce_deadline`, `WorldReadOnly` fires within 60 s, and the runbook's first mitigation
   (restart the brokers; `make broker-assert`) restores writes with no restart of the server.
3. **Given** read-only mode **when** a player is connected **then** they keep receiving Events and see
   the typed `UNAVAILABLE` on `Submit` (`AW-SRV-010` AC-6).
4. **Given** a running server **when** `andara-server config-assert` runs **then** it exits `0` only if
   `acks=all`, `enable.idempotence=true`, `max.in.flight.requests.per.connection<=5`,
   `delivery.timeout.ms=ingress.produce_deadline`, and the consumer's `isolation.level=read_committed`
   are in effect; a deliberate deviation in a test exits `1` naming the setting.
5. **Given** `make broker-assert` **when** run against any environment **then** it exits `0` only if
   every topic matches `topics.yaml` and every broker has `unclean.leader.election.enable=false`,
   `default.replication.factor=3` (`local`: 1 with a printed warning), `min.insync.replicas=2`, and
   `auto.create.topics.enable=false`.
6. **Given** a Command acknowledged with `(partition, offset)` **when** brokers are killed and recovered
   **then** that offset is present and `andara_acknowledged_commands_lost_total` is 0 — the rehearsal
   records the acked offsets before the kill and reads them after.
7. **Given** the SLO documents **when** read **then** each states an SLI as a PromQL expression, a
   target, a window, an error budget, and an exhaustion policy.
8. **Given** consumer lag over `sim.tick_budget_ms × 50` for 2 m **when** evaluated **then**
   `SimulationConsumerLagging` fires; its runbook's first step is to compare against tick duration,
   because the two symptoms have different causes.

## Interface contract

### Broker contract (`local` / `dev` / `prod`)

| Setting | Value | Claim it carries |
|---------|-------|------------------|
| brokers | 1 / 3 / 3 | |
| `default.replication.factor` | 1 / 3 / 3 | RPO |
| `min.insync.replicas` | 1 / 2 / 2 | RPO — `acks=all` means nothing at 1 |
| `unclean.leader.election.enable` | `false` | RPO — the one people miss |
| `auto.create.topics.enable` | `false` | a typo must not create a topic |
| `log.retention` per topic | `topics.yaml` | replay window |
| rack awareness | none on the box; required off it | recorded, not enforced, until the cluster moves |

### Client contract

| Role | Setting | Value |
|------|---------|-------|
| producer (ingress, events, state, content, accounts) | `acks` | `all` |
| | `enable.idempotence` | `true` |
| | `max.in.flight.requests.per.connection` | `5` |
| | `delivery.timeout.ms` | `ingress.produce_deadline` |
| | `compression.type` | `zstd` |
| consumer (sim, projectors) | `isolation.level` | `read_committed` |
| | `enable.auto.commit` | `false` — offsets commit at tick/batch boundaries only |
| | `auto.offset.reset` | `none` — a missing offset is `ErrLogGap`, never silently `earliest` |
| all | `client.id` | `andara-<component>-<env>-<pod>` |

### Alerts

| Alert | Expression | For | Severity | Runbook |
|-------|------------|-----|----------|---------|
| `WorldReadOnly` | `max(andara_ingress_degraded) == 1` | 1 m | page | `world-read-only.md` |
| `SimulationConsumerLagging` | `max(andara_consumer_lag) > sim.tick_budget_ms * 50` | 2 m | page | `simulation-consumer-lagging.md` |

### Make targets

`make broker-assert ENV=<env>` · `make kafka-rehearsal ENV=<env>` (kills brokers per the script,
asserts AC-1, 2, 6, writes the report) · `make slo-report` (renders the 28-day SLI values into the SLO
documents' measurement tables).

## Data / state impact

None directly. The RF and `min.insync.replicas` choices are the durability contract behind every claim
this project makes about not losing player actions; on the kind box they are correct settings that
cannot protect against the disk they share (`AW-INF-003`). Retention: infinite on `commands`, 30 d on
`events`, both already in `topics.yaml`; this story makes them a decision rather than a default.

## Observability requirements

- **Metrics:** consumed — `andara_ingress_degraded`, `andara_ingress_produce_duration_seconds`,
  `andara_consumer_lag`, `andara_acknowledged_commands_lost_total`; broker — under-replicated
  partitions, ISR shrink rate, `andara_kafka_log_bytes{topic}` (from broker metadata, bounded label).
- **Logs:** rehearsal script output retained as the report.
- **Alerts:** the two above. No alert on under-replicated partitions — a cause, and a diagnostic step in
  the runbook.

## Test plan

- **CI:** `make broker-assert ENV=local` on every run; `config-assert` as a server unit test with an
  injected deviation.
- **Rehearsal:** `make kafka-rehearsal ENV=dev` on the box, recorded; repeated after any broker upgrade.
- **Manual/operator:**
  ```
  make broker-assert ENV=dev                   # expect: "3 brokers, 9 topics, contract holds"
  make kafka-rehearsal ENV=dev                 # expect: report with RPO=0, read-only entry/exit times
  ```

## Definition of done

CLAUDE.md §8, plus: both SLO documents exist with a first measurement table; both runbooks resolve their
alerts; one rehearsal report is committed.

## Open questions

- `[ASSUMPTION]` Retention as above. Infinite `commands` retention is the value ADR-0002 wanted and
  `topics.yaml` already holds; tiered storage is the follow-up when disk says so.
- **World write-availability target proposed** at 99.5% → 99.9%; the deploy-budget consequence (~40
  deploys / 28 d at 99.9%) stands and `AW-INF-007`'s `make deploy` prints the remaining budget.
- `[ASSUMPTION]` Self-hosted Redpanda on the box. If this moves to managed Kafka, the broker half of the
  contract becomes an assertion about someone else's cluster, which `broker-assert` still runs.
