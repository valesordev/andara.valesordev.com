# Kafka client contract

> **Status: decided 2026-10-08, `AW-INF-005`.** Asserted at runtime by `andara-server config-assert`
> (implementation, a child of `AW-INF-005`). Companion to `broker-contract.md`.

Every Kafka client in the server and projector is to take its options from one shared
constructor (ADR-0011 §7; **not built yet**: today each site calls `kgo.NewClient` itself, each described to `config-assert` as a `kafkaclient.Site`). This contract is what that constructor must produce. It was written against the code as
of 2026-10-08, and where the first draft of the story disagreed with the code the disagreement is
resolved below, not left for the implementer.

## Producers

Applies to ingress, events, state, content, accounts and audit producers.

| Setting | Value | Claim it carries | In the code today |
|---|---|---|---|
| `acks` | `all` | RPO: "ordered" means the ISR has it | set (`kgo.AllISRAcks()`) |
| `enable.idempotence` | `true` | An ambiguous retry must not duplicate a record | franz-go default on; `DisableIdempotentWrite()` turns it off, so `config-assert` must assert it |
| `max.in.flight.requests.per.connection` | `<= 5` | Order within a Partition under retry | franz-go's own value for an idempotent producer; **not settable**, so `config-assert` reports the library's configured value (1 today, 2026-10-09) and fails only above 5 (`AW-SRV-010`, 2026-09-19; `AW-SRV-053`) |
| `delivery.timeout.ms` | `ingress.produce_deadline` (ingress producer only) | Bounds the ambiguity window of `AW-SRV-010` AC-5 | set (`RecordDeliveryTimeout`) |
| partitioner | explicit `hash(ZoneID) % 64` on the 64-Partition ZoneID-keyed topics (commands, events, state) | Order: a library upgrade must not move a Zone to another Partition | `ManualPartitioner` on the ingress, tick-loop and projector producers. The `recordlog` producers (accounts, audit, content; 6 Partitions, compacted or keyed by actor) pin `StickyKeyPartitioner(nil)` with the murmur2 key hash the library default used, so no key moved (`AW-SRV-053`, 2026-10-09; a test holds the mapping on a fixture of keys). The gap is closed |
| `compression.type` | `zstd` | Disk and network cost only. No correctness claim; `config-assert` reports a deviation as a warning, not a failure (AC-4 lists the failing settings and compression is not among them). | set on every producer (`AW-SRV-053`) |

## Consumers

The server, projectors and recovery **do not use consumer groups**. They assign Partitions directly and
take their starting offset from the Snapshot round or the projector checkpoint (ADR-0002 §4, "recovery
resumes strictly from the checkpointed offset"). The first draft's `enable.auto.commit=false` and
`auto.offset.reset=none` described group consumers and are replaced by the rows below. The group names in
`topics.yaml` are reserved for ACL prefixes (ADR-0011) and for a consumer that is later given a group.

| Setting | Value | Claim it carries | In the code today |
|---|---|---|---|
| `isolation.level` | `read_committed` | Nothing reads an aborted transactional record. No producer is transactional today, so this is a guard, not a fix: it is cheap now and a silent bug later | set on every consumer (`AW-SRV-053`) |
| group membership | none; direct Partition assignment | Position is owned by the checkpoint, not by the broker | holds |
| committed offsets in Kafka | none for a reader's position, with two named exceptions: the **projector checkpoint** and the **tick loop's observer commit** (below) | A second source of "where was I" is a second way to be wrong | holds for every reader's *position*; the projector's `Committer` and the tick loop's `KafkaSource.Commit` write commits, for the reasons below |
| start offset | a resumed position is always explicit: `At(offset)` from the checkpoint. Never `AtStart`/`AtEnd` to resume | Replay is exact | holds for the resuming readers; the named one-shot scans below are the exceptions |
| an offset the broker no longer has | `ErrLogGap` (exit `3`), never a silent reset to earliest or latest | A gap is lost history; skipping it is silent divergence | holds on the `At(offset)` readers, which run on `NoResetOffset`, apart from the pinned watch named below |

**The permitted exceptions are named one-shot scans**, which read for content and not to resume a
position. A reader not in this table that starts at `AtStart` or `AtEnd` is a contract violation:

| Reader | Start | Why |
|---|---|---|
| accounts, content blobs, content versions (`recordlog`, `content`) | `AtStart` | Compacted: reading from the start is how the latest value per key is found. |
| content active pointer (watch) | `AtEnd` after an initial read | Changes after the initial read are the point. |
| audit replay (`recordlog` on `andara.audit.v1`, client `andara-server-replay`) | `AtStart` | A bounded replay of a `delete`-policy topic to rebuild the in-memory audit view; no position is resumed. |
| Tick Boundary scan (`tickloop`, `andara.events.v1` Partition 0) | `AtStart` | A one-shot scan for the Tick Boundary Records recovery needs; `ErrLogGap` still applies to the round it looks for. |

`config-assert` knows these by reader name; adding a reader here is a contract change.

**Three named exceptions to the rows above** (ruled 2026-10-09, `AW-SRV-053`):

1. **The projector checkpoint is a committed offset, on purpose.** `projector.Committer` stores the
   projector's checkpoint (tick, per-Partition offsets, and a divergence record in the metadata) as an
   offset commit under the projector's group on `andara.commands.v1`, and `--rebuild` deletes the group
   (`AW-SRV-019`). The group is never joined and the broker's own position is never used: the
   projector reads the commit explicitly and resumes with `At(offset)`. So the checkpoint is the single
   source of "where was I", which is what the row protects, and the commit is its storage, not a
   second copy.
2. **The tick loop's checkpoint commit is the other commit, and it is write-only.** `tickloop.KafkaSource.Commit`
   commits the consumed offsets under `andara-sim-<env>` at each checkpoint (`sim.checkpoint_every_ticks`, default 100) so a dashboard or a runbook can see
   lag. Nothing reads it back for position: the tick loop resumes from the Snapshot round and the Tick
   Boundary offsets (ADR-0002 §4), so it is not a second source of "where was I".
   The `config-assert` row reports `false` for both consumers and passes, because it sees only the
   client's own group and marks, not a commit made through `kadm.CommitOffsets`. Those two are the only
   commit sites today; a third, or any reader that takes its position from the broker's committed
   offset, is a contract change, and only review catches it until a test pins the sites.
3. **The pinned Active Pointer watch stays on the library's reset** (`content active pointer watch
   (pinned)`, reported on its row with the reason). The topic is `cleanup.policy: compact` alone
   (`deploy/kafka/topics.yaml`), so its log start never passes a pinned offset, a reset to the start
   re-reads pointers the watch applies idempotently, and the watch has no exit to take on a gap. If the
   topic ever gains `delete` in its cleanup policy, this exception lapses and the watch goes on
   `NoResetOffset` with an exit.

## `client.id`

**`client.id` is the principal's name plus an optional purpose suffix: `andara-server`,
`andara-server-recovery`, `andara-projector-state`, `andara-projector-state-commit`.** No environment and
no pod. The rule is `^<principal>(-[a-z]+)*$`, with `<principal>` from ADR-0011 §3's table (`andara-server`, `andara-projector-state`, `andara-projection-redis`, `andara-projection-pg`, `andara-operator`); the suffixes in use today (`-ingress`, `-tick`, `-replay`, `-commit` and so on) all fit. A client with **no** `ClientID` is a violation `config-assert` reports, and the constructor fixes. The three that had none now carry `andara-server-boundaries` (Boundary scan), `andara-server-fetch` (commands fetch) and `andara-server-offsets` (end-offset query), on the fixed principal and not on `cfg.ServiceName` (`AW-SRV-053`). This replaces the first draft's `andara-<component>-<env>-<pod>`, for two reasons:

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
   successfully inside the deadline is not a failure and degrades nothing. **How the error is seen** (the tap, under Constraints): franz-go retries these errors until `RecordDeliveryTimeout` and then completes the promise with `ErrRecordTimeout`, not the broker error, so the implementation must capture the broker's per-Partition error another way (a client hook, for example). If it cannot, the probe below is the only trigger and this one is dropped, with the Submit that discovers the fault answered `DEADLINE_EXCEEDED` as now. (`NOT_ENOUGH_REPLICAS_AFTER_APPEND`
   is added to the three the review named: the record *was* appended, so its Submit is ambiguous in the
   same way a deadline is.) Or
2. **The probe finds it.** Once per probe interval (the producer's existing `ProbeInterval`, one second; the metadata request is bounded by it) the ingress
   reads topic metadata for `andara.commands.v1` and marks `p` degraded if its leader has been absent on **two consecutive probes**, or its
   in-sync replica count is below the topic's `min.insync.replicas`. The probe asks the broker the
   configuration, once at boot and every 60 s, and does not hard-code 2. It does **not** `Ping` a broker to judge
   a Partition: a reachable broker says nothing about a Partition. (It does send its metadata and configuration
   request to **every broker the metadata lists at once**, and the first answer wins, so one dark address in
   the metadata cannot cost the whole interval and degrade all 64 while the other brokers serve, which is the #129
   failure. A broker isolated from the controller can answer fastest with stale metadata, and the two-probe rule only confirms
   the same stale answer, so **first answer wins is not enough**: the probe collects the answers that arrive within a
   short grace (a tenth of the probe interval) after the first, and judges each Partition by the answer with the
   **highest leader epoch**. The implementation does not do this yet (it is a follow-up of `AW-SRV-052`); until it does, a
   stale healthy answer is corrected only after a Submit fails, and a stale unhealthy one can hold a Partition
   read-only until the hold and a fresh answer. Ratified 2026-10-10, `AW-SRV-052`, with that requirement.)

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
Partition with no leader are `UNAVAILABLE` from the second probe that finds it leaderless: at most about
two probe intervals after the loss, plus the interval it fell inside.
For a Partition led by a survivor whose dead followers remain in the ISR, metadata still shows a full ISR
until the broker shrinks it (`replica.lag.time.max.ms`, pinned at most 60 s by the broker contract, 30 s at the Kafka default) and the probe cannot see the
fault; only the produce-error trigger can, which is why `REQUEST_TIMED_OUT` is in it. If that error is
unobservable, such a Partition stays unmarked for up to that pinned value, every Submit to it is `DEADLINE_EXCEEDED`,
and `WorldReadOnly` still fires through the other Partitions. AC-2's rehearsal is therefore amended to:
the World is read-only for every Partition within the deadline plus two probe intervals *or*, where the
trigger is unobservable, within the broker's `replica.lag.time.max.ms` (read at rehearsal time) plus a probe interval, and the
rehearsal records which. The SRE child owns the wording of that AC.

### Leaving the state

A Partition marked by the **probe** leaves the degraded state on the first probe that finds a leader
and an ISR of at least `min.insync.replicas`. A Partition marked by a **produce error** leaves it only
after a fixed hold, `ingress.degraded_hold` (default 120 s), *and* a healthy probe, because the probe's metadata can show a full ISR while the broker is
still refusing writes (`REQUEST_TIMED_OUT`), and clearing on it would flap the Partition. The hold runs from the most recent produce-error mark. A Partition carrying both marks leaves when the
hold has elapsed and a healthy probe has been seen; a probe mark can only extend it, never shorten it. A refused
Partition receives no produce, so nothing re-observes the error during the hold; if the fault outlasts it,
the next Submit discovers it again (`DEADLINE_EXCEEDED`) and re-marks it. No restart, no operator action.

**Why a fixed hold and not a canary.** A canary produce would put a record on `andara.commands.v1`,
which holds only parsed, authorized Commands (`AW-SRV-010`, Data / state impact). The hold must exceed
`WorldReadOnly`'s `for:` plus a scrape and evaluation interval, or the alert can miss a fault the
hold keeps re-marking: the floor is a number, **90 s** (twice today's `for: 30s`, plus 30 s), and the implementing story fails
config load below it. The alert rule and `ingress.degraded_hold` change together: raising `for:` raises
the floor, and `make check` in the SLO child asserts `degraded_hold >= 2 * for + 30`. The hold is a ceiling on how long a
`REQUEST_TIMED_OUT` Partition can read healthy to the probe while still refusing writes, so
`replica.lag.time.max.ms`, the time the broker takes to shrink the ISR, must not exceed it: the broker
contract pins and asserts it. The ingress never queries a broker config for this.

### Constraints on the implementation

- **Degrading one Partition must not fail, delay or drop an in-flight produce to any other.** If the
  mechanism that keeps a record from landing after its Submit was refused is a producer swap, as it is
  today, it may not be a swap of the client that carries the other 63 Partitions' records.
- A record whose Submit was answered `UNAVAILABLE` never lands. A record whose Submit was answered
  `DEADLINE_EXCEEDED` may land only until its deadline, and never after it. Both are unchanged from
  `AW-SRV-010`; they are restated because a per-Partition mechanism is where they would be lost.
- **One client per Partition is the isolation mechanism.** Closing a client is the only way to drop what it holds, so the
  ingress holds one `kgo` client per Partition, built on first use, and entering the state closes that Partition's
  client and no other. The cost is accepted and quantified: each client holds about two connections (metadata and
  `ApiVersions`, plus a produce connection to its leader), its own producer ID and its own metadata refresh, and once
  SASL is on (ADR-0011) every connection authenticates and a degrade-and-rebuild re-authenticates. That is up to
  64 clients per server, times the servers, times two environments on the shared brokers of ADR-0013 (which names
  connection exhaustion as not prevented): about 128 connections per server per environment, which byte-rate
  `KafkaUser` quotas do not bind. **No Kafka connection budget exists today**; SRE records the broker's limit and this
  figure in the broker contract's capacity section (a request on `AW-INF-038`), and the figure is accepted subject to it. The buffer bound stays producer-wide.
  (Ratified 2026-10-10, `AW-SRV-052`.)
- **The produce error is read from the wire by a read-only tap**, because franz-go v1.20 has no hook that sees a
  produce response. The tap wraps **all of the client's connections** through `kgo.Dialer` (metadata, `ApiVersions` and
  SASL as well as produce) and decodes each `ProduceResponse`'s per-Partition error codes. **TLS must live inside the
  `Dialer`** (a `tls.Dialer`'s `DialContext`), never in `kgo.DialTLSConfig`, which would layer TLS over the tapped
  connection. The constraint binds the ingress producer's constructor and any shared constructor that builds it (the
  one of ADR-0011 §7, `AW-SRV-044`, which must accept or compose a caller-supplied `Dialer`); TLS is not in force today
  (ADR-0011 defers it), so this is a gate to write now: `AW-SRV-044` fails a `DialTLSConfig` on a client with a tap. A
  connection the tap cannot follow is let go with a warning, and the probe is then the only trigger; with SASL on,
  `tapLoss` stays 0 and a `NOT_ENOUGH_REPLICAS` still marks the Partition is an acceptance criterion of the SASL
  stories (`AW-SRV-044`, `AW-INF-056`).
- **`min.insync.replicas` where the broker reports none.** Redpanda's `DescribeConfigs` does not implement the property
  (`scripts/topics.py`, `AW-INF-004` AC-5a, which skips it on `local`), so a broker **known not to implement it** is
  held at the Kafka default of 1 instead of never being read. On Kafka/Strimzi, which is every cluster once compose is
  retired (ADR-0013), the value is read at boot and every 60 s, and an **absent value is a `warn`**, not a silent 1;
  AC 7 is scoped to Kafka. The implementation reads this today as 1 on every broker, silently; the change is a
  follow-up of `AW-SRV-052`, and this text is the target it is held to.
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
   `NOT_ENOUGH_REPLICAS_AFTER_APPEND`, `LEADER_NOT_AVAILABLE`, `NOT_LEADER_OR_FOLLOWER`, `REQUEST_TIMED_OUT`; one test per error,
   at the point the implementation observes it (see Entering the state). A story that finds the error
   unobservable says so in a comment `--to architecture` rather than dropping the criterion.
2. With a Partition degraded, a Submit for a Zone on a different Partition is produced and acknowledged.
3. A leader absent on one probe marks nothing; on two consecutive probes it marks the Partition.
4. The probe marks a Partition degraded with the ISR below `min.insync.replicas` while every broker
   answers a `Ping`, and clears it on recovery without a restart.
5. All 64 series are present at boot, 0.
6. A metadata failure on two consecutive probes degrades all 64; one failure degrades none.
7. The topic's `min.insync.replicas` is read at boot and refreshed every 60 s; a change is honoured within 60 s.
8. A Partition marked by a produce error stays degraded across probes that show a full ISR until `ingress.degraded_hold` has elapsed, measured from the most recent produce-error mark, then clears on the next healthy probe. A probe mark on the same Partition does not shorten the hold; it ends when both the hold and a healthy probe have occurred.
9. With a persistent `REQUEST_TIMED_OUT` fault and a full ISR in metadata, `andara_ingress_degraded` for that Partition reads 1 continuously for longer than `WorldReadOnly`'s `for:` plus two scrape intervals, so the alert fires. The window is from the mark to the hold's end. Under a persistent fault the gauge then reads 0 until the next Submit rediscovers it (up to `ingress.produce_deadline`), a sawtooth the alert and the SLI must tolerate.
10. `ingress.degraded_hold` below 90 s fails config load.
