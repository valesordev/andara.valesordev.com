---
id: AW-SRV-018
title: Postgres tabular projection for accounts, rosters, and builder queries
epic: EPIC-10
component: server
type: feature
status: ready
size: M
depends_on: [AW-SRV-017]
blocks: []
lane: implementation
risk: medium
---

## Context

ADR-0002 stages Postgres at M3, when Builder and Operator tooling arrives and there are real queries to
serve. It is the tabular index: Accounts, Character rosters, content version history, audit search — the
questions that want joins and predicates rather than key lookups.

It follows `AW-SRV-017` deliberately: projector shape, idempotency, rebuild, and staleness reporting are
established there and copied here.

## User story

As an operator, I want to answer questions about accounts, characters, and content history with a real
query, so that investigating something does not mean reading a log by hand.

## Scope

### In scope
- `andara-projector postgres` — consumer group `andara-projector-pg-<env>` over `andara.state.v1`,
  `andara.accounts.v1`, `andara.audit.v1`, `andara.content.versions.v1`, `andara.content.active.v1`.
- Schema `projection` with migrations under `server/projector/postgres/migrations/`, run by the
  projector at start.
- Idempotency via `(key, tick)` / `(topic, partition, offset)` upserts; staleness in a `projection_meta`
  table; `--rebuild` truncates and replays.
- Write isolation: `andara_projector` owns the schema; `andara_cli_ro` has `SELECT` only.
- The first authored queries, exposed via `Admin.QueryProjection{projection="postgres"}`:
  `account by username`, `roster by account`, `pack history`, `audit by actor/time`.

### Out of scope
- ClickHouse. Any authoritative state. The in-game read path.

## Acceptance criteria

1. **Given** an `Account` record **when** applied **then** `projection.accounts` has the row within
   `lag_budget`, and no column in the schema contains `credential`, `hash`, `salt`, or token bytes —
   asserted by planting known secrets and scanning `pg_dump`.
2. **Given** `ContentVersion` and `ActiveVersion` records **when** applied **then**
   `pack history <pack>` lists every version with `published_at`, `author`, `approved_by`, and the
   interval during which it was active.
3. **Given** an empty database **when** `--rebuild` runs **then** the result equals the incremental
   projector's row set, and the procedure is one command.
4. **Given** a query **when** answered **then** it carries `projection_tick` (state topics) and
   per-topic `offset` (account/audit topics) so staleness is reportable per source.
5. **Given** `andara_cli_ro` **when** it issues `INSERT` **then** Postgres returns `permission denied`.
6. **Given** the projector stopped **when** the World continues **then** no server metric changes.
7. **Given** a migration **when** it runs **then** it has a `down`; migrations that cannot be reversed
   are rejected by the migration test, because a projection's recovery path is always rebuild.
8. **Given** an audit record with `acting_as_account_id` set **when** queried by actor **then** it
   appears under both the actor and the acted-as identity.

## Interface contract

### Tables (schema `projection`)

| Table | Key | Source | Notes |
|-------|-----|--------|-------|
| `accounts` | `account_id` | accounts.v1 | `username`, `roles[]`, `status`, `created_at`, `agent_pack_id`; **no credential columns** |
| `characters` | `character_id` | accounts.v1 + state.v1 | `account_id`, `name`, `status`, `zone_id`, `room_id`, `tick`, `dormant` |
| `name_reservations` | `folded_name` | accounts.v1 | |
| `content_versions` | `(pack_id, version)` | versions.v1 | `parent_version`, `author`, `published_at`, `approved_by`, `approved_at`, `core_version` |
| `content_activations` | `(pack_id, activated_at)` | active.v1 | `version`, `activated_by`; interval derived |
| `audit` | `(partition, offset)` | audit.v1 | `actor_account_id`, `acting_as_account_id`, `action`, `target`, `session_id`, `trace_id`, `at` |
| `projection_meta` | `source` | — | `offset`, `tick`, `verified_at`, `status` |

Indexes: `characters(account_id)`, `audit(actor_account_id, at)`, `audit(acting_as_account_id, at)`,
`content_versions(pack_id, version desc)` — each named for the query that needs it in the migration
comment. Nothing speculative.

### Configuration

| Key | Env | Default |
|-----|-----|---------|
| `projector.postgres.dsn_file` | `ANDARA_PG_DSN_FILE` | — |
| `projector.postgres.lag_budget` | `ANDARA_PG_LAG_BUDGET` | `10s` |
| `projector.postgres.batch` | `ANDARA_PG_BATCH` | `500` rows per transaction |

Exit codes as `AW-SRV-017`, plus `5` migration failure.

## Data / state impact

Everything here is derived and disposable; a bad migration is recovered by rebuild, not backup. The
schema is shaped for queries, not durability. Credential material never reaches a column (AC-1) because a
projection of the Account topic is exactly where a hash would end up by accident: the projector maps
`Account` through an allow-list of fields, never a reflection dump.

## Observability requirements

- **Metrics:** the `AW-SRV-017` set with `projection="postgres"`, plus
  `andara_projection_query_duration_seconds{projection="postgres", query}` and
  `andara_projection_batch_rows` (histogram).
- **Logs:** as `AW-SRV-017`, plus migration start/complete with version.
- **Traces:** `projection.apply` per batch; `projection.query` per query.
- **Alerts:** `ProjectionStale{projection="postgres"}`, shared runbook.

## Test plan

- **Unit:** field allow-list for `accounts` (AC-1 static half); upsert idempotency; interval derivation
  for activations.
- **Integration:** against a throwaway Postgres — `pg_dump` secret scan (AC-1); rebuild equality (AC-3);
  `down` for every migration (AC-7); role permission (AC-5); acting-as query (AC-8).
- **Manual/operator:**
  ```
  make up && andara-projector postgres
  andara-cli account show --username brian       # expect: roles, characters, no hash
  andara-cli content history <pack>              # expect: versions with active intervals
  andara-cli audit --actor <account> --since 1h  # expect: rows include acting-as entries
  ```

## Definition of done

CLAUDE.md §8, plus: the credential-absence test, `--rebuild` in CI, and every migration reversible.

## Open questions

- `[ASSUMPTION]` Postgres self-hosted in the cluster (kind on Brian's box, 2026-09-10); the projector is
  unchanged if that moves to managed.
- `[ASSUMPTION]` One projector process for all five source topics; three would isolate failures and
  triple the surface for a disposable projection.
