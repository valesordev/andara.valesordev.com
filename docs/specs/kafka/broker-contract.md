# Kafka broker contract

> **Status: decided 2026-10-08, `AW-INF-005`.** Enforced by `make broker-assert` (SRE, `AW-INF-005`'s
> enforcement child). Declared in `deploy/kafka/topics.yaml`; this document says what each setting is
> *for*, so that nobody relaxes one without knowing which claim it carries.

ADR-0002 makes Kafka the ordering authority for the World, so Kafka availability bounds World
availability and Kafka durability bounds what we can promise a player about an acknowledged Command.
Every row below carries one of those two claims. A setting with no claim is not in the table.

**RPO** below means: a Command acknowledged with `(partition, offset)` is on the log after any
single-broker failure and any recovery. **Order** means: per-Partition offset order is the order things
happened.

## Environments

| | `local` | `dev` | `prod` |
|---|---|---|---|
| broker software | Redpanda, 1 broker | Apache Kafka under Strimzi, 3 brokers | same as `dev` |
| where | compose | kind box (`AW-INF-014`) | kind box |

`local` is not a rehearsal of `dev`. It has no `unclean.leader.election.enable` (Raft cannot elect a
leader missing committed records) and no replication.

## Settings

| Setting | `local` / `dev` / `prod` | Claim it carries |
|---|---|---|
| `default.replication.factor` | 1 / 3 / 3 | RPO. A topic declared in `topics.yaml` states its own; the default catches a topic created outside it. |
| `min.insync.replicas` | 1 / 2 / 2 | RPO. `acks=all` waits for the ISR, and an ISR of one makes `acks=all` mean `acks=1`. At 2 of 3, one broker can fail with writes continuing, and two cannot. |
| `unclean.leader.election.enable` | `false` everywhere it exists | RPO. With it `true`, an out-of-sync replica can lead and silently truncate acknowledged records. This is the setting people miss. |
| `auto.create.topics.enable` | `false` | Order. A typo in a topic name must fail, not create a topic with default partitions and a default key mapping. |
| per-topic `partitions` | `topics.yaml` | Order. Permanent for keyed topics: repartitioning changes the key-to-Partition mapping and reorders history. |
| per-topic `retention.ms`, `cleanup.policy` | `topics.yaml`; see Retention | Replay window. |
| rack awareness | none on the box; required off it | Recorded, not enforced. Three replicas on one box share one disk (see below). |

`make broker-assert` fails on any deviation in the first four rows and on any topic that differs from
`topics.yaml`. On `local` it prints a warning for RF 1 and `min.insync.replicas` 1 and exits `0`, because
that is the contract for `local`.

## What a green check on the box does not prove

`dev` and `prod` are three brokers on one kind box, one disk (`AW-INF-003`). The settings are correct for a
real cluster and cannot protect against the disk they share. A rehearsal that kills brokers proves
broker-failure semantics and cannot prove disk-failure semantics. ADR-0002's first "revisit when" is
this: the zero-RPO claim in `docs/specs/slo/recovery.md` is, until the cluster has independent failure
domains, a claim about one machine.

## Retention

| Topic | Retention | Decision |
|---|---|---|
| `andara.commands.v1` | infinite (`-1`) | The write-ahead log. Replay can start from a Snapshot only if the log reaches back to it, and the only bound on how far back a recovery may need that does not depend on snapshot health is "all of it": the oldest starting point is the empty World. Tiered storage is the follow-up when `andara_kafka_log_bytes` says the disk demands it; shrinking retention is not. |
| `andara.events.v1` | 30 days | Carries Tick Boundary Records (ADR-0002 §4), which replay reads. The invariant is **retention > the age of the oldest Snapshot round recovery would restore from, plus margin**. 30 days is chosen against the snapshot retention keys (`snapshot.keep_*`): the retention story for rounds (`AW-INF-007`'s implementation child) must not let a retained round outlive the Boundary Records it needs, and a round older than `retention.ms - 7 d` is treated as unusable. Shrinking this value to save disk is a correctness change. |
| `andara.audit.v1` | 365 days | The record of who did what with operator authority. |
| compacted topics | n/a | State, accounts, content. Compaction retains the latest per key; content keys never repeat so compaction retains everything (ADR-0004). |

`[ASSUMPTION]` resolved: retention is as above. These values were already in `topics.yaml`; this
document makes them a decision with a reason rather than a default.

## Revisit when

- The World runs on a cluster with independent failure domains: add rack awareness to the enforced rows.
- `andara_kafka_log_bytes` approaches the disk: tiered storage for `andara.commands.v1`.
- The cluster moves to managed Kafka: the table becomes an assertion about someone else's cluster, and
  `make broker-assert` still runs it.
