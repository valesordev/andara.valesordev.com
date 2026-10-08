# Kafka client contract

> **Status: decided 2026-10-08, `AW-INF-005`.** Asserted at runtime by `andara-server config-assert`
> (implementation, a child of `AW-INF-005`). Companion to `broker-contract.md`.

Every Kafka client in the server and projector takes its options from one shared constructor
(ADR-0011 §7). This contract is what that constructor must produce. It was written against the code as
of 2026-10-08, and where the first draft of the story disagreed with the code the disagreement is
resolved below, not left for the implementer.

## Producers

Applies to ingress, events, state, content, accounts and audit producers.

| Setting | Value | Claim it carries | In the code today |
|---|---|---|---|
| `acks` | `all` | RPO: "ordered" means the ISR has it | set (`kgo.AllISRAcks()`) |
| `enable.idempotence` | `true` | An ambiguous retry must not duplicate a record | franz-go default; cannot be turned off while `acks=all` |
| `max.in.flight.requests.per.connection` | `<= 5` | Order within a Partition under retry | pinned to 5 by franz-go for an idempotent producer; **not settable**, so `config-assert` reports the library's pinned value and does not set it (`AW-SRV-010`, 2026-09-19) |
| `delivery.timeout.ms` | `ingress.produce_deadline` (ingress producer only) | Bounds the ambiguity window of `AW-SRV-010` AC-5 | set (`RecordDeliveryTimeout`) |
| partitioner | explicit `hash(ZoneID) % 64` on keyed topics | Order: a library upgrade must not move a Zone to another Partition | `ManualPartitioner` on the keyed producers |
| `compression.type` | `zstd` | Disk and network cost only. No correctness claim, which is why it is the one row `config-assert` may be told to skip. | **not set**; the library default applies. The `config-assert` child sets it |

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
| start offset | always explicit: `At(offset)` from the checkpoint | Replay is exact | holds |
| an offset the broker no longer has | `ErrLogGap` (exit `3`), never a silent reset to earliest or latest | A gap is lost history; skipping it is silent divergence | holds on the `At(offset)` readers |

**The permitted exceptions are the compacted and pointer topics**, which are read for their current
state and not for a position in history:

| Reader | Start | Why |
|---|---|---|
| accounts, content blobs, content versions (`recordlog`, `content`) | `AtStart` | Compacted: reading from the start is how the latest value per key is found. |
| content active pointer (watch) | `AtEnd` after an initial read | Changes after the initial read are the point. |

`config-assert` knows these by reader name. A new reader that starts at `AtStart` or `AtEnd` on a
non-compacted topic is a contract violation and a review finding.

## `client.id`

**`client.id` is the principal's name plus an optional purpose suffix: `andara-server`,
`andara-server-recovery`, `andara-projector-state`, `andara-projector-state-commit`.** No environment and
no pod. This replaces the first draft's `andara-<component>-<env>-<pod>`, for two reasons:

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
   `NOT_LEADER_OR_FOLLOWER`. A transient single error that the idempotent producer retries
   successfully inside the deadline is not a failure and degrades nothing. (`NOT_ENOUGH_REPLICAS_AFTER_APPEND`
   is added to the three the review named: the record *was* appended, so its Submit is ambiguous in the
   same way a deadline is.) Or
2. **The probe finds it.** Once per probe interval (the producer's existing `ProbeInterval`, one second) the ingress
   reads topic metadata for `andara.commands.v1` and marks `p` degraded if its leader is absent, or its
   in-sync replica count is below the topic's `min.insync.replicas`. The probe asks the broker the
   configuration, once at boot and every 60 s, and does not hard-code 2. It does **not** ping a broker:
   a reachable broker says nothing about a Partition.

If the metadata request itself fails (no broker answers), all 64 Partitions are degraded.

### What a Submit sees

A Submit whose actor's Partition is degraded returns `UNAVAILABLE`, reason `world_read_only`, at once,
with nothing produced and the wording Brian ruled on 2026-09-19. The error does not name the Partition;
the reason and the message are unchanged, so no client changes. The Submit that discovers the failure
by its own produce is `DEADLINE_EXCEEDED` (outcome unknown), exactly as `AW-SRV-010` AC-5 says now.

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

1. A produce failing with `NOT_ENOUGH_REPLICAS` marks only that Partition degraded. Likewise
   `NOT_ENOUGH_REPLICAS_AFTER_APPEND`, `LEADER_NOT_AVAILABLE`, `NOT_LEADER_OR_FOLLOWER`; one test per error.
2. With a Partition degraded, a Submit for a Zone on a different Partition is produced and acknowledged.
3. The probe marks a Partition degraded with the ISR below `min.insync.replicas` while every broker
   answers a `Ping`, and clears it on recovery without a restart.
4. All 64 series are present at boot, 0.
5. A metadata failure degrades all 64.
