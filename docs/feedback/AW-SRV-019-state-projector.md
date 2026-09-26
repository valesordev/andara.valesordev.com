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

