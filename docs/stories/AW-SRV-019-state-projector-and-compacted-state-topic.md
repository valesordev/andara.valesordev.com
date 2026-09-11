---
id: AW-SRV-019
title: State projector and the compacted current-state topic
epic: EPIC-10
component: server
type: feature
status: ready
size: M
depends_on: [AW-SRV-004, AW-SRV-006]
blocks: [AW-SRV-017]
lane: implementation
risk: high
---

## Context

ADR-0002: the ordered history flows into a **compacted** `andara.state.v1` holding the current state of
each aggregate, and the indexes read from *that* rather than from the full history. Without it, building
or rebuilding a Redis or Postgres index costs time proportional to the World's entire history, forever.
With it, a rebuild costs time proportional to live state.

The design point that makes it safe: **the projector is a replica of the simulation, not a second
implementation of it.** It runs `sim.Engine` — the same dependency-free package — over the same
`LoggedCommand` records and the same `TickCompleted` boundaries the live server consumes, using exactly
the `Replay` seam `AW-SRV-007` uses for recovery. Its `StateHash()` after tick `T` must equal
`TickCompleted{T}.state_hash`, which turns "are the indexes in sync with the World" from belief into an
assertion. The draft's alternative — folding *Events* into state — would require the sim to mutate state
only through the Events it emits, which `AW-SRV-003` does not promise and which nothing else needs.

## User story

As an operator, I want rebuilding an index to cost minutes rather than the age of the world, so that a
projection schema change is routine.

## Scope

### In scope
- `andara-projector state` — a second binary in the server module, consuming `andara.commands.v1` and
  `andara.events.v1` (for `TickCompleted` and `SnapshotWritten`), bootstrapping from the newest complete
  snapshot round through a read-only `sim.WorldStore`.
- The `andara.state.v1` record: `StateRecord{key, kind, tick, source_offset, content_version,
  state_version, digest, body}`; typed keys; tombstones.
- Producing, after each replayed tick, one record per aggregate that tick's Events touched, plus
  tombstones for destroyed ones.
- Digest verification against every `TickCompleted`; halt on divergence.
- `andara-projector state --rebuild`: wipe the consumer group, restart from the newest round.
- Write isolation: the projector's Kafka principal is the only one with write on `andara.state.v1`.
- Depends on `AW-SRV-006` (new edge) because bootstrap reads snapshot rounds.

### Out of scope
- Redis and Postgres indexes — `AW-SRV-017`, `AW-SRV-018`.
- Recovery. `andara.state.v1` has a ragged tick edge across Partitions and compaction has discarded
  earlier versions; it is a read-path bootstrap and never a recovery source.
- Any authoritative role. Nothing may read `andara.state.v1` to make a game decision.

## Acceptance criteria

1. **Given** a tick whose Events include `CharacterArrived` **when** the projector replays it **then**
   records for `character:<id>` and `room:<zone>/<id>` are produced with `tick` equal to that tick and
   `body` equal to the replica's state for those aggregates.
2. **Given** the projector has replayed through tick `T` **when** its `StateHash()` is compared with
   `TickCompleted{T}.state_hash` **then** they are equal, for every `T`.
3. **Given** a divergence at `T` **when** it is detected **then** the projector produces nothing further,
   commits no offset past `T-1`, sets `andara_state_digest_mismatches_total` to 1, exits `2`, and the
   `error` line names `T`, both hashes, and the last good offset per Partition.
4. **Given** a Partition batch redelivered after a crash **when** it is replayed **then** every record
   produced is byte-identical to the first delivery. At-least-once is safe because the replica is
   deterministic and the record is canonical.
5. **Given** a destroyed Entity (`CharacterPurged`, item destroyed) **when** its tick replays **then** a
   tombstone (null value) is produced for its key, and after `topics.py` forces compaction the key is
   absent.
6. **Given** an empty `andara.state.v1` **when** `--rebuild` runs against a World with 10,000 Entities and
   a 24 h history **then** it reaches a state whose records equal the incremental projector's, in under
   `snapshot load + tail replay` — never a from-zero replay when a complete round exists.
7. **Given** any record **when** it is inspected **then** `content_version` names the `packID@version`
   active when the aggregate was last written, so a runtime object traces to authored source.
8. **Given** the projector stopped for an hour **when** the World continues **then** tick metrics on the
   server are unchanged and no player-visible behavior differs.
9. **Given** any Kafka principal other than `andara-projector-state` **when** it produces to
   `andara.state.v1` **then** the broker rejects it with an authorization error (`AW-INF-004` ACLs).
10. **Given** a `state_version` newer than the projector binary **when** bootstrap reads the round
    **then** it exits `4` naming both, as `AW-SRV-007` does.

## Interface contract

```protobuf
// CONTRACT SKETCH — not an implementation; andara/state/v1/record.proto
message StateRecord {
  string key = 1;                 // "character:<id>" | "npc:<id>" | "item:<id>" | "room:<zone>/<id>" | "zone:<id>"
  AggregateKind kind = 2;
  uint64 tick = 3;
  int64 source_offset = 4;        // commands.v1 offset of the last Command applied to this aggregate's Zone
  string content_version = 5;     // packID@version active at write
  uint32 state_version = 6;
  bytes digest = 7;               // sha256 of body; per-record integrity, not the World hash
  bytes body = 8;                 // canonical EntityState | RoomState | ZoneSummary
}
```

Key = record key on the topic. Partitioner: `hash(zone_id) % 64`, the same function as
`andara.commands.v1`, so a Zone's aggregates share a Partition and a future shard reads only its own.
Tombstone = null value with the same key.

```go
// CONTRACT SKETCH — not an implementation
package projector
// Touched maps the Events of one tick to the aggregate keys whose bodies must be re-emitted.
// It is a table over EventType, not logic; an EventType absent from the table fails a unit test.
func Touched(events []sim.Event) []Key
// Run: bootstrap(round) → engine.Replay(boundaries, records, afterTick: emit Touched) → verify each TickCompleted.
```

### Consumer and producer

| Property | Value |
|----------|-------|
| consumer group | `andara-projector-state-<env>` |
| consumes | `andara.commands.v1` (all Partitions), `andara.events.v1` (control records only) |
| produces | `andara.state.v1`, `acks=all`, idempotent, key-ordered |
| offset commit | after the records for tick `T` are acked, never before |
| bootstrap | newest complete round via `sim.WorldStore` read-only; `--from-zero` overrides |

### Configuration

| Key | Env | Default | Notes |
|-----|-----|---------|-------|
| `projector.state.batch_ticks` | `ANDARA_PROJECTOR_BATCH_TICKS` | `10` | ticks replayed between produce flushes |
| `projector.state.lag_budget` | `ANDARA_PROJECTOR_LAG_BUDGET` | `5s` | `ProjectionStale` threshold |
| `snapshot.*` | as `AW-SRV-006` | | read-only use |

### Exit codes

`0` clean stop · `1` config/store · `2` digest divergence · `3` log gap · `4` state_version.

## Data / state impact

`andara.state.v1`: compacted, 64 Partitions, `min.compaction.lag.ms=60000`, `cleanup.policy=compact`,
provisioned by `AW-INF-004`'s `topics.yaml` in this story's PR. Derived and disposable; AC-6 proves it
by deleting it.

The projector holds a full replica of World state in memory — the same footprint as the server. That is
the cost of the replica design and is stated in the Helm values (`AW-INF-003`).

Compaction throughput is the risk to watch: if churn outpaces the compactor, the topic becomes a second
full history. `andara_state_topic_bytes` makes that visible.

## Observability requirements

### Metrics
- `andara_state_projector_lag_seconds` — gauge; `now − TickCompleted.accepted_at` of the last verified tick.
- `andara_state_projector_tick` — gauge.
- `andara_state_records_produced_total` — counter, label `kind` (bounded enum).
- `andara_state_tombstones_total` — counter.
- `andara_state_digest_mismatches_total` — counter; must stay 0.
- `andara_state_rebuild_duration_seconds` — histogram, label `phase` (`bootstrap`, `replay`).
- `andara_state_topic_bytes` — gauge, from broker metadata.
Aggregate ID is rejected as a label.

### Logs
- `info` on start with the round used; per `batch_ticks` at `debug`; `error` on divergence per AC-3.
- Required fields: `ts`, `level`, `msg`, `service=andara-projector-state`, `env`, `tick`, `partition`.

### Traces
- `state.replay` per batch with `ticks`, `records_in`, `records_out`; `state.verify` per boundary.

### Alerts
- `StateProjectorDiverged` on `andara_state_digest_mismatches_total > 0`: the indexes no longer describe
  the World. Runbook `docs/runbooks/state-projector-diverged.md` ships here: stop downstream projectors,
  `--rebuild`, and if it diverges again the *server* is non-deterministic — escalate as a sim bug.
- `ProjectionStale{projection="state"}` on lag over `lag_budget` for 5 m, low severity, runbook
  `docs/runbooks/projection-stale.md` shared with `AW-SRV-017`/`018`.

## Test plan

- **Unit:** `Touched` table completeness against every `EventType`; record canonical encoding; tombstone
  emission for each destroy Event.
- **Integration:** a 10-minute fixture run asserting AC-2 at every boundary; injected divergence (flip a
  byte in the replica) asserting AC-3; crash-and-redeliver asserting AC-4; forced compaction asserting
  AC-5; `--rebuild` from round vs incremental equality (AC-6); ACL rejection (AC-9).
- **Manual/operator:**
  ```
  make up && andara-projector state
  andara-cli projection status                 # expect: state: tick N, lag <1s, digest ok
  andara-projector state --rebuild             # expect: bootstrap from round, replay, "digest ok"
  ```

## Definition of done

CLAUDE.md §8, plus: the digest assertion runs continuously in production; `--rebuild` is exercised in
CI; a test asserts the projector binary imports `server/sim` and contains no `Apply` of its own
(depguard rule).

## Open questions

- `[ASSUMPTION]` Partitioned by Zone with the commands partitioner. Permanent, and the same reason
  everything else is Zone-partitioned.
- `[ASSUMPTION]` One projector process for all aggregate kinds, because the single-hash assertion needs
  one replica.
- `[ASSUMPTION]` Replica rather than Event fold, for the reason in Context. If a later story makes the
  sim internally event-sourced, the projector can drop the commands consumer without changing its
  output.
