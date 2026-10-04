---
id: AW-SRV-046
title: The owed follow-ups from SPRINT-03's reviews
epic: EPIC-05
component: server
type: chore
status: review
size: S
depends_on: [AW-SRV-013, AW-SRV-034, AW-SRV-035, AW-SRV-036, AW-SRV-037, AW-CLI-002, AW-CLI-003]
blocks: []
lane: implementation
risk: low
---

## Context

SPRINT-03's §8 reviews closed seven stories with follow-ups marked "not holding the story": README
rows, test assertions that are weaker than the contract, and one rendering bug in `andara-cli sim`.
Each follow-up is recorded in its story's feedback file and nowhere else. No story carries them, so
`make status` never offers them, and each says "on the next touch", a touch that may not come. This
story gathers them in one place so that they're done once and the feedback files can say so.

Every item's contract already holds. These are corrections to tests, docs and one output line
against rulings already made, so no item needs a decision. Each item cites its source.

## User story

As a developer, I want the follow-ups SPRINT-03's reviews left on merged stories done in one pass,
so that the tests assert what the contracts say and the READMEs describe what ships.

## Scope

### In scope
1. **`andara-cli sim` renders an empty direction** (`AW-SRV-036` §8; `admin/cli/simcmd.go`).
   `to_direction: ""`, as `goto` sends it, prints `X leaves .`. It must print `X leaves.`, as the
   server's own line does.
2. **`content validate`'s failure summary counts warnings**
   (`docs/feedback/AW-CLI-002-content-validate-inspect.md`, "For implementation, not holding the
   story"). The summary's finding count must count errors only. Today it counts warnings too.
3. **`admin/README.md`'s command table** (same source). The `version` row says that it prints the
   embedded `andara.core`.
4. **Four missing `content` tests** (`docs/feedback/AW-CLI-003-content-publish.md`, "not holding"):
   - `fetch` with a `src/../x` path;
   - `previousActive` over three or more moves;
   - AC-2 activating as the approver;
   - a blob over 1 MiB, to exercise chunking.

   Also `AW-CLI-003`'s implementation record: the rehearsal is in CI (#265), and the confirmation
   `info` line isn't asserted. Correct the record to say both.
5. **One audit record per refusal** (`docs/feedback/AW-SRV-013-publish-path.md`, "not holding").
   `TestActivateVersion_RefusesWhatTheLoaderWouldRefuse` asserts exactly one record per refusal.
6. **`server/README.md` and one test line** (`docs/feedback/AW-SRV-034-loader-compiler-agree.md`,
   §8 review, 2026-09-30):
   - the refusal list gains `duplicate_direction`, and says that a refused load reports only its
     errors (`errors.md` §1 rule 7);
   - the `content.strict_orphans` row states the one-Room exemption;
   - `TestBuildWorld_LoaderAgreesWithCompiler`'s "two Exits north" case asserts that the finding's
     `Line` is the second Exit's.
7. **Account grants** (`docs/feedback/AW-SRV-035-pack-grants.md`, "not holding"):
   - `TestSetBuilderPacks_ARefusalDoesNotHoldTheWriteLock` waits on `stall.entered` in a `select`
     with a deadline;
   - the log fields the implementation record lists (`acting_as_account_id`, `session_id`,
     `trace_id`) are asserted as `AW-SRV-035` rules them (AC-6), and the record is corrected where it
     differs;
   - `server/README.md`'s Accounts section names `accounts.write` and the `andara.accounts` domain.
8. **The dev fixture test uses the embedded core** (`docs/feedback/AW-SRV-037-purgatory.md`, "a
   follow-up that doesn't hold the story"). `TestDevFixtureSourceMatchesTestContent` compiles
   against the embedded `andara.core`, not `content/core/templates/` on disk.

### Out of scope
- #312, where publish shows other packs' findings as the Builder's own. It needs architecture's
  ruling on wording first, and it's planned on its own in SPRINT-04.
- #287, #290 and #319, each a bug with its own issue.
- Any change to a contract. If an item turns out to need one, it leaves this story and goes to
  `docs/feedback/` for architecture.

## Acceptance criteria

1. **Given** a `goto` Event with an empty `to_direction` **when** `andara-cli sim` renders it
   **then** the line is `<name> leaves.`, and a test asserts the exact string.
2. **Given** a pack with 2 errors and 3 warnings **when** `content validate` fails **then** the
   summary's count is `2`, and a test asserts the whole summary line.
3. **Given** each test that items 4, 5, 6 and 8 name **when** `go test ./...` runs **then** it
   exists and passes. Each one fails when the behaviour it covers is reverted (mutation-checked once,
   and recorded in the implementation record).
4. **Given** a regression that holds the Account write lock on a refusal **when**
   `TestSetBuilderPacks_ARefusalDoesNotHoldTheWriteLock` runs **then** it fails within its deadline
   instead of hanging.
5. **Given** `admin/README.md` and `server/README.md` **when** read **then** they contain each row
   that items 3, 6 and 7 name.
6. **Given** the Account grant path (item 7) **when** its test runs a refused and an accepted
   `set-packs` **then** it asserts the log line's fields as `AW-SRV-035`'s Observability section
   rules them (2026-09-30): `trace_id` is non-empty, since it's the correlation ID on Admin; the
   `session_id` key is present and empty, since an Admin call runs in no Game Session; and
   `acting_as_account_id` is present, and empty without `--as`. If the record says otherwise, it's
   corrected through `AW-SRV-035`'s feedback file. The code emits all three keys today
   (`server/auth/packs.go`), so no code change is expected.
7. **Given** `AW-CLI-003`'s implementation record **when** this story merges **then** it says the
   rehearsal runs in CI (#265) and that the confirmation line isn't asserted.
8. **Given** each source feedback file **when** this story merges **then** that file records the
   item as done, citing this story. For item 1, `AW-SRV-036`'s story records it.

## Interface contract

None changes. Item 1's output line is the server's existing departure line, without a direction.
Item 2's summary text is unchanged except for its count. Everything else is tests and docs.

## Data / state impact

None.

## Observability requirements

- **Metrics / Traces / Alerts:** none added or changed.
- **Logs:** none added. Item 7 asserts the existing log fields of the Account grant path. If they
  differ from the implementation record, the record is corrected and the code isn't changed.

## Test plan

- **Unit:** as AC-1 to AC-4. Each new or strengthened test is mutation-checked once.
- **Integration:** none new.
- **Manual/operator:** none.

## Definition of done

CLAUDE.md §8.

## Open questions

- None. Every item is a ruling already made, and its source is cited.

## Verification record — 2026-10-03 (implementation; `review` until the §8 checklist passes)

Branch `impl/aw-srv-046-sprint-03-follow-ups`. Every new or strengthened test was mutation-checked
once. The mutation column names the reversion that makes it fail.

| AC | Item | Test | Mutation it catches |
|----|------|------|---------------------|
| 1 | 1 | `TestSimRendersADepartureWithNoDirection`: `[tick 3] Aldric leaves.`, and with a direction `leaves north.`. `sim` now renders the departure through `play`'s `renderEvent`, so the two can't drift | It failed on the old `"%s leaves %s."` |
| 2 | 2 | `TestContentValidate_SummaryCountsErrorsOnly`: 2 errors and 3 warnings give `mypack: 2 finding(s) refuse the pack` | It failed on the old count of 5, for hand-built findings only (see "AC-2" below) |
| 3 | 4 | `TestContentFetch_RefusesADotDotPath` | Removing both `filepath.IsLocal` and the `os.Root` write. Either guard alone still refuses |
| 3 | 4 | `TestPreviousActive` (three and more moves, including a zero move as the newest before the active one) | Searching the moves oldest-first, and dropping the `v != 0` guard |
| 3 | 4 | `TestContentActivate_AsTheApprover` | Refusing the approver at activation |
| 3 | 4 | `TestContentPublish_ABlobOverOneMiBIsChunked` | Sending each blob as one chunk, which the server refuses |
| 3 | 5 | `TestActivateVersion_RefusesWhatTheLoaderWouldRefuse`: exactly one record per refusal | Recording each refusal twice ("4 audit records for 2 refusals") |
| 3 | 6 | `TestBuildWorld_LoaderAgreesWithCompiler` "two Exits north": `Line` 5 | Reporting the first Exit's line (4) |
| 3 | 8 | `TestDevFixtureSourceMatchesTestContent` against the embedded core | Removing `andara.core.Npc` from `content/core` shows that the test is live. It doesn't distinguish the embed from the disk, since `go:embed` serves the same files. The difference that matters is the version, which now comes from `VERSION` rather than a hard-coded 1 |
| 4 | 7 | `TestSetBuilderPacks_ARefusalDoesNotHoldTheWriteLock`: a `select` with a deadline on `stall.entered` | A refusal that never reaches its audit used to hang the test. Taking the write lock twice is one way to cause it. It now fails at the 5 s deadline. Holding the lock through the audit was already caught by the second `select`, in about 1 s |
| 5 | 3, 6, 7 | `admin/README.md`: the `version` row. `server/README.md`: `duplicate_direction` and the errors-only rule, the one-Room exemption, and a "Builder pack grants" subsection naming `andara.accounts` and `accounts.write` | — |
| 6 | 7 | `TestSetBuilderPacks_LogFields`: on a refused and an accepted grant, `trace_id` equals the RPC span's, and `session_id` and `acting_as_account_id` are present and empty | Dropping `session_id` from the refusal line. The record already listed the three fields, so it needed no correction |
| 7 | 4 | `AW-CLI-003`'s implementation record: the rehearsal runs in CI (#265). On the confirmation line, AC-7's "isn't asserted" was already stale: `TestContentActivate_ConfirmationLineJoinsTheServersRecord` (7592bd5) asserts it, so the record says that instead (Codex on #368) | — |
| 8 | all | Each source feedback file has a "Done in `AW-SRV-046`" section, and `AW-SRV-036`'s §8 note records item 1 | — |

No contract changed, and no item needed a decision.

**AC-2's fixture, and what changed.** No input reaches AC-2's Given.
- **Why the old count was already right.** `mergeDiagnostics` has dropped every non-error
  finding whenever there's an error since `AW-CLI-002` (2d5a72f), on `--path` and on
  `validatePublished` alike. So on every reachable path the old `len(v.diags)` already counted
  errors only. The review premise behind the item wasn't reachable.
- **What this branch adds.** The count is now errors-only by construction, not by the merge. A
  later path that keeps warnings on a refusal can't inflate it.
- **How it's tested.** The test drives `reportValidated` with 2 errors and 3 warnings directly.
  Architecture is asked to accept AC-2 as satisfied in substance.

## §8 instrumentation check — 2026-10-03 (SRE, `sre/aw-srv-046-verify`)

**The story's §7 passes.** It adds no metric, trace or alert and no log line. Its one
instrumentation item (item 7, AC-6) asserts the Account grant path's existing log fields, so the
check observed those on the running server (`main` bb796bc, local stack). As the operator, a
`builder` account was granted `town` (accepted) and then `andara.core` (refused):

| Line | Level | `trace_id` | `session_id` | `acting_as_account_id` | Other |
|------|-------|------------|--------------|------------------------|-------|
| `builder packs set` | `INFO` | `da18474c…`, non-empty | present, empty | present, empty | `actor_account_id`, `target_account_id` |
| `builder packs refused` | `WARN` | `6971d110…`, non-empty | present, empty | present, empty | `reason=core_not_grantable` |

That is `AW-SRV-035`'s rule (2026-09-30): the correlation ID is `trace_id`, `session_id` is empty
because an Admin call runs in no Game Session, and `acting_as_account_id` is empty without `--as`.
Both `trace_id`s resolve in Tempo to the `andara.admin.v1.Admin/SetBuilderPacks` trace (the accepted
one with its `accounts.write` child). `TestSetBuilderPacks_LogFields` and
`TestSetBuilderPacks_ARefusalDoesNotHoldTheWriteLock` pass.

**Not observed live:** `acting_as_account_id` with a value, because `--as` wasn't used. The unit
test covers that side. Items 1 to 6 and 8 touch no instrumentation.
