---
id: EPIC-04
title: Snapshots and recovery
phase: 1
component: server
milestone: M2
status: ready
adr_gates: []
adr_refs: [ADR-0001, ADR-0002, ADR-0007]
---

## Goal
`kill -9` the server; the World returns within RTO with a matching State Hash and every Character
where it was.

## Why now
Follows `EPIC-02` because snapshots checkpoint state that must exist first.

Worth stating precisely: **recovery is correct from M1**, because replaying the Command Log from its
beginning reconstructs the World exactly. This epic is an *RTO optimization* — it bounds how much log
has to be replayed. Building snapshots as an optimization over a working recovery path, rather than as
the recovery mechanism itself, means the correctness question is settled before the performance one is
opened.

## In scope
- Zone-scoped Snapshots keyed to the Partition offsets that produced them, written from a consistent
  cut without stalling the tick beyond a stated budget.
- Snapshot storage in object storage; manifests recorded on the Event Topic so recovery finds them by
  reading the log it already reads.
- `state_version` and forward migration, from the first snapshot written.
- Recovery: load newest snapshot, resume consuming from its offsets, assert State Hash.
- `andara-server recover --verify` — recover and compare without accepting connections.
- Re-apply idempotency: Commands between the checkpoint and a crash are deterministically re-applied.
- A CI integration test that actually kills and recovers a server.

## Out of scope
- Snapshot storage backend selection beyond "object storage" — an implementation detail behind the
  interface.
- Cross-partition consistent cuts. Snapshots are per-Zone and therefore per-Partition by construction.

## Done when
The kill-and-recover test runs in CI on every sim-core change, and measured recovery time is published
against the RTO SLO.

## Stories
`AW-SRV-006`, `AW-SRV-007`
