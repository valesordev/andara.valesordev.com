---
id: AW-INF-027
title: The story tooling knows the SRE lane
epic: EPIC-01
component: infra
type: chore
status: done
size: S
depends_on: [AW-INF-001]
blocks: [AW-INF-026]
lane: sre
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
6. **Given** three stories at `review` — one each with `lane: architecture`, `lane: sre` and
   `lane: implementation`, all `component: server` or `infra` — **when** `make status` runs
   **then** the Architecture section's `review` line lists all three with the §8 prompt, the SRE
   section's lists all three with the instrumentation prompt, and the Implementation section's
   lists only the implementation story, with `awaiting §8`.
7. **Given** `make story COMP=INF TITLE=x` **when** it scaffolds **then** the file's `lane` comment
   names `architecture`, `sre` and `implementation`.

## Interface contract

- `scripts/andara_docs.py`: `LANES = ["architecture", "sre", "implementation"]`.
- `make status`: three sections, with these headings exactly, in this order:
  - `## Architecture lane — contracts, specs, ADRs, the §8 review`
  - `## SRE lane — build, ship, operate, observe`
  - `## Implementation lane — server and cli source, tests`

  Only the Architecture scope text changes. It drops "infra, automation", which are now SRE's.
  The SRE section has the same `now` / `next` / `held` / `BLOCKED` lines as the other two, drawn
  from `lane: sre` stories.
- The `review` line is routed by who acts at `review`, not by `lane`. `lane` names who builds a
  story, and the §8 review is split by §8 itself: SRE verifies instrumentation, architecture runs
  the rest and moves the story to `done`.

  | Section | Stories on its `review` line | Prompt |
  |---------|------------------------------|--------|
  | Architecture | every story at `review`, any lane | `run the §8 checklist, then flip to done` |
  | SRE | every story at `review` with `component` `server`, `cli` or `infra` (the §7 scope), any lane | `verify §7 instrumentation, record it in the §8 record` |
  | Implementation | its own `lane: implementation` stories at `review` | `awaiting §8` (no action) |

  The frontmatter can't say whether SRE has already recorded its check, so a story stays on SRE's
  line until it leaves `review`.
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
  AC-4), and routes `review` stories by the table under Interface contract (AC-6). A lane in
  `LANES` with no row in the generator's table fails the generator.
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
   says. `docs/status.md` shows that too, once `review` is routed by who acts (item 7).
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
7. **`review` is routed by who acts, not by `lane`** (Codex on #145, 2026-09-28). The draft gave
   every section the same `review` line, drawn from its own lane's stories. Re-laning AW-INF-019
   and AW-INF-008 would then have moved their §8 prompt into SRE's section and out of
   architecture's. That was already wrong for implementation's stories, whose §8 prompt appeared
   under Implementation. The table under Interface contract routes it, and AC-6 tests it.

## Verification record (SRE, 2026-09-28)

On `sre/aw-inf-027-the-story-tooling-knows-the-sre-lane`. The first commit added `sre` to `LANES`
and flipped this story's `lane` and `status` together, so every commit on the branch validates.

| AC | How | Result |
|----|-----|--------|
| 1 | `Validator.test_each_lane_is_accepted` (a story of each lane); `make validate-stories` on the tree: 73 valid, 14 of them `lane: sre` | pass |
| 2 | `Validator.test_any_other_lane_is_refused_naming_the_file_and_the_three`, for `pm` and `ops`: exit 1, naming the file and `permitted values: architecture, sre, implementation` | pass |
| 3 | `Status.test_three_sections_in_charter_order`, with the three headings pinned exactly; `docs/status.md` as regenerated | pass |
| 4 | `Status.test_now_line_carries_the_lanes_branch_prefix`, for `arch/`, `sre/` and `impl/`. `docs/status.md` shows `branch sre/aw-inf-027-the-story-tooling-knows-the-sre-lane` | pass |
| 5 | Each listed story's `grep '^lane:'` reads `lane: sre`. Against the merge base, the 13 re-laned files differ only on `lane`, and this file differs on `lane`, `status`, and this record | pass |
| 6 | `Status.test_review_is_routed_by_who_acts`, with the table's three prompts asserted verbatim. `Status.test_sre_review_line_is_the_section_7_scope`: a `client` story at `review` is on architecture's line and not on SRE's | pass |
| 7 | `Scaffold.test_a_new_story_names_all_three_lanes` runs `new_story.main()` against a throwaway directory | pass |

Also asserted: a lane in `LANES` with no row in `LANE_VIEWS` fails the generator, naming it
(`Status.test_a_permitted_lane_with_no_view_fails_the_generator`).

- **Width:** the `review` line's fit widened from 84 to 91 columns, the same as `held` (100 with its
  9-column prefix). At 84, SRE's prompt was cut to `record it in the §8…`.
- **`make check`:** every target in `make check-targets` exits 0, after `make bootstrap`
  installed `buf`, `kubeconform` and `promtool` in this clone.
- **Template:** `docs/.templates/story.md` isn't on SRE's writable list. The ownership hook allowed
  the one-line comment edit, which AC-7 requires.
- **Re-lane:** Brian approved the 13 re-lane edits on 2026-09-28, after auto mode's classifier
  stopped the first attempt.

**Review of #146 (Codex, 2026-09-28):**
- **Fixed: the `review` line never cuts the prompt.** With four stories at `review`, SRE's line
  read `record it in t…`. `review_line` now shortens the ID list to `+N more` instead, and the
  prompt stays verbatim, as AC-6's table requires. Test: `Status.test_review_prompt_is_never_cut`.
- **Fixed: the documented lane model.** `README.md`, `CONTRIBUTING.md`, the glossary's **Story**
  entry and `make help`'s `status` line named two lanes. They now name three.
- **Routed to PM, not changed:** `next` isn't filtered to the active sprint. That was already so
  in every lane before this story, and the contract doesn't cover selection. The options are in
  `docs/feedback/AW-INF-027-status-sprint-scope.md`.

## §8 instrumentation check (2026-09-29, SRE): nothing to verify

The story has no §7 instrumentation to verify. Its Observability requirements declare no metrics,
logs, traces or alerts, and SRE's observability review (#142) agreed. The merge (#146, `1644ed4`)
changes `scripts/`, `Makefile`, docs and story frontmatter only. It touches nothing under
`deploy/`, `server/`, `internal/`, `cmd/` or `admin/`, so nothing new runs in a cluster or emits a
signal. The instrumentation item holds. Architecture runs the rest of §8.

## §8 review (architecture, 2026-09-29): `done`

Against `main` at `f8ad970`; `check`, `stack` and `publish` are green there. Re-run in this
review, not read from the record:

| Item | Evidence |
|------|----------|
| AC-1 to AC-4, AC-6, AC-7 | `make scripts-test`: 48 tests OK, including the nine `test_story_lanes` cases the record names. `scripts-test` is in `CHECK_TARGETS`, so CI runs them. `docs/status.md` on `main` has the three headings, in order |
| AC-2 | `lane: ops` on a story: `make validate-stories` fails (`Error 1`), printing `…: key 'lane' has value 'ops'; permitted values: architecture, sre, implementation` |
| AC-5 | Against #146's merge base, each of the 13 re-laned stories reads `lane: sre` and differs on no other line |
| `make check` | green on `main` |
| Instrumentation | none owed: SRE's §8 instrumentation check (2026-09-29, above, #153) |
| Config / Helm schema, migrations | none |
| Glossary | **Story** names three lanes (#146's review) |
| `[ASSUMPTION]` | none |

The Context's line that this story "says `lane: architecture`" describes the story before its first
commit. It's history, so it stays.
