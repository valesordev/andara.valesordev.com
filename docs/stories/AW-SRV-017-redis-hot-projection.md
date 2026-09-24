---
id: AW-SRV-017
title: Redis hot projection from the state topic
epic: EPIC-10
component: server
type: feature
status: ready
size: M
depends_on: [AW-SRV-019]
blocks: [AW-SRV-018]
lane: implementation
risk: medium
---

## Context

ADR-0002's split read/write path puts read indexes behind the log. Redis is the hot one: current Character
locations, Room occupancy, and Zone summaries that tooling asks about constantly.

It reads from **`andara.state.v1`** (`AW-SRV-019`), not from `andara.events.v1`, so bootstrapping costs
live-state time rather than world-age time.

The rule that governs this story is the one ADR-0002 says is easiest to lose: **the in-game read path
does not read from Redis.** Redis serves tooling, out-of-session queries, and — once ADR-0001's sharding
is active — cross-shard reads. This story is also the template every future projector copies, so its
shape matters more than its content.

## User story

As an operator, I want to ask where a character is without touching the simulation, so that inspecting
the world costs the world nothing.

## Scope

### In scope
- `andara-projector redis` — consumer group `andara-projector-redis-<env>` over `andara.state.v1`.
- Key schema, versioned by prefix from the first key written.
- Idempotent application keyed on `(key, tick)`: an older record never overwrites a newer one.
- Staleness: every read path exposes the projection's last verified tick and age.
- `--rebuild`: flush the prefix, reset the group to earliest, reapply.
- Write isolation: one Redis ACL user with write; tooling users are read-only.
- `Admin.QueryProjection` and `andara-cli where <character>` / `andara-cli room <zone>/<id>` as the
  first consumers, so the index is exercised by the tooling it exists for.

### Out of scope
- The in-game read path, which never touches this.
- Postgres — `AW-SRV-018`. ClickHouse — deferred per ADR-0002.

## Acceptance criteria

1. **Given** a `character:<zone>/<id>` record at tick `T` **when** the projector applies it **then**
   `andara-cli where <id>` answers with the Room and `tick=T` within `projector.redis.lag_budget`.
2. **Given** the same record delivered twice, or an older tick delivered after a newer one **when**
   applied **then** the resulting keys equal the newer state; the older write is counted on
   `andara_projection_events_applied_total{outcome="stale_skipped"}`.
3. **Given** an empty Redis **when** `--rebuild` runs against 10,000 Entities **then** it completes in
   under 60 s and the keys equal an incremental projector's.
4. **Given** any query **when** it is answered **then** the response carries `projection_tick` and
   `projection_age_seconds`.
5. **Given** the tooling ACL user **when** it issues `SET` **then** Redis returns `NOPERM`.
6. **Given** the projector stopped **when** the World continues **then** no server metric changes.
7. **Given** a record **when** it is applied **then** its `content_version` is stored with the aggregate
   and returned by every query.
8. **Given** `AW-SRV-019` halted on divergence **when** this index is queried **then** the response
   carries `projection_status=diverged` and the CLI prints it before any answer.

## Interface contract

### Key schema (prefix `aw1:`)

| Key | Type | Value |
|-----|------|-------|
| `aw1:char:<id>` | hash | `zone`, `room`, `tick`, `content_version`, `dormant`, `linkdead` |
| `aw1:room:<zone>/<room>:occ` | set | entity IDs present |
| `aw1:room:<zone>/<room>` | hash | `tick`, `content_version`, `count` |
| `aw1:zone:<id>` | hash | `tick`, `rooms`, `entities`, `characters` |
| `aw1:meta` | hash | `tick`, `verified_unix`, `status` (`ok`/`diverged`/`rebuilding`), `schema=1` |

Each record applies as one `MULTI` block guarded by a Lua compare-and-set on `tick`.

```protobuf
// CONTRACT SKETCH — addition to andara/admin/v1/admin.proto
rpc QueryProjection(QueryProjectionRequest) returns (QueryProjectionResponse);
message QueryProjectionRequest { string projection = 1; string key = 2; }        // "redis", "character:<zone>/<id>"
message QueryProjectionResponse { bytes body = 1; uint64 projection_tick = 2; int64 projection_age_ms = 3;
                                  string projection_status = 4; string content_version = 5; }
```

### Configuration

| Key | Env | Default | Notes |
|-----|-----|---------|-------|
| `projector.redis.addr` | `ANDARA_REDIS_ADDR` | `localhost:6379` | |
| `projector.redis.user` / `password_file` | `ANDARA_REDIS_USER` / `…_PASSWORD_FILE` | `andara-projector` / — | write ACL |
| `projector.redis.lag_budget` | `ANDARA_REDIS_LAG_BUDGET` | `5s` | alert threshold |
| `projector.redis.prefix` | `ANDARA_REDIS_PREFIX` | `aw1:` | schema version |

### Exit codes

`0` clean · `1` config/connect · `2` upstream diverged (refuses to apply past the last verified tick).

## Data / state impact

Redis: persistence off (`save ""`, `appendonly no`), `maxmemory-policy noeviction` — an evicted key is
a silently wrong answer. Derived and disposable; AC-3 proves it. A schema change is a new prefix and a
cutover, never an in-place migration of derived data.

## Observability requirements

### Metrics
- `andara_projection_lag_seconds{projection="redis"}` — gauge.
- `andara_projection_events_applied_total{projection, outcome}` — `applied`, `stale_skipped`, `error`.
- `andara_projection_errors_total{projection, reason}`.
- `andara_projection_rebuild_duration_seconds{projection}`.
- `andara_projection_query_duration_seconds{projection, query}` — bounded, queries are authored.

### Logs
- `info` on start, checkpoint, rebuild; `error` naming key and offset on a record that cannot apply.

### Traces
- `projection.apply` per batch; `projection.query` per `QueryProjection`.

### Alerts
- `ProjectionStale{projection="redis"}` on lag over budget for 5 m, low severity; runbook
  `docs/runbooks/projection-stale.md` (shared) says it is a tooling problem.

## Test plan

- **Unit:** key mapping per `AggregateKind`; compare-and-set script under out-of-order ticks (AC-2).
- **Integration:** against a throwaway Redis — rebuild equality (AC-3), ACL (AC-5), diverged upstream
  propagation (AC-8); against `make up` — stop the projector, assert server metrics (AC-6).
- **Manual/operator:**
  ```
  make up && andara-projector redis
  andara-cli where <character_id>      # expect: zone/room, tick, age, content version
  andara-projector redis --rebuild     # expect: flush, replay, "rebuilt N keys in Ns"
  ```

## Definition of done

CLAUDE.md §8, plus: `--rebuild` exercised in CI; `andara-cli where` and `room` land with the projector.

## Open questions

- **Inherited from `AW-SRV-019` (2026-09-24):** `andara.state.v1` Entity keys name their Zone
  (`character:<zone>/<id>`), because an Entity changes Zone and a key must not move between Partitions.
  A cross-Zone move is a tombstone on the old key and a new key on the new Zone. `aw1:char:<id>` is
  therefore where this index joins the two: a tombstone for `character:<z>/<id>` clears
  `aw1:char:<id>` only when its `zone` is still `z`. A tombstone carries no `tick`, so the Zone
  comparison is what keeps a late tombstone from the old Partition from erasing the arrival on the
  new one.

- `[ASSUMPTION]` Lag budget 5 s: tooling that is five seconds behind a 10 Hz world is current for every
  operator question; the alert fires at sustained breach.
- `[ASSUMPTION]` Redis is self-hosted in the cluster (kind on Brian's box, 2026-09-10).
- Which further queries belong in Redis versus Postgres follows from the operator commands as they
  arrive; the two schemas above are the ones `andara-cli` needs first.
