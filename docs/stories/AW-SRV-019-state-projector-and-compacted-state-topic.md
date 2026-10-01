---
id: AW-SRV-019
title: State projector and the compacted current-state topic
epic: EPIC-10
component: server
type: feature
status: review
size: M
depends_on: [AW-SRV-004, AW-SRV-006]
blocks: [AW-SRV-017, AW-INF-025, AW-SRV-043, AW-SRV-044]
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
- `store.ListRounds` and `store.Round`, exactly as `AW-SRV-007`'s interface contract states them, and
  the load-Zones phase of its sequence (round → a `sim.Engine` at the round's tick, offsets, PRNG, and
  next EventID). *(Added 2026-09-24, Brian's call.)* Bootstrap cannot select a round without them, and
  `AW-SRV-007` is held behind `AW-SRV-026`/`AW-SRV-028`. This story implements a contract that is
  already written rather than writing one from code; `AW-SRV-007` inherits both and keeps everything
  else in its sequence — boot recovery, `recover --verify`, the Admin RPCs, and its exit codes.

### Out of scope
- Redis and Postgres indexes — `AW-SRV-017`, `AW-SRV-018`.
- Recovery. `andara.state.v1` has a ragged tick edge across Partitions and compaction has discarded
  earlier versions; it is a read-path bootstrap and never a recovery source.
- Any authoritative role. Nothing may read `andara.state.v1` to make a game decision.

## Acceptance criteria

1. **Given** a tick whose Events include `CharacterArrived` **when** the projector replays it **then**
   records for `character:<zone>/<id>` and `room:<zone>/<id>` are produced with `tick` equal to that tick and
   `body` equal to the replica's state for those aggregates.
2. **Given** the projector has replayed through tick `T` **when** its `StateHash()` is compared with
   `TickCompleted{T}.state_hash` **then** they are equal, for every `T`.
3. **Given** a divergence at `T` **when** it is detected **then** the projector produces nothing further,
   commits no offset past `T-1`, sets `andara_state_digest_mismatches_total` to 1, exits `2`, and the
   `error` line names `T`, both hashes, and the last good offset per Partition.
4. **Given** a Partition batch redelivered after a crash **when** it is replayed **then** every record
   produced is byte-identical to the first delivery. At-least-once is safe because the replica is
   deterministic and the record is canonical.
5. **Given** an Entity that leaves a Zone — a cross-Zone move, or destruction — **when** its tick
   replays **then** a tombstone (null value) is produced for its key in that Zone, and after compaction
   the key is absent. *(Amended 2026-09-24.)* *(Amended again 2026-09-24 at §8: `topics.py` never
   could force compaction, since it creates topics and compares their config. The test forces it
   the way a broker allows. It produces to a throwaway compacted topic declared with
   `segment.ms`, `min.cleanable.dirty.ratio` and `delete.retention.ms` lowered to seconds, then
   polls a full read of that topic to a deadline until the key is gone (live-assertions.md).
   `andara.state.v1`'s own settings stay as `topics.yaml` declares them.)* The Zone exit is the case this story
   can exercise: the sim has no destroy Event yet. `CharacterPurged` gets its `Touched` row and its
   tombstone assertion in `AW-SRV-032`, as an inherited Definition-of-done line.
6. **Given** an empty `andara.state.v1` **when** `--rebuild` runs against a World with 10,000 Entities and
   a 24 h history **then** it reaches a state whose records equal the incremental projector's, in under
   `snapshot load + tail replay` — never a from-zero replay when a complete round exists.
   *(Clarified 2026-09-24, from implementation:)* "equal" is on `key`, `kind`, `body`, `digest`,
   `content_version`, and `state_version`, not on `tick` or `source_offset`. A rebuild writes every
   key at its bootstrap tick, and the incremental projector writes each at the tick that last
   touched it, so those two fields can differ while the topics describe the same World. They say
   when a record was written, not what it describes.
   *(Amended 2026-09-27, §8, architecture:)* "in under `snapshot load + tail replay`" can't be met
   literally. `--rebuild` does both, plus the dump and the broker round trips. The bound is on
   history: the rebuild starts from the latest complete round, and 24 h of history ahead of that
   round adds less to its run time than `snapshot load + tail replay`.
   *(Reopened 2026-09-29, §8, architecture: #143 falsifies this criterion's property on a live
   World. A rebuild from a round diverges at round tick + 1, and a from-zero replay of the same log
   verifies. AC-6 fails until #143 is fixed.)*
7. **Given** any record **when** it is inspected **then** `content_version` names the `packID@version`
   active when the aggregate was last written, so a runtime object traces to authored source.
8. **Given** the projector stopped for an hour **when** the World continues **then** tick metrics on the
   server are unchanged and no player-visible behavior differs.
9. **Given** any Kafka principal other than `andara-projector-state` **when** it produces to
   `andara.state.v1` **then** the broker rejects it with an authorization error (`AW-INF-004` ACLs).
   *(Note 2026-09-24:)* `AW-INF-004` shipped no ACLs, and the local Redpanda runs without SASL, so
   nothing enforces this yet. This story authenticates as the principal and declares the ACL
   in `deploy/kafka/topics.yaml`. Enforcing it, and this criterion's assertion, belongs to the
   first broker story that turns on authentication. That story inherits this as a
   Definition-of-done line.
10. **Given** a `state_version` newer than the projector binary **when** bootstrap reads the round
    **then** it exits `4` naming both, as `AW-SRV-007` does.

## Interface contract

```protobuf
// CONTRACT SKETCH — not an implementation; andara/state/v1/record.proto
message StateRecord {
  string key = 1;                 // "character:<zone>/<id>" | "npc:<zone>/<id>" | "item:<zone>/<id>" | "room:<zone>/<id>" | "zone:<id>"
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

**Every Entity key names its Zone. *(Amended 2026-09-24, Brian's call.)*** An Entity changes Zone: a
cross-Zone move deletes it from the source Zone at tick `T` (`server/sim/verbs.go`) and the `Arrive`
applies on the target's Partition at a later tick. With `character:<id>` and a Zone partitioner, one
key would live on two Partitions. Compaction runs per Partition, so the stale record would never be
collapsed, and Kafka has no cross-Partition order to say which record is current. So the key is
`<kind>:<zone>/<id>`. A Zone exit is a tombstone on the source Partition, and the arrival writes a new
key on the target Partition. Keys never migrate, compaction stays correct, and each shard reads only
its own Zones. Answering "where is Character X now" is an index's job (`AW-SRV-017`'s `aw1:char:<id>`),
not this topic's. While the Entity is in transit, a reader can see it in neither Zone, or briefly in
both. Both records carry `tick`, which settles it.

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
| consumer group | `andara-projector-state-<env>` (the reserved name in `deploy/kafka/topics.yaml` is renamed to match in this story's PR) |
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
  *(Implemented 2026-09-24 as the boundary record's Kafka timestamp. `TickCompleted` carries no
  `accepted_at`, and the record timestamp is the moment the server produced it.)*
- `andara_state_projector_lag_budget_seconds` — gauge; `projector.state.lag_budget`. *(Added
  2026-09-24.)* `ProjectionStale` compares the lag with it, the way `SnapshotStale` reads
  `andara_snapshot_interval_seconds`, rather than a number copied into the rule.
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
  *(Implemented 2026-09-24:)* a batch spans every Partition, so the per-batch line carries `tick`
  and `ticks`. Per-Partition detail lives where one Partition is the subject: the divergence
  line's `last_good_offsets` and a log gap's error, which names the Partition.

### Traces
- `state.replay` per batch with `ticks`, `records_in`, `records_out`; `state.verify` per boundary.

### Alerts
- `StateProjectorDiverged` on `andara_state_digest_mismatches_total > 0`: the indexes no longer describe
  the World. Runbook `docs/runbooks/state-projector-diverged.md` ships here: stop downstream projectors,
  `--rebuild`, and if it diverges again the *server* is non-deterministic — escalate as a sim bug.
- `ProjectionStale{projection="state"}` on lag over `lag_budget` for 5 m, low severity, runbook
  `docs/runbooks/projection-stale.md` shared with `AW-SRV-017`/`018`.
- *(Implemented 2026-09-24:)* both are tied to `docs/specs/slo/projection-freshness.md`, written
  here because CLAUDE.md §7 puts an SLO before an alert. Its SLI follows from this story; its target
  is proposed and `[NEEDS BRIAN]`. As specified, `StateProjectorDiverged` could not fire. The
  counter reaches 1 and the process exits, and the restart starts it at 0 before any scrape. So
  the binary holds `/metrics` up, unready, for 60 s before exiting `2`. Neither alert can see a
  projector that is not running; the SLO names that gap.

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
  *(2026-09-24:)* `andara-cli projection status` does not exist. The projection query RPC is
  `AW-SRV-017`'s (`QueryProjection`). Until then, the status is the projector's `/readyz` and its
  `andara_state_projector_*` series.

## Definition of done

CLAUDE.md §8, plus: the digest assertion runs continuously in production; `--rebuild` is exercised in
CI; a test asserts the projector binary imports `server/sim` and contains no `Apply` of its own
(depguard rule).

### §8 pass (2026-09-26, architecture): stays `review`

Against `origin/main` `3e0adf6`, on #103.

**These hold:**
- **`andara_state_topic_bytes` reads 0.** Fixed by naming the partitions, with
  `TestTopicBytesReportsWhatTheTopicHolds` on Redpanda.
- **Sticky divergence, as ruled.** `TestRun_ADivergenceSurvivesARestartPastANewerRound`, on Redpanda
  and mutation-checked. `docs/runbooks/state-projector-diverged.md` is rewritten for it in this
  pass: a crash loop on the same tick is the expected state, and `--rebuild` is the release.
- **AC-7 after #93.** `TestCharacterRecordsNameTheirContentVersion`.
- **#78.** The binary is untracked and `/andara-*` is ignored.

**AC-5: architecture's ruling is (a), delivered on this branch.** The stack's Redpanda runs with
`log_segment_ms_min` at 1 s. It's declared under `broker.local` in `deploy/kafka/topics.yaml`, so
`make up` / `make topics-apply` set it on existing volumes and in CI. `make topics-diff` reports it.

Measured on the stack before ruling, a throwaway topic with `segment.ms=1000`,
`min.cleanable.dirty.ratio=0.01` and `delete.retention.ms=1000`, and a filler record every 5 s:
- the key's two values were compacted away by 10 s;
- the tombstone was gone by 21 s.

AC-5 therefore stands as amended, and the test implementation held back can be restored. Poll it
to about 60 s.

**Still owed, and the story stays `review` until they land:**
1. **Implementation: the AC-5 test**, restored against the lowered floor.
2. **AC-6 at 10,000 Entities and 24 h.** Delivered in #108, which merged while this pass was
   open. It's reviewed at the next §8 pass, with its two points in the feedback file.
3. **Unchanged from the 2026-09-24 pass:** the production digest line waits on #77 (`AW-INF-018`,
   this sprint) and #80 (SPRINT-03), and AC-9 has no carrier (with PM).

## Open questions

- **Resolved 2026-09-24 (§8, architecture): accepted.** Partitioned by Zone with the commands partitioner. Permanent, and the same reason
  everything else is Zone-partitioned.
- **Resolved 2026-09-24 (§8, architecture): accepted.** One projector process for all aggregate kinds, because the single-hash assertion needs
  one replica.
- **Resolved 2026-09-24 (§8, architecture): accepted.** Replica rather than Event fold, for the reason in Context. If a later story makes the
  sim internally event-sourced, the projector can drop the commands consumer without changing its
  output.

## Verification record — 2026-09-24 (implementation; merged in #64 on 2026-09-24, `review` until the §8 checklist passes)

PR [valesordev/andara.valesordev.com#64](https://github.com/valesordev/andara.valesordev.com/pull/64),
branch `claude/gallant-volta-q1hz5t`.

| AC | How | Result |
|----|-----|--------|
| 1 | `TestArrivalWritesTheCharacterAndTheRoom` (unit); `TestRun_FollowsTheLogAndDescribesTheWorld` (Redpanda) | pass |
| 2 | Every boundary verified inside `Engine.ReplayEach`. `TestIncrementalRecordsEqualADumpAtEveryTick` holds the records to a full dump after every tick of every verb; the Redpanda suite holds the topic to it | pass |
| 3 | `TestDivergenceHaltsBeforeTheTick` (unit); `TestRun_DivergenceExitsTwoAndCommitsNothingPastIt`: exit 2, committed tick T-1, counter 1 | pass |
| 4 | `TestRedeliveryIsByteIdentical`; `TestRun_RestartReplaysSilentlyToTheCheckpoint` (a restart behind its checkpoint adds no record) | pass |
| 5 | Zone-exit tombstone: `TestZoneExitTombstonesTheOldKey`, and on the topic in the Redpanda suite. *Not asserted:* absence after a forced compaction. Redpanda has no forced-compaction call the suite can make, so the suite reads the topic the way compaction converges (last value per key, tombstone deletes). `CharacterPurged` is `AW-SRV-032`'s inherited line | partial |
| 6 | `TestRun_RebuildFromTheRoundEqualsIncremental`: the log before the round is deleted, a from-zero rebuild then exits 3, and a round rebuild equals incremental and a dump. *Not measured:* the 10,000-Entity, 24 h scale | pass at fixture scale |
| 7 | Entity records carry the Entity's `content_version`. Room and Zone records carry `packID@version` from `content.Loader.ZoneVersions` (kafka source). *Gap:* a Character's is `""` because `server/sim/character.go`'s spawn instantiates with an empty version (`AW-SRV-014`'s code), and the dir source has no versions | partial |
| 8 | Structural: the projector is a separate process that reads the log and writes only `andara.state.v1`. Not asserted by a test | not asserted |
| 9 | Not enforceable: no SASL, no ACLs (see the AC's note). The write ACL is declared in `deploy/kafka/topics.yaml`. The client ID is `andara-projector-state`; a client ID is not a principal | carried |
| 10 | `TestRun_NewerStateVersionExitsFour`; `Engine.Replay` now returns `*sim.ErrStateVersion` for a boundary too | pass |

**Live, against the running server** (local Redpanda, `dir` content, the real `andara-server` and
`andara-projector` binaries, Commands written to the log): it bootstrapped from the server's own
round, verified every tick with lag ≈ 13 ms, and the topic held exactly the World across a bind,
same-Zone moves, a cross-Zone move, an unbind to dormant, and a rebind. A restart behind a newer
round took the dump-and-reconcile branch and tombstoned the stale `character:wilds/hero`. A restart
behind its checkpoint replayed silently. `--rebuild` wiped the group and rebuilt from the round. Two
bugs were found here and fixed with regression tests. Readiness required an empty read, which a
World ticking at 10 Hz never gives (`TestRun_ReadyWhileTheWorldKeepsTicking`). Skipped history
counted as caught up.

**Outstanding before `done`:** the §8 "verified against a real backend" check for traces (spans are
emitted but not yet checked in Tempo); `projectors.state.enabled` stays `false` until
`min.compaction.lag.ms` is applied to `andara.state.v1` on dev and prod (`topics-diff` names the
drift); the image with `andara-projector` in it is built by CI's `kind` workflow, and the sandbox this
was written in could not reach the Alpine mirror to build it locally.

### §8 pass (2026-09-24, architecture) — stays `review`

Against `origin/main` `033f2c6` and the compose stack, with the server image rebuilt from that
commit (#73).

**Verified live in this pass.** The record above had not checked traces, and had not named its
series. `andara-projector state` ran against the stack's Redpanda: no complete round (#74), so it
bootstrapped from offset zero, then replayed 18,760 ticks in 1.29 s and caught up at ≈ 12 ms lag.
- **Tempo:** `state.replay` roots and `state.verify` under service `andara-projector-state`.
- **Its registry:**
  - `andara_state_projector_tick` advancing
  - `andara_state_projector_lag_seconds` 0.012, against a budget of 5
  - `andara_state_records_produced_total{kind}`: character 21, room 30, zone 22
  - `andara_state_rebuild_duration_seconds{phase="bootstrap"|"replay"}`
  - `andara_state_digest_mismatches_total` 0
  - `andara_state_tombstones_total` 0
- **`andara_state_topic_bytes` reads 0** while the broker's log dirs report bytes on every
  Partition. It is a defect (feedback §1).
- **`andara.state.v1-value`** is now registered, `StateRecord` from `record.proto` (ADR-0007). It
  had stayed `pending`.

**Holds:**
- ACs 1–4 and 10, tested in `make test` and `make test-integration`.
- AC-8, accepted by construction: the server imports nothing of the projector, and the projector
  writes only `andara.state.v1`.
- Config in `keys.yaml`, the schema and the projector README.
- The glossary.
- The persisted format is derived and disposable.
- The depguard and no-own-`Apply` DoD lines, and `--rebuild` in CI.
- The three `[ASSUMPTION]`s are accepted and struck.

**Fails or is owed:**
1. **AC-7: Characters carry an empty `content_version`** (#70).
2. **AC-5 as amended above:** the forced-compaction assertion on a throwaway topic. `topics.py`
   never could force compaction.
3. **AC-6 at the stated scale:** 10,000 Entities and 24 h, timed against snapshot load plus tail
   replay. It holds at fixture scale only.
4. **The divergence must be sticky** (feedback §2, architecture's ruling). A restart that finds a
   round newer than an unresolved divergent tick bootstraps past it. The one piece of evidence the
   projector exists to produce is then gone within a snapshot interval.
5. **DoD, "the digest assertion runs continuously in production":** blocked on #77 (no target
   applies `min.compaction.lag.ms`) and #80 (the Deployment mounts no snapshot store, and there
   are no stop/rebuild targets).
6. **AC-9 has no carrier.** No story turns on SASL. This goes to PM.
7. The projection-freshness target stays `[NEEDS BRIAN]`.

**Architecture's review of the architecture-owned paths #64 wrote** (it merged from
`claude/gallant-volta-q1hz5t`, with no lane prefix, and none of it had been reviewed). Amended in
this pass:
- **`state-projector-diverged.md`: rejected and rewritten.** Its escalation test ("if it diverges
  again, the server is non-deterministic") cannot trigger once rounds are readable, because the
  restart skips the tick.
- **`StateProjectorDiverged`** resolved after the counter's one-minute life. It now holds for 6 h,
  with a test.
- **`ProjectionStale`** now counts only while the World ticks, as its SLO says, with a test. Both
  tests are mutation-checked.
- **`projection-freshness.md`** gained PromQL for both SLIs and lost the false "frozen" claim. It
  moved to Current in the SLO index.
- **`projection-stale.md`:** the exit-3 row names the right topic, and the unreachable "records
  flat" row became the bootstrap blind spot.
- **`record.proto` comments:** `source_offset` is a consumed position, `content_version` may be
  empty, and NPC is any non-ITEM Entity.
- **The protocol README layout.** The `topics.yaml` compaction-lag rationale. The `values.yaml`
  enablement blockers.

Everything else is accepted as written: `keys.yaml`, the schema, `_env.tpl`, the Makefile, the
Dockerfile, the glossary and the depguard rule. The committed 35 MB `andara-projector` binary is
#78.

### §8 owed items (2026-09-26, implementation): four of six delivered, the story stays `review`

On `impl/aw-srv-019-s8-owed`. The detail is in `docs/feedback/AW-SRV-019-state-projector.md`, under
"Implementation, 2026-09-26".

| Item | Result |
|------|--------|
| #78, the 35 MB binary | Untracked, and `/andara-*` is ignored |
| `andara_state_topic_bytes` reads 0 (feedback §1) | Fixed. `TopicBytes` named no partitions, and Redpanda answers that with nothing. It now names every partition, and a short answer is an error. The poll logs `warn` when the query starts failing and `info` when it recovers. Tests: `TestTopicBytesReportsWhatTheTopicHolds` (Redpanda), `TestTopicBytesReportLogsAChangeOfState` |
| Sticky divergence (feedback §2) | Delivered as ruled. The halt commits T−1 with the divergence in the offset metadata, and a start that finds it exits `2` whatever rounds exist. Only `--rebuild` clears it, with an `info` line. Tests: `TestRun_ADivergenceSurvivesARestartPastANewerRound` (Redpanda), `TestCheckpointMetaRoundTripsADivergence`. Mutation-checked |
| AC-7 after #93 | Holds. `TestCharacterRecordsNameTheirContentVersion`: every Character record carries `packID@version` |
| AC-5, forced compaction | **Not delivered: needs architecture.** This Redpanda (v25.1) could not be made to compact a throwaway topic from topic config. See feedback |
| AC-6 at 10,000 Entities and 24 h | **Not delivered this session.** Still owed by implementation |

### AC-6 at scale (2026-09-26, implementation)

On `impl/aw-srv-019-ac6-scale`. The detail is in the feedback file, under "AC-6 at scale, measured".
Measured with 25,000 Entities and 864,000 ticks (24 h at 10 Hz) of history, on a throwaway Redpanda.
`--rebuild` read the whole boundary Partition before the round, and that cost 6.7 s at 24 h.
`BoundaryReader.SeekAfter` now binary-searches to the round, so 24 h of history adds 0.13 s. At a
600-tick tail, `--rebuild` takes 42.7 s against an in-process snapshot load plus tail replay of
39.5 s. The difference is the dump and the broker. Tests: `TestRun_RebuildAtScaleIsBoundedByTheRoundAndTheTail` (opt-in,
`ANDARA_AC6_HISTORY_TICKS`), `TestBoundaryReader_SeekAfterStartsAtTheNextTick`,
`TestBoundaryReader_SeekAfterFallsBackToTheStart`. Both are mutation-checked. The story stays
`review`, for AC-5 (architecture) and the production line (#80).

`make check` is clean, and so is `make test-integration`.


### §8 pass (2026-09-27, architecture): stays `review`

Against `main` at `3d34212`. This is the pass the 2026-09-26 record deferred #108 to.

**AC-6 at scale: accepted, and AC-6 is amended to what it measures** (above). The two points
implementation raised:
- **The literal bound.** It can't be met, as implementation says. The overhead the old wording left
  unnamed is the dump and the broker, and neither scales with history. The property the bound
  existed for is the one `TestRun_RebuildAtScaleIsBoundedByTheRoundAndTheTail` asserts:
  - history adds less than a snapshot load plus the tail replay;
  - the rebuild never replays from zero while a round exists.

  The test defaults to a 10-tick tail, which separates history from run-to-run noise, and fails
  with `SeekAfter` removed. That's the right shape. It stays opt-in (`ANDARA_AC6_HISTORY_TICKS`):
  1.73M records don't belong in every CI run.
- **The tail replay rate.** 65 ms per tick at 25,000 Entities is slower than the 50 ms tick budget.
  A replica catches up at about 1.5× real time. That doesn't bind this story, whose Entities and
  budget are 10,000 and "under snapshot load + tail replay". It does bind `AW-SRV-007`, whose
  recovery replays the same tail under a 120 s target. At one snapshot interval (600 ticks) that's
  about 39 s, inside the target. A World that runs over budget can't be replayed faster than it
  ran. Sent to PM in the feedback file for `AW-SRV-007`'s grooming, with the `SeekAfter` question that
  story doesn't yet list.

**Still owed; the story stays `review`:**
1. **Implementation: AC-5's forced-compaction test.** It hasn't landed. The floor it needs has
   been on the stack since the 2026-09-26 pass.
2. **The production digest line** waits on #80 (SPRINT-03). #77's half was closed by `AW-INF-018`.
3. **AC-9 has no carrier.** Unchanged; it's with PM.

### §8 instrumentation check (2026-09-28, SRE): emitting; one backend owed; #143 found

On `sre/sprint-03-carryover-verify`. The binary is built from `main` at `0819105`. It ran against
the running compose stack, whose server is `478d547`; `git diff 478d547 0819105` is empty over
`server internal cmd gen testdata`. CLAUDE.md §8's instrumentation line, per signal:

| Signal | Backend | Observed |
|--------|---------|----------|
| `andara_state_projector_lag_seconds` / `_lag_budget_seconds` | the process's `/metrics` | 0.0116 against 5, following live after catch-up |
| `andara_state_projector_tick` | `/metrics` | 1,391,527 and advancing with the World |
| `andara_state_records_produced_total{kind}` | `/metrics` | character 33, room 40, zone 30; `item` and `npc` pre-seeded at 0. Bounded, as specified |
| `andara_state_tombstones_total` | `/metrics` | 15, the stale keys a from-zero bootstrap tombstoned |
| `andara_state_topic_bytes` | `/metrics` | 19,310. **Non-zero**: #103's fix holds live |
| `andara_state_rebuild_duration_seconds{phase}` | `/metrics` | `bootstrap` 0.04 s, `replay` 107.3 s for 1,391,496 ticks |
| `andara_state_digest_mismatches_total` | `/metrics` | 1 on each round bootstrap (below), with `/metrics` held 60 s, then exit `2`. 0 on the from-zero run |
| Spans `state.replay` → `state.verify` | local Tempo | `state.replay` roots with `state.verify` children, service `andara-projector-state` (e.g. trace `22e42828de641a8c…`) |
| Logs | local Loki | `{service_name="andara-projector-state"} |= "diverged"` returns the `error` line with `tick`, both hashes, and `last_good_offsets`: the runbook's query works as written. `service`, `env` and `tick` are on every line. The start line carries `round_tick` |

**Not yet observed on a real Prometheus.** The compose stack has no projector scrape job, and no
environment runs the projector. So the series above were read from the process's own registry,
not from Prometheus or Grafana Cloud. **Inherited Definition-of-done line, carried by
`AW-INF-025`:** the projector's series are scraped under `job="andara-projector-state"` on `dev`
and queried from Grafana Cloud. That's also `AW-INF-008` AC-2.

**Found: #143.** Bootstrapped from a snapshot round, the projector diverged at round + 1, twice:
- rounds 1,387,750 and 1,389,553;
- the first divergent tick applied no Commands.

From offset zero, the same binary verified every tick through 1,391,496. So apply is
deterministic, and a round, or its restore, doesn't reproduce hashed state. That contradicts
AC-6's property, "`--rebuild` from round equals incremental", on a live 38-hour World. The fixture
test passes. It's filed for implementation, and PM triages it. For architecture's §8: whether #143
reopens AC-6 is yours to rule. It will fail `AW-INF-025` AC-4 as written, and it bears on
`AW-SRV-007`.

The instrumentation item is **satisfied**, except for the Prometheus scrape carried above.

### AC-5's forced-compaction test (2026-09-28, implementation)

On `impl/aw-srv-019-ac5-compaction`, as the 2026-09-26 §8 pass ruled it (option (a), the stack's
`log_segment_ms_min=1000`). `TestRun_ATombstonedKeyIsCompactedAway` (Redpanda):
- The projector itself runs the script into a throwaway state topic at the broker's default
  `segment.ms`. `andara.state.v1`'s settings are untouched.
- A raw read of town's Partition then shows `character:town/hero`'s values and its one tombstone.
  At the default `segment.ms` the segment holding the tombstone can't roll, so it can't be compacted.
- Only then is the topic altered to `segment.ms=1000`, `min.cleanable.dirty.ratio=0.01` and
  `delete.retention.ms=1000`. Declaring them at creation, as the first push did, let a slow
  projection roll and clean the tombstone before the test looked for it (Codex on #147).
- Then each poll appends a filler record to that Partition and reads the whole Partition raw,
  to a 90 s deadline (`live-assertions.md`). It passes once no record with the key remains,
  tombstone included.
- Last, the compacted topic still equals a dump of the World, so compaction removed only the key.

Measured on the stack's Redpanda v25.1.10: it passes in about 22 s, and it passed 3 of 3 runs back to back.
**Mutation-checked:** without the alteration, the test fails at 90 s. Its failure
message names the four records still there (three values and the tombstone).

`make check` is clean, and so is the projector's integration suite. Nothing more on AC-5 is owed by
implementation. The story stays `review` for the production digest line (#80), AC-9's carrier (PM), and
whatever architecture rules on #143 (SRE's finding above).

### §8 pass (2026-09-29, architecture): stays `review` on AC-6 (#143) and AC-9

Against `main` at `c25a789`. SRE's instrumentation check (2026-09-28) is accepted: every series is
emitting from the process's registry, and the scrape from a real Prometheus is carried by
`AW-INF-025` as an inherited line. `AW-INF-025`'s Definition of done names it.

**Now holds:**
- **AC-5.** `TestRun_ATombstonedKeyIsCompactedAway` is on `main` (#147) and runs in `stack`'s
  `make test-integration`. It passed in 14.3 s on run 36632695351, for #147's merge `0374624`. The
  implementation record's mutation check and 3-of-3 runs stand. Nothing more is owed on AC-5.

**Ruling: #143 reopens AC-6.** AC-6's property is that `--rebuild` from a round reaches a state
equal to the incremental projector's. #143 shows that property failing on a 38-hour World: the
first tick after the round, an idle one, hashes differently. A from-zero replay of the same log
verifies, so apply is deterministic and the fault is in what a round captures or restores. The
fixture test `TestRun_RebuildFromTheRoundEqualsIncremental` passes, because the fixture World
doesn't hold the state that differs. So the 2026-09-27 acceptance of AC-6 is withdrawn, and AC-6 is
marked above.

AC-6 passes again when #143's fix lands with:
- **a regression test that fails on the code before the fix,** by building the state the live
  World had and the fixture lacks, taking a round, and verifying round tick + 1 onward. Mutation-
  checked like AC-5;
- **the fix in the round, or its restore, not in the projector.** The server's recovery
  (`AW-SRV-006`) restores the same rounds. A projector-only fix would leave `AW-SRV-007` and M2's
  "matching State Hash" gate exposed.

`AW-INF-025` AC-4, as amended on 2026-09-29, is the in-cluster observation of the same fix.

**Still owed; the story stays `review`:**
1. **AC-6: #143** (implementation). With PM, as a risk to `AW-INF-025` and M2
   (`docs/feedback/AW-INF-025-projector-operations.md`).
2. **AC-9's carrier.** PM's 2026-09-25 question in the feedback file is architecture's and is
   unanswered: no ADR decides broker authentication, so PM can't groom the story that inherits
   AC-9. The write ACL in `deploy/kafka/topics.yaml` is declared and enforced nowhere. Architecture
   owes that ADR.
3. **The production digest line:** carried by `AW-INF-025`'s Definition of done, not owed here.
   It no longer holds this story in `review` once 1 and 2 close.

### §8 check (2026-09-30, architecture): stays `review` on AC-6 (#143)

Against `main` at `79fd622`. Nothing that touches this story's code has merged since the 2026-09-29
pass, and #143 is still open, so **AC-6 is still owed** on the terms set out there.

The pass's item 2 has moved on: architecture answered PM's question with `ADR-0011`, which Brian
accepted on 2026-09-29. AC-9's carrier is now PM's to groom (the SRE authentication story, with AC-9
as its inherited Definition-of-done line; `docs/feedback/AW-SRV-019-state-projector.md`). Once that
story is written, AC-9 stops holding this story, per CLAUDE.md §8. From then on, #143 is the only
thing keeping it in `review`.

### #143 fixed (2026-10-01, implementation)

On `impl/aw-srv-019-restore-default-seed`. The ruling's terms (§8 pass, 2026-09-29): the fix is in
the round's restore, and a regression test fails on the code before it.

**Cause.** The seed is in the State Hash. The round doesn't carry it, so a restore takes it from
`sim.seed`. With `sim.seed` at its default of 0, `NewEngine` derives the seed from the World it's
given:
- The server's Engine starts from `EmptyWorld()` (AW-SRV-012), so its seed is
  `DeriveSeed(EmptyWorld)`.
- `RestoreEngine` passes the round's content World, so it derived a different seed.

Every bootstrapped restore therefore hashed differently from the round's tick onward. The
projector verifies nothing before round + 1, so that's where it showed, idle or not. A from-zero
replay starts from `EmptyWorld()` as the server does, so it verified. Every round test pinned a
seed, which is the "state the fixture lacks".

**Fix.** `sim.RestoreEngine`: with `cfg.Seed == 0`, it uses `DeriveSeed(EmptyWorld())`, the seed
the World started with. A configured seed is unchanged. The fix is in the restore, so AW-SRV-007's
recovery gets it too: it's the same function.

| Test | What it shows | Without the fix |
|------|---------------|-----------------|
| `sim` `TestRestore_DefaultSeedIsTheOneTheWorldStartedWith` | An Engine built as the server builds it (empty, seed 0, content by swap, two Characters bound). Its round goes through the snapshot codec, and `RestoreEngine` restores it as the projector does. The restored seed and hash equal the live World's at the round's tick, and at the idle tick after it. A pinned seed restores as itself | fails: restored seed `16673930416586794289`, the World's `16406829232824261652`, and the hashes differ at the round's tick |
| `projector` suite, `seed` now 0 (sim.seed's default, as `dev` runs) | `TestRun_RebuildFromTheRoundEqualsIncremental` (Redpanda) bootstraps `--rebuild` from a round over a log truncated before it, verifies every tick after the round, and reaches the incremental projector's records | `TestRun_RebuildFromTheRoundEqualsIncremental` fails |

`make check` passes. #172's stall-budget flake failed once under load and passed on the rerun.

**Still owed:** AW-INF-025 AC-4 on `dev`, the in-cluster observation, is SRE's. `dev`'s
checkpoint still records the divergence at tick 132838. Once a projector with this fix is
deployed, it needs `make projector-rebuild ENV=dev` before it starts.
