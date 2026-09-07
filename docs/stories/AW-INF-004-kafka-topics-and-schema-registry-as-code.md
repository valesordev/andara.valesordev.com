---
id: AW-INF-004
title: Kafka topic and schema registry provisioning as code
epic: EPIC-10
component: infra
type: infra
status: ready
size: M
depends_on: [AW-INF-001]
blocks: [AW-INF-002, AW-INF-005, AW-SRV-002, AW-SRV-010]
assignee: claude-code
risk: high
---

## Context

ADR-0002 makes Kafka the ordering authority for the entire World. Its topic configuration is therefore
not infrastructure trivia — the partition count on `andara.commands.v1` is effectively permanent,
because repartitioning a keyed topic reorders history, and cleanup policy on the content topics is the
difference between a version history and a version eraser (ADR-0004).

Configuration that important cannot live in a compose file, a wiki, and an operator's shell history in
three slightly different versions. It lives in one declarative definition, applied to local and
production alike, and drift from it is detectable.

ADR-0007 adds the schema registry: Builders author content outside the repository, so the repository
cannot be the only place a schema is validated.

## User story

As an operator, I want every topic and schema to be declared in one place and applied identically to
every environment, so that a local test exercises the same log semantics production will.

## Scope

### In scope
- A declarative topic definition: name, partitions, replication factor, cleanup policy, retention,
  `min.insync.replicas`, and a comment stating *why* for anything non-default.
- An apply tool: create missing topics, report drift on existing ones, and **refuse** to change
  partition count on a topic that has data.
- Schema registry provisioning and subject-compatibility configuration.
- Registration of the protobuf schemas from `AW-SRV-005` against their subjects.
- `make topics-apply`, `make topics-diff`, `make schemas-apply`, `make schemas-check`.
- Consumer group naming convention and its reservation.

### Out of scope
- Producer and consumer client configuration, degradation mode, and the availability SLO —
  `AW-INF-005`.
- Kubernetes deployment of a Kafka cluster — `AW-INF-003`. This story configures topics on whatever
  broker it is pointed at.
- Tiered storage tuning for `andara.commands.v1`. Needed eventually; not needed to play.

## Acceptance criteria

1. **Given** an empty broker **when** `make topics-apply` runs **then** every declared topic is created
   with its declared partition count, replication factor, and cleanup policy, and the command exits 0.
2. **Given** all topics already correct **when** `make topics-apply` runs again **then** it exits 0 and
   changes nothing.
3. **Given** a broker where `andara.commands.v1` exists with 32 partitions and the definition declares
   64 **when** `make topics-apply` runs **then** it exits 1, states that repartitioning a keyed topic
   reorders history, and makes no change. Partition count is never silently altered.
4. **Given** a broker where a topic's retention differs from the definition **when** `make topics-diff`
   runs **then** it exits 1 and prints the topic, the property, the actual value, and the declared one.
5. **Given** the declaration **when** it is inspected **then** `andara.commands.v1` has 64 partitions and
   `andara.state.v1`, `andara.content.blobs.v1`, `andara.content.versions.v1`, `andara.content.active.v1`,
   and `andara.accounts.v1` all have `cleanup.policy=compact`.
5a. **Given** a live cluster **when** `topics-diff` runs **then** it fails if
   `unclean.leader.election.enable` is not `false` or `min.insync.replicas` is below 2 in production.
   These two settings are what the zero-RPO target rests on, and drift in them is silent.
6. **Given** a protobuf schema change that removes a field **when** `make schemas-check` runs **then**
   it exits 1 naming the field and the subject. ADR-0007's additive-only rule is enforced here, not by
   review.
7. **Given** a fresh registry **when** `make schemas-apply` runs **then** every subject is registered
   with `BACKWARD` compatibility and the command exits 0.
8. **Given** any environment **when** `make check` runs **then** `make schemas-check` runs as part of
   it, so an incompatible schema cannot merge.

## Interface contract

### Topic declaration

One file, `deploy/kafka/topics.yaml`, applied everywhere.

| Topic | Partitions | Cleanup | RF (prod / local) | Notes |
|-------|-----------:|---------|-------------------|-------|
| `andara.commands.v1` | **64** | delete | 3 / 1 | the WAL; partition count is permanent |
| `andara.events.v1` | 64 | delete | 3 / 1 | derived Events + Tick Boundary Records |
| `andara.state.v1` | 64 | **compact** | 3 / 1 | current state per aggregate; what the indexes read (ADR-0002 §5.5) |
| `andara.audit.v1` | 6 | delete, long retention | 3 / 1 | privileged actions |
| `andara.accounts.v1` | 6 | **compact** | 3 / 1 | Account state; not World state |
| `andara.content.blobs.v1` | 6 | **compact** | 3 / 1 | keys are hashes, so compaction retains all |
| `andara.content.versions.v1` | 6 | **compact** | 3 / 1 | keys are `packID@version`, so history is kept |
| `andara.content.active.v1` | 6 | **compact** | 3 / 1 | the only mutable pointer |

`andara.events.v1` retention must exceed the age of the oldest Snapshot that recovery would restore
from, plus margin — it carries the Tick Boundary Records replay needs (ADR-0002 §4). The definition
states this as a comment next to the value, because a future operator shrinking it to save disk would
otherwise be making an invisible correctness change.

### Broker settings that are not defaults and are not negotiable

| Setting | Value | Why |
|---------|-------|-----|
| `unclean.leader.election.enable` | **`false`** | with it enabled, an out-of-sync replica can be elected leader and silently truncate records that were already acknowledged — which means `acks=all` does **not** guarantee durability. The zero-RPO claim in `docs/specs/slo/recovery.md` is a claim about this setting as much as about `acks`. |
| `min.insync.replicas` | `2` (RF=3) | without it, `acks=all` degenerates to a single replica and one disk loses acknowledged player actions |

These are asserted against the live cluster by `topics-diff`, not merely written into a values file.

### Consumer groups

| Group | Consumer | Topics |
|-------|----------|--------|
| `andara-sim-<env>` | simulation processes | `andara.commands.v1` |
| `andara-state-<env>` | state projector (`AW-SRV-019`) | `andara.events.v1` → `andara.state.v1` |
| `andara-projection-redis-<env>` | Redis index | `andara.state.v1` |
| `andara-projection-pg-<env>` | Postgres index | `andara.state.v1`, `andara.accounts.v1`, `andara.audit.v1` |
| `andara-content-<env>` | content resolver | content topics |

### Make targets

| Target | Exit 0 | Exit non-zero |
|--------|--------|---------------|
| `topics-apply` | topics match the declaration | a change is required that is unsafe to make |
| `topics-diff` | no drift | drift, printed per property |
| `schemas-apply` | subjects registered | registration rejected |
| `schemas-check` | schemas are backward-compatible | an incompatible change |

## Data / state impact

Creates the topics that hold the World's entire history. The partition-count guard in AC-3 is the most
important line in this story: a tool that silently repartitions `andara.commands.v1` would reorder the
World's history irrecoverably, and it is exactly the kind of convenience an apply tool grows by
accident.

Replication factor 3 with `min.insync.replicas=2` in production. Locally 1, because a single-node
Redpanda cannot do better — which means durability semantics are the one thing local does *not*
faithfully reproduce, and `AW-INF-005` must verify them against a real cluster.

## Observability requirements

### Metrics
- `andara_topic_drift` — gauge, labels `topic`, `property`. Cardinality: topics × a small property
  set. 1 when the live configuration differs from the declaration. This makes drift alertable rather
  than discovered.
- Broker and consumer-group metrics scraped from Kafka itself: `kafka_consumergroup_lag` by
  `group` and `partition`. Partition is a bounded label (64); consumer-group is bounded by the table
  above.

### Logs
- `info` per topic created or verified, at apply time, with every property.
- `error` on a refused change, naming the topic, the property, both values, and why it was refused.

### Traces
None. This is a provisioning tool, not a request path.

### Alerts
- `KafkaTopicDrift` — fires on `andara_topic_drift == 1`. Symptom-based: the log's configuration is
  not what we declared, which is a correctness problem regardless of cause. Runbook
  `docs/runbooks/kafka-topic-drift.md` ships in this story.

Consumer-lag alerting belongs to `AW-INF-005`, which owns the SLO it would be tied to.

## Test plan

- **Unit:** declaration parsing; drift detection per property; the partition-count guard, including
  the case where the topic exists but is empty (still refused — emptiness is not checkable without a
  race).
- **Integration:** against a throwaway Redpanda: apply to an empty broker, apply again asserting no
  change, mutate a topic out of band and assert `topics-diff` catches it, attempt a repartition and
  assert refusal. Schema registry: register, then attempt a field removal and assert rejection.
- **Manual/operator:**
  ```
  make topics-apply && make topics-diff     # expect: exit 0, no drift
  make schemas-apply && make schemas-check  # expect: exit 0
  ```

## Definition of done

CLAUDE.md §8, plus:
- `AW-INF-002`'s local stack creates topics by calling this story's tooling, not by duplicating the
  configuration.
- `make schemas-check` runs in `make check`.
- `docs/runbooks/kafka-topic-drift.md` exists and resolves its alert.

## Open questions

- `[ASSUMPTION]` 64 partitions on `andara.commands.v1`, per ADR-0002 §6. This is the one number in
  this story that cannot be changed later. Confirm it before the first production apply.
- `[ASSUMPTION]` Schema registry compatibility mode is `BACKWARD` — new readers can read old data,
  which is what replay of an old log requires. `FULL` would also forbid changes that break old readers
  of new data; worth considering once Behavior Agents version-skew from the server.
- `[NEEDS BRIAN]` Retention on `andara.commands.v1`. Infinite retention with tiered storage makes the
  entire World history replayable forever, which is a genuinely valuable debugging and audit property.
  Finite retention is cheaper. This is a cost decision.
