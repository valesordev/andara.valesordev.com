---
id: AW-INF-026
title: Generated views stop being committed
epic: EPIC-01
component: infra
type: infra
status: in-progress
size: S
depends_on: [AW-INF-001, AW-INF-027]
blocks: []
lane: sre
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
- `make backlog` and `make status` write the file locally, as today, and now also print the view
  to stdout. Today they print only a `wrote …` line.
- `backlog-check` and `status-check` stay in `make check`, as **render checks**. They render the
  view and apply every rule the generator has, including `status.md`'s one-screen budget
  (`MAX_LINES`, `MAX_COLS`). They no longer compare against a file. Dropping them would drop the
  budget gate along with the staleness check.
- `.github/workflows/ci.yaml`: the `backlog freshness` and `status freshness` steps are renamed
  `backlog render` and `status render`, and still run `make backlog-check` and
  `make status-check`, so the parity step holds. On `main`, the `check` job appends both views to
  `$GITHUB_STEP_SUMMARY`.
- `.github/CODEOWNERS`: the `/BACKLOG.md` entry and its comment go.
- CLAUDE.md §2, §3, §6 step 6 and §11 say to run `make status` instead of reading the file, and
  drop "regenerate in the same commit" and the rebase note. **CLAUDE.md and `.gitignore` are
  unowned, so the ownership hook asks Brian before each edit.** SRE drafts the wording in the PR,
  and Brian approves it at the hook.

### Out of scope
- The generators' output format, which `AW-INF-027` sets.
- Options (b) and (c) in the feedback file.
- The role charters, skills, and the hook's generated-file list (`.claude/roles/_repo.md`) in
  `.claude/`. They're installed from automate.bashburn.com
  (`make install TARGET=andaras-world`), so their wording changes there, as Brian's change, and
  this repository picks it up on the next install. *(PM, 2026-09-28: #139 moved the lane files
  into `.claude/roles/` after this story was written.)* Until then, the hook's denial of hand
  edits to the two paths is harmless: nothing writes them by hand.

## Acceptance criteria

1. **Given** a clean clone of `main` **when** `git ls-files BACKLOG.md docs/status.md` runs **then**
   it prints nothing, and `git check-ignore` names both paths.
2. **Given** two branches from the same `main` commit, each moving a different story's `status`
   **when** `git merge-tree --write-tree` merges them **then** it exits 0 with no conflict.
3. **Given** `make status` **when** it runs **then** it writes `docs/status.md`, prints the same
   bytes to stdout, and exits 0. The same holds for `make backlog` and `BACKLOG.md`.
4. **Given** a push to `main` **when** the `check` job finishes **then** its summary shows both
   views for that commit.
5. **Given** a story with invalid frontmatter **when** `make check` runs **then** it fails through
   `validate-stories`.
6. **Given** frontmatter whose status view would exceed `MAX_LINES` or `MAX_COLS` **when**
   `make check` runs **then** it fails through `status-check`, with no `docs/status.md` present.
7. **Given** CLAUDE.md and `.github/` **when** they're searched for "regenerate", `backlog-check`
   as a freshness check, or an instruction to read or commit `status.md` **then** nothing still
   asks for the committed file.

## Interface contract

- `make backlog`, `make status`: unchanged names. They write the file and print the view to
  stdout. Progress lines stay on stderr.
- `make backlog-check`, `make status-check`: unchanged names, still in `make check`. They render
  and apply the generator's rules, exit 1 on a rule violation, and read no file.
- `gen_backlog.py --check` and `gen_status.py --check` keep their flag. `--check` means "render
  and validate, write nothing".
- CI: two renamed steps, and the `check` job's summary on `main` gains the two views.
- `.gitignore`: `/BACKLOG.md` and `/docs/status.md`.

## Data / state impact

None. The frontmatter stays the record. A past view is reproducible with
`git checkout <sha> && make status`, because the generator is deterministic.

## Observability requirements

- **Metrics / Logs / Traces / Alerts:** none.

## Test plan

- **Unit:** in `scripts/tests/`: `--check` exits 0 with neither file present; it exits 1 on a
  render over the budget (AC-6); `make status` prints what it writes (AC-3).
- **Integration:** CI on the story's own PR and its first merge to `main` (AC-4). AC-2's
  `merge-tree` run is quoted in the verification record.
- **Manual/operator:** `make status`, then AC-1's two commands.

## Definition of done

CLAUDE.md §8.

## Open questions

- None. Brian chose (a) on 2026-09-27.

## Contract review (architecture, 2026-09-28)

SRE's observability review is in `docs/feedback/AW-INF-001-generated-views-conflict.md`: no
change. The story is `ready`.

1. **The `-check` targets stay, as render checks.** The draft dropped them from `make check`. But
   `status-check` is also the only gate on `status.md`'s one-screen budget, since
   `gen_status.py` dies over `MAX_LINES` or `MAX_COLS` before it compares anything. Dropping the
   target would have dropped the budget with it. Keeping the names also keeps CI's parity step
   working without edits to its list. AC-6 tests the budget.
2. **`make status` didn't print the view.** The draft's "prints it, as today" was wrong: today it
   writes the file and prints one `wrote` line. It now prints the view to stdout, which is what
   session start reads.
3. **Two more files named the committed views:** CI's two freshness steps and CODEOWNERS'
   `/BACKLOG.md` entry. Both are in scope now.
4. **AC-2 is mechanical.** "Observed on the next pair of PRs" became a `git merge-tree` run on two
   branches.
5. **`AW-INF-027` lands first** (`depends_on`), and both edit `gen_status.py`. This story changes
   only the `--check` path and printing. 027 changes the lane views, so the two don't overlap in
   meaning.
6. **Brian approves two edits at the hook: CLAUDE.md and `.gitignore`.** Both are unowned paths.
   The `.claude/` wording goes upstream, as the story already says.
