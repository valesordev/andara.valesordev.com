---
id: AW-SRV-006
title: Zone snapshots keyed to partition offsets
epic: EPIC-04
component: server
type: feature
status: ready
size: M
depends_on: [AW-SRV-001, AW-SRV-004]
blocks: [AW-SRV-007, AW-SRV-019]
lane: implementation
risk: high
---

## Context

ADR-0002 makes Kafka the authority on order and in-memory state the authority on current state.
Snapshots are the checkpoint that lets recovery skip the replay. This story writes them.

The framing matters and is worth repeating from `EPIC-04`: **recovery is already correct without this
story**, because replaying `andara.commands.v1` from its beginning reconstructs the World exactly. This
is an RTO optimization built on top of a working recovery path, which is a much healthier position than
building the recovery mechanism and the performance mechanism as one thing. RPO is zero and is decided
by broker settings, not by anything here (`docs/specs/slo/recovery.md`); snapshot cadence affects RTO
only.

## User story

As an operator, I want the World checkpointed regularly, so that recovery takes seconds rather than the
length of the world's entire history.

## Scope

### In scope
- `sim.WorldStore` — the persistence interface, defined in `server/sim` and implemented outside it.
- A **snapshot round**: one consistent cut of every owned Zone at a single tick boundary, written as
  one object per Zone. Per-Zone objects exist so that a shard restores only its Zones (ADR-0001); the
  single tick exists so that cross-Zone movement is never half-applied on restore.
- Copy-on-write at the boundary: the only work inside the tick is the state copy; encoding and upload
  run off-tick.
- The snapshot **body** — `andara.state.v1.ZoneState` — and `state_version` handling from the first
  snapshot written.
- Object storage keyed `{zone_id}/{state_version}/{offset}`; a filesystem implementation for `make up`
  and tests, an S3-compatible implementation for the cluster.
- A `SnapshotWritten` control record on `andara.events.v1` after each object is durable.
- Cadence: `snapshot.interval` **60 s** per round (`docs/specs/slo/recovery.md`).

### Out of scope
- Recovery and replay — `AW-SRV-007`.
- Log retention and compaction — `AW-INF-004` and `AW-INF-005`.
- Volume and bucket provisioning — `AW-INF-003`.
- Compression and chunking of the body. `SnapshotEnvelope` leaves numbers 8+ for them; they arrive
  when a measured body size demands them.

## Acceptance criteria

1. **Given** a World of the sizing fixture (§Test plan) **when** a snapshot round overlaps a tick
   **then** `andara_snapshot_tick_stall_seconds` for that tick is under `snapshot.max_stall_ms`
   (default 5 ms) and the tick stays inside the Tick Budget.
2. **Given** identical Zone state **when** it is snapshotted twice **then** the two objects are
   byte-identical — canonical encoding per ADR-0007 rule 3, every `repeated` sorted by key.
3. **Given** a written snapshot **when** its envelope is decoded **then** it names `state_version`,
   `tick`, `zone_id`, the per-Partition `offsets` committed at that tick, and a `state_hash` equal to
   the hash the sim would compute for that Zone at that tick.
4. **Given** an envelope whose `state_version` is older than the binary's **when** it is read **then**
   the body is migrated forward through every intermediate version; **given** one that is newer
   **then** the read fails with `ErrStateVersion{Have, Want}` naming both. Never a partial or silent
   read.
5. **Given** an object write that fails or is interrupted **when** the store is listed **then** the
   partial object is not listed, the previous round remains the newest complete one, and the tick that
   triggered the round was unaffected.
6. **Given** a round where one Zone's write fails **when** `AW-SRV-007` lists rounds **then** that
   round is reported incomplete and is not selectable; the failure is counted on
   `andara_snapshot_failures_total{reason}`.
7. **Given** a Zone with a `SnapshotWritten` record on `andara.events.v1` **when** the record is read
   **then** its key resolves to an object whose envelope hash matches the record's.
8. **Given** `snapshot.interval` elapsed **when** the next tick boundary arrives **then** the round
   starts on that boundary and not mid-tick; the envelope `tick` equals the `TickCompleted.tick`
   emitted for the same boundary.

## Interface contract

```go
// CONTRACT SKETCH — not an implementation
package sim

// Snapshot is a copy of one Zone's mutable state, taken at a tick boundary.
// The copy is the only work done inside the tick. Encode is called off-tick.
type Snapshot struct {
    Zone         ZoneID
    Tick         Tick
    StateVersion uint32
    Offsets      []PartitionOffset   // sorted by partition
    StateHash    [32]byte
    body         *ZoneState          // immutable after the boundary
}
func (s *Snapshot) Encode() ([]byte, error)      // canonical andara.state.v1.SnapshotEnvelope

// SnapshotAll is called by the loop at a boundary and never anywhere else.
func (e *Engine) SnapshotAll() []Snapshot

// WorldStore is owned by sim and implemented in server/store. It knows keys and
// bytes; it does not know Kafka or the tick.
type WorldStore interface {
    Put(ctx context.Context, key string, envelope []byte) error   // atomic: visible only when complete
    Get(ctx context.Context, key string) ([]byte, error)
    List(ctx context.Context, zone ZoneID) ([]string, error)      // keys, newest offset first
}
```

Key format: `{zone_id}/{state_version}/{offset}` where `offset` is the Zone's Partition offset at the
boundary, zero-padded to 20 digits so lexical order is offset order. `Put` is atomic in both
implementations: S3 `PutObject` is; the filesystem store writes to `{key}.tmp` and renames.

### Body

```protobuf
// CONTRACT SKETCH — not an implementation; goes in andara/state/v1/zone_state.proto
message ZoneState {
  string zone_id = 1;
  uint64 tick = 2;
  bytes prng_state = 3;                       // the sim PRNG, so replay after restore is exact
  repeated EntityState entities = 4;          // sorted by entity_id
  repeated andara.log.v1.LoggedCommand deferred = 5;  // records deferred by sim.max_per_tick, in order
  uint64 next_event_id = 6;
}
message EntityState {
  string entity_id = 1;
  string room_id = 2;
  repeated andara.content.v1.ComponentValue components = 3;  // sorted by type
  uint64 linkdead_deadline_tick = 4;          // 0 when not linkdead (AW-SRV-015)
  // 5–9: dormant, dormant_since_tick (AW-SRV-014); linkdead_since_tick (AW-SRV-015);
  //      template, content_version (AW-SRV-022). Each bumps state_version by one.
}
```

Determinism rules of `log.proto` apply. `SnapshotEnvelope.body` carries the serialized `ZoneState`;
the envelope itself is unchanged.

### Migration

`server/store/migrate.go` holds `migrations map[uint32]func(*ZoneState) error`, one per
`state_version` bump, applied in order at read. A `state_version` bump without a migration entry fails
`make check`. Rollback of a binary against a newer snapshot is refused (AC-4); the operator recovers
from the newest snapshot the older binary can read, which is what `AW-INF-007` documents.

### Configuration

| Key | Env | Default | Notes |
|-----|-----|---------|-------|
| `snapshot.interval` | `ANDARA_SNAPSHOT_INTERVAL` | `60s` | per round; `slo/recovery.md` |
| `snapshot.max_stall_ms` | `ANDARA_SNAPSHOT_MAX_STALL_MS` | `5` | AC-1 threshold; a warn, not a refusal |
| `snapshot.store` | `ANDARA_SNAPSHOT_STORE` | `fs` | `fs` or `s3` |
| `snapshot.fs_path` | `ANDARA_SNAPSHOT_FS_PATH` | `/var/lib/andara/snapshots` | the volume `AW-INF-003` mounts |
| `snapshot.s3_bucket` | `ANDARA_SNAPSHOT_S3_BUCKET` | — | required when `store=s3` |
| `snapshot.s3_endpoint` | `ANDARA_SNAPSHOT_S3_ENDPOINT` | — | MinIO locally |
| `snapshot.upload_timeout` | `ANDARA_SNAPSHOT_UPLOAD_TIMEOUT` | `30s` | a round exceeding it is failed, not queued behind the next |

### Control record

```protobuf
// CONTRACT SKETCH — added to andara/log/v1/log.proto, next free number on the Event oneof carrier
message SnapshotWritten {
  string zone_id = 1; uint32 state_version = 2; uint64 tick = 3;
  repeated PartitionOffset offsets = 4; bytes state_hash = 5;
  string key = 6; uint64 size_bytes = 7;
}
```

Produced to the Zone's Partition on `andara.events.v1` after `Put` returns. It is an audit and tooling
record; `AW-SRV-007` discovers snapshots through `WorldStore.List` and verifies through the envelope
and `TickCompleted`, because a backwards scan of an Event Partition for the newest manifest is
unbounded and a `List` is one call.

### Error taxonomy

| Error | Meaning | Effect |
|-------|---------|--------|
| `ErrStateVersion{Have, Want}` | envelope newer than the binary | refuse the read |
| `ErrSnapshotStall` | copy exceeded `max_stall_ms` | warn; round continues |
| `ErrRoundIncomplete{Tick, Missing []ZoneID}` | one or more Zone writes failed | round not selectable |
| `ErrStoreUnavailable` | `Put`/`List` failed | round failed; next round on schedule |

## Data / state impact

This is the story that creates persisted World state, and therefore the story where format versioning
either happens or becomes impossible. `state_version` starts at `1` with this story; `AW-SRV-002`
already hashes it. The first real bump is exercised by the migration test before this story is done.

Snapshot cadence trades storage and write cost against recovery time, and it is also the rebalance
stall when ADR-0001's sharding is activated — a second consumer of the same number.

Storage growth: one round per minute per Zone. No retention here; `AW-INF-007` decides how many rounds
to keep, because that is a rollback-window question.

## Observability requirements

### Metrics
- `andara_snapshot_duration_seconds` — histogram, whole round, copy through last `Put`.
- `andara_snapshot_tick_stall_seconds` — histogram, the in-tick copy. The part that can hurt players.
- `andara_snapshot_bytes` — gauge, label `zone`. Zone count is bounded by content, not players.
- `andara_snapshot_last_tick`, `andara_snapshot_age_seconds` — gauges, per process.
- `andara_snapshot_failures_total` — counter, label `reason` (`store`, `encode`, `timeout`, `stall`).
- `andara_snapshot_rounds_total` — counter, label `outcome` (`complete`, `incomplete`).

### Logs
- `info` per round: `tick`, `zones`, `bytes`, `duration_ms`.
- `warn` on stall over budget and on any Zone failure, naming the Zone and key.
- Required fields: `ts`, `level`, `msg`, `service`, `env`, `tick`, `zone_id`, `trace_id`.

### Traces
- `persistence.snapshot` — root span per round, explicitly not inside `sim.tick`. Children:
  `snapshot.copy` (in-tick), `snapshot.encode`, `snapshot.put` per Zone with `key` and `bytes`.

### Alerts
- `SnapshotStale` on `andara_snapshot_age_seconds > 3 × snapshot.interval` for 5 m, tied to the RTO
  SLO: a stale snapshot is a recovery-time problem. Ships `docs/runbooks/snapshot-stale.md`.

## Test plan

- **Unit:** canonical encode byte-identity (AC-2); migration chain across `state_version` 1→2→3 with a
  refused 4 (AC-4); sorted `repeated` invariants; `Encode` of a Snapshot after the engine has advanced
  proves the copy is immutable.
- **Integration:** sizing fixture — 2,000 Rooms across 16 Zones, 10,000 Entities, 500 Characters —
  asserting AC-1 under a tick loop at 10 Hz; injected `Put` failure on one Zone asserting AC-5 and AC-6;
  filesystem store crash mid-write (kill between tmp write and rename) asserting AC-5; `SnapshotWritten`
  round-trip against a throwaway Redpanda (AC-7).
- **Manual/operator:**
  ```
  make up && andara-server
  andara-cli snapshot list --zone <zone>          # expect: one row per round, newest first
  ls /var/lib/andara/snapshots/<zone>/1/           # expect: zero-padded offsets, no *.tmp
  ```

## Definition of done

CLAUDE.md §8, plus:
- `state_version` migration is tested across at least one real bump.
- `docs/runbooks/snapshot-stale.md` exists.
- The sizing fixture is committed and its numbers are recorded in the story so that a later change to
  World scale is a visible change to the test, not a silent drift.

## Open questions

- `[ASSUMPTION]` Copy-on-write at the tick boundary rather than stop-the-world serialize. The copy is
  O(state) and measured by AC-1; if it ever exceeds the stall budget, the fallback is to stagger Zones
  across boundaries, which reintroduces the cross-Zone consistency problem this story avoids by taking
  one cut.
- `[ASSUMPTION]` World scale for the sizing fixture: 2,000 Rooms, 10,000 Entities, 500 Characters. A
  starting point Brian can revise; the numbers live in one fixture so revising them is one change.
- `[ASSUMPTION]` Discovery via `WorldStore.List` rather than by reading the manifest out of the log.
  ADR-0002 says "recovery finds snapshots by reading the log it already reads"; this story keeps the
  manifest in the log for audit and tooling and uses the store for discovery, for the reason stated in
  the contract. Verification still goes through the log's `TickCompleted`.
- `[ASSUMPTION]` MinIO in the local stack for the `s3` store, so the cluster path is exercised before
  the cluster exists. `AW-INF-002` gains a service; recorded there as a follow-up.
