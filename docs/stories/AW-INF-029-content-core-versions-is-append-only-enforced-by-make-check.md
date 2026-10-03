---
id: AW-INF-029
title: content/core/VERSIONS is append-only, enforced by make check
epic: EPIC-05
component: infra
type: infra
status: review
size: S
depends_on: [AW-SRV-013]
blocks: [AW-CLI-013]
lane: sre
risk: low
---

## Context

`content/core/VERSIONS` maps each `andara.core` version to the digest of its blobs. The server and
`andara-cli` each hold their embedded core to the line for `content/core/VERSION` (`AW-SRV-013`
AC-19, `AW-CLI-002` AC-10). So `andara.core@N` means the same bytes everywhere only while no line,
once written, ever changes. Nothing enforces that today. A rebase or a hand edit could rewrite line
`N`, and every environment already holding `andara.core@N` would then disagree with the next build.
Source: `docs/feedback/AW-SRV-013-publish-path.md`, "For PM", item 2. It isn't on the demo path.

## User story

As a developer, I want `make check` to refuse a change to any existing line of
`content/core/VERSIONS`, so that a core version number can never come to mean different bytes.

## Scope

### In scope
- `make core-versions-check`, in `CHECK_TARGETS`. It compares `content/core/VERSIONS` with the merge
  base and allows only new lines appended at the end.
- Supplying the merge base in CI (`ci.yaml`): enough fetch depth or an explicit base ref.

### Out of scope
- The digest check itself, which is `AW-SRV-013` and `AW-CLI-002`.
- Bumping the core, which is implementation's, in `content/`.

## Acceptance criteria

*The exit codes below are the check script's, `scripts/core_versions_check.sh`, which the Unit
tests run directly. Through `make core-versions-check`, every non-zero exit is make's `2`, and the
`core-versions-check:` line is the evidence (pinned 2026-10-02, pre-PR review).*

1. **Given** a change that appends one line `N+1 <digest>` **when** the check runs **then** it
   exits `0`, and so does `make core-versions-check`.
2. **Given** a change that edits, deletes or reorders an existing line **then** it exits `1` with
   `core-versions-check: line <n> changed; VERSIONS is append-only`.
3. **Given** an appended line whose version isn't the previous last version plus one **then** it
   exits `1` with `core-versions-check: line <n> is version <v>; expected <last + 1>`. *(Message
   pinned at contract review, 2026-10-02.)*
4. **Given** no change to the file **then** it exits `0`.
5. **Given** CI on a pull request **then** the merge base with `main` is available, and the check
   runs against it. On `main` itself, it compares with the first parent.
6. **Given** a local run with no `origin/main` **then** it exits `2` with
   `core-versions-check: no merge base; fetch origin`.

## Interface contract

- `## core-versions-check: fail if content/core/VERSIONS changes other than by appending the next version`
- The base is `BASE_REF`, defaulting to `origin/main`, and the merge base is computed against `HEAD`.
- Exits: `0` ok; `1` a violation, named; `2` no base. These are the script's exits
  (`scripts/core_versions_check.sh`), which the Unit tests assert directly. Through `make`, GNU make exits `2` for any
  failing recipe, so the ACs' distinguishing evidence at the `make` level is the
  `core-versions-check:` line, not the code. *(Clarified 2026-10-02, pre-PR review.)*

## Data / state impact

None.

## Observability requirements

None. It's a `make` target in CI, with no service, metric or trace. It reports through its exit code
and its `core-versions-check:` lines in the CI log.

## Test plan

- **Unit** (`scripts/tests/`): fixture repositories for an append, an edit, a deletion, a reorder, a
  skipped number, and no change (ACs 1–4); and the missing base (AC-6).
- **Integration:** CI's `check` job runs it with the base supplied (AC-5).
- **Manual/operator:** `make core-versions-check` on a clean tree exits `0`.

## Definition of done

CLAUDE.md §8.

## Open questions

- `[ASSUMPTION]` The next version is the previous one plus one. `VERSION` is monotonic (ADR-0004,
  amended 2026-09-28), so no gap is legitimate.

## Implementation record (SRE, 2026-10-03)

On `sre/aw-inf-029-core-versions-append-only`.

- `scripts/core_versions_check.sh` compares the working tree's `content/core/VERSIONS` with
  `git merge-base $BASE_REF HEAD`, where `BASE_REF` defaults to `origin/main`. Because it reads the
  working tree, an uncommitted edit is caught too. When HEAD is the merge base (a push to `main`,
  or a branch with nothing committed yet), it checks twice: HEAD's commit against `HEAD^1`
  (AC-5), then the working tree against HEAD, so an uncommitted edit of a line HEAD appended is
  caught (Codex on #361). A root commit has no parent, so only the second check runs. On success it prints one line,
  `core-versions-check: ok: …`.
- `make core-versions-check` is in `CHECK_TARGETS`, and `ci.yaml` gains the step
  `core VERSIONS append-only` (the parity guard requires it). CI already checks out with
  `fetch-depth: 0`. On a pull request HEAD is the merge commit, whose first parent is `main`'s
  tip, so the merge base is `main`.
- An inserted line counts as a change to the line it displaces. Deletions are named at the first
  missing line.
- From the pre-PR review:
  - **A shallow clone exits `2`**, with
    `core-versions-check: shallow clone; run git fetch --unshallow`.
    It can't tell a root commit from a parent that wasn't fetched, and comparing HEAD with itself
    would pass a bad edit.
  - **On a push to `main`, CI sets `BASE_REF` to the push's `before` commit**, so a multi-commit
    push is checked whole, not only its last commit. For that chain to cover all of `main`, every
    push's step must run. So `ci.yaml`'s concurrency group is the commit SHA for a push (no run
    on `main` is cancelled or replaced), and the step runs `if: !cancelled()`, even after an
    earlier step fails. A run cancelled by hand breaks the chain, and re-running it restores
    it. AC-5's first-parent rule remains the default without `BASE_REF`.
  - **Fields split on whitespace and parse as decimal**, as `content/core` reads them
    (`strings.Fields`, base 10). A base version `08` isn't octal, and an appended `02` is refused.
  - The fixture's git calls drop `GIT_*` and an inherited `BASE_REF`.

**How each AC is covered** (`scripts/tests/test_core_versions_check.py`, 24 tests, run by
`make scripts-test`; fixture repositories with a bare `origin`):

| AC | Tests |
|---|---|
| 1 | `test_appending_the_next_version_passes`, `test_appending_two_in_sequence_passes` |
| 2 | edit, delete last, delete middle, reorder, insert-before-end: each exits `1` naming the line |
| 3 | skipped, repeated, and a bad second appended version: `line <n> is version <v>; expected <e>` |
| 4 | `test_no_change_passes` |
| 5 | `test_on_main_itself_it_compares_with_the_first_parent` (a bad edit pushed to `main` fails), `test_on_main_with_a_good_append_passes` |
| 6 | `test_no_base_exits_2`; `test_base_ref_overrides_origin_main` (also: an unknown `BASE_REF` exits `2`) |

Also: `test_an_uncommitted_edit_is_caught`, introducing the file (at `1`, and refused at `2`), a
leading-zero base version, an appended leading-zero version, a tab-separated line, and
`test_a_shallow_clone_refuses_rather_than_passing`. Manual: `make core-versions-check` on this
branch prints `core-versions-check: ok: unchanged against 897786a42e60` and exits `0`.

§8 instrumentation: the story has no instruments (Observability requirements: none).
