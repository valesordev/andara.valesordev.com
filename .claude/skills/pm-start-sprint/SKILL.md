---
name: pm-start-sprint
description: Plan and start the next sprint when no sprint is active, with stories and sprints as GitHub issues. Works for the first sprint, after a sprint was closed without a successor, or to activate a sprint left at planned. If a sprint is still active, it stops and points to /pm-close-sprint, which closes it and plans the next.
disable-model-invocation: true
allowed-tools: Bash(.claude/bin/role:*) Bash(.claude/bin/sprint-state:*) Bash(.claude/bin/story list:*) Bash(.claude/bin/story show:*)
---

# PM: start the next sprint

## Preflight

0. Run `.claude/bin/role require pm ${CLAUDE_SESSION_ID}`. If it fails, stop
   and tell Brian to run `/role pm` first.
   Then confirm the repo has switched over: `.claude/roles/_repo.md` has
   `stories: github` and `sprints: project`. If it doesn't, stop. This skill
   works the GitHub board, and this repo still keeps stories in files.
1. Run `git fetch origin` and work from `origin/main` (repo §11 session start).
   Read `.claude/skills/role/references/stories-on-github.md`.
2. Run `.claude/bin/sprint-state`. Its `state:` decides what happens next:

   | state | Meaning | Do |
   |---|---|---|
   | `first` | no sprint issues | plan **SPRINT-01**: *First sprint* below |
   | `closed` | last sprint closed, none planned | plan `next`: *After a closed sprint* below |
   | `planned` | `next` exists, labeled `sprint:planned` | *Activate a planned sprint* below |
   | `active` | `current` is still running | stop: `/pm-close-sprint` closes it and plans `next` |
   | `invalid` / error | issues contradict each other, or GitHub can't be read | stop and report the output verbatim; don't repair sprint issues |

3. Check for an open PM PR (`gh pr list --state open --json headRefName,url --jq '.[] | select(.headRefName | startswith("pm/"))'`).
   If one already holds `next`'s planning, stop and report its URL.
4. Read the session-start documents in the order repo §11 gives them.

## First sprint (`state: first`)

- **CARRYOVER**: a snapshot of the board. That's every story `in-progress`,
  every story at `review` (they go on architecture's §8 list), and any status
  defects. Use the sources in your role file's "Status reporting", and don't
  infer anything from prose.
- **DEFECTS**: none.

## After a closed sprint (`state: closed`)

`current` is the closed sprint, **PREV**.

- **CARRYOVER**: the carryover list in `docs/sprints/<PREV>-closeout.md`,
  re-checked against the board. Anything now `done` drops out. Anything the
  close-out missed that is still `in-progress` or `review` is added and
  flagged as a status finding.
- **DEFECTS**: every `§9 defect → AW-INF-NNN` in `docs/sprints/<PREV>-demo.md`.
- If PREV has no close-out or no demo on `origin/main`, it wasn't closed
  properly. Stop and tell Brian; closing is `/pm-close-sprint`'s job.

## Activate a planned sprint (`state: planned`)

`next` already exists. Re-check its stories (`story list --sprint <n>`):
drop what's `done`, add carryover the plan missed, and confirm every
blocker still holds (`story show`). Then continue at the checkpoint in the
planning procedure. Its last step activates the sprint.

## Plan

Read `.claude/skills/pm-start-sprint/references/plan-sprint.md` and follow it
with NEXT = `next`, CARRYOVER, and DEFECTS from above.

## Ship

The board changes take effect when you make them. The PR is only for files
(roadmap, glossary): open it on `pm/<next-lower>-<slug>` if you changed any,
and report its URL. Report the sprint issue's URL and `sprint-state`'s
output. Other roles start when the sprint is active.
