---
id: AW-INF-004
title: Kafka topic and schema registry provisioning as code
epic: EPIC-10
component: infra
type: infra
status: done
size: M
depends_on: [AW-INF-001]
blocks: [AW-INF-002, AW-INF-005, AW-SRV-002, AW-SRV-010, AW-INF-014]
lane: architecture
risk: high
---

## Context

> **Partially delivered — 2026-09-07, as a dependency of `AW-INF-002`.** The local stack cannot
> create its topics without this story's declaration, and duplicating topic configuration into a
> compose file is the exact drift this story exists to prevent. What landed:
> `deploy/kafka/topics.yaml` (all eight topics, replication factor and `min.insync.replicas` per
> environment, and the broker settings, split into `broker.assert` for real Kafka and `broker.local`
> for the local Redpanda), plus `make topics-apply` and `make topics-diff` — verified against a live
> broker, including the refusal to change a partition count (AC-1 through AC-5).
>
> **Still open:** AC-5a's live-cluster assertions against real Kafka, and AC-6 through AC-8 —
> `make schemas-apply` and `make schemas-check`, which need the `.proto` sources from `AW-SRV-005`
> before they can register or check anything. Both targets exist and exit non-zero naming this story.
>
> One finding for whoever picks this up: `unclean.leader.election.enable` has no Redpanda equivalent,
> because Raft replication cannot elect a leader missing committed records. The declaration therefore
> asserts it rather than applying it, and the local stack does not exercise it at all. That is a real
> local/production divergence and belongs in `AW-INF-005`'s rehearsal.


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
   64 **when** `make topics-apply` runs **then** it exits non-zero, states that repartitioning a keyed topic
   reorders history, and makes no change. Partition count is never silently altered.
4. **Given** a broker where a topic's retention differs from the definition **when** `make topics-diff`
   runs **then** it exits non-zero and prints the topic, the property, the actual value, and the declared one.
5. **Given** the declaration **when** it is inspected **then** `andara.commands.v1` has 64 partitions and
   `andara.state.v1`, `andara.content.blobs.v1`, `andara.content.versions.v1`, `andara.content.active.v1`,
   and `andara.accounts.v1` all have `cleanup.policy=compact`.
5a. **Given** a live cluster **when** `topics-diff` runs **then** it fails if
   `unclean.leader.election.enable` is not `false` or `min.insync.replicas` is below 2 in production.
   These two settings are what the zero-RPO target rests on, and drift in them is silent.
   **Found failing at review, 2026-09-14 — both halves.** `min.insync.replicas` was declared and
   listed for comparison but skipped whenever the broker did not report it, and
   `unclean.leader.election.enable` was read through `rpk cluster config get`, which is Redpanda's
   admin API and does not exist on the Kafka broker ADR-0002 §7 names for production; an unreadable
   property was then treated as agreement. `topics-diff --env prod` reported no drift against a
   broker carrying neither setting. Both are now checked per topic through DescribeConfigs — the
   Kafka API call the rest of the tool already uses — and on any non-local environment an absent
   property is drift, not assent. On `local` they are skipped by design: Redpanda implements
   neither (its Raft replication cannot elect a leader missing committed records), which the
   declaration already says. Proven against Redpanda: `local` clean, `--env prod` reports 16
   drifts naming each topic, property, and declared value. Not yet proven against real Kafka.
6. **Given** a protobuf schema change that removes a field **when** `make check` runs **then** it exits 1
   naming the field *and the subjects that carry it*. ADR-0007's additive-only rule is enforced
   mechanically, not by review.
   **Amended 2026-09-10, with the measurement that forced it.** This originally said `schemas-check`
   would be the detector. It cannot be: Redpanda's registry was measured and its `BACKWARD` check
   accepts a removed field *and a renumbered one*. Renumbering is the one that would end the project —
   replay would misread every historical record (ADR-0002) — so the detector is `buf breaking` in
   `make proto-check`, which already ran it. `proto-check` now names the carrying subjects on failure,
   which is the part that was genuinely missing. Both targets are in `make check`, so the criterion
   holds as written at the level that matters: the change cannot merge.
7. **Given** a fresh registry **when** `make schemas-apply` runs **then** every subject is registered
   with `BACKWARD` compatibility and the command exits 0. Running it a second time registers nothing.
7a. **Given** a registry holding something other than the declaration **when** `make schemas-diff` runs
   **then** it exits non-zero, naming the subject and whether it is undeclared, unregistered, or superseded.
8. **Given** any environment **when** `make check` runs **then** `make schemas-check` runs as part of
   it, so a topic without a declared record type cannot merge. `schemas-check` is offline by
   construction — it needs no broker, because `make check` must stay containerless (`AW-INF-002`).

## Interface contract

### Topic declaration

One file, `deploy/kafka/topics.yaml`, applied everywhere.

| Topic | Partitions | Cleanup | RF (prod / local) | Notes |
|-------|-----------:|---------|-------------------|-------|
| `andara.commands.v1` | **64** | delete | 3 / 1 | the WAL; partition count is permanent. `retention.ms: -1` — infinite, decided 2026-09-10 |
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
| `schemas-apply` | subjects registered; idempotent | registration rejected |
| `schemas-check` | the declaration is complete and every declared message exists | a topic with no subject and no `pending:` entry, a message that does not exist, a missing reference |
| `schemas-diff` | the registry matches `deploy/kafka/schemas.yaml` | a subject undeclared, unregistered, or superseded |

**Which target owns which detector.** Stated because the obvious arrangement is wrong and the next
reader will try it:

| Question | Target | Needs a broker |
|----------|--------|----------------|
| Is this schema change additive-only (ADR-0007)? | `proto-check` (`buf breaking`) | no |
| Is every topic's record type declared, and does it exist? | `schemas-check` | no |
| Does the registry hold what we declared? | `schemas-diff` | **yes** |

`schemas-check` deliberately does **not** re-run `buf breaking`. Same detector, same baseline, two red
steps for one change. The offline pair runs in `make check`; `schemas-diff` runs in the `stack`
workflow, where a registry exists.

### Subject declaration

`deploy/kafka/schemas.yaml`. Every topic in `topics.yaml` is either mapped to a subject or listed under
`pending:` with the story that will define its record type — an omitted topic is indistinguishable from
a forgotten one, and `schemas-check` fails if the named story does not exist.

`andara.events.v1` carries two record types with no envelope wrapping them — derived Events and the
Tick Boundary Records replay reads (ADR-0002 §4) — so it uses **TopicRecordNameStrategy** and holds two
subjects. The alternative is adding an envelope message, a permanent wire change this story has no
mandate to make.

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

## Verification record — 2026-09-10

Executed against a live Redpanda registry, not reasoned about. (The topic half, ACs 1–5a, was verified
when it shipped inside `AW-INF-002`.)

| AC | Result |
|----|--------|
| 6 | Removed `LoggedCommand.client_ref`, ran `make proto-check`: named the field, then named all three carrying subjects (`andara.commands.v1-value`, both `andara.events.v1-*`). Exit 1. |
| 7 | `make schemas-apply` on an empty registry: 6 subjects, global and per-subject compatibility `BACKWARD`. Second run: `0 newly registered`. |
| 7a | Three drift modes, each detected: a subject registered out of band; global compatibility moved to `NONE`; a field added locally and not applied. |
| 8 | `make schemas-check` is in `CHECK_TARGETS`; the CI parity guard failed until `ci.yaml` gained the step, which is the guard working. |
| — | `schemas-check` catches an unaccounted topic, a `pending:` entry naming a story that does not exist, a message that is not in the descriptor, and a subject name that does not match its strategy. |
| — | Reference path exercised by temporarily declaring `snapshot.proto` (which imports `log.proto`): `schemas-check` demanded the `references:` entry, and `apply` then registered both in order. |

Three findings worth keeping:

- **The registry cannot enforce ADR-0007.** Measured: `is_compatible: true` for a removed field and for
  a field renumbered `2 → 7`. It does reject a scalar type change and a removed message. BACKWARD
  compatibility and additive-only are different rules, and only the second one protects replay.
- **Redpanda rejects `version: -1` in a schema reference** — the Confluent "latest" convention — with
  `422 parse error at offset 2772`, an offset past the end of the schema. That error sends you reading
  the `.proto`. References now resolve to concrete versions.
- **A subject name that is an import path must be URL-encoded** in the request path, or one path
  segment becomes four and the registry answers 404.

## Definition of done

CLAUDE.md §8, plus:
- `AW-INF-002`'s local stack creates topics by calling this story's tooling, not by duplicating the
  configuration.
- `make schemas-check` runs in `make check`.
- `docs/runbooks/kafka-topic-drift.md` exists and resolves its alert.

## Open questions

- **Resolved by ADR-0002 §6, noted 2026-09-14:** 64 partitions on `andara.commands.v1` is that
  ADR's recorded decision ("Decision: 64 partitions"), not this story's assumption. It is applied in
  the declaration and AC-3 — proven at review by recreating the topic with 32 — is what keeps it from
  ever being changed by a make target. It cannot be changed later; that is the point.
- **Resolved 2026-09-14 (by the implementation):** registry compatibility is `BACKWARD` — new readers read old data,
  which replay requires — set globally by `schemas-apply` and verified by `schemas-diff`. `FULL`
  would additionally forbid changes that break old readers of new data; that is a question for
  `AW-SRV-009` once Behavior Agents version-skew from the server, and is noted there rather than
  left open here.
- **Resolved 2026-09-10 (Brian): infinite retention, with tiered storage as the mechanism.**
  `retention.ms: -1` on `andara.commands.v1` is applied in `deploy/kafka/topics.yaml` and verified —
  `-1` round-trips through `topics-apply` and `topics-diff` at creation.

  **Tiered storage itself is not enabled, and needs two things this decision did not settle.** Measured
  against the running broker on 2026-09-10:

  1. **An S3-compatible bucket.** `cloud_storage_enabled` refuses to turn on without
     `cloud_storage_region`, `_bucket`, `_access_key`, `_secret_key` (or the Azure equivalents). MinIO
     locally, real object storage in production. On a single box (`AW-INF-003`) a local MinIO puts the
     archive on the same disk as the log, which is archival, not durability.
  2. `[NEEDS BRIAN]` **A Redpanda enterprise licence.** The broker reports
     `Type: free_trial, Organization: Redpanda Built-In Evaluation Period`. Tiered Storage is an
     enterprise feature, so it works during the trial and stops when the trial ends. Infinite retention
     without tiered storage means the log grows on local disk forever, which on this box is a capacity
     question with a date on it.

  Until both are settled, retention is infinite **on local disk**. The properties are applied in
  `AW-INF-005`, which owns the operational contract and is where disk growth becomes an alert.
