---
id: EPIC-10
title: Data layer — Kafka, schema registry, and projections
phase: 1
component: infra
milestone: M1
status: ready
adr_gates: []
adr_refs: [ADR-0002, ADR-0007]
---

## Goal
The log exists, is operable, and has read indexes built from it: Kafka with its topics and schema
registry, Redis for hot reads, Postgres for tabular.

## Why now
ADR-0002 makes Kafka the ordering authority, which puts it in the critical path of the very first
playable tick. It cannot be a durability concern bolted on at M2.

Building the Tick Loop against an in-memory queue and swapping Kafka in later was considered and
rejected: the ordering, offset, rebalance, and re-apply behavior that the entire architecture rests on
would go untested through M1, and M1 would prove less than it appears to.

## In scope
- Topic definitions as code: `andara.commands.v1` (64 partitions — see below), `andara.events.v1`,
  `andara.state.v1`, `andara.audit.v1`, `andara.accounts.v1`, and the three content topics, each with its
  cleanup policy, replication factor, and retention.
- The **state projector** and the compacted `andara.state.v1` it produces (ADR-0002 §5.5) — the log flows
  into a current-state topic that the indexes read, so a rebuild costs live-state time rather than
  world-age time. Its digest is asserted against `TickCompleted.state_hash`, which turns index sync from
  a belief into a check.
- Schema registry with compatibility enforcement at publish.
- Redpanda in `make up`; a real cluster in `deploy/`.
- Producer and consumer configuration contracts: `acks=all`, `min.insync.replicas=2`,
  `unclean.leader.election.enable=false`, offset commit semantics, consumer group naming. The third of
  those is what the zero-RPO target actually rests on, and it is not a default.
- Read-only degradation mode: when the log is unreachable the tick continues and the Gateway rejects
  new Commands with a typed error. Documented in a runbook before the first deploy.
- Redis index (M2) and Postgres index (M3), each a consumer of the **State Topic**, not of the Event Topic.
- Kafka availability as an SLO, because it now bounds World availability.

## Out of scope
- ClickHouse. Added when there is an analytical query Postgres handles badly — at which point it is a
  new projector against a log that already exists, which is the point of this architecture.
- Multi-process partition assignment. The topology supports sharding; activating it is a later,
  measurement-driven change.
- Tiered storage tuning. Needed eventually for `andara.commands.v1`; not needed to play.

## The partition-count decision

`andara.commands.v1` is created with **64 partitions**, and this is effectively permanent —
repartitioning a keyed topic reorders history, which is not something we will do to a live world. 64
is the maximum future shard count. One process owning all 64 costs almost nothing; discovering the
number was too small costs the World's history.

## Done when
`make up` brings up Redpanda with every topic created and validated, a Command produced by the Gateway
is consumed by the tick, and killing the broker degrades the World to read-only with a typed client
error rather than a crash.

## Stories
`AW-INF-004`, `AW-INF-005`, `AW-SRV-019`, `AW-SRV-017`, `AW-SRV-018`
