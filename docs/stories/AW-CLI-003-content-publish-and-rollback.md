---
id: AW-CLI-003
title: andara-cli content publish, approve, activate, rollback, history, diff, and fetch
epic: EPIC-05
component: cli
type: feature
status: ready
size: M
depends_on: [AW-CLI-001, AW-CLI-002, AW-CLI-006, AW-SRV-013, AW-SRV-021]
blocks: []
lane: implementation
risk: medium
---

## Context

This is the Builder's whole workflow. ADR-0004 put content in the data layer so Builders need no
repository access; this command is how they reach it. The language is `AW-CLI-005`, the compiler is
`AW-CLI-006`, and the server side is `AW-SRV-013`; what is left here is the commands — and the one
property ADR-0004 asks the CLI to keep visible, that last-pointer-move-wins is the whole concurrency
model.

Activation requires a second approver (2026-09-07). This command **surfaces** approval state and renders
the server's rejection; it enforces nothing, because the server is the security boundary.

## User story

As a builder, I want to write a Zone in a format I can read, publish it, see it live, and roll it back if
I was wrong, so that world-building is a fast loop rather than a release process.

## Scope

### In scope
- `content publish [--path DIR]` — compile, validate locally, `HasBlobs`, stream missing blobs,
  `PublishVersion`; prints the version and `awaiting approval`.
- `content approve <pack> <version>`, `content activate <pack> <version> [--yes] [--override --reason]`,
  `content rollback <pack> [--to N] [--yes]`.
- `content history <pack>` and `content diff <pack> <N> <M>` at the level of Zones, Rooms, Exits,
  Templates, and Component fields — a semantic diff over decompiled source, never a byte diff.
- `content fetch <pack> <version> [--out DIR]` — the published source blobs (ADR-0009).
- Confirmation on live-world mutations: `activate` and `rollback` print pack, both versions, publisher,
  approver, and the current pointer, and wait unless `--yes`.

### Out of scope
- Server-side rules — `AW-SRV-013`. Language, compiler — `AW-CLI-005`, `AW-CLI-006`.
- In-game building — `EPIC-11`.
- Conflict resolution. `publish` sets `parent_version` to what `history` last showed; a stale parent is
  the server's `FAILED_PRECONDITION`, which this command explains rather than retries.

## Acceptance criteria

1. **Given** source in `./town` **when** `content publish` runs **then** it compiles, validates,
   uploads only blobs `HasBlobs` reported absent (asserted by upload count), calls `PublishVersion`, and
   prints `town@8 published (parent 7), awaiting approval` — without moving the pointer.
2. **Given** a published version **when** `content activate town 8 --yes` runs as the approver **then**
   the pointer moves and `andara-cli server info` shows `town@8` within `content.reload_debounce + 2 s`.
3. **Given** `activate` of an unapproved version **when** it runs **then** it prints the server's
   `FAILED_PRECONDITION` as `town@8 needs approval by a second builder holding the pack (published by
   <user>)` and exits `4`.
4. **Given** a bad activation **when** `content rollback town --yes` runs **then** the previous active
   version is active again, `history` shows `8` as published and inactive, and `8` can be re-activated.
5. **Given** local validation passing but the server rejecting **when** `publish` runs **then** the
   server's diagnostics print in the same format as local ones and the exit code is `1`; server
   unreachable is `3`.
6. **Given** `content history town` **when** it runs **then** every version ever published is listed with
   author, published time, approver, and active intervals, newest first, active marked `*`.
7. **Given** `content diff town 7 8` **when** it runs **then** output lists added/removed/changed Zones,
   Rooms, Exits, Templates, and Component fields, with `file:line` references into version 8's source.
8. **Given** `content fetch town 8` **when** it runs **then** the written files compile to the same
   blobs the manifest names (`AW-CLI-006` AC-4).
9. **Given** `activate` without `--yes` on a non-TTY **when** it runs **then** it exits `2` with `--yes
   required` rather than hanging.
10. **Given** `--override` without `--reason` **when** `activate` runs **then** exit `2` before any RPC.

## Interface contract

```
andara-cli content publish  [--path DIR] [--pack ID]              # pack from `pack` declaration unless overridden
andara-cli content approve  <pack> <version>
andara-cli content activate <pack> <version> [--yes] [--override --reason TEXT]
andara-cli content rollback <pack> [--to N] [--yes]               # default: previous active
andara-cli content history  <pack> [--limit N]
andara-cli content diff     <pack> <N> <M>
andara-cli content fetch    <pack> <version> [--out DIR]
```

| Exit | Meaning |
|-----:|---------|
| `0` | done |
| `1` | diagnostics (local or server) |
| `2` | usage, missing `--yes`/`--reason` |
| `3` | server unreachable |
| `4` | server refused: approval, permission, stale parent (message from the server's status details) |

RPC mapping is one-to-one onto `AW-SRV-013`'s `Admin` methods. `--output json` applies to every
command with the `AW-CLI-001` envelope.

## Data / state impact

None locally beyond the compile cache. All state changes go through `AW-SRV-013`'s audited path.

## Observability requirements

Per `AW-CLI-001`, plus: `content.publish` span with `blobs_total`, `blobs_uploaded`, `bytes`;
`activate`/`rollback` log the confirmation text they showed, so an audit question can be answered from
the CLI's own trace as well as the server's record.

## Test plan

- **Unit:** confirmation prompt logic (TTY, `--yes`); exit-code mapping from gRPC status; diff renderer
  over fixture versions.
- **Integration:** against a throwaway Redpanda with `AW-SRV-013`: publish-approve-activate-rollback as
  two identities (AC-1–4); stale parent; blob dedup by upload count; fetch round-trip (AC-8).
- **Manual/operator:** the Phase 1 exit criterion 4 rehearsal, as two users:
  ```
  A$ andara-cli content publish --path ./town      # town@8, awaiting approval
  B$ andara-cli content approve town 8
  A$ andara-cli content activate town 8            # prompt → y
  A$ andara-cli content rollback town              # prompt → y; town@7 active
  ```

## Definition of done

CLAUDE.md §8, plus: the two-identity rehearsal is a scripted CI job, because a single-person rehearsal
now fails by design.

## Open questions

- **Resolved 2026-09-07 (Brian):** text language (ADR-0009); second approver; in-game building is
  `EPIC-11`.
- **Resolved 2026-09-10 (Brian):** ADR-0010 accepted; the grammar moved to `AW-CLI-005` and the
  compiler to `AW-CLI-006` on 2026-09-11 so this story is the commands only.
