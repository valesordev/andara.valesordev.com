---
id: AW-SRV-019
title: State projector and the compacted current-state topic
epic: EPIC-10
component: server
type: feature
status: draft
size: M
depends_on: [AW-SRV-004]
blocks: [AW-SRV-017]
lane: implementation
risk: high
---

> `status: draft` — unblocked by ADR-0002 §5.5 and scoped. Groomed to `ready` when M2 approaches.

## Context

ADR-0002 §5.5: the ordered Event history flows into a **compacted** `andara.state.v1` holding the current
state of each aggregate, and the indexes read from *that* rather than from the full history. Without it,
building or rebuilding a Redis or Postgres index costs time proportional to the World's entire history,
forever. With it, a rebuild costs time proportional to live state.

This story is the projector that produces it, and it is deliberately first among the read-path stories
because every index downstream depends on it.

The design point that makes it safe: the projector does not re-implement the simulation's fold. It
**embeds `server/sim` in fold-only mode** — the same dependency-free, deterministic package, with no
transport, no scheduler, and no producer — so there is exactly one implementation of how an Event changes
state. Its accumulated hash must then equal the `state_hash` in the corresponding `TickCompleted` record,
which turns "are the indexes in sync with the World" from a matter of belief into an assertion.

## User story

As an operator, I want rebuilding an index to cost minutes rather than the age of the world, so that a
projection schema change is routine.

## Scope

### In scope
- The `andara.state.v1` record format: aggregate state plus
  `{tick, source_offset, source_event_id, content_version_sha256, state_digest}`.
- Typed aggregate keys: `character:<id>`, `npc:<id>`, `item:<id>`, `room:<zone>/<id>`.
- Tombstones for destroyed aggregates, so compaction can actually remove them.
- The projector: consume `andara.events.v1`, fold via the embedded sim core, produce to
  `andara.state.v1`.
- **Digest verification** against `TickCompleted.state_hash`, with divergence as an alert rather than a
  discovery.
- Rebuild from `andara.events.v1` from the earliest available offset.
- Write isolation: exactly one component may produce to `andara.state.v1`.

### Out of scope
- Redis and Postgres indexes — `AW-SRV-017`, `AW-SRV-018`. They consume this topic.
- Recovery. The compacted topic is a read-path bootstrap, **not** a recovery mechanism — it has a ragged
  tick edge and compaction has discarded the versions an earlier consistent cut would need. Snapshots
  (`AW-SRV-006`) remain the recovery path, and ADR-0002 §5.5 says why at length.
- Any authoritative role. Nothing may read `andara.state.v1` to make a game decision.

## Acceptance criteria (known now; completed at grooming)

1. **Given** a `CharacterArrived` Event **when** the projector applies it **then** `character:<id>` and
   `room:<zone>/<id>` records are produced reflecting the new state.
2. **Given** the projector has consumed through tick `T` **when** its accumulated state hash is compared
   to `TickCompleted{tick: T}.state_hash` **then** they are equal. This is the sync assertion the whole
   design exists to make possible.
3. **Given** a divergence at tick `T` **when** it is detected **then** the projector halts, does not
   produce further records, and alerts. A projector that keeps writing after it knows it is wrong is
   worse than one that stops.
4. **Given** an Event applied twice **when** the second application completes **then** the produced record
   is identical. At-least-once delivery must be safe.
5. **Given** a destroyed Entity **when** it is destroyed **then** a tombstone is produced for its key, and
   after compaction the key is absent.
6. **Given** an empty `andara.state.v1` **when** a rebuild runs **then** it consumes `andara.events.v1`
   from the earliest available offset and the result matches a projector that consumed incrementally.
7. **Given** a record in `andara.state.v1` **when** it is inspected **then** `content_version_sha256`
   identifies the content version that defined the aggregate, so a runtime object traces back to its
   authored source.
8. **Given** the projector is stopped **when** the World continues ticking **then** nothing in the game is
   affected. This is a tooling outage.
9. **Given** any component other than the projector **when** it produces to `andara.state.v1` **then** it
   fails on credentials.

## Interface contract

To be written at grooming. Committed now: the projector imports `server/sim` and calls the same fold the
simulation calls. It contains no state-transition logic of its own. A second implementation of how an
Event changes state is the defect this story is shaped to prevent.

## Data / state impact

`andara.state.v1` is compacted and derived. It is disposable — losing it costs a rebuild, not data — and
that should be verified by actually deleting and rebuilding it in a non-production environment rather
than assumed.

Record format carries `state_version` like every other persisted artifact (ADR-0007), because a change to
how an aggregate serializes is a change to what every index reads.

Compaction throughput is the risk worth watching: if state churn outpaces the compactor, a "compacted"
topic becomes a second full history with worse ergonomics. ADR-0002 lists this as a revisit trigger.

## Observability requirements

- **Metrics:** `andara_state_projector_lag_seconds` (gauge), `andara_state_records_produced_total`
  (counter, label `kind`), `andara_state_tombstones_total` (counter),
  `andara_state_digest_mismatches_total` (counter — must stay 0),
  `andara_state_rebuild_duration_seconds` (histogram). Aggregate ID is rejected as a label.
- **Logs:** `info` on start, checkpoint, rebuild; `error` on digest mismatch naming the tick, both
  hashes, and the last good offset.
- **Traces:** `state.fold` per batch, not per Event.
- **Alerts:** `StateProjectorDiverged` on `andara_state_digest_mismatches_total > 0` — symptom: the
  indexes no longer describe the World. Runbook ships in this story. `ProjectionStale` on lag, at lower
  severity, since a stale index is a tooling problem and a diverged one is a correctness problem.

## Test plan

Digest equality against `TickCompleted` over a long fixture run (AC-2); an injected divergence asserting
halt-and-alert; idempotency under duplicate delivery; tombstone removal verified against a broker with
compaction forced; rebuild-from-empty matching an incremental projector.

## Definition of done

CLAUDE.md §8, plus:
- The digest assertion runs continuously in production, not only in tests.
- The rebuild is a documented command exercised in CI — a rebuild that has never been run is a rebuild
  that does not work.
- A test proves the projector imports the sim core rather than reimplementing the fold.

## Open questions

- `[ASSUMPTION]` One projector process for all aggregate kinds. Splitting by kind would parallelize the
  fold and would break the single-hash sync assertion, which is worth more than the throughput.
- `[NEEDS BRIAN]` Whether `andara.state.v1` should be partitioned by Zone like the other topics — which
  keeps a Zone's state co-located and lets a future shard read only its own — or by aggregate key for
  even compaction load. Zone-partitioning is probably right for the same reason everything else is
  Zone-partitioned, and it is a permanent choice.
