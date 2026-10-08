# Kafka client contract

> **Status: decided 2026-10-08, `AW-INF-005`.** Asserted at runtime by `andara-server config-assert`
> (implementation, a child of `AW-INF-005`). Companion to `broker-contract.md`.

Every Kafka client in the server and projector is to take its options from one shared
constructor (ADR-0011 §7; **not built yet**: today each site calls `kgo.NewClient` itself). This contract is what that constructor must produce. It was written against the code as
of 2026-10-08, and where the first draft of the story disagreed with the code the disagreement is
resolved below, not left for the implementer.

## Producers

Applies to ingress, events, state, content, accounts and audit producers.

| Setting | Value | Claim it carries | In the code today |
|---|---|---|---|
| `acks` | `all` | RPO: "ordered" means the ISR has it | set (`kgo.AllISRAcks()`) |
| `enable.idempotence` | `true` | An ambiguous retry must not duplicate a record | franz-go default on; `DisableIdempotentWrite()` turns it off, so `config-assert` must assert it |
| `max.in.flight.requests.per.connection` | `<= 5` | Order within a Partition under retry | pinned to 5 by franz-go for an idempotent producer; **not settable**, so `config-assert` reports the library's pinned value and does not set it (`AW-SRV-010`, 2026-09-19) |
| `delivery.timeout.ms` | `ingress.produce_deadline` (ingress producer only) | Bounds the ambiguity window of `AW-SRV-010` AC-5 | set (`RecordDeliveryTimeout`) |
| partitioner | explicit `hash(ZoneID) % 64` on the 64-Partition ZoneID-keyed topics (commands, events, state) | Order: a library upgrade must not move a Zone to another Partition | `ManualPartitioner` on the ingress, tick-loop and projector producers. The `recordlog` producers (accounts, audit, content; 6 Partitions, compacted or keyed by actor) use the library's default key partitioner: **an open gap**, since a library upgrade could move a key and split its compaction history. The `config-assert` child pins the default partitioner there or records why it need not |
| `compression.type` | `zstd` | Disk and network cost only. No correctness claim; `config-assert` reports a deviation as a warning, not a failure (AC-4 lists the failing settings and compression is not among them). | **not set**; the library default applies. The `config-assert` child sets it |

## Consumers

The server, projectors and recovery **do not use consumer groups**. They assign Partitions directly and
take their starting offset from the Snapshot round or the projector checkpoint (ADR-0002 §4, "recovery
resumes strictly from the checkpointed offset"). The first draft's `enable.auto.commit=false` and
`auto.offset.reset=none` described group consumers and are replaced by the rows below. The group names in
`topics.yaml` are reserved for ACL prefixes (ADR-0011) and for a consumer that is later given a group.

| Setting | Value | Claim it carries | In the code today |
|---|---|---|---|
| `isolation.level` | `read_committed` | Nothing reads an aborted transactional record. No producer is transactional today, so this is a guard, not a fix: it is cheap now and a silent bug later | **not set**; franz-go's default is `read_uncommitted`. The `config-assert` child sets it |
| group membership | none; direct Partition assignment | Position is owned by the checkpoint, not by the broker | holds |
| committed offsets in Kafka | none | A second source of "where was I" is a second way to be wrong | holds |
| start offset | a resumed position is always explicit: `At(offset)` from the checkpoint. Never `AtStart`/`AtEnd` to resume | Replay is exact | holds for the resuming readers; the named one-shot scans below are the exceptions |
| an offset the broker no longer has | `ErrLogGap` (exit `3`), never a silent reset to earliest or latest | A gap is lost history; skipping it is silent divergence | holds on the `At(offset)` readers |

**The permitted exceptions are named one-shot scans**, which read for content and not to resume a
position. A reader not in this table that starts at `AtStart` or `AtEnd` is a contract violation:

| Reader | Start | Why |
|---|---|---|
| accounts, content blobs, content versions (`recordlog`, `content`) | `AtStart` | Compacted: reading from the start is how the latest value per key is found. |
| content active pointer (watch) | `AtEnd` after an initial read | Changes after the initial read are the point. |
| audit replay (`recordlog` on `andara.audit.v1`, client `andara-server-replay`) | `AtStart` | A bounded replay of a `delete`-policy topic to rebuild the in-memory audit view; no position is resumed. |
| Tick Boundary scan (`tickloop`, `andara.events.v1` Partition 0) | `AtStart` | A one-shot scan for the Tick Boundary Records recovery needs; `ErrLogGap` still applies to the round it looks for. |

`config-assert` knows these by reader name; adding a reader here is a contract change.

## `client.id`

**`client.id` is the principal's name plus an optional purpose suffix: `andara-server`,
`andara-server-recovery`, `andara-projector-state`, `andara-projector-state-commit`.** No environment and
no pod. The rule is `^<principal>(-[a-z]+)*$`, with `<principal>` from ADR-0011 §3's table (`andara-server`, `andara-projector-state`, `andara-projection-redis`, `andara-projection-pg`, `andara-operator`); the suffixes in use today (`-ingress`, `-tick`, `-replay`, `-commit` and so on) all fit. A client with **no** `ClientID` (the tick-loop scan and reader clients in `server/tickloop/kafka.go`) is a violation `config-assert` reports, and the constructor fixes. This replaces the first draft's `andara-<component>-<env>-<pod>`, for two reasons:

1. ADR-0011 §3 names principals without the environment, because each environment has its own
   cluster. The environment in the ID carries nothing the broker does not already know.
2. Brokers label per-client metrics and quotas by `client.id`. A pod name in it makes a new series per
   restart.

A pod is identified in the server's own logs and metrics labels from the platform, not through the Kafka client.

## Degraded state is per Partition

*This is the amendment to `AW-SRV-010` that `AW-INF-005`'s Definition of done requires. It resolves
the blind spot found in the review of PR #34 (2026-09-19).*

`AW-SRV-010` degrades the whole World when a broker `Ping` fails. That misses the failures that matter
under `min.insync.replicas=2`: with one of three brokers down, the others still answer `Ping`, but a
Partition whose ISR has fallen below 2 refuses writes with `NOT_ENOUGH_REPLICAS`. Every Submit for that
Zone waits the full `ingress.produce_deadline`, returns `DEADLINE_EXCEEDED`, the gauge stays 0 and
`WorldReadOnly` never fires. The mode is wrong in the other direction too: a Partition that is healthy
should keep accepting Commands while another is not.

**The unit of degradation is the Partition** (64 on `andara.commands.v1`). A Command's Partition is
`hash(ZoneID) % 64` from the actor's Zone, so a degraded Partition makes exactly the Zones on it
read-only.

### Entering the state

A Partition `p` becomes degraded when either:

1. **A produce to `p` fails after the producer's own retries, with a broker-side retriable error:**
   `NOT_ENOUGH_REPLICAS`, `NOT_ENOUGH_REPLICAS_AFTER_APPEND`, `LEADER_NOT_AVAILABLE`, or
   `NOT_LEADER_OR_FOLLOWER`, or `REQUEST_TIMED_OUT` (what a Partition led by a surviving broker returns while its dead followers are still in the ISR). A transient single error that the idempotent producer retries
   successfully inside the deadline is not a failure and degrades nothing. **How the error is seen is the implementer's to solve**: franz-go retries these errors until `RecordDeliveryTimeout` and then completes the promise with `ErrRecordTimeout`, not the broker error, so the implementation must capture the broker's per-Partition error another way (a client hook, for example). If it cannot, the probe below is the only trigger and this one is dropped, with the Submit that discovers the fault answered `DEADLINE_EXCEEDED` as now. (`NOT_ENOUGH_REPLICAS_AFTER_APPEND`
   is added to the three the review named: the record *was* appended, so its Submit is ambiguous in the
   same way a deadline is.) Or
2. **The probe finds it.** Once per probe interval (the producer's existing `ProbeInterval`, one second) the ingress
   reads topic metadata for `andara.commands.v1` and marks `p` degraded if its leader has been absent on **two consecutive probes**, or its
   in-sync replica count is below the topic's `min.insync.replicas`. The probe asks the broker the
   configuration, once at boot and every 60 s, and does not hard-code 2. It does **not** ping a broker:
   a reachable broker says nothing about a Partition.

If the metadata request itself fails (no broker answers) on **two consecutive probes**, all 64 Partitions are degraded; one failed probe marks nothing, so a single transient failure inside an election cannot undo the grace above.

### What a Submit sees

A Submit whose actor's Partition is degraded returns `UNAVAILABLE`, reason `world_read_only`, at once,
with nothing produced and the wording Brian ruled on 2026-09-19. The error does not name the Partition;
the reason and the message are unchanged, so no client changes. The Submit that discovers the failure
by its own produce is `DEADLINE_EXCEEDED` (outcome unknown), exactly as `AW-SRV-010` AC-5 says now.

**Interaction with `AW-INF-005` AC-1** (one broker killed, `andara_ingress_degraded = 0`): killing a
broker that leads Partitions causes an election. The two-probe rule lets an election that settles in
about two seconds pass unmarked; one that does not settle is a real outage of those Zones. The
rehearsal asserts that the gauge is 0 *after* the election settles and that `WorldReadOnly` does not fire,
not that it never reads 1 in between. An ISR below `min.insync.replicas` is marked at the first probe
that sees it, because the broker is already refusing writes.

**Timing this delivers against `AW-INF-005` AC-2** ("read-only within `ingress.produce_deadline`"): the
Submit in flight when the fault begins is `DEADLINE_EXCEEDED` at the deadline. Later Submits for a
Partition with no leader are `UNAVAILABLE` within two probe intervals (about 2 s) of the leader's loss.
For a Partition led by a survivor whose dead followers remain in the ISR, metadata still shows a full ISR
until the broker shrinks it (`replica.lag.time.max.ms`, 30 s by default) and the probe cannot see the
fault; only the produce-error trigger can, which is why `REQUEST_TIMED_OUT` is in it. If that error is
unobservable, such a Partition stays unmarked for up to 30 s, every Submit to it is `DEADLINE_EXCEEDED`,
and `WorldReadOnly` still fires through the other Partitions. AC-2's rehearsal is therefore amended to:
the World is read-only for every Partition within the deadline plus two probe intervals *or*, where the
trigger is unobservable, within `replica.lag.time.max.ms` plus a probe interval, and the rehearsal records
which. The SRE child owns the wording of that AC.

### Leaving the state

A Partition leaves the degraded state on the first probe that finds a leader and an ISR of at least
`min.insync.replicas`. No restart, no operator action.

### Constraints on the implementation

- **Degrading one Partition must not fail, delay or drop an in-flight produce to any other.** If the
  mechanism that keeps a record from landing after its Submit was refused is a producer swap, as it is
  today, it may not be a swap of the client that carries the other 63 Partitions' records.
- A record whose Submit was answered `UNAVAILABLE` never lands. A record whose Submit was answered
  `DEADLINE_EXCEEDED` may land only until its deadline, and never after it. Both are unchanged from
  `AW-SRV-010`; they are restated because a per-Partition mechanism is where they would be lost.
- Entering and leaving log at `info` with `partition` and the cause (`produce_error` or `probe`, and the
  error name or `leader_absent` / `isr_below_min`).

### Metrics

| Metric | Type | Labels | Cardinality |
|---|---|---|---|
| `andara_ingress_degraded` | gauge, 0 or 1 | `partition` | 64 |

The probe publishes all 64 series from boot, 0 when healthy, so a missing series is a missing server.
`max(andara_ingress_degraded) == 1` is the alert's expression unchanged in shape, and fires if any
Partition is degraded. `sum(andara_ingress_degraded)` tells the runbook which case it is: 64 is the log
being unreachable, a few is a Partition-level fault (an under-replicated Partition, a leader election
that did not settle), and the runbook's mitigation differs.

Consumers of the old unlabeled gauge, to be re-pointed by their stories: `world-write-availability.md`
(its SLI averages the gauge and needs an aggregation over `partition`), the `WorldReadOnly` alert, the
runbook `world-read-only.md`, and the compose dashboards.

### Acceptance criteria for the implementing story

Each of these is a criterion in the per-Partition implementation story, named here so they are not
softened when it is written:

1. A broker-side produce error of `NOT_ENOUGH_REPLICAS` marks only that Partition degraded. Likewise
   `NOT_ENOUGH_REPLICAS_AFTER_APPEND`, `LEADER_NOT_AVAILABLE`, `NOT_LEADER_OR_FOLLOWER`; one test per error,
   at the point the implementation observes it (see Entering the state). A story that finds the error
   unobservable says so in a comment `--to architecture` rather than dropping the criterion.
2. With a Partition degraded, a Submit for a Zone on a different Partition is produced and acknowledged.
3. A leader absent on one probe marks nothing; on two consecutive probes it marks the Partition.
4. The probe marks a Partition degraded with the ISR below `min.insync.replicas` while every broker
   answers a `Ping`, and clears it on recovery without a restart.
5. All 64 series are present at boot, 0.
6. A metadata failure on two consecutive probes degrades all 64; one failure degrades none.
7. The topic's `min.insync.replicas` is read at boot and refreshed every 60 s; a change is honoured within 60 s.
