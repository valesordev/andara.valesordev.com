---
id: AW-CLI-003
title: andara-cli content publish, approve, activate, rollback, history, diff, and fetch
epic: EPIC-05
component: cli
type: feature
status: review
size: M
depends_on: [AW-CLI-001, AW-CLI-002, AW-CLI-006, AW-SRV-013, AW-SRV-021]
blocks: [AW-INF-021, AW-INF-023]
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
- `content approve <pack> <version> [--yes]`, `content activate <pack> <version> [--yes] [--override
  --reason]`, `content rollback <pack> [--to N] [--yes]`. An Operator's approval of their own publish
  asks first (ADR-0004, amended 2026-09-26).
- `server info`, over `Admin.GetServerInfo`: build, protocol range, and the content in effect, one
  pack per line. The demo and `AW-INF-021` read it at every step.
- `content history <pack>` and `content diff <pack> <N> <M>` at the level of Zones, Rooms, Exits,
  Templates, and Component fields — a semantic diff over decompiled source, never a byte diff.
- `content fetch <pack> <version> [--out DIR]` — the published source blobs (ADR-0009), over
  `GetVersion` and `GetBlob`. It supersedes the `decompile --pack/--version` that `AW-CLI-006` left
  here: the source is always published, so there's nothing to decompile.
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
   `FAILED_PRECONDITION` as `town@8 needs approval by a second builder holding the pack or an operator
   (published by <user>)` and exits `1` with `error.code` `unapproved`.
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
11. **Given** an Operator approving a version whose author is the Operator, or the Account the
    Operator is acting as **when** `content approve town 8` runs on a TTY **then** it asks
    `You published town@8. Approve it yourself as <operator>? [y/N]` before any `ApproveVersion`; on a
    non-TTY without `--yes` it exits `2` with `--yes required`. On success it prints
    `town@8 approved by <operator> (self-approval: you published it)`, taken from the response's
    `self_approval`. Any other approval prints `town@8 approved by <user>`.
12. **Given** `activate` or `rollback` refused under `AW-SRV-013` AC-14 **when** it runs **then** it
    prints `town@8 refused: <reason> (<subject>, …)`, for example `town@8 refused: zone_removed
    (docks)`, and exits `1`, with `error.code` the reason and `error.detail.subjects` the list.
13. **Given** a server whose content in effect is `andara.core@1` and `town@8` **when**
    `andara-cli server info` runs **then** it prints `version`, `commit`, `environment`,
    `protocol <min>-<max>`, one `content <pack>@<version>` line per pack sorted by pack, and
    `content_digest <hex>`, and exits `0`. `--output json` carries `content` as an array of
    `{pack, version}`. Server unreachable is exit `3`.
14. **Given** an Operator **when** `content rollback andara.core --yes` runs **then** the core pointer
    moves back with no approval (`AW-SRV-013` AC-11). **When** `content publish` is run on a pack
    declaring `pack andara.core` **then** it prints the server's refusal,
    `andara.core is published by the server at boot`, and exits `1`.

## Interface contract

```
andara-cli content publish  [--path DIR] [--pack ID]              # pack from `pack` declaration unless overridden
andara-cli content approve  <pack> <version> [--yes]
andara-cli content activate <pack> <version> [--yes] [--override --reason TEXT]
andara-cli content rollback <pack> [--to N] [--yes]               # default: previous active
andara-cli content history  <pack> [--limit N]
andara-cli content diff     <pack> <N> <M>
andara-cli content fetch    <pack> <version> [--out DIR]
andara-cli server info
```

| Exit | Meaning |
|-----:|---------|
| `0` | done |
| `1` | diagnostics (local or server), or the server refused: approval, permission, stale parent, activation refusal. `error.code` is the server's `ErrorInfo.reason` |
| `2` | usage, missing `--yes`/`--reason` |
| `3` | server unreachable |
| `4` | timeout |

This is `AW-CLI-001`'s shared taxonomy, which `admin/cli/errors.go` already implements.
*(Amended 2026-09-28: the draft used `4` for "server refused", which `AW-CLI-001` reserves for a
timeout.)* The `error.code` values are `AW-SRV-013`'s reasons, pinned in its error taxonomy.

RPC mapping is one-to-one onto `AW-SRV-013`'s `Admin` methods, pinned in `admin.proto`. `server info`
maps to `GetServerInfo`. `--output json` applies to every
command with the `AW-CLI-001` envelope.

## Data / state impact

None locally beyond the compile cache. All state changes go through `AW-SRV-013`'s audited path.

## Observability requirements

Per `AW-CLI-001`, plus: `content.publish` span with `blobs_total`, `blobs_uploaded`, `bytes`;
`activate`/`rollback` log the confirmation text they showed, so an audit question can be answered from
the CLI's own trace as well as the server's record.

*(SRE observability review, 2026-09-28.)*
- **Metrics:** none. The CLI is a short-lived process, and the server's counters (`AW-SRV-013`)
  are the record.
- **Traces:**
  - Each command's `cli.command` span carries `pack` and `version` attributes. It propagates
    `traceparent` on every RPC, so one trace runs from the Builder's command to the server's
    `content.activate`, and from there to the Loader's `content.swap` (`AW-INF-021` AC-4).
  - `content.publish`'s blob uploads are one child span per `PublishBlob` stream, never one per
    chunk.
  - `--override` sets `override=true` and `reason` on the span.
- **Logs:**
  - The confirmation text is logged at `info` with the `trace_id` the CLI sent, so the CLI's line
    and the server's audit record join on one ID.
  - `--output json` includes `trace_id` in the envelope, so an operator can go from a Builder's
    report to the trace.

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
  And Brian's path on `dev` (ADR-0004, amended 2026-09-26), as one Operator:
  ```
  andara-cli --as <builder> content publish --path ./brian   # brian@1, awaiting approval
  andara-cli content approve brian 1                         # prompt → y; "(self-approval: you published it)"
  andara-cli content activate brian 1 --yes
  andara-cli server info                                     # content brian@1
  ```

## Definition of done

CLAUDE.md §8, plus: the two-identity rehearsal is a scripted CI job. A single Builder still can't
approve their own work, and only an Operator's self-approval (AC-11) lets one person pass.

## Open questions

- **Resolved 2026-09-07 (Brian):** text language (ADR-0009); second approver; in-game building is
  `EPIC-11`.
- **Resolved 2026-09-10 (Brian):** ADR-0010 accepted; the grammar moved to `AW-CLI-005` and the
  compiler to `AW-CLI-006` on 2026-09-11 so this story is the commands only.

## Contract review (architecture, 2026-09-28)

A contract change after `ready`, recorded here (CLAUDE.md §6). Implementation hasn't started. SRE's
observability review is in `docs/feedback/AW-SRV-013-operator-self-approval.md`.

1. **Operator self-approval** (ADR-0004, amended 2026-09-26): AC-11. The CLI asks before the RPC,
   comparing the manifest's author with the caller and the `--as` Account. The printed line comes
   from the server's `self_approval`, so the CLI never claims what the server didn't record. AC-3's
   message gains "or an operator".
2. **Exit `4` was a timeout.** `AW-CLI-001` defines `4` as timeout, and `admin/cli/errors.go`
   implements that. The draft's "server refused" is exit `1` with the reason as `error.code`.
   `AW-SRV-035` had the same mistake, and it's fixed there too.
3. **`server info` wasn't carried by any story**, though `AW-SRV-012`'s test plan, `AW-INF-021` and
   the SPRINT-03 demo read it (`docs/feedback/AW-SRV-013-activation-refusals.md` item 2). It's here now:
   AC-13.
4. **Activation refusals print their reason and subjects** (AC-12), from `AW-SRV-013` AC-14's
   `ActivationRefusal` detail.
5. **`andara.core`**: an Operator rolls its pointer back with the same command (AC-14). That's the
   first step of `AW-SRV-013`'s image-rollback order. A publish of core is refused by the server.
6. **`fetch` and `diff` read over `GetBlob`**, which `AW-SRV-013` now pins. `fetch` supersedes the
   `decompile --pack/--version` that `AW-CLI-006` deferred here.

## Implementation record (2026-09-30)

On `impl/aw-cli-003-content-publish`. The commands are in `admin/cli/contentpublish.go`,
`contentdiff.go` and `servercmd.go`. `publish` reuses `content validate`'s local compile and
validation, and places the server's findings on the source with the compiler's source map. The
tests run against `contentStack`: the gateway, AW-SRV-013's Admin, the Account store, and a Loader
following pointer moves through an Engine. The same rehearsal also runs over Redpanda. Decisions
the contract didn't make are in `docs/feedback/AW-CLI-003-content-publish.md`.

| AC | Covered by | Result |
|----|------------|--------|
| 1 | `TestContentPublishPath_TwoIdentities`: `town@2 published (parent 1), awaiting approval`. The first publish uploads every blob; the same source again uploads none. The pointer doesn't move. Mutation-checked: uploading without `HasBlobs` fails it | pass |
| 2 | the same test: `activate town 1 --yes` as the publisher once bob approves, then `server info` polled until it shows `content town@1`, within `reload_debounce + 2 s` | pass; `dev` itself waits on AW-INF-021 (feedback, For SRE 2) |
| 3 | the same test: exit 1, `unapproved`, `town@1 needs approval by a second builder holding the pack or an operator (published by alice)` | pass |
| 4 | the same test: `rollback town --yes` restores `town@1`; `history` shows 4 published and inactive; 4 is activated again | pass |
| 5 | `TestContentPublish_ServerRefusalPrintsLikeLocal`: a pack valid alone, whose Zone collides with `town`, is refused by the server. `duplicate_zone` is printed as `z.aw:1:1`, in the local format, exit 1. Server unreachable is exit 3 | pass |
| 6 | the rehearsal's `history`: four versions newest first, `*` on the active one, author, approver, every active interval | pass |
| 7 | the rehearsal's `diff town 1 4`: a changed title, an added Room, an added Exit, each with `(town.aw:N)`. Identical versions say so | pass |
| 8 | the rehearsal's `fetch town 4`: the sources compile to exactly the manifest's blobs. They must be in a directory with the name they were published from, and `fetch` defaults to it (feedback, For architecture 4) | pass |
| 9 | `TestContentActivate_UsageBeforeAnyRPC`: `activate` and `rollback` on a non-TTY without `--yes` exit 2, `yes_required`, `--yes required`; the pointer doesn't move. Mutation-checked | pass |
| 10 | the same test: `--override` without `--reason` exits 2 with no server configured | pass |
| 11 | `TestContentApprove_OperatorSelfApproval`: non-TTY without `--yes` exits 2. On a TTY it asks `You published town@1. Approve it yourself as oper? [y/N]`; `n` changes nothing, and `y` prints `town@1 approved by oper (self-approval: you published it)`. Another's approval asks nothing | pass; compares with the caller only (feedback, For architecture 1) |
| 12 | `TestContentActivate_RefusalReasonAndSubjects`: `town@2 refused: zone_removed (purgatory)`, exit 1, `error.code` `zone_removed`, `error.detail.subjects` `[purgatory]` | pass |
| 13 | `TestServerInfo`: the lines in order, `content andara.core@1`, `content town@1`, a 64-hex digest. JSON `content` is `[{pack, version}]`. Unreachable is exit 3 | pass |
| 14 | `TestContent_CoreRollbackAndPublish`: an Operator rolls `andara.core` from 2 back to 1 with no approval. Publishing a pack declaring `andara.core` prints `andara.core is published by the server at boot`, exit 1 | pass |

`TestContentPublishPath_TwoIdentitiesOverRedpanda` passes against the local Redpanda.

**Instrumentation**, asserted by `TestContentPublish_Spans`:
- `content.publish` carries `blobs_total`, `blobs_uploaded` and `bytes`, with one
  `content.publish_blob` child per stream.
- `cli.command` carries `pack` and `version`, and, for `--override`, `override`, `reason` and the
  confirmation text.
- The confirmation is logged at `info` with the trace ID.
- `trace_id` is in every JSON result and error. `TestContentActivate_RefusalReasonAndSubjects`
  checks it on a refusal.

`make check` passes.

**Not done here, and why:**
- **The Redpanda rehearsal isn't in CI yet.** `make test-integration` doesn't list `./admin/cli/`,
  and the Makefile is SRE's (feedback, For SRE 1).
- **`--as`:** acting-as doesn't exist for Admin RPCs (feedback, For architecture 1).
