---
id: AW-INF-018
title: make topics-apply applies declared config drift to existing topics
epic: EPIC-10
component: infra
type: bug
status: draft
size: S
depends_on: [AW-INF-004]
blocks: []
lane: architecture
risk: medium
---

## Context

`deploy/kafka/topics.yaml` is the one declaration of every topic (AW-INF-004). `make topics-apply`
creates missing topics and never alters an existing one, and `make topics-diff` reports drift that
nothing applies (#77). AW-SRV-019 (#64) added `min.compaction.lag.ms: 60000` to
`andara.state.v1`. On a cluster where the topic already existed, that line can only reach the
broker by hand. `deploy/helm/andara/values.yaml` says so: "an operator step, not a make target".
Under CLAUDE.md §9, that's a defect.

It gates the box session three SPRINT-01 carryovers wait on. `AW-INF-014` AC-3's re-confirmation
fails `topics-diff` until the drift is applied. `AW-INF-008` AC-2 and `AW-SRV-019`'s "the digest
assertion runs continuously in production" line both need `projectors.state` on `dev`, and that
needs the lag setting live first.

## User story

As an operator, I want one make target to bring an existing topic's config in line with the
declaration, so that a config change in `topics.yaml` reaches every environment the same way a new
topic does.

## Scope

### In scope
- `scripts/topics.py apply` alters every `COMPARED` key that differs on an existing topic, using
  incremental alter-configs (`rpk topic alter-config --set`), locally and through the box's rpk
  toolbox, and prints each change.
- Partition-count drift stays refused, as it is today, and names the topic and both counts.
- A key that the broker accepts but doesn't report (Redpanda: `min.insync.replicas`,
  `min.compaction.lag.ms`) is skipped locally, as `topics-diff` skips it now, and says so once per
  run.
- `scripts/tests/test_topics.py` covers the alter path.

### Out of scope
- Removing a key from the declaration: this never deletes a topic config, it only sets declared
  values.
- Repartitioning: permanent by ADR-0002, and refused here.
- The projector's targets and volumes: #80.

## Acceptance criteria

1. **Given** an existing topic whose `retention.ms` differs from the declaration **when**
   `make topics-apply ANDARA_ENV=<env>` runs **then** the topic's `retention.ms` equals the
   declared value, the output has one line
   `topics: altered <topic> retention.ms <old> -> <new>`, and `make topics-diff` then exits 0.
2. **Given** no drift **when** `topics-apply` runs **then** no alter call is made, nothing is
   printed per topic, and it exits 0 (idempotent).
3. **Given** a topic whose partition count differs from the declaration **when** `topics-apply`
   runs **then** it exits 1 with
   `topics: <topic> has <live> partitions, declared <declared>; repartitioning is refused`, and it
   alters nothing on any topic (the check runs before any alter).
4. **Given** the local Redpanda **when** `topics-apply` runs against a declaration with
   `min.compaction.lag.ms` **then** it prints
   `topics: local broker does not report min.compaction.lag.ms, min.insync.replicas; skipped` once
   and exits 0.
5. **Given** an alter call that the broker refuses **when** `topics-apply` runs **then** it exits 1
   naming the topic, the key, and the broker's error, and prints which alters had already applied.
6. **Given** `ANDARA_ENV=dev` on the box **when** `topics-apply` runs after AW-SRV-019's lag
   setting **then** `andara.state.v1` reports `min.compaction.lag.ms=60000` and `topics-diff
   ANDARA_ENV=dev` exits 0.

## Interface contract

- `make topics-apply [ANDARA_ENV=<local|dev|prod>]`: unchanged invocation; creates missing
  topics, then aligns `COMPARED` keys on existing ones.
- Output lines, one per change: `topics: created <topic>` (existing) and
  `topics: altered <topic> <key> <old> -> <new>` (new).
- Exit codes: `0` in line with the declaration; `1` on any failure (refused partition drift, an
  invalid declaration, a broker refusal, no broker reachable), as every `topics.py` failure exits
  today. The message names which.
- Order: validate the declaration, then refuse partition drift across all topics, then create,
  then alter.
- No new environment variables. `COMPARED` stays the single list of keys this tool owns.

## Data / state impact

Alter-configs is online and needs no restart. Lowering `retention.ms` makes the broker delete
older segments on its next cleanup. That's the declared intent, and it's why the output names every
change. Rollback is reverting the line in `topics.yaml` and re-running `topics-apply`.

## Observability requirements

- **Metrics / Traces / Alerts:** none. It's a CLI tool over the broker.
- **Logs:** one stdout line per change as above; errors on stderr prefixed `make: topics:`
  (existing).

## Test plan

- **Unit:** `test_topics.py` with the rpk runner faked: an alter for each `COMPARED` key, no call
  on a clean state, partition drift refused before any alter, the unreported-key skip, and a
  broker refusal mid-run naming the alters already applied.
- **Integration:** the `stack` workflow sets `retention.ms` on one topic out of line with
  `rpk topic alter-config`, then runs `make topics-apply` and `make topics-diff` and asserts exit 0.
- **Manual/operator (box, in Brian's session with AW-INF-014):**
  ```
  make topics-diff ANDARA_ENV=dev     # names min.compaction.lag.ms on andara.state.v1
  make topics-apply ANDARA_ENV=dev    # "altered andara.state.v1 min.compaction.lag.ms … -> 60000"
  make topics-diff ANDARA_ENV=dev     # exit 0
  ```

## Definition of done

CLAUDE.md §8, plus: #77 is closed by the merging PR, and `values.yaml`'s "operator step, not a
make target" comment is replaced by the target.

## Open questions

None.
