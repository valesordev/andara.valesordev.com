---
id: AW-SRV-006
title: Zone snapshots keyed to partition offsets
epic: EPIC-04
component: server
type: feature
status: draft
size: M
depends_on: [AW-SRV-001, AW-SRV-004]
blocks: [AW-SRV-007]
lane: implementation
risk: high
---

> `status: draft` — unblocked and scoped. Groomed to `ready` when M2 approaches, against a tick loop
> that has produced real state sizes and real tick timings.

## Context

ADR-0002 makes Kafka the authority on order and in-memory state the authority on current state.
Snapshots are the checkpoint that lets recovery skip the replay. This story writes them.

The framing matters and is worth repeating from `EPIC-04`: **recovery is already correct without this
story**, because replaying `andara.commands.v1` from its beginning reconstructs the World exactly. This
is an RTO optimization built on top of a working recovery path, which is a much healthier position than
building the recovery mechanism and the performance mechanism as one thing.

## User story

As an operator, I want the World checkpointed regularly, so that recovery takes seconds rather than the
length of the world's entire history.

## Scope

### In scope
- Zone-scoped snapshots — the Zone is the Partition is the unit of authority (ADR-0001).
- Snapshots taken from a consistent cut at a tick boundary, without stalling the tick beyond a stated
  budget.
- Each snapshot records the per-Partition offsets that produced it and the State Hash at that tick.
- Snapshot storage in object storage, keyed `{zone}/{state_version}/{offset}`, with the manifest written
  to `andara.events.v1` so recovery finds snapshots by reading the log it already reads.
- `state_version` on every snapshot from the first one written, and the forward-migration path.
- Snapshot cadence policy: **60 s of wall time per Zone**, recommended in `docs/specs/slo/recovery.md`.
  At 10 Hz that bounds tail replay to ~600 ticks, which makes replay the smallest term in RTO. It is also
  the rebalance stall when ADR-0001's sharding is activated — a second consumer of the same number, and a
  reason not to tune it purely for RTO.

### Out of scope
- Recovery and replay — `AW-SRV-007`.
- Log retention and compaction — `AW-INF-004` and `AW-INF-005`.
- Snapshot storage backend selection beyond "object storage", which is an implementation detail behind
  the interface.

## Acceptance criteria (known now; completed at grooming)

1. **Given** a snapshot in progress **when** the overlapping tick runs **then** tick duration stays
   within the Tick Budget at a realistic World size.
2. **Given** the same state **when** a snapshot is written twice **then** the two artifacts are
   byte-identical, which requires the canonical encoding from ADR-0007 rule 3.
3. **Given** a snapshot **when** it is inspected **then** it names its `state_version`, its tick, its
   per-Partition offsets, and its State Hash.
4. **Given** a snapshot written by an older `state_version` **when** the current binary reads it **then**
   it either migrates it forward or refuses with both versions named. Never a partial or silent read.
5. **Given** a snapshot write that fails **when** the failure occurs **then** the tick is unaffected, the
   previous snapshot remains the newest valid one, and no partially-written artifact is ever selectable
   by recovery.

## Interface contract

To be written at grooming. Committed now: `WorldStore` is an interface defined by `server/sim` and
implemented outside it, per CLAUDE.md §10 — persistence is an adapter behind an interface the sim owns,
not the reverse.

## Data / state impact

This is the story that creates persisted World state, and therefore the story where format versioning
either happens or becomes impossible. Snapshot-format migration is a less well-trodden path than SQL
migration; the forward path must be documented and tested here, not deferred.

Snapshot cadence is the knob that trades storage and write cost against recovery time. It also determines
rebalance pain when sharding is eventually activated (ADR-0001), which is a second consumer of the same
number and a reason not to tune it purely for RTO.

## Observability requirements

- **Metrics:** `andara_snapshot_duration_seconds` (histogram), `andara_snapshot_bytes` (gauge, label
  `zone`), `andara_snapshot_last_tick` and `andara_snapshot_age_seconds` (gauges),
  `andara_snapshot_failures_total` (counter, label `reason`), `andara_snapshot_tick_stall_seconds`
  (histogram — the part that can hurt players).
- **Traces:** `persistence.snapshot` as its own root span, explicitly not inside `sim.tick`.
- **Alerts:** `SnapshotStale` on `andara_snapshot_age_seconds`, tied to the RTO SLO from `AW-SRV-007` —
  a stale snapshot is a recovery-time problem, which is the symptom. Runbook ships with it.

## Test plan

Byte-identity for repeated snapshots; snapshot-under-load asserting no tick overrun; a failed write
asserting the previous snapshot remains selectable; a `state_version` bump asserting migrate-or-refuse.

## Definition of done

CLAUDE.md §8, plus: `state_version` migration is tested across at least one real bump, and
`docs/runbooks/snapshot-stale.md` exists.

## Open questions

- **RPO is decided and it is zero** for acknowledged actions (`docs/specs/slo/recovery.md`). It falls out
  of `acks=all` plus `min.insync.replicas=2` plus `unclean.leader.election.enable=false`, not out of
  anything this story does. Snapshot cadence therefore affects RTO only, which is a far easier number to
  tune and a far less consequential one to get slightly wrong.
- `[NEEDS BRIAN]` Expected World scale — Rooms, Entities, Characters — which determines whether snapshot
  duration is a footnote or the dominant constraint.
- `[ASSUMPTION]` Copy-on-write at the tick boundary rather than a stop-the-world serialize. The
  alternative is simpler and stalls the tick for the snapshot's duration, which is acceptable only if
  that duration stays well inside the budget.
