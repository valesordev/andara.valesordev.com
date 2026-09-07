---
id: AW-SRV-018
title: Postgres tabular projection for accounts, rosters, and builder queries
epic: EPIC-10
component: server
type: feature
status: draft
size: M
depends_on: [AW-SRV-017]
blocks: []
assignee: cursor
risk: medium
---

> `status: draft` — unblocked by ADR-0002 and scoped. Groomed to `ready` when M3 approaches, alongside
> the Builder and Operator commands that are its only consumers.

## Context

ADR-0002 stages Postgres at M3, when Builder and Operator tooling arrives and there are real queries to
serve. It is the tabular index: Accounts, Character rosters, content version history, audit search — the
questions that want joins and predicates rather than key lookups.

It follows `AW-SRV-017` deliberately. The projector shape, the idempotency requirement, the rebuild
procedure, and the staleness-reporting rule are all established there; this story copies a proven pattern
rather than inventing a second one.

## User story

As an operator, I want to answer questions about accounts, characters, and content history with a real
query, so that investigating something does not mean reading a log by hand.

## Scope

### In scope
- A projector consuming `andara.state.v1`, `andara.accounts.v1`, and `andara.audit.v1` in consumer group
  `andara-projection-pg-<env>`. World state comes from the compacted state topic (`AW-SRV-019`), for the
  same rebuild-cost reason as `AW-SRV-017`; the Account and audit topics are consumed directly, being
  already compacted or already the shape a query wants.
- Relational schema and its migrations, for the derived tables only.
- The same idempotency, checkpointing, rebuild, and staleness-reporting contract as `AW-SRV-017`.
- Write isolation to a single component, by credentials.
- Indexes chosen against the actual queries `andara-cli` issues, not speculatively.

### Out of scope
- ClickHouse. Added when there is an analytical query Postgres handles badly — at which point it is a new
  projector against a log that already exists, which is the entire point of the architecture.
- Any authoritative state. Nothing may read Postgres to make a game decision.
- The in-game read path.

## Acceptance criteria (known now; completed at grooming)

1. **Given** an Account created on `andara.accounts.v1` **when** the projector applies it **then** it is
   queryable in Postgres within the lag budget, with credential material absent from every column.
2. **Given** a content version published **when** the projector applies it **then** the pack's full version
   history is queryable, including which version was active at a given time.
3. **Given** an empty database **when** the projector rebuilds **then** it reaches a correct state and the
   procedure is documented and exercised.
4. **Given** a query **when** it is answered **then** the response carries the projection's offset, so a
   caller can tell how stale it is.
5. **Given** any component other than the projector **when** it attempts to write **then** it fails on
   credentials.
6. **Given** the projector is stopped **when** the World continues ticking **then** nothing in the game is
   affected.
7. **Given** a schema migration **when** it runs **then** it is reversible, or the story documents why not
   and what the recovery is — which for a projection is always "rebuild from the log", making this the one
   place in the system where migrations are genuinely low-risk.

## Interface contract

To be written at grooming, driven by the queries `AW-CLI-002`, `AW-CLI-003`, and the Operator commands
actually need.

## Data / state impact

Everything here is derived and disposable. AC-7 is worth dwelling on: because the log is authoritative,
a bad migration is recoverable by rebuilding rather than by restoring a backup. That is a materially
better position than the database-backed architecture ADR-0002 rejected, and it is the reason to keep the
schema unapologetically shaped for queries rather than for durability.

Credential material must never reach a column. AC-1 asserts it because a projection of an Account topic is
exactly where a hashed password would end up by accident.

## Observability requirements

- **Metrics:** the `AW-SRV-017` set with `projection="postgres"`, plus
  `andara_projection_query_duration_seconds` (histogram, label `query`) — bounded because queries are
  authored, not user-supplied.
- **Logs:** as `AW-SRV-017`, plus migration start and completion.
- **Traces:** `projection.apply` per batch; `projection.query` per query from tooling.
- **Alerts:** `ProjectionStale` with `projection="postgres"`, same lower severity as Redis, same runbook
  pattern.

## Test plan

The `AW-SRV-017` suite repeated for Postgres; a credential-absence test scanning every column for AC-1;
migration reversibility; a rebuild asserting the result matches a projector that consumed incrementally.

## Definition of done

CLAUDE.md §8, plus: the credential-absence test, and a rebuild exercised in CI.

## Open questions

- `[NEEDS BRIAN]` Whether Postgres is managed or self-hosted, which changes the operational half of this
  story but not the projector.
- Indexes should be added when a query needs them, with the query named in the migration comment.
  Speculative indexing on derived data that can be rebuilt is a cost with no corresponding benefit.
- `[ASSUMPTION]` One projector process serving all three source topics, rather than three. Three would
  isolate failures and triple the operational surface for a projection that is already disposable.
