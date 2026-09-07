# Persistence and content schemas

Snapshot format, Kafka record formats, content manifests, and the migration plan for each.

The canonical definitions are protobuf and live in `docs/specs/protocol/` (ADR-0007). This directory
holds what protobuf cannot express: versioning policy, migration procedures, and the content authoring
format that compiles to the canonical form.

## Two rules that apply from the first byte ever written

- Every snapshot carries a `state_version`. Every Event carries a `SchemaVersion`. Both are present from
  the first record, because adding them later means migrating a live World's history.
- Fields may be added within a version. Fields may not be removed or have their meaning changed without a
  version bump and a documented reader path for both versions.

## Why migration is unusually low-risk here — except where it isn't

ADR-0002 makes Kafka authoritative. That means **projection schemas** (Redis, Postgres) are derived and
disposable: a bad migration is recovered by rebuilding from the log, not by restoring a backup. Keep those
schemas shaped for queries.

**Snapshot format migration is the exception.** A snapshot is a checkpoint of authoritative state, and a
binary that writes a format its predecessor cannot read makes rollback impossible without replaying from
an older snapshot — survivable only if log retention reaches back far enough. This is the interaction
between `AW-SRV-006`, `AW-INF-007`, and the retention question left open in `AW-INF-004`.

## Planned

| Document | Story |
|----------|-------|
| snapshot format and migration procedure | `AW-SRV-006` |
| content authoring format specification | `AW-CLI-003` |
| projection schemas and rebuild procedures | `AW-SRV-017`, `AW-SRV-018` |

Nothing here yet.
