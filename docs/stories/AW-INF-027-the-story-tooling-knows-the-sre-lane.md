---
id: AW-INF-027
title: The story tooling knows the SRE lane
epic: EPIC-01
component: infra
type: chore
status: ready
size: S
depends_on: [AW-INF-001]
blocks: [AW-INF-026]
lane: architecture
risk: low
---

## Context

#139 added the SRE role, and CLAUDE.md §2 now names three lanes: `architecture`, `sre` and
`implementation`. The tooling still knows two. `scripts/andara_docs.py` has `LANES =
["architecture", "implementation"]`, so `make validate-stories` rejects `lane: sre`, and
`scripts/gen_status.py` renders two lane views. So no story can say `sre`, `docs/status.md` shows
the SRE agent nothing to pick up, and every infra story is still reported as architecture's work.

This story's frontmatter says `lane: architecture` only because the validator can't say `sre` until
this story merges. SRE builds it. The branch's first commit flips this story's own `lane` to `sre`
along with its `status`, in the same commit as the validator change, so every commit on the branch
validates.

## User story

As the SRE agent, I want the story tooling to know my lane, so that `make status` shows me my next
story and the infra stories stop being reported as architecture's.

## Scope

### In scope
- `lane` accepts `architecture`, `sre` and `implementation`, and nothing else.
- `make status` renders three lane sections, in §2's order: Architecture, SRE, Implementation.
- The `now` line's branch carries the lane's prefix from §2 (`arch/`, `sre/`, `impl/`).
- `docs/.templates/story.md`'s `lane` comment lists all three lanes.
- The one-time re-lane: the `lane` field of each story in the table under Interface contract changes
  to `sre`, and nothing else in those files changes.

### Out of scope
- Re-laning `done` stories. Their `lane` records who delivered them, and it stays.
- AW-INF-005 and AW-INF-007. Each mixes contract, SRE and implementation work, and each is split
  at the next sprint boundary rather than re-laned whole.
- Committing the generated views, or not. That's `AW-INF-026`, which follows this story.

## Acceptance criteria

1. **Given** a story with `lane: sre` **when** `make validate-stories` runs **then** it passes.
2. **Given** a story with `lane: pm` or `lane: ops` **when** `make validate-stories` runs **then** it
   exits non-zero, and names the file and the three allowed values.
3. **Given** the backlog after this story merges **when** `make status` runs **then** the output
   has exactly three lane sections, headed `Architecture lane`, `SRE lane` and `Implementation
   lane`, in that order.
4. **Given** a story at `in-progress` with `lane: sre` **when** `make status` runs **then** its
   `now` line reads `branch sre/<id-lower>-<slug>`. The same holds for `arch/` and `impl/`.
5. **Given** the table under Interface contract **when** `grep '^lane:'` runs on each listed story
   **then** every one reads `lane: sre`, and `git diff` on those files against the merge base
   shows only that line changed. The one exception is this story's own file, whose `status` line
   also changes as it moves through `in-progress` and `review`.
6. **Given** `make story COMP=INF TITLE=x` **when** it scaffolds **then** the file's `lane` comment
   names `architecture`, `sre` and `implementation`.

## Interface contract

- `scripts/andara_docs.py`: `LANES = ["architecture", "sre", "implementation"]`.
- `make status`: three sections, with these headings exactly, in this order:
  - `## Architecture lane — contracts, specs, ADRs, the §8 review`
  - `## SRE lane — build, ship, operate, observe`
  - `## Implementation lane — server and cli source, tests`

  Only the Architecture scope text changes. It drops "infra, automation", which are now SRE's.
  The SRE section has the same `now` / `next` / `review` / `held` / `BLOCKED` lines as the other
  two.
- Branch prefixes for the `now` line: `architecture` → `arch/`, `sre` → `sre/`,
  `implementation` → `impl/`. The rest of the branch name is unchanged: the story ID in lower
  case, then the file's slug (`branch_for` in `scripts/gen_status.py`).
- The lane → heading, scope and prefix mapping lives in one table in `scripts/gen_status.py`.
  `LANES` in `scripts/andara_docs.py` stays the one list of permitted values, and the generator
  fails loudly (non-zero exit, naming the lane) if a lane in `LANES` has no row in that table.
  A fourth lane then can't vanish from `docs/status.md`.
- AC-2's error is the validator's existing enum message, unchanged:
  `<file>: key 'lane' has value '<v>'; permitted values: architecture, sre, implementation`.

Stories re-laned to `sre`:

| Story | Status | Why it's SRE's (§2: builds, ships, operates, or observes) |
|-------|--------|------|
| AW-INF-027 | draft | this story |
| AW-INF-026 | draft | Makefile, CI, `.gitignore` |
| AW-INF-025 | draft | projector make targets, Deployment, runbooks |
| AW-INF-020 | draft | release workflow and `make cli-release` |
| AW-INF-024 | draft | environment values and the stack scripts |
| AW-INF-021 | draft | `dev`'s values, the content seed target |
| AW-INF-022 | draft | the Content Repository and its CI |
| AW-INF-015 | draft | a schema registry on the box |
| AW-INF-019 | review | Argo CD delivery of `dev` |
| AW-INF-008 | review | the chart's telemetry wiring |
| AW-INF-009 | ready | alert rule delivery |
| AW-INF-010 | ready | local stack criteria |
| AW-INF-011 | ready | probes and sizing |
| AW-INF-012 | ready | ingress configuration |

`AW-INF-023`, the Builder's Guide, stays `architecture`: Brian assigned the guide to architecture on
2026-09-26 (`docs/feedback/AW-INF-021-dev-content-store.md`).

## Data / state impact

Frontmatter only. `status` is untouched on every re-laned story, so no story moves in the sprint
cycle. Architecture still runs the §8 review of AW-INF-019 and AW-INF-008, and SRE still records
their instrumentation check.

## Observability requirements

- **Metrics / Logs / Traces / Alerts:** none. The tooling runs in `make` and CI only.

## Test plan

- **Unit:** in `scripts/tests/`, so `make scripts-test` runs them under `make check`. The
  validator accepts each of the three lanes and rejects any other value (AC-1, AC-2). The status
  generator places a story of each lane in its own section, with the lane's branch prefix (AC-3,
  AC-4). A lane in `LANES` with no row in the generator's table fails the generator.
- **Integration:** `make check` on the story's own PR.
- **Manual/operator:** `make status`, and AC-5's `grep`.

## Definition of done

CLAUDE.md §8.

## Open questions

- None that affect the contract. Architecture confirms the re-lane table at contract review, since
  it moves AW-INF-019 and AW-INF-008 out of architecture's lane while they're at `review`.

## Contract review (architecture, 2026-09-28)

SRE's observability review is in `docs/feedback/AW-INF-027-sre-lane.md` on #142: no change, since
the tooling emits no runtime signals. The story is `ready`.

1. **The re-lane table is confirmed, and it's complete.** On `origin/main` at `0819105` there are
   17 non-`done` stories with `lane: architecture`. The table moves 14 of them. The other three are
   AW-INF-005 and AW-INF-007, which PM splits at the SPRINT-04 boundary, and AW-INF-023, which
   stays architecture's. AW-INF-019 and AW-INF-008 move while they're at `review`. That's
   right: `lane` names who builds a story, and the §8 review is architecture's in every lane
   whatever the field says. Their §8 reviews stay on architecture's list, as the sprint plan
   says.
2. **AC-5 exempts this story's own `status` line.** As written, the story couldn't pass its own
   criterion: its first commit moves its `status` along with its `lane`.
3. **The three section headings are pinned.** The draft named only the SRE heading, and left
   Architecture's scope text claiming "infra, automation", which §2 now gives to SRE.
4. **Adding a lane is now one edit, plus a loud failure.** `LANES` and the generator's view table
   were two lists nothing held together. A lane the validator accepts and the generator doesn't
   render would silently drop its stories from `docs/status.md`. That's the same failure this
   story fixes, so the generator now refuses it.
5. **The unit tests go in `scripts/tests/`,** which `make scripts-test` already discovers under
   `make check`.
6. **Expect a rebase against #142.** #142 edits the bodies of AW-INF-020, AW-INF-021 and
   AW-INF-025, and this story edits their `lane` line. The hunks don't overlap. AC-5 still holds,
   because it diffs against the merge base.
