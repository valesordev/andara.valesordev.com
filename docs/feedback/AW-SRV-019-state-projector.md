# Feedback — AW-SRV-019 State projector and compacted state topic

Spec: `docs/stories/AW-SRV-019-state-projector-and-compacted-state-topic.md`
Raised: 2026-09-24, architecture, at the §8 review on `arch/sprint-01-batch-3-review`.

PR #64 changed this story's contract after `ready`: the Zone-qualified keys (`7aba5c6`) and
AC-5/6/9 plus the Observability section, attributed to Brian's call. CLAUDE.md §6 wants that
recorded here once implementation has started. It was not, so this file is the record. The key
change is sound (a key without its Zone would sit on two Partitions and never compact), and
architecture accepts it.

## For implementation

### 1. `andara_state_topic_bytes` reads 0, silently

`cmd/andara-projector/main.go` `topicBytes` polls every 30 s and drops the error. Against the local
Redpanda, the gauge stayed 0 for minutes, with 73 records on the topic and
`rpk cluster logdirs describe` reporting bytes per Partition. Log the failure at `warn`, once per
state change, and make the query work against Redpanda and Kafka both. The runbook's row 4 depends
on this gauge.

### 2. A divergence must survive the restart (architecture's ruling; a contract change)

`run.go` bootstraps from the newest complete round when it is newer than the checkpoint. After a
divergence at tick *T*, the checkpoint is *T−1*. The next round is at most `snapshot.interval`
away, so the restart dumps, reconciles and continues past *T*. The divergence, which is the
evidence this projector exists to produce, is gone, and a clean run afterwards proves nothing.

**Ruling:**
- The checkpoint the projector commits on halting records the divergent tick. Commit metadata on
  the consumer group is the natural place.
- A start that finds an unresolved divergence in its checkpoint exits `2` again, naming the tick
  and both hashes from the record, **whatever rounds exist**.
- Only `--rebuild` clears it. That is the operator's explicit acknowledgment, and its `info` line
  says it discarded an unresolved divergence at *T*.

Add a test: diverge, write a newer round, restart, and assert exit `2` with the same tick.
`state-projector-diverged.md` is written for today's behavior, and architecture updates it when
this lands.

### 3. AC-5 and AC-6: the assertions owed

- **AC-5 (amended):** produce the projector's tombstone to a throwaway compacted topic with
  `segment.ms`, `min.cleanable.dirty.ratio` and `delete.retention.ms` lowered. Then poll a full
  read of that topic to a deadline until the key is gone (`live-assertions.md`).
- **AC-6:** time `--rebuild` against a 10,000-Entity World with 24 h of history, and compare it
  with snapshot load plus tail replay. The sizing fixture (`server/simtest`) is the World. If a
  24 h log cannot be generated in CI time, say how it was measured and record the numbers in the
  story, as `AW-SRV-006` did.

### 4. AC-7: #70

Characters are spawned with an empty `content_version`. The issue says what the fix must decide
about replay.

## For PM

### 5. AC-9 has no carrier

The story says "the first story that turns on SASL applies it and inherits AC-9's assertion". No
such story exists, and `AW-INF-014` puts SASL out of scope. The broker authenticates nobody, so
the write ACL declared in `topics.yaml` is decoration until one does. Needs a story, a Kafka
authentication and ACL story in the `AW-INF` series, that carries this AC as an inherited line.

### 6. Enabling the projector anywhere waits on #77 and #80

Both are architecture's issues. They need triage into a sprint before this story's "runs
continuously in production" line can close.

## PM, 2026-09-25 (SPRINT-01 close-out)

- **§5, AC-9's carrier:** no ADR covers broker authentication (SASL mechanism, SCRAM vs mTLS,
  where credentials live). ADR-0002 and `AW-INF-014` both leave it out. PM can't write the story
  until architecture decides the mechanism, by ADR or by amending ADR-0002. **Question for
  architecture:** which mechanism, and under which ADR? Once that's answered, PM grooms the
  `AW-INF` authentication and ACL story, with AC-9 as its inherited line.
- **§6, #77 and #80:** #77 is groomed as `AW-INF-018` in SPRINT-02. #80 waits for SPRINT-03,
  because its volume half needs `AW-INF-007`'s snapshot-store decision (`s3` in-cluster, or a
  read-only share).
- **#70** (AC-7) was fixed in #93. AC-7 needs re-checking at this story's next §8.
- **#78** (the 35 MB binary): assigned to implementation in SPRINT-02, alongside this story's
  owed items.

## Implementation, 2026-09-26

On `impl/aw-srv-019-s8-owed`.

- **§1, delivered.** The cause was `kadm.TopicsSet{topic: nil}`: an empty partition set asks for no
  partitions, and Redpanda answers it with nothing and no error. `TopicBytes` now lists the
  topic's partitions and names them. A describe that covers fewer partitions than the topic has is
  an error. The poll logs `warn` when the query starts failing and `info` when it recovers, and
  keeps the last good value in between.
- **§2, delivered as ruled.** The halt commits `tick=<T−1>;diverged=<T>:<recorded>:<replayed>` as
  offset metadata. A start that reads it exits `2` before bootstrap, naming T and both hashes, and
  counts the mismatch, so `StateProjectorDiverged` fires again. `--rebuild` logs `--rebuild
  discards an unresolved divergence` with the tick, then wipes the group.
  `state-projector-diverged.md` is architecture's to update.
- **§4 (AC-7), holds after #93.** `TestCharacterRecordsNameTheirContentVersion`.
- **#78, delivered.**

### For architecture: AC-5's forced compaction needs broker config, not topic config

I tried the amended AC-5 against the stack's Redpanda v25.1.10. The projector's tombstone for
`character:town/hero` lands on a throwaway compacted topic, but no topic-level setting got the key
compacted away. In 3 minutes of polling, the value records and the tombstone all stayed:
- `segment.ms` is floored by the cluster's `log_segment_ms_min` (600000 ms), so the active segment
  doesn't roll inside a test.
- `segment.bytes=1024` did not roll a compacted topic's segment either: 4.5 KB of records sat in
  one `0-1-v1.log`. Redpanda sizes compacted segments with `compacted_log_segment_size` (256 MiB
  here). `segment.bytes=1` stalls produce outright.
- `min.cleanable.dirty.ratio=0` is stored as `-1`, so the test would need `0.01`.

The active segment is never compacted, so nothing here can ever satisfy the assertion. Forcing it
needs one of these, all architecture's:
- (a) the compose Redpanda run with `log_segment_ms_min` (and `log_segment_ms`) lowered, or
  `compacted_log_segment_size` small
- (b) a `make` target that sets those cluster properties for the integration run and restores them
- (c) an amended AC-5 that asserts the tombstone and the compacted-topic config, and leaves
  convergence to the broker

The test I wrote is not on the branch, because it can't pass on this broker config. It's ready to
restore once (a) or (b) exists: a 1 KiB-segment topic, a filler behind the tombstone, and a full
read polled to a deadline.

### Still owed by implementation: AC-6 at scale

The 10,000-Entity, 24 h `--rebuild` timing (§3) was not done this session. At 10 Hz, 24 h is
864,000 boundaries. Written to the shared stack's Redpanda, that's roughly 0.5 GB of boundary
records alone. It either gets its own throwaway broker or is measured in-process, and the record
has to say which.

## Implementation, 2026-09-26: AC-6 at scale, measured

On `impl/aw-srv-019-ac6-scale`. `TestRun_RebuildAtScaleIsBoundedByTheRoundAndTheTail` is the
measurement. It is skipped unless `ANDARA_AC6_HISTORY_TICKS` is set, so CI doesn't run it.

**How it was measured.** A throwaway Redpanda v25.1.10 container (`--smp 2 --memory 6G`,
`topic_memory_per_partition` lowered so six 64-partition topics fit), not the shared stack's.
- **World:** the sizing fixture, 25,000 Entities (more than the story's 10,000) across 16 Zones,
  brought into effect by a genesis swap. It is restored at tick 864,000, as if it had run 24 h.
- **History:** 864,000 ticks of filler ahead of it on the boundary Partition, an Event and a
  boundary per tick (1.73M records).
- **Round and tail:** a round, then a tail of 600 ticks (one snapshot interval), with Characters
  moving.
- **Comparison:** `--rebuild` over those topics against the same World mirrored with no history,
  and against snapshot load plus tail replay in process. Both rebuilds produce records equal to a
  dump of the live World.

**What it found.** The boundary reader always read from offset 0. After bootstrapping from a
round it read, and discarded, every boundary and Event before it. At 24 h that cost **6.7 s**, and
it grows linearly with retention. The fix is `BoundaryReader.SeekAfter`:
- A binary search over the boundary Partition: boundary ticks rise with the offset, so it takes
  about 21 probes at 1.73M records.
- It positions the reader at the round's tick + 1 before the first read.
- If the first boundary delivered is later than tick + 1 (boundaries out of tick order), the
  reader falls back to the start, which is what it did before.
- It reads the log only. `taken_at_unix_nano` would have been the obvious index, but
  `snapshot.proto` says it is never read by recovery logic.

| Run (864,000 ticks of history) | No history | With history |
|---|---|---|
| 10-tick tail, no seek | 1.51 s | **8.24 s** (fails the test) |
| 10-tick tail, seek | 1.50 s | **1.71 s** (+0.13 s for the probes) |
| 600-tick tail, seek | 38.99 s | 42.70 s |

In process, at 600 ticks: snapshot load 0.12 s + tail replay 39.3 s = 39.5 s.

Two things for architecture's §8:
- **The strict AC-6 bound isn't met, and can't be.** `--rebuild` does the snapshot load and the
  tail replay, *plus* the dump and the broker round trips, so it is always somewhat over their
  sum: 1.5 s against 0.77 s at a 10-tick tail. What the test asserts is the property the bound
  exists for: history adds less than snapshot load + tail replay, and no from-zero replay happens
  (the existing `TestRun_RebuildFromTheRoundEqualsIncremental` deletes the pre-round log). If you
  want a literal bound, name the overhead it allows.
- **At 600 ticks the tail replay dominates:** about 65 ms per tick at 25,000 Entities, so a replica
  catches up at roughly 1.5× real time. The 600-tick runs vary by seconds from run to run, which
  is why the short-tail runs are the ones that separate history from noise. The test defaults
  to a 10-tick tail for that reason (review of #108): with the seek removed it fails.
  `ANDARA_AC6_TAIL_TICKS=600` reproduces the 600-tick row.

`AW-SRV-007` has the same scan in `tickloop.Recover` (its open question). `SeekAfter` is the
projector's and isn't shared. Whether recovery reuses it is `AW-SRV-007`'s call.


## Architecture, 2026-09-26: §8 pass on #103

The story stays `review`. The record is in the story under "§8 pass (2026-09-26)".

### For implementation: AC-5 is (a), and the test can come back

The stack's Redpanda now runs with `log_segment_ms_min=1000`. It's declared in
`deploy/kafka/topics.yaml` under `broker.local`, and `make up` / `make topics-apply` apply it, so it
reaches existing volumes and CI.

Measured before ruling, on a throwaway compacted topic with `segment.ms=1000`,
`min.cleanable.dirty.ratio=0.01` and `delete.retention.ms=1000`, and a filler record every 5 s:
- the key's value records were compacted away by 10 s;
- the tombstone was gone by 21 s.

What the restored test needs:
- **A filler record on each poll iteration.** Without new writes the active segment has nothing
  to roll behind it.
- **Poll a full read to about 60 s**, per `live-assertions.md`.
- **`0.01` for the dirty ratio.** You found that `0` is stored as `-1`.

`compacted_log_segment_size` doesn't need to change. The time roll is what closes the segment.

### For implementation: still owed
- The AC-5 test.

AC-6 at scale merged in #108 while this pass was open. Its two points for architecture, the
literal bound and the tail replay rate, are taken at the story's next §8 pass.

### Architecture's, done
- `state-projector-diverged.md` is rewritten for the sticky divergence.
