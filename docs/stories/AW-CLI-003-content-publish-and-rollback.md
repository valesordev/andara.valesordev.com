---
id: AW-CLI-003
title: andara-cli content publish, approve, activate, rollback, history, diff, and fetch
epic: EPIC-05
component: cli
type: feature
status: done
size: M
depends_on: [AW-CLI-001, AW-CLI-002, AW-CLI-006, AW-SRV-013, AW-SRV-021]
blocks: [AW-INF-021, AW-INF-023, AW-SRV-039]
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
11. **Given** an Operator approving a version whose author is the Operator **when** `content approve town 8` runs on a TTY **then** it asks
    `You published town@8. Approve it yourself as <operator>? [y/N]` before any `ApproveVersion`; on a
    non-TTY without `--yes` it exits `2` with `--yes required`. On success it prints
    `town@8 approved by <operator> (self-approval: you published it)`, taken from the response's
    `self_approval`. Any other approval prints `town@8 approved by <user>`. *(Amended 2026-09-30, §8
    review: "or the Account the Operator is acting as" moves to the Admin acting-as story, with
    `--as` on content commands. Acting-as over Admin isn't built.)*
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
  And Brian's path on `dev` (ADR-0004, amended 2026-09-26), as one Operator. It publishes as the
  Operator: `--as <builder>` moves to the Admin acting-as story (amended 2026-09-30).
  ```
  andara-cli content publish --path ./brian                  # brian@1, awaiting approval
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

## §8 review (architecture, 2026-09-30): stays `review`

Against `main` at `fa9911e`. Merged in #271. `check`, `stack`, `cli-release` and `determinism` are
green on `af5fd11`. `stack` run 36783844089 ran `TestContentPublishPath_TwoIdentitiesOverRedpanda`
in CI (3.4 s): `./admin/cli/` has been in `make test-integration` since #265. So the Definition of
done's scripted rehearsal is met, and the implementation record's "not in CI yet" is stale.
Re-run in this review: the story's `admin/cli` tests with `-race` and the Redpanda rehearsal (2.5 s).

| AC | Evidence (`admin/cli/publish_test.go`) | Result |
|----|----------|--------|
| 1, 3 | `testTwoIdentities`: all blobs, then 0 on repeat, exact lines, pointer unmoved; `unapproved` refusal | pass |
| 2 | same test, `awaitServing` polls `server info` within debounce + 2 s | pass. Weak: it activates as the publisher, not the approver |
| 4 | same test: rollback, `history` shows 4 inactive then re-activated | pass. Weak: `previousActive` iterating oldest-first survives, because two moves can't tell the orders apart |
| 5 | `TestContentPublish_ServerRefusalPrintsLikeLocal` | pass |
| 6 | the `history` regexes in `testTwoIdentities` | pass |
| 7 | the `diff` case: a Room title, an added Room, an added Exit | **gap.** No Zone, Template or Component-field case. Emptying `diffComponents` and the Template loop leaves the suite green (mutation-checked), and the test plan's diff-renderer unit test doesn't exist |
| 8 | `fetch`, compile, hashes equal the manifest's | pass, under the default `--out` name (ruling 4) |
| 9, 10 | `TestContentActivate_UsageBeforeAnyRPC` | pass |
| 11 | `TestContentApprove_OperatorSelfApproval`. Mutation-checked | pass, for the caller. The acting-as clause moves (ruling 1) |
| 12 | `TestContentActivate_RefusalReasonAndSubjects` | pass |
| 13 | `TestServerInfo` | pass |
| 14 | `TestContent_CoreRollbackAndPublish` | pass |

**#267's defence holds on the client.** `fetch` checks `filepath.IsLocal`, then writes through
`os.Root`. In a scratch test, `src/../x`, a doubled slash and `src/zz/../../x` are each refused
`unsafe_source_path`, and nothing lands outside `--out`. With either layer removed, the other still
refuses (mutation-checked). The repository has a symlink case but no `..` case.

**Contract rulings** on implementation's questions (`docs/feedback/AW-CLI-003-content-publish.md`),
recorded as the contract from here on:
1. **No `--as` on content commands: accepted for this story.** Acting-as over Admin is `andara-act-as`
   metadata, and that story isn't built (`AW-SRV-013` rulings 2 and 3). Sending the header now would
   be silently ignored, and the publish would land as the Operator. **Moving to that story:** `--as`
   on content commands, AC-11's "or the Account the Operator is acting as", and the test plan's
   `--as <builder>` line. There, the CLI compares with its own `--as` value, not a token's `act` claim.
   Until then, `isCaller` (`contentpublish.go`) reading `act` is dead code, harmless because no token
   carries one, and that story removes it. **The demo doesn't need it:** the Operator publishes as
   themselves, audited `override=true` on a pack they don't hold, then self-approves (AC-11).
2. **Accounts named by ID, the username only for the caller:** accepted. A username lookup is
   separate work, the same gap as `AW-SRV-035`'s.
3. **The CLI reading its own Account from the token, unverified:** accepted. It decides only
   whether to prompt. The server decides everything.
4. **AC-8 depends on the `--out` name:** accepted. Byte identity under any name is a change to
   `source.file`, an `AW-CLI-006` corpus decision. Not now.
5. **`publish`'s parent is the newest version, read just before `PublishVersion`:** accepted. It keeps
   stale-parent semantics and holds no local state.
6. **The smaller decisions:** accepted.

**Accepted deviations:** the self-approval prompt fires for any author, not only an Operator. The
token carries no roles, and with `--yes` a Builder still gets the server's `self_approval` refusal.
Chunks are `content.BlobChunkBytes` (1 MiB). That's correct by reading, and no fixture is big
enough to test it.

**Not holding the story** (implementation, feedback file):
- A `fetch` test with a `..` path. A `previousActive` test with three or more moves. AC-2's test
  activating as the approver. A blob over 1 MiB.
- The implementation record says the confirmation `info` line is asserted. It isn't, and at the
  default `--log-level warn` it isn't printed. SRE decides whether it needs a test.

**What closes it** *(revised on review of #275: items 2–4 were first filed as follow-ups)*:
1. **AC-7** (implementation): a diff test covering a Zone added and removed, a Template changed,
   and a Component field added, removed and changed, each with `file:line`. Mutation-checked against
   `diffComponents` and the Template loop.
2. **`fetch` and `diff` pass the server's reason through** (implementation). They go through
   `rpcError`, not `contentError`, so `error.code` is `server_error` for `not_found` and
   `permission_denied` for `pack_not_held`. The Interface contract's exit table says `error.code` is
   the server's `ErrorInfo.reason`. Owed with a test per command.
3. **The test plan's stale-parent integration case** (implementation). The only CLI test,
   `TestContentPublish_StaleParentIsExplained`, builds a synthetic error. Two publishes against one
   parent, over the Redpanda rehearsal, must show the second exiting 1 with `stale_parent` and its
   hint.
4. **`AW-SRV-035` AC-2's CLI half** (implementation): `content publish` by a Builder without the pack
   exits 1 with `error.code` `pack_not_held`. It's that story's AC, and the test lives in
   `admin/cli`. It closes both stories. *(Delivered in #281: `TestAccountSetPacks_PublishToAnUngrantedPackIsPackNotHeld`.)*
5. SRE's §8 instrumentation record, under `AW-CLI-002`'s ruling: CLI spans are verified in-process
   (`TestContentPublish_Spans`), and the backend join is `AW-INF-021`'s.
   *(SRE's record, #276: the spans are satisfied. What's still owed is the confirmation line's test,
   by implementation: `activate` and `rollback` with `--log-level info`, with the line decoded, its
   text checked, and its `trace_id` equal to the JSON result's and the server's. With that, the item
   is satisfied. The store-backed live path is `AW-INF-021`'s inherited line.)*

## §8 instrumentation check (2026-09-30, SRE): spans satisfied under architecture's ruling; the confirmation line's test owed; live store path carried to `AW-INF-021`

On `sre/sprint-03-srv035-cli003-verify`, against `main` at `fa9911e`.

| Signal | How | Observed |
|--------|-----|----------|
| `traceparent` to the server | `server info --output json` and `content history town --output json` on the compose stack | each JSON result or error carries `trace_id`. That ID resolves in local Tempo to the server's `Admin/GetServerInfo` or `Admin/ListVersions` span, parented by the CLI's `cli.command`, so the CLI's trace and the server's join |
| The Redpanda rehearsal | `TestContentPublishPath_TwoIdentitiesOverRedpanda` | pass (1.82 s). It runs in CI now: #265 put `./admin/cli/` in `make test-integration` (feedback, For SRE 1) |
| CLI spans | `TestContentPublish_Spans`, in `make test` | `content.publish` with `blobs_total`, `blobs_uploaded` and `bytes`, one `content.publish_blob` per stream, and `cli.command`'s `pack`, `version` and `override` attributes, all asserted in-process and run in CI. Under architecture's ruling (`AW-CLI-002` §8 review, 2026-09-30) that **is** the CLI span's verification. Its backend half is the server's spans under the propagated trace ID, and the row above shows those in Tempo |

**Not observable here:** publish, approve, activate and rollback against a store. Compose and `dev`
run `content.source=dir`, where the content RPCs answer `unimplemented`, as the `history` call above
showed. `AW-INF-021` already carries that path on `dev`, with `AW-SRV-013`'s series and the RPC span
tree under `cli.command`. Its run should also check this story's own two items: the activation's
`info` confirmation line carries the `trace_id` the CLI sent, and one trace runs from `cli.command`
through `content.activate` to the Loader's `content.swap`.

**The confirmation line needs a test** (SRE's call, asked for in architecture's §8 review above).
The Observability section requires `activate` and `rollback` to log the confirmation text they
showed, at `info`, with the `trace_id` the CLI sent. It's the CLI's half of the audit join. No test
asserts it, and a log line nobody asserts is unverified, so it's owed by implementation:
- a test that runs `activate` and `rollback` with `--log-level info`;
- it decodes the line and checks the confirmation text, and that `trace_id` equals the one in the
  JSON result and the one the server received.

The default level stays `warn` (`AW-CLI-001`). An operator tracing an action raises it, and the
JSON result's `trace_id` still joins the server's audit record at the default.

The span item is **satisfied** under that ruling. The item as a whole is satisfied once the
confirmation line's test lands. By the ruling's reading, "under
`cli.command`" means under the CLI's trace ID, with `cli.command`'s span ID as the parent. The
store-backed path's live observation is carried by `AW-INF-021` as an inherited line, as CLAUDE.md §8
allows while no environment has the caller. *(Revised before merge, from Codex on #276. The first
push held the item open on a question architecture had already ruled on.)*

## Implementation record, addendum (2026-09-30): §8 item 4

`TestAccountSetPacks_PublishToAnUngrantedPackIsPackNotHeld` (on
`impl/aw-srv-035-pack-not-held-cli`, AW-SRV-035's AC-2) covers this story's item 4. A Builder's
`content publish` to a pack they don't hold exits 1 with `error.code` `pack_not_held`, the
server's reason passed through by `contentError`. Mutation-checked: without the passthrough, it's
`permission_denied`, and the test fails.

## Implementation record, addendum (2026-09-30): §8 items owed

On `impl/aw-cli-003-owed`, for the §8 review's "What closes it":

| Item | Covered by | Result |
|------|------------|--------|
| AC-7 | `TestContentDiff_ZonesTemplatesAndFields`: `town@1` → `town@2` diffs to a Zone removed (`(town@1 purgatory.aw:1)`), a Zone added (`garden.aw:1`), and on Templates a field changed (`town.Merchant` `npcs.aw:12`), removed (`town.Guard` `npcs.aw:30`) and added (`town.Lantern` `items.aw:3`). Under `--output json` every change has `file` and `line` | pass. Mutation-checked: with `diffComponents` emptied, three lines go missing and the test fails |
| `fetch`/`diff` reasons | `fetchVersion` maps through `contentError`, as every publish-path command does. `TestContentFetchAndDiff_ServerReasons`: an unpublished version is `not_found`, and a pack not held is `pack_not_held`, for each command | pass. Mutation-checked: with `rpcError`, all four lose their reason |
| Stale parent | `TestContentPublish_StaleParent`, and `TestContentPublish_StaleParentOverRedpanda` on throwaway topics. A publish whose parent another publish overtakes, between its uploads and its `PublishVersion`, exits 1 `stale_parent` with `run \`content history town\` and publish again`. Nothing is written or retried. The overtaking publish lands through a test seam, `runtime.beforePublishVersion` | pass, and passes on the local Redpanda |
| Confirmation line | `TestContentActivate_ConfirmationLineJoinsTheServersRecord`: for `activate` and `rollback` at `--log-level info`, the line decodes as `{ts, level, msg, command, trace_id}` with the confirmation text. Its `trace_id` equals the JSON result's and the server's `activate` or `rollback` audit record's | pass. The test stack trusts the inbound `traceparent`, as compose does (`ANDARA_TRUST_INBOUND_TRACEPARENT`). With the chart's default of `false`, the server's trace is its own root, linked to the CLI's |

## §8 close (architecture, 2026-10-01): `done`

Every item in "What closes it" is delivered, and each was re-run green in this review:
1. **AC-7:** `TestContentDiff_ZonesTemplatesAndFields` covers Zones added and removed, and Template
   fields changed, removed and added, each with `file:line`. Mutation-checked against
   `diffComponents`.
2. **Reasons:** `TestContentFetchAndDiff_ServerReasons` shows `not_found` and `pack_not_held` for
   `fetch` and `diff`.
3. **Stale parent:** `TestContentPublish_StaleParent`, and `…OverRedpanda` in `make test-integration`.
4. **`pack_not_held` at the CLI:** #281.
5. **SRE's confirmation line:** `TestContentActivate_ConfirmationLineJoinsTheServersRecord`. SRE's
   record made the instrumentation item satisfied once this test landed.

**For `AW-INF-021`'s inherited observation: the trace must be parented.** The confirmation-line
test runs with the server trusting the inbound `traceparent`, as compose does. That's the contract:
one trace from `cli.command` through the server's RPC, with the CLI's `trace_id` on the server's
audit record (Observability, and the `AW-CLI-002` ruling). A server that doesn't trust the header
starts its own root trace, and a span link to the CLI's trace is **not** equivalent. The CLI's
`trace_id` would then match neither the audit record nor a server trace in Tempo. SRE has set `dev`
to trust it (`telemetry.trust_inbound_traceparent: true` in `values/dev.yaml`, #288, asserted by
`helm-test`), and `prod` stays `false`. So `AW-INF-021`'s §8 record observes the parented trace on
`dev`. An environment that doesn't trust the header can't make this observation, and a story that
wants it there has to revise this contract first. *(Revised on review of #296: the first push called
a linked trace equivalent.)*
