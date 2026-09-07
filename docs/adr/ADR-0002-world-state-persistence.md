---
id: ADR-0002
title: World state persistence — Kafka as the distributed write-ahead log with projected read layers
status: accepted
date: 2026-09-07
deciders: [brian]
gates: []
supersedes_draft_of: 2026-09-07
---

## Context

The simulation core holds World state and advances it on a Tick Loop. Something must make that state
durable, ordered, and recoverable, and something must let tooling read World state without stopping
the World.

The earlier draft of this ADR framed the choice as in-process log-and-snapshot versus a
database-backed authority. Brian has taken a third path that subsumes both: **a Kafka layer as the
central distributed write-ahead log that every process reads and writes, with a split read/write
path and databases acting as indexes built from that log.**

This is a strictly better shape than either drafted option, for a reason worth stating plainly: the
log stops being a persistence implementation detail and becomes the system's ordering authority. Once
order lives outside any single process, the single-process assumption in ADR-0001 becomes a
deployment choice rather than an architectural one. That is the property that makes the eventual move
to zone-sharded simulation a partition-reassignment exercise instead of a rewrite.

## Decision

### 1. The log is the authority on order; memory is the authority on state

Kafka holds the ordered Command log. The simulation holds World state in memory, derived by applying
that log. Neither alone is the source of truth: the log defines *what happened and in what order*,
in-memory state is *the current consequence of it*, and a snapshot is a checkpoint that lets us skip
the replay.

### 2. Split read/write path

**Write path.** Gateway → parse → authorize → **produce to Kafka** → ack the client as *accepted*.
The client is told its Command was durably ordered, not that it succeeded.

**Tick path.** The simulation process consumes its Zone partitions and applies records in offset
order → validate → apply → emit Events.

**Read path (in-game).** The tick's own reads are against in-memory state. They are instant and
authoritative. The game path does not read from Redis or Postgres.

**Read path (everything else).** Projections consume the Event topic and materialize into Redis
(hot per-Character and per-Room state), Postgres (tabular: accounts, rosters, admin and Builder
queries), and later ClickHouse (columnar analytics). These are indexes. They are never authoritative
and are never written to directly.

The distinction in the third and fourth bullets is load-bearing and easy to lose. "Split read/write
path" does not mean the game reads from a cache — a player who moves north and sees a stale room
description is a bug we would have designed in. The fast read layer serves tooling, out-of-session
queries, analytics, and — once ADR-0001's sharding is live — cross-shard reads, which is a shard's
only way to see state it does not own.

### 3. The Command is durable before it is applied, by construction

The tick only ever sees Commands that are already in the log — that is what "consume" means. So the
old rule ("append before emitting Events") is unnecessary: a player can be told the outcome the
moment `apply` returns, with no produce-ack on the emit path.

Events are **derived**, not primary. They are produced to Kafka asynchronously for projections and
audit. Losing an unproduced Event is recoverable, because replaying the Commands regenerates it
identically. This is the first concrete dividend of the determinism requirement, and it removes the
durability write from the tick's critical path entirely.

### 4. Tick boundaries are recorded, not re-derived

A tick applies "whatever records were available when it started," which is timing-dependent and
therefore not replayable. To close that hole, each tick emits a control record:

```
TickCompleted{ tick, state_version, partition_offsets: {partition -> offset}, state_hash }
```

Replay does not re-decide tick boundaries; it reads them. This makes replay exact rather than
approximately-exact, and it makes `state_hash` a checkpointed assertion rather than a test-only
affordance. The Event topic is therefore not purely derivative — it carries the tick-boundary
decisions — and its retention policy must reach back at least as far as the oldest snapshot.

### 5. Topic topology

| Topic | Key | Cleanup | Purpose |
|-------|-----|---------|---------|
| `andara.commands.v1` | `ZoneID` | retention, tiered | the WAL. Per-partition order is the World's order. |
| `andara.events.v1` | `ZoneID` | retention | derived Events plus `TickCompleted` control records |
| `andara.state.v1` | aggregate key | **compact** | current state per aggregate; what projections read (§5.5) |
| `andara.audit.v1` | `ActorID` | retention, long | privileged GM/Operator actions |
| `andara.accounts.v1` | `AccountID` | compact | Account state — **not** World state (ADR-0006) |
| content topics | — | compact | see ADR-0004 |

Snapshots are **not** a Kafka topic. They are large, binary, and read once at recovery; object
storage keyed by `{zone}/{state_version}/{offset}` is the right home, with the manifest recorded in
`andara.events.v1` so recovery can find them by reading the log it already reads.

### 6. Partition count is chosen once, now, and over-provisioned

Partition count on `andara.commands.v1` is the **maximum future shard count**. Repartitioning a keyed
topic reorders history and is not something we will do to a live world. Zones map to partitions by
`hash(ZoneID) % partitions`; a process owns a set of partitions.

**Decision: 64 partitions.** At launch one process owns all 64. That is not expensive — partitions
are cheap on the broker and a consumer handles dozens without difficulty — and it means sharding
later is a consumer-group rebalance, not a data migration.

### 7. Backend choices, staged

- **Kafka** in production. **Redpanda** locally: Kafka-API-compatible, one binary, no coordination
  ceremony, which keeps `make up` honest.
- **The state projector and `andara.state.v1`** from M2, before any index, because every index reads
  from it.
- **Redis** from M2, as the hot projection.
- **Postgres** from M3, when Builder and Operator tooling arrives.
- **ClickHouse** deferred until there is an analytical query that Postgres handles badly. Adding it
  then is a new projector against a log we already have — which is the entire point of this
  architecture. Adding it now is a second datastore to operate for no current question.

## Consequences

**Kafka availability becomes World availability.** This is the significant new cost, and it did not
exist in either drafted option. If the log is unreachable, the World cannot accept Commands. The
mitigations are real but they are work: `acks=all` with `min.insync.replicas=2`, a documented
read-only degradation mode where the tick continues and the Gateway rejects new Commands with a typed
error, and Kafka's own availability becoming a Phase 1 SLO. This must be in the runbook before the
first deploy, not after the first outage.

**Produce latency is on the player's input path**, not the tick. A player's Command waits for a
produce ack before being acknowledged — 5–20 ms in a healthy cluster. At MUD tick rates that
disappears into the tick interval. It would not at 60 Hz, which is one more reason the Tick Rate
question matters.

**Cross-Zone effects always go through the log**, including when both Zones are in the same process.
Shortcutting the same-process case would make behavior differ between sharded and unsharded
deployments, which is precisely the bug class this design exists to prevent. The cost is a log
round-trip on every zone crossing; the benefit is that sharding changes nothing observable.

**We are foreclosing** ad-hoc SQL against authoritative World state, permanently, and we are
foreclosing any design where a process mutates World state it did not consume from the log.

**Index rebuild becomes routine rather than heroic.** With `andara.state.v1`, rebuilding Redis or
Postgres from scratch is bounded by live entity count. That changes the operational posture: a
projection schema change is "rebuild into a new key prefix and cut over," which is why the projection
schemas can be shaped unapologetically for queries.

**The state projector is a new component on the M2 critical path**, and a lagging one silently serves
stale answers to every index behind it. Its lag is a first-class metric and its digest assertion
against `TickCompleted.state_hash` is what turns a silent divergence into an alert.

**Exactly-once matters and is not free.** A Command consumed and applied but not checkpointed will be
re-applied after a crash. The tick must be idempotent with respect to re-applied offsets, or the
offset commit must be atomic with the state change. The chosen mechanism: state is checkpointed with
the offsets that produced it (§4), and recovery resumes strictly from the checkpointed offset. Any
Command between the checkpoint and the crash is re-applied — deterministically, to the same state —
which is correct precisely because apply is a pure function.

**Snapshot format migration** remains the hard part. `state_version` is on every snapshot and every
`TickCompleted` record from the first one written.

## Revisit when

- Produce p99 on the input path exceeds 25% of the tick interval.
- A single partition's consume-and-apply rate approaches the tick budget, which is the signal that
  ADR-0001's sharding should be activated.
- Kafka availability, measured, falls below the Session availability target it now bounds.
- Recovery time from snapshot plus log tail exceeds the RTO target.
- `andara.state.v1` compaction cannot keep up with state churn, making a "compacted" topic effectively
  a second full history.
