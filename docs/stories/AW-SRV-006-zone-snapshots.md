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
- Object storage keyed `{zone_id}/{state_version}/{tick}/{offset}`; a filesystem implementation for
  `make up` and tests, an S3-compatible implementation for the cluster.
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
   **Amended 2026-09-22, with the failure mode that forced it.** A boundary that was *not* published
   — `Publish` returning an error, `ErrBoundaryLost` most clearly — leaves no `TickCompleted` for the
   round to equal, and the criterion as first written did not say what happens then. It says it now:
   **no round starts on a boundary that was not published.** A snapshot without its boundary record is
   one `AW-SRV-007` cannot verify (its AC-4 and AC-5 both compare against the log), and writing one
   anyway keeps `andara_snapshot_age_seconds` reporting health through exactly the outage where
   recovery time is about to matter. `AW-SRV-026` makes the `ErrBoundaryLost` case terminal by exiting
   `5` within a tick; this criterion holds whether or not that story has landed, and covers the
   non-terminal publish failures it does not.

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
    body         *ZoneState          // immutable after the boundary
}
func (s *Snapshot) Encode() ([]byte, error)      // canonical andara.state.v1.SnapshotEnvelope

// A METHOD, not a field: a field is filled where the struct is built, which is
// inside the tick. Measured, under "Sizing fixture" below.
func (s *Snapshot) StateHash() [32]byte
func (s *Snapshot) Key() string                  // SnapshotKey of this Snapshot's four values

// Formatted in one place so the store, the CLI, and AW-SRV-007 produce the
// same string from the same values rather than each formatting it by hand.
func SnapshotKey(zone ZoneID, stateVersion uint32, tick Tick, offset int64) string

// SnapshotAll is called by the loop at a boundary and never anywhere else.
// The timestamp is passed in, not read here: it lands in the envelope's
// taken_at_unix_nano, and reading a clock at encode time would make two encodes
// of the same Snapshot differ (AC-2).
func (e *Engine) SnapshotAll(takenAtUnixNano int64) []Snapshot

// WorldStore is owned by sim and implemented in server/store. It knows keys and
// bytes; it does not know Kafka or the tick.
type WorldStore interface {
    Put(ctx context.Context, key string, envelope []byte) error   // atomic: visible only when complete
    Get(ctx context.Context, key string) ([]byte, error)
    List(ctx context.Context, zone ZoneID) ([]string, error)      // keys, newest tick first
}
```

Key format: `{zone_id}/{state_version}/{tick}/{offset}`, where `tick` is the boundary the round was
taken at and `offset` is the Zone's Partition offset there. Both are zero-padded to 20 digits, so a
lexical listing is a tick-ordered listing and the newest round is the last key under a Zone's prefix.
`Put` is atomic in both implementations: S3 `PutObject` is; the filesystem store writes to `{key}.tmp`
and renames.

**Amended 2026-09-22, with the failure mode that forced it.** This was
`{zone_id}/{state_version}/{offset}`, with no tick. That is wrong in two ways, and both of them are
mine rather than the implementation's — the branch followed the contract exactly as written.

*It loses the previous complete round during a partial failure, which is the one case `AC-5` exists
for.* A Zone that received no Command since the last round is at the same offset, so it writes the
same key again. Zone A idle at offset 50 writes its object at tick 100; at tick 200 it is still at
offset 50 and **overwrites that object** with tick 200. If Zone B's `Put` fails in that second round,
tick 100 has lost A and tick 200 never got B: neither round is complete, and a store outage that
should have cost one round has cost every round. `AC-5`'s "the previous round remains the newest
complete one" does not hold, and `AC-6`'s "that round is reported incomplete" loses the premise that
some *other* round is not.

*And it cannot support the contract this story already hands to `AW-SRV-007`*, whose `ListRounds`
"groups `WorldStore` keys by tick" and whose `recover --round T` and `snapshot verify --round T` both
select a round by tick. A key with no tick in it supports none of the three. Discovery would have had
to be a `List` plus a `Get` per candidate just to read each envelope's tick back out.

The tick is therefore in the key, and the offset stays: it keeps the key self-describing for the seek
and keeps `SnapshotKey` a pure function of values the `Snapshot` already holds. One object per Zone
per round, no overwrite, and round grouping is a prefix scan. The story's own storage-growth line —
"one round per minute per Zone" — already assumed this shape; offset-keying was quietly under-counting
it. Raised by an automated review on `PR #46` and confirmed against `AW-SRV-007`'s contract.

### Body

The normative file is `docs/specs/protocol/andara/state/v1/zone_state.proto`. It arrives with
`PR #46` and is not on `main` until that merges; the table below is what it says, so this story stands
on its own in the meantime.

| `ZoneState` | # | Notes |
|---|---:|---|
| `zone_id` | 1 | |
| `tick` | 2 | equal to the envelope's tick and to `TickCompleted.tick` for the same boundary |
| `prng_state` | 3 | the sim PRNG, four words big-endian. Process-wide — see below |
| `entities` | 4 | sorted by `entity_id` |
| `deferred` | 5 | defined, **never populated** — see below |
| `next_event_id` | 6 | process-wide, like `prng_state` |
| `faulted` | 7 | set when applying a Command panicked in this Zone (`AW-SRV-002` AC-12) |
| `faulted_tick` | 8 | |

| `EntityState` | # | Notes |
|---|---:|---|
| `entity_id` | 1 | |
| `room_id` | 2 | the Zone is implicit (ADR-0001 rule 3), so a RoomID is the whole position |
| `components` | 3 | sorted by type, fields sorted by name, both re-established on encode |
| `linkdead_deadline_tick` | 4 | `AW-SRV-015`; nothing writes it yet |
| `dormant` | 5 | `AW-SRV-014` |
| `dormant_since_tick` | 6 | `AW-SRV-014`; `AW-SRV-032`'s retention reads it |
| — | 7 | `AW-SRV-015`'s `linkdead_since_tick`. Left **unused, not reserved**: a reserved range has to shrink to be used, and `buf` counts that as breaking |
| `template` | 8 | `AW-SRV-022` |
| `content_version` | 9 | `AW-SRV-022` |
| `name` | 10 | a Character's display name, immutable, carried by the `BindCharacter` that made the body (`AW-SRV-014`) |

Determinism rules of `log.proto` apply. `SnapshotEnvelope.body` carries the serialized `ZoneState`;
the envelope itself is unchanged.

**Amended 2026-09-22, with the two stories that had merged underneath it.** The sketch this replaces
reserved 5–9 in a comment for "dormant, dormant_since_tick (`AW-SRV-014`); linkdead_since_tick
(`AW-SRV-015`); template, content_version (`AW-SRV-022`)" and assigned none of them, and it omitted
`name` and `ZoneState.faulted`/`faulted_tick` entirely. `AW-SRV-014` and `AW-SRV-022` had both merged
by the time this story was picked up; `sim.EntityCanonicalBytes` already hashed every one of those
fields. **A body that omits a field the State Hash covers cannot reproduce that hash on restore**, and
`AW-SRV-007` AC-5 exits `2` on exactly that mismatch — so the omission would have surfaced as a World
that will not boot, months later, pointing nowhere near the cause. Field numbers are assigned above
because a `ready` story that leaves them to be discovered is not a pinned contract; going forward the
`.proto` itself is written before a story is marked `ready`, not a sketch with holes in it.

**`state_version` stays `1`, and the sketch's "each bumps `state_version` by one" is retracted.** It
contradicted `snapshot.proto`'s own text, which is the rule that stands: protobuf absorbs an added
field, and `state_version` exists for a change in what the state *means*. Nothing above is a bump —
no v1 snapshot has ever been written, so these fields are present from the first object rather than
added to an existing one.

**`deferred` (field 5) is defined and never populated**, and that is deliberate rather than
unfinished. The deferred backlog is the tick loop's buffer, not simulation state: it lives in
`tickloop.Source`'s per-Partition buffers, and `Engine.SnapshotAll` — which the loop calls at a
boundary and nowhere else — cannot see it. It does not need to. `Engine.Step` advances a Partition's
offset only past records it actually applied, so a deferred record sits *at or after* the offset the
envelope records, and `AW-SRV-007` reads it back from the log in its original order. Populating the
field would make the same Command replay twice, which is worse than replaying it once. The number is
spent and the shape is on record; checkpointing the backlog for real would need a seam from the loop
into `SnapshotAll` and a dedup rule in `AW-SRV-007`, which is a contract change rather than an
implementation detail.

**`prng_state` and `next_event_id` are process-wide, carried per-Zone.** `sim.WorldState` holds one
RNG and one `NextEventID` for the World, not one per Zone, so every object in a round carries the same
two values. A round is one cut at one tick, so the copies agree by construction, and a Zone restored
alone still has the PRNG state replay needs. `AW-SRV-007` must decide which copy wins and must refuse
a round whose copies disagree; that is carried into its acceptance criteria.

### Migration

`server/store/migrate.go` holds `migrations map[uint32]func(*ZoneState) error`, one per
`state_version` bump, applied in order at read. A `state_version` bump without a migration entry fails
`make check`. Rollback of a binary against a newer snapshot is refused (AC-4); the operator recovers
from the newest snapshot the older binary can read, which is what `AW-INF-007` documents.

### Configuration

| Key | Env | Default | Notes |
|-----|-----|---------|-------|
| `snapshot.interval` | `ANDARA_SNAPSHOT_INTERVAL` | `60s` | per round; `slo/recovery.md`. **`0` disables snapshots entirely** |
| `snapshot.max_stall_ms` | `ANDARA_SNAPSHOT_MAX_STALL_MS` | `15` | AC-1 threshold; a warn, not a refusal |
| `snapshot.store` | `ANDARA_SNAPSHOT_STORE` | `fs` | `fs` or `s3` |
| `snapshot.fs_path` | `ANDARA_SNAPSHOT_FS_PATH` | `/var/lib/andara/snapshots` | the volume `AW-INF-003` mounts |
| `snapshot.s3_bucket` | `ANDARA_SNAPSHOT_S3_BUCKET` | — | required when `store=s3` |
| `snapshot.s3_endpoint` | `ANDARA_SNAPSHOT_S3_ENDPOINT` | — | MinIO locally |
| `snapshot.upload_timeout` | `ANDARA_SNAPSHOT_UPLOAD_TIMEOUT` | `30s` | a round exceeding it is failed, not queued behind the next; kept at or under `interval` |

**Amended 2026-09-22.** The `0` semantic was not in this table and needed to be. It is the obvious
reading and implementation chose it, but it is not a free-floating convenience: a disabled cadence is
what makes the `SnapshotStale` alert below fire forever if the alert is not written to account for it,
so the two rows have to be read together. Recovery is still correct with snapshots off — replay from
offset zero always is — which is why disabling them is allowed at all; what it costs is RTO.

### Control record

```protobuf
// CONTRACT SKETCH — added to andara/log/v1/log.proto as a top-level message
message SnapshotWritten {
  string zone_id = 1; uint32 state_version = 2; uint64 tick = 3;
  repeated PartitionOffset offsets = 4; bytes state_hash = 5;
  string key = 6; uint64 size_bytes = 7;
}
```

Produced to the Zone's Partition on `andara.events.v1` after `Put` returns, under its own record key.
It is an audit and tooling record; `AW-SRV-007` discovers snapshots through `WorldStore.List` and
verifies through the envelope and `TickCompleted`, because a backwards scan of an Event Partition for
the newest manifest is unbounded and a `List` is one call.

Its `key` field carries the store key, so it moves with the amended format above.

**Amended 2026-09-22, with the reading of `log.proto` that forced it.** The sketch said this record
takes "the next free number on the Event oneof carrier." There is no oneof in `log.proto`: `Event` and
`TickCompleted` are top-level messages, and a control record on `andara.events.v1` is told apart by
its record key, not by its position in a union. `SnapshotWritten` is a third top-level message,
registered as its own subject under `TopicRecordNameStrategy` in `deploy/kafka/schemas.yaml`
(`AW-INF-004`). Caught in implementation and fixed correctly there before this amendment existed.

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
already hashes it. **Amended 2026-09-22:** no real bump is exercised here, and none is scheduled —
under the rule this story settled, `state_version` moves only when the meaning of state changes, and
nothing on the roadmap does that. The migration machinery is tested against a synthetic chain instead,
and `make check` fails a bump that arrives without a migration. See the Definition of done.

Snapshot cadence trades storage and write cost against recovery time, and it is also the rebalance
stall when ADR-0001's sharding is activated — a second consumer of the same number.

Storage growth: one round per minute per Zone, and with the tick in the key an idle Zone now writes a
new object each round rather than overwriting its last — the object count is the round count, which it
was not before. No retention here; `AW-INF-007` decides how many rounds to keep, because that is a
rollback-window question.

At the 25,000-Entity fixture scale a body is roughly 2.5× what it was, which lands on the "load newest
snapshot" term in `docs/specs/slo/recovery.md` — quoted there as 1–5 s inside a ~20–55 s total against
a 120 s M2 target. That document's own conclusion is that Kubernetes failure detection and pod startup
dominate recovery, not the simulation, so the target is not threatened; it is worth re-checking when
the term is measured rather than estimated.

## Observability requirements

### Metrics
- `andara_snapshot_duration_seconds` — histogram, whole round, copy through last `Put`.
- `andara_snapshot_tick_stall_seconds` — histogram, the in-tick copy. The part that can hurt players.
- `andara_snapshot_bytes` — gauge, label `zone`. Zone count is bounded by content, not players.
- `andara_snapshot_interval_seconds` — gauge, the configured cadence. *Recommended*, for the alert
  below; see the note there for what is required and what is left to implementation.
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
  **The threshold is derived from the configured cadence, not from the default**, and the alert does
  not fire when `snapshot.interval` is `0`.
  **Amended 2026-09-22, with the configurations that break a constant.** The `3 ×` was read as
  decoration and rendered as the constant `3 * 60`. It is not decoration: `server.snapshot.interval`
  is a free value in the chart, so a ten-minute cadence alerts at eight minutes — before the first
  round has been taken — while a ten-second cadence is caught nearly a minute late. And because
  `andara_snapshot_age_seconds` counts from process start when no round has been taken, `interval: 0`
  alerts permanently on a configuration that was asked for.
  *Recommended mechanism, not mandated:* export the configured cadence as a gauge and write the rule
  against it —
  `andara_snapshot_age_seconds > 3 * andara_snapshot_interval_seconds and andara_snapshot_interval_seconds > 0`.
  That avoids parsing a Go duration string in a Helm template and stays correct if the value is ever
  set somewhere the chart cannot see. Rendering the threshold from values is acceptable if it also
  suppresses at `0`.

## Test plan

- **Unit:** canonical encode byte-identity (AC-2); migration chain across `state_version` 1→2→3 with a
  refused 4 (AC-4); sorted `repeated` invariants; `Encode` of a Snapshot after the engine has advanced
  proves the copy is immutable.
- **Integration:** sizing fixture — 2,000 Rooms across 16 Zones, 25,000 Entities, 500 Characters —
  asserting AC-1 under a tick loop at 10 Hz; injected `Put` failure on one Zone asserting AC-5 and AC-6;
  filesystem store crash mid-write (kill between tmp write and rename) asserting AC-5; `SnapshotWritten`
  round-trip against a throwaway Redpanda (AC-7).
- **AC-5's second clause, explicitly.** *Two* rounds, with at least one Zone idle across both so that
  it repeats its Partition offset, and a `Put` failing on a different Zone in the second round. Assert
  the **first round is still complete and still selectable**. Added 2026-09-22: the injected-failure
  test above asserts that the failing round is *counted* incomplete, which is AC-6, and nothing
  asserted that the previous round survived it. Under the superseded key format that test fails, which
  is what made the format wrong; it is the regression test for the amendment.
- **A boundary that was not published starts no round** (AC-8, as amended). `Publish` returning an
  error — including `ErrBoundaryLost` — and the next due interval elapsing produces no object and no
  manifest.
- **Manual/operator:**
  ```
  make up && andara-server
  andara-cli snapshot list --zone <zone>          # expect: one row per round, newest first
  ls /var/lib/andara/snapshots/<zone>/1/           # expect: one dir per round tick, zero-padded
  ls /var/lib/andara/snapshots/<zone>/1/*/         # expect: zero-padded offsets, no *.tmp
  ```

## Definition of done

CLAUDE.md §8, plus:
- ~~`state_version` migration is tested across at least one real bump.~~ **Retired 2026-09-22, by the
  versioning rule this story settled.** The line assumed a real bump would be along shortly — the
  superseded body sketch had `AW-SRV-014`, `AW-SRV-015` and `AW-SRV-022` each bumping by one. Under
  the rule that now stands, none of them bump: every field they add is additive and protobuf absorbs
  it, and `state_version` moves only when the *meaning* of state changes. There is no scheduled bump
  to carry this line forward to, so it does not move to another story — it retires. What it was
  reaching for is already enforced: the synthetic 1→2→3-with-4-refused chain covers AC-4, and the
  registry-completeness check fails `make check` on a `state_version` bump with no migration entry, so
  the first real bump cannot ship untested whenever it comes.
- `docs/runbooks/snapshot-stale.md` exists.
- The sizing fixture is committed and its numbers are recorded in the story so that a later change to
  World scale is a visible change to the test, not a silent drift. **Recorded below.**

### Sizing fixture, measured

**Fixture scale decided 2026-09-22 (Brian): 25,000 Entities**, up from 10,000, for headroom as the
World grows. 16 Zones, 2,000 Rooms and 500 Characters are unchanged — see the note below on why.
`snapshot.max_stall_ms` moves from `5` to `15` with it.

Measured at **10,000** Entities, AMD Ryzen 9 3900X, worst of 5 rounds:

| What the tick does at the boundary | Worst round | Budget then |
|---|---|---|
| Copy **and hash** each Zone | 24.7 ms | 5 ms |
| Copy only; hash off-tick | **2.7 ms** uncontended, **4.8 ms** under load | 5 ms |

At **25,000**, *extrapolated linearly and not yet measured*: ~6.8 ms uncontended, ~12 ms under load.
**Measure it and replace this line with the real numbers.** The extrapolation is from a single point,
and the copy is O(Entities × Component fields), so it holds only while the Component mix per Entity
stays roughly what the fixture builds.

**Why `max_stall_ms` is `15` and not `5`.** The load-bearing constraint is ADR-0008's: the copy runs
inside the tick, so what must hold is *copy + tick work under the 50 ms tick budget*. 15 ms of copy on
top of a tick that measures under 1 ms at the idle floor is 16 ms of 50 — and the 5 ms was never
derived from anything, it was headroom this story reserved for handler work that mostly does not exist
yet. 15 ms clears the extrapolated loaded figure with margin, and if the measurement comes in
differently the rule is roughly **twice the measured loaded number**.

**And it does not move p50.** A round is one tick in 600 — a 60 s cadence at 10 Hz — so the stall is a
p99.8 event, not a typical tick. That matters because ADR-0001's "revisit sharding when p50 tick
duration exceeds 25 ms" is the trigger a large stall would otherwise threaten, and a once-a-minute
spike cannot move a median. `andara_snapshot_tick_stall_seconds` is a separate histogram precisely so
this is visible on its own rather than smeared into tick duration.

**Why only the Entity count moved.** Rooms are topology: content-versioned and hashed by AW-SRV-012's
`ContentSwap`, not carried in `ZoneState` at all, so Room count does not enter the boundary copy. The
Character count is a concurrency assumption — how many bodies are bound at once — rather than a
statement about World size, and 25,000 Entities over 2,000 Rooms does raise average Room occupancy to
about 12.5, which is a perception-scoping cost (`AW-SRV-004`) and a `look` cost rather than a snapshot
one. Both are worth their own look; neither is this story's.

**The first row is why `StateHash` is a method.** A field is filled where the struct is built, and the
struct is built inside the tick; a CPU profile put 18% of the round in `sim.writeEscaped`, because the
per-Zone hash walks every Entity, Component and field and escapes each into a canonical record — about
five times the cost of the copy it accompanies. This story's own rule settles it: "the only work
inside the tick is the state copy; encoding and upload run off-tick", and hashing is encoding.

**The margin is thin and the World scale above is an `[ASSUMPTION]`.** 2.7 ms of a 5 ms budget, 4.8 ms
on a busy machine. Entity count is the term that moves: doubling to 20,000 puts the copy over budget,
and the stated fallback — staggering Zones across boundaries — reintroduces the cross-Zone consistency
problem the single cut exists to avoid. Revising the scale is Brian's call and is one change to
`server/simtest/sizing.go`; the point of committing the fixture is that revising it is a visibly
failing test rather than a drift.

Two cautions on reading the numbers above. **The 20,000 figure is extrapolated linearly from a single
measured point** — the fixture runs at one scale, and the copy is O(Entities × Component fields), so
the real curve depends on how Components grow with Entity count. And **the 5 ms is a sub-budget this
story set for itself**, not ADR-0008's: the tick budget is 50 ms against a 100 ms interval, and the
copy is inside the tick, so the load-bearing constraint is *copy + tick work under 50 ms*. The 5 ms
was headroom reserved for handler work that mostly does not exist yet. The rebalance stall is a
different number again — ADR-0001 ties it to snapshot *cadence*, because the stall is the rebalance
plus the recovery it implies, and recovery duration follows snapshot age.

## Open questions

- ~~**Inherited from `AW-SRV-003` (2026-09-18):** whether the snapshot body reuses
  `andara.log.v1.Entity` rather than defining a second shape.~~ **Resolved 2026-09-22: it does not.**
  `log.v1.Entity` is the Entity *in transit* across a Zone boundary, and it deliberately carries no
  position, because an `Arrive` names the target Room. A snapshot must carry position. Extending the
  transit record with snapshot-only fields would put state that means nothing on the wire into a
  record the cross-Zone path encodes on every handoff. `andara.state.v1.EntityState` is a separate
  message, with the round-trip test against `sim.EntityState` that the question asked for.

- ~~`[ASSUMPTION]` Copy-on-write at the tick boundary rather than stop-the-world serialize.~~
  **Holds, measured 2026-09-22** — see the fixture table above — but only once hashing moved off the
  tick, and with less headroom than the assumption implied.
- `[ASSUMPTION]` **Fixture scale decided 2026-09-22 (Brian): 2,000 Rooms, 25,000 Entities, 500
  Characters**, with `snapshot.max_stall_ms` at `15` to match. What remains an assumption is that the
  fixture stands in for the World the game actually holds; if it does not, `server/simtest/sizing.go`
  is the one constant to change and a failing test is how you find out.
- ~~`[ASSUMPTION]` Discovery via `WorldStore.List` rather than by reading the manifest out of the
  log.~~ **Resolved 2026-09-22, and cheaper than when it was written.** ADR-0002 says "recovery finds
  snapshots by reading the log it already reads"; this story keeps the manifest in the log for audit
  and tooling and uses the store for discovery, because a backwards scan of an Event Partition is
  unbounded and a `List` is one call. With the tick in the key, discovery is a `List` and a prefix
  group — not a `List` plus a `Get` per candidate to read each envelope's tick back. Verification
  still goes through the log's `TickCompleted`.
- `[ASSUMPTION]` **Open, deferred by the story itself.** MinIO in the local stack, so the cluster path
  is exercised before the cluster exists. `AW-INF-002` gains the service; until it does, the `s3`
  store is covered by a test that skips unless an endpoint is named in the environment.
