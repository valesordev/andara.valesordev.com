---
id: AW-INF-005
title: Kafka operational contract, degradation mode, and availability SLO
epic: EPIC-10
component: infra
type: infra
status: draft
size: M
depends_on: [AW-INF-004, AW-SRV-010]
blocks: []
lane: architecture
risk: high
---

> `status: draft` — scoped and unblocked. Groomed to `ready` once `AW-SRV-010` has produced real
> produce-latency and degradation behavior to write an SLO against. An SLO written before there is a
> measurement is a guess with a number on it.

## Context

ADR-0002 names this as the significant new cost of the Kafka decision: **Kafka availability now bounds
World availability.** If the log is unreachable, no Command can be accepted. That did not exist in the
in-process alternative, and it must be a designed, documented, exercised degradation rather than a
discovered one.

`AW-SRV-010` implements the read-only mode and emits `andara_ingress_degraded`. This story owns the
operational contract around it: the SLO the alert hangs from, the runbook, the production cluster
configuration that makes the mode rare, and the verification that Redpanda-locally and Kafka-in-
production actually behave the same way under failure.

## User story

As an operator, I want the world's behavior when the log is unavailable to be documented, alerted, and
rehearsed, so that the first time it happens is not the first time we think about it.

## Scope

### In scope
- Production broker configuration contract: replication factor, `min.insync.replicas`, rack awareness,
  and the reasoning tying each to the SLO.
- Client configuration contract for every producer and consumer, and a test that asserts running
  processes match it.
- `docs/specs/slo/kafka-availability.md`. `docs/specs/slo/world-write-availability.md` already exists with
  proposed targets; this story validates them against measurement.
- Verification of the zero-RPO claim: an acknowledged-Command survival test across induced broker failure,
  and an assertion that `unclean.leader.election.enable=false` holds on the live cluster.
- `WorldReadOnly` alert on `andara_ingress_degraded`, and consumer-lag alerting tied to the tick-health
  SLO from `AW-SRV-002`.
- `docs/runbooks/world-read-only.md` completion and a rehearsal procedure.
- **Verification against real Kafka**, not only Redpanda: `AW-INF-002` flags durability semantics as the
  one thing the local stack cannot faithfully reproduce at RF=1.

### Out of scope
- Topic definitions — `AW-INF-004`.
- The degradation implementation — `AW-SRV-010`.
- Tiered storage cost tuning. Needed once retention is decided; not needed to operate.

## Acceptance criteria (known now; completed at grooming)

1. **Given** a production-shaped cluster with RF=3 **when** one broker is killed **then** the World
   continues accepting Commands with no degradation, because `min.insync.replicas=2` still holds.
2. **Given** two brokers killed **when** a Command is submitted **then** the World enters read-only mode
   within the produce deadline, the alert fires, and the runbook's first mitigation resolves it.
3. **Given** the World in read-only mode **when** a player is connected **then** they continue receiving
   Events and see a typed explanation, per `AW-SRV-010` AC-6.
4. **Given** a running process **when** its producer and consumer configuration is inspected **then** it
   matches the declared contract, asserted by a test rather than by review.
5. **Given** the SLO documents **when** they are read **then** each states an SLI with its metric
   expression, a target, a window, an error budget, and a policy when the budget is exhausted.
6. **Given** a Command acknowledged with a partition and offset **when** brokers are killed and recovered
   **then** that offset is still present. `andara_acknowledged_commands_lost_total` stays 0 — this is the
   test that turns the zero-RPO target from a claim into a measurement.

## Interface contract

To be written at grooming, once there are measurements. It will cover: the full broker and client
configuration contracts, the degradation state machine, and the alert expressions.

## Data / state impact

None directly. The RF and `min.insync.replicas` choices are the durability contract behind every claim
this project makes about not losing player actions, so they belong in a spec rather than in a cluster's
current state.

## Observability requirements

- **Metrics:** consumed from `AW-SRV-010` (`andara_ingress_degraded`, produce duration and retries) and
  from the brokers (under-replicated partitions, ISR shrink rate, consumer-group lag).
- **Alerts:** `WorldReadOnly` (symptom: players cannot act) and `SimulationConsumerLagging` (symptom:
  the world is behind), each tied to an SLO and each with a runbook. No alert on under-replicated
  partitions — that is a cause, and it is a diagnostic step inside the runbook.

## Test plan

A rehearsal in a non-production environment: kill brokers to the degradation threshold, verify the alert,
follow the runbook, verify recovery, and record the measured times against the SLO.

## Definition of done

CLAUDE.md §8, plus: both SLO documents exist, both alerts have runbooks that resolve them, the
degradation rehearsal has been performed and its results recorded, and the client-configuration
assertion runs in CI.

## Open questions

- `[NEEDS BRIAN]` Retention on `andara.commands.v1` — carried from `AW-INF-004`. Infinite retention with
  tiered storage makes the World's whole history replayable forever, which is genuinely valuable and not
  free.
- **World write-availability target is proposed**: 99.5% pre-launch, tightening to 99.9% at closed launch
  (`docs/specs/slo/world-write-availability.md`). That document also works out the consequence worth
  arguing about — at a 60 s RTO, a 99.9% budget allows roughly 40 deploys per 28 days, which means
  **deploy cadence, not load, is the first thing likely to make ADR-0001's sharding urgent.**
- Whether managed Kafka or self-hosted. Changes who owns the broker half of this contract, though not
  the client half.
