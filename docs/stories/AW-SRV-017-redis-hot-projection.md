---
id: AW-SRV-017
title: Redis hot projection from the event topic
epic: EPIC-10
component: server
type: feature
status: draft
size: M
depends_on: [AW-SRV-019]
blocks: [AW-SRV-018]
assignee: cursor
risk: medium
---

> `status: draft` — unblocked by ADR-0002 and scoped. Groomed to `ready` when M2 approaches.

## Context

ADR-0002's split read/write path puts read indexes behind the log. Redis is the hot one: current Character
locations, Room occupancy, and Session-adjacent state that tooling asks about constantly.

It reads from **`andara.state.v1`**, the compacted current-state topic built by `AW-SRV-019` — not from
`andara.events.v1` directly. That is the whole point of ADR-0002 §5.5: bootstrapping this index from the
full Event history would cost time proportional to the World's age, while bootstrapping from the compacted
topic costs time proportional to live state.

The rule that governs this story is the one ADR-0002 says is easiest to lose: **the in-game read path
does not read from Redis.** The tick reads in-memory state, which is instant and authoritative. A player
who moves north and sees a stale room description would be a bug we designed in. Redis serves tooling,
out-of-session queries, and — once ADR-0001's sharding is active — cross-shard reads, which is a shard's
only way to see state it does not own.

This story is also the template every future projector copies, so its shape matters more than its
content.

## User story

As an operator, I want to ask where a character is without touching the simulation, so that inspecting
the world costs the world nothing.

## Scope

### In scope
- A consumer of `andara.state.v1` in consumer group `andara-projection-redis-<env>`.
- Redis key schema and its documentation.
- Idempotent application: an Event applied twice produces the same state, since at-least-once delivery is
  what the consumer gives.
- Offset checkpointing and rebuild-from-scratch, which must be a routine operation rather than an
  emergency one.
- Projection lag as a first-class metric, because a stale projection presenting as fresh is the failure
  mode.
- Write isolation: exactly one component has write access, enforced by credentials.

### Out of scope
- The in-game read path, which never touches this.
- Postgres projection — `AW-SRV-018`.
- ClickHouse, deferred per ADR-0002 until there is an analytical query Postgres handles badly.

## Acceptance criteria (known now; completed at grooming)

1. **Given** a `CharacterArrived` Event **when** the projector applies it **then** the Character's location
   and the Room's occupancy are both queryable within the stated lag budget.
2. **Given** the same Event applied twice **when** the second application completes **then** the resulting
   state is identical. At-least-once delivery must be safe.
3. **Given** an empty Redis **when** the projector is told to rebuild **then** it consumes
   `andara.state.v1` from the earliest offset and reaches a correct state in time proportional to live
   entity count rather than to the World's age, and the procedure is documented as routine.
4. **Given** a projector that is behind **when** it is queried **then** the response carries the projection's
   offset or timestamp, so a caller can tell how stale the answer is. A projection that cannot report its
   own staleness is worse than no projection.
5. **Given** any component other than the projector **when** it attempts to write to Redis **then** it
   fails on credentials. The "never written to directly" rule is enforced, not documented.
6. **Given** the projector is stopped **when** the World continues ticking **then** nothing in the game is
   affected. A projection outage is a tooling outage.
7. **Given** a state record **when** it is applied **then** its `content_version_sha256` is carried into the
   index, so a query can report which content version defined what it is describing.
8. **Given** `AW-SRV-019` has halted on a digest divergence **when** this index is queried **then** it
   reports its staleness rather than serving answers it cannot vouch for.

## Interface contract

To be written at grooming. Committed now: the projector is a consumer of `andara.state.v1` and nothing
else. It never reads the Command log, never reads the Event topic, never calls the simulation, and never
writes to Kafka.

## Data / state impact

Redis holds derived state exclusively. It is disposable by design — losing it costs a rebuild, not data.
That property should be verified by actually flushing it in a non-production environment rather than
assumed.

Key schema needs a version prefix from the first key written, so a schema change is a rebuild into a new
prefix with a cutover, rather than an in-place migration of derived data nobody can validate.

## Observability requirements

- **Metrics:** `andara_projection_lag_seconds{projection="redis"}` (gauge — the metric that matters),
  `andara_projection_events_applied_total` (counter, label `type`),
  `andara_projection_errors_total` (counter, label `reason`),
  `andara_projection_rebuild_duration_seconds` (histogram).
- **Logs:** `info` on start, checkpoint, and rebuild; `error` on an Event the projector cannot apply,
  naming the Event type and offset rather than skipping silently.
- **Traces:** `projection.apply` per batch, not per Event.
- **Alerts:** `ProjectionStale` on lag, tied to an SLO. Its severity is deliberately lower than anything
  game-affecting, and the runbook must say so — an operator woken for a stale projection at 3am should
  know it is a tooling problem.

## Test plan

Idempotency under duplicate delivery; rebuild-from-empty asserting correctness against a known Event
stream; a stopped projector asserting no game impact; write-isolation asserting a credential failure.

## Definition of done

CLAUDE.md §8, plus: the rebuild procedure is a documented make target or CLI command, exercised in CI —
because a rebuild that has never been run is a rebuild that does not work.

## Open questions

- `[ASSUMPTION]` Redis persistence is off; it is a cache rebuilt from the log. Enabling persistence would
  make restarts faster and create a second thing that can be wrong.
- `[NEEDS BRIAN]` The projection lag budget, which determines the alert threshold and how much tooling can
  be trusted to be current.
- Which queries actually belong in Redis versus Postgres will be clearer once `andara-cli`'s operator
  commands exist. Splitting them by guess now would be premature.
