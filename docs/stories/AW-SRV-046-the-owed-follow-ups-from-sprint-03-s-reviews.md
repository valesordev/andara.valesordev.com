---
id: AW-SRV-046
title: The owed follow-ups from SPRINT-03's reviews
epic: EPIC-05
component: server
type: chore
status: draft
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
     `trace_id`) are asserted, or the record is corrected;
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
   `set-packs` **then** it asserts that the log line carries `acting_as_account_id`, `session_id` and
   `trace_id`. If the code doesn't emit one of them, the test asserts what it does emit, and the
   implementation record in `AW-SRV-035` is corrected through its feedback file.
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
