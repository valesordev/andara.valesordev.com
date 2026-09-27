---
id: AW-INF-026
title: Generated views stop being committed
epic: EPIC-01
component: infra
type: infra
status: draft
size: S
depends_on: [AW-INF-001]
blocks: []
lane: architecture
risk: low
---

## Context

`BACKLOG.md` and `docs/status.md` are generated from story frontmatter and committed. Both hold
whole-backlog aggregates, so any two PRs that move any story's status conflict on the same lines.
On 2026-09-26, #110, #111 and #112 each needed a rebase after every sibling merged, and each rebase
reran about 15 minutes of CI. The cost is recorded in
`docs/feedback/AW-INF-001-generated-views-conflict.md`.

Architecture recommended option (a): generate, don't commit. **Brian approved (a) on 2026-09-27.**
It changes the charter, so this story carries the charter edits with it.

## User story

As a lane working in parallel, I want a status move to touch only the story it moves, so that
sibling PRs stop conflicting on files nobody edits by hand.

## Scope

### In scope
- Git-ignore `BACKLOG.md` and `docs/status.md`, and remove them from the tree.
- `make backlog` and `make status` write the file locally and print it, as today.
- `make check` drops `backlog-check` and `status-check`. `make validate-stories` still gates the
  frontmatter.
- CI's `check` job on `main` writes both views to the job summary.
- The charter's §3, §6 step 6 and §11, and each lane's `CLAUDE.md`, say to run `make status`
  instead of reading the file, and drop "regenerate in the same commit".

### Out of scope
- The generators' output format.
- Options (b) and (c) in the feedback file.

## Acceptance criteria

1. **Given** a clean clone of `main` **when** `git ls-files` runs **then** neither file is tracked,
   and both are in `.gitignore`.
2. **Given** two PRs that each move a different story's status **when** one merges **then** the
   other has no conflict.
3. **Given** `make status` **when** it runs **then** it writes `docs/status.md` and prints the same
   view as today.
4. **Given** a push to `main` **when** the `check` job finishes **then** its summary shows both
   views for that commit.
5. **Given** a story with invalid frontmatter **when** `make check` runs **then** it fails through
   `validate-stories`.
6. **Given** the charter and the lane files **when** they're searched for "regenerate" or for
   reading `status.md` **then** nothing still asks for the committed file.

## Interface contract

- `make backlog` and `make status`: unchanged names and output. They no longer have `-check`
  twins in `make check`.
- CI: the `check` job summary on `main` gains the two views.

## Data / state impact

None. The frontmatter stays the record. A past view is reproducible with
`git checkout <sha> && make status`, because the generator is deterministic.

## Observability requirements

- **Metrics / Logs / Traces / Alerts:** none.

## Test plan

- **Unit:** the generators' existing tests.
- **Integration:** CI on the story's own PR and its first merge to `main` (AC-4).
- **Manual/operator:** AC-2 is observed on the next pair of sibling PRs.

## Definition of done

CLAUDE.md §8.

## Open questions

- None. Brian chose (a) on 2026-09-27.
