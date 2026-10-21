# Kafka broker contract

> **Status: decided 2026-10-08, `AW-INF-005`; amended 2026-10-21 for `ADR-0013`'s shared broker (`AW-INF-038`).**
> Enforced by `make broker-assert` once `AW-INF-038` lands. Topics, replication factor, `min.insync.replicas` and
> `unclean.leader.election.enable` are declared in `deploy/kafka/topics.yaml`. The broker settings are declared
> in `deploy/k8s/kafka/kafka.yaml`, except `replica.lag.time.max.ms`, which rests on Kafka's default (30000), and
> `max.connections`, which AW-INF-038 adds. This document says what each setting is *for*, so that nobody relaxes
> one without knowing which claim it carries. The shared broker below is the target of AW-INF-055, which is not
> built yet.

ADR-0002 makes Kafka the ordering authority for the World, so Kafka availability bounds World
availability and Kafka durability bounds what we can promise a player about an acknowledged Command.
Every row below carries one of those two claims. A setting with no claim is not in the table.

**RPO** below means: a Command acknowledged with `(partition, offset)` is on the log after any
single-broker failure and any recovery. **Order** means: per-Partition offset order is the order things
happened.

## Environments

*Amended 2026-10-21 (`ADR-0013` decisions 3 and 4).* `local` and `dev` share one broker set. Broker settings are
therefore cluster-wide, and an environment owns only its topics.

| | `local` | `dev` | `prod` |
|---|---|---|---|
| brokers | the shared `andara-log` in `andara-shared`: Apache Kafka under Strimzi, 3 combined KRaft nodes (AW-INF-055) | the same brokers | its own cluster, the same settings; no environment today (2026-10-19) |
| topics | `local.<name>`, RF 1, `min.insync.replicas` 1, bounded retention (below) | `dev.<name>`, RF 3, `min.insync.replicas` 2 | `<name>` (unprefixed), RF 3, `min.insync.replicas` 2 |

`local` is not a rehearsal of `dev`'s durability: its topics have no replication. It shares `dev`'s brokers,
disk and failure domain, and the bounds below limit what it can do to them (`ADR-0013` decision 4). From
AW-INF-055 on, both `unclean.leader.election.enable` and `replica.lag.time.max.ms` apply to every environment.
Until AW-INF-067 retires compose, its Redpanda (`BROKER=compose`) still serves `local` (`ADR-0013` amendment 23),
outside this contract. It is not asserted, and its auto-create stays on (`--mode=dev-container`).

## Settings

| Setting | `local` / `dev` / `prod` | Claim it carries |
|---|---|---|
| `default.replication.factor` | broker: 3; `local.` topics declare 1 | RPO. A topic declared in `topics.yaml` states its own; the default catches a topic created outside it. |
| `min.insync.replicas` | broker default 2; `local.` topics declare 1; `dev.` and prod topics 2 | RPO. `acks=all` waits for the ISR, and an ISR of one makes `acks=all` mean `acks=1`. At 2 of 3, one broker can fail with writes continuing, and two cannot. |
| `unclean.leader.election.enable` | `false` | RPO. With it `true`, an out-of-sync replica can lead and silently truncate acknowledged records. This is the setting people miss. |
| `replica.lag.time.max.ms` | `<= 60000` (Kafka default 30000) | Detection. The time the broker takes to drop a dead follower from the ISR. Metadata shows a full ISR until then while a survivor-led Partition refuses writes, so it bounds how long the ingress's probe is blind (`client-contract.md`, leaving the state). The cap must stay at or below the 90 s floor of `ingress.degraded_hold` (`client-contract.md`, leaving the state): raising it is a contract change to both. |
| `auto.create.topics.enable` | `false` | Order. A typo in a topic name must fail, not create a topic with default partitions and a default key mapping. |
| per-topic `partitions` | `topics.yaml` | Order. Permanent for keyed topics: repartitioning changes the key-to-Partition mapping and reorders history. |
| per-topic `retention.ms`, `cleanup.policy` | `topics.yaml`; see Retention | Replay window. |
| `local.` topics' `segment.bytes` and `segment.ms` (every `local.` topic); `delete` topics' `retention.bytes` and `retention.ms` | exactly `topics.yaml`'s `local` values, which must satisfy `segment.bytes` ≤ `retention.bytes` / 4 and `segment.ms` ≤ 86400000. Today: retention 64 MiB (67108864) and 7 days, segments 16 MiB (`ADR-0013` decision 4; AW-INF-055 declares and measures them) | Isolation. `dev`'s `andara.commands.v1` is infinite on a 20Gi PVC per broker, and a `local` soak must not fill the disk `dev`'s RPO depends on. See **`local.` bounds** below the table. |
| `max.connections` | at most 1000 per broker | Availability. A backstop against file-descriptor exhaustion taking a shared broker down for both environments, not a budget (see Capacity). |
| rack awareness | none on the box; required off it | Recorded, not enforced. Three replicas on one box share one disk (see below). |

**`local.` bounds.** Neither retention nor compaction touches the active segment (`topics.yaml`, `andara.state.v1`). With Kafka's default 1 GiB segment, every `local.` partition could hold about 1 GiB before either bites, which is 64 GiB for `local.andara.state.v1` alone against 60 GiB of broker storage. With 16 MiB segments:

- a `delete` partition holds about 80 MiB, plus what is produced between retention checks (`log.retention.check.interval.ms`, 5 min), which `local`'s byte-rate quotas bound;
- a compacted partition holds its 16 MiB active segment plus its dirty portion, which the cleaner targets at about its live keyed data (the default `min.cleanable.dirty.ratio` of 0.5). Cleaner lag, `min.compaction.lag.ms` and tombstones held for `delete.retention.ms` can exceed that under a soak;
- in total, `local.`'s 134 `delete` and 88 compacted partitions on 3 brokers come to about 4 GiB per broker, plus about twice `local`'s live keyed state.

Compacted `local.` topics keep `cleanup.policy: compact` and declare no retention. Retention is ignored without `delete`, and `compact,delete` would drop keyed state. So `broker-assert` checks their segment values only. `make shared-check`'s 80% threshold is the backstop (AW-INF-055).

`make broker-assert ENV=<local|dev>` fails on any deviation in the broker rows (`default.replication.factor`,
`min.insync.replicas`'s broker default, `unclean.leader.election.enable`, `replica.lag.time.max.ms`,
`auto.create.topics.enable`, `max.connections`) and on any of the environment's topics that differs from
`topics.yaml`, or from `local.`'s retention and segment bounds. `ENV` selects the topics, not the brokers. With
`ENV=local`, it prints one warning line for RF 1 and `min.insync.replicas` 1 and exits `0`, because that is the
contract for `local`. Broker settings are read with Kafka's Admin API (`DescribeConfigs` on the broker resource),
not through `rpk`, which reads Redpanda's admin API and returns nothing for Kafka (`scripts/topics.py`). A broker
row missing from the response is a deviation, not agreement.

## Capacity

*Added 2026-10-21 (`AW-INF-038`; SRE's figures, 2026-10-11).* `ADR-0013` leaves the partition and connection
budget unenforced. This section states the budget and the one backstop.

- **Partitions:** 222 per environment, 444 on the shared brokers, and 888 replicas (666 for `dev` at RF 3, 222 for
  `local` at RF 1) on 3 brokers (`ADR-0013` decision 4). Kafka's internal topics (`__consumer_offsets`, 50 at
  RF 3), the registry's `_schemas` and the integration suite's `it<nonce>.` topics come on top.
- **Connections, demand:** the server holds about 128 connections per environment (64 per-Partition clients,
  about two each: bootstrap plus the leader, `client-contract.md`). Tickloop, the record log, content and the
  projector add a handful, so the total is about **140 per environment, 280 on the shared brokers**, spread over 3
  brokers. SASL adds a handshake per connection, not a connection. The schema registry, the toolbox and the
  integration suite's `it` listener add a few more, not counted here.
- **Connections, limit:** `max.connections: 1000` per broker. That is about 3.5× the worst case of all 280 landing
  on one broker. It doesn't divide the budget between environments: a runaway `local` client could still take
  connections `dev` needs. But it stops the runaway at a limit the broker survives, where today the broker's
  file-descriptor limit would take it down for both. It counts every client listener, the `it` listener
  included. At the limit, an inter-broker connection is still admitted, by closing the least-recently-used
  client connection on another listener (KIP-402). Kafka's `max.connections.per.ip` is not set, because pod
  IPs on kind are neither stable nor per-environment.
- **No per-principal connection cap exists.**
  - KIP-612's `connection_creation_rate` is a quota on the `ips` entity only. Combining it with a user is an
    invalid request, and it bounds a rate, not a count.
  - Strimzi's `KafkaUser.spec.quotas` has byte rates, a request percentage and a controller mutation rate, and no
    connection field.

  A per-listener cap (`maxConnections` on a Strimzi listener) could divide the budget, at the cost of one listener
  per environment, each admitted by its own namespace label. It isn't taken now, because 280 against 1000 leaves no
  pressure to divide. `local`'s isolation stays its byte-rate quotas (`ADR-0013` decision 4, `AW-INF-056`).
- **What reports it:** `make shared-check` will report PVC usage (AW-INF-055). A live connection count per broker
  and per listener (`socket-server-metrics` `connection-count`, which `kafka.yaml`'s metrics `allowList` doesn't
  keep today) is the measurement that would justify lowering the limit.

## What a green check on the box does not prove

`dev`'s and `local`'s topics are on three brokers on one kind box, one disk (`AW-INF-003`); a prod cluster is the same shape until it has its own failure domains. The settings are correct for a
real cluster and cannot protect against the disk they share. A rehearsal that kills brokers proves
broker-failure semantics and cannot prove disk-failure semantics. ADR-0002's first "revisit when" is
this: the zero-RPO claim in `docs/specs/slo/recovery.md` is, until the cluster has independent failure
domains, a claim about one machine.

## Retention

Topic names below are without the environment prefix (`dev.`; unprefixed for prod). On `local.` topics, the
Settings row's bounds override every value here.

**`local` is outside the recovery invariant (decided 2026-10-21).** With `local.andara.events.v1` at 7 days and
`local.andara.commands.v1` at 64 MiB per partition, the invariant below (a round older than `retention.ms - 7 d`
is unusable) leaves `local` no usable round age. `local` is disposable (`ADR-0013` decision 4), so:
- its recovery is good only while its newest complete round is within its events window and its Commands since
  that round are still retained;
- past that, the honest outcome is `ErrLogGap`, and the remedy is a reset, not a recovery: `make world-reset ENV=local BROKER=cluster CONFIRM=andara-local` (AW-INF-055 AC-7; `BROKER=` drops out when AW-INF-067 retires compose);
- `lifecycle.md`'s `make check` assertion (`snapshot.max_round_age <= retention.ms(events) - 7 d`) reads `dev`'s and
  prod's events retention, not `local`'s;
- no `local`-specific `snapshot.max_round_age` is introduced;
- `local` therefore doesn't meet ADR-0002 §4's "retention reaches back at least as far as the oldest snapshot", and its zero-RPO claim doesn't apply. `local` runs at RF 1 with bounded Command retention by design (`ADR-0013` decision 4).

| Topic | Retention | Decision |
|---|---|---|
| `andara.commands.v1` | infinite (`-1`) | The write-ahead log. Replay from a Snapshot needs the log to reach back to the Snapshot's offsets, and the history is the record of what happened and the source for rebuilds and audit. Tiered storage is the follow-up when `andara_kafka_log_bytes` says the disk demands it; shrinking retention is not. **Infinite Commands are not a recovery guarantee on their own** (see below). |
| `andara.events.v1` | 30 days | Carries Tick Boundary Records (ADR-0002 §4), which replay reads. The invariant is **retention > the age of the oldest Snapshot round recovery would restore from, plus margin**. 30 days is chosen against the snapshot retention keys (`snapshot.keep_*`): the round retention sweep must not let a retained round outlive the Boundary Records it needs, and a round older than `retention.ms - 7 d` is treated as unusable. `snapshot.max_round_age` (default 552 h, 23 days) carries that bound (`docs/specs/deploy/lifecycle.md`, Round tags). Shrinking this value to save disk is a correctness change. |
| `andara.audit.v1` | 365 days | The record of who did what with operator authority. |
| compacted topics | n/a | State, accounts, content. Compaction retains the latest per key; content keys never repeat so compaction retains everything (ADR-0004). |

**Snapshots are the only recovery start (ruling, 2026-10-08, on Codex's review of PR #469).** Exact
replay needs Tick Boundary Records from the replay's first tick. They live on `andara.events.v1`, which
keeps 30 days, so a World older than that cannot be replayed from the empty World: the projector already
refuses it (`ErrLogGap`, `server/projector/run.go`). `server/recovery/recover.go` still falls back to
offset zero when no complete round exists; that path is exact only while the World is younger than the
events retention, and from then on it is not a recovery path. The durability claim is therefore: **the snapshot store (`snapshot.store`: `fs` on a PVC, or `s3`) is part of the recovery path, and losing every complete round of an
old World is unrecoverable**, not "recoverable from the log". Making the log alone sufficient would mean
infinite retention on `andara.events.v1` or a separate boundary topic, and was declined as permanent
disk for a path that only runs after a double failure. The follow-ups are routed on `AW-INF-005`:
`docs/specs/slo/recovery.md` and `AW-SRV-007` state the fallback honestly.

`[ASSUMPTION]` resolved: retention is as above. These values were already in `topics.yaml`; this
document makes them a decision with a reason rather than a default.

## Revisit when

- The World runs on a cluster with independent failure domains: add rack awareness to the enforced rows.
- `andara_kafka_log_bytes` approaches the disk: tiered storage for `andara.commands.v1`.
- The cluster moves to managed Kafka: the table becomes an assertion about someone else's cluster, and
  `make broker-assert` still runs it.
