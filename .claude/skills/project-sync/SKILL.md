---
name: project-sync
description: Report where the active sprint stands (done/total, open stories by lane and status, open PRs), first syncing the GitHub Project from the story and sprint files on origin/main in repos that still keep stories in files. Trigger on "sync the project", "update the board", "sync github", "where are we in the sprint", "sprint status", and at sprint boundaries (the PM sprint skills call it). Not for editing stories or sprint plans (those are PM's files), and not for issues the mirror didn't create.
argument-hint: "[--dry-run]"
allowed-tools: Bash(.claude/bin/project-mirror:*) Bash(.claude/bin/sprint-state:*) Bash(.claude/bin/story list:*) Bash(git fetch:*) Bash(gh pr list:*)
---

# Sync the GitHub Project and report the sprint

**If `.claude/roles/_repo.md` has `stories: github`**, the board is the source
of truth and there's nothing to mirror. Skip section 1, and report from the
board instead (section 2 with `sprint-state`, `story list --sprint <n>`, and
`gh pr list`; the progress line is your count of `story list --sprint <n>` by
status).

The board is a one-way mirror. Story files on `origin/main` are the source of
truth, and any GitHub-side edit to a mirrored issue or field is overwritten.
Stories change on the board only after their PR merges.

## 1. Sync

1. `git fetch origin`.
2. Run `.claude/bin/project-mirror` (a dry run).
   - If it says **not configured**, stop and say so. This repo has no `project:` in `.claude/roles/_repo.md`.
   - If `gh` reports a missing scope, stop and ask Brian to run `! gh auth refresh -s project`.
   - The summary line comes just before the Project URL. If it is `in sync`, skip to step 4.
3. Unless `$ARGUMENTS` contains `--dry-run`, run `.claude/bin/project-mirror --apply`.
   - Writing to GitHub is outward-facing. Apply only when Brian asked for the sync, either directly or by running a sprint skill that calls this step.
   - Otherwise, show the dry run's summary line and ask first.
4. Run the dry run again. Its summary line must be `in sync`. If it doesn't, report the remaining changes; don't loop.

## 2. Report

Run `.claude/bin/sprint-state` and `.claude/bin/project-mirror --offline`, and
`gh pr list --state open --json number,title,headRefName,labels`. Then reply
with:

- **Progress line**: copy it verbatim from `--offline`, e.g. `Current (SPRINT-03): 12/20 done — 2 ready, 6 review, 12 done`.
- **Open stories**: a table of ID, title, lane, status, and open PR if any. Order it by sprint rank.
- **Next sprint**: the `Next (SPRINT-NN)` block, if a planned sprint exists.
- **What the sprint end depends on**: only what the files say.
  - Stories still at `ready` on the demo path.
  - `blocked` stories and what blocks them.
  - Open issues a sprint file pulled in.

  Cite the sprint file or feedback file. Don't guess.
- The Project URL from the sync output.

End with one next action, sized for a Kanban pull. Name the role that owns it.
