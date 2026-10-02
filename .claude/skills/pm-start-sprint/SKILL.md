---
name: pm-start-sprint
description: Plan and start the next sprint when no sprint is active. Works for SPRINT-01 (no sprint files yet), after a sprint was closed without a successor, or to activate a sprint left at planned. If a sprint is still active, it stops and points to /pm-close-sprint, which closes it and plans the next in one PR.
disable-model-invocation: true
allowed-tools: Bash(.claude/bin/role:*) Bash(.claude/bin/sprint-state:*) Bash(.claude/bin/project-mirror:*)
---

# PM: start the next sprint

## Preflight

0. Run `.claude/bin/role require pm ${CLAUDE_SESSION_ID}`. If it fails, stop
   and tell Brian to run `/role pm` first.
1. Run `git fetch origin` and work from `origin/main` (repo §11 session start).
2. Run `.claude/bin/sprint-state`. Its `state:` decides what happens next:

   | state | Meaning | Do |
   |---|---|---|
   | `first` | no sprint files | plan **SPRINT-01**: *First sprint* below |
   | `closed` | last sprint closed, none planned | plan `next`: *After a closed sprint* below |
   | `planned` | `next` exists at `Status: planned` | *Activate a planned sprint* below |
   | `active` | `current` is still running | stop: tell Brian `current` must be closed first and `/pm-close-sprint` closes it and plans `next` in one PR |
   | `invalid` / error | files contradict each other, or no `origin/main` | stop and report the output verbatim; don't repair sprint files |

3. Check for an open PM PR (`gh pr list --state open --json headRefName,url --jq '.[] | select(.headRefName | startswith("pm/"))'`). If
   one already plans `next`, stop and report its URL rather than planning twice.
4. Read the session-start documents in the order repo §11 gives them.
5. Run the Sync steps of `.claude/skills/project-sync/SKILL.md` so the board
   matches `origin/main` before planning. Brian started this skill, so apply
   without asking again. If the repo isn't configured for a Project, skip this
   step.

## First sprint (`state: first`)

Follow the "First sprint" note in your role file. There's nothing to close and
no previous demo.

- **CARRYOVER**: snapshot `origin/main`: every story `in-progress`, every
  story at `review` (they go on architecture's §8 list), and any status defects
  you find. Use the sources in your role file's "Status reporting" section, and
  don't infer anything from prose.
- **DEFECTS**: none.

## After a closed sprint (`state: closed`)

`current` is the closed sprint, **PREV**.

- **CARRYOVER**: take PREV's `## Close-out` carryover list, then check each
  story against its frontmatter on `origin/main` (`make status`). Anything
  merged to `done` since the close-out drops out. Anything the close-out
  missed that is still `in-progress` or `review` is added, flagged as a
  status finding.
- **DEFECTS**: every `§9 defect → AW-INF-NNN` in `docs/sprints/<PREV>-demo.md`.
- If PREV has no `## Close-out` content or no demo file, it wasn't closed
  properly. Stop and tell Brian; closing is `/pm-close-sprint`'s job.

## Activate a planned sprint (`state: planned`)

`next` already exists at `Status: planned`. Re-check it against `origin/main`
instead of replanning from scratch: drop stories that are now `done`, add
carryover the plan missed, and confirm every `depends_on` still holds. Then
continue at the checkpoint in the planning procedure, and set
`Status: active` when you write.

## Plan

Read `.claude/skills/pm-start-sprint/references/plan-sprint.md` and follow it
with NEXT = `next`, CARRYOVER, and DEFECTS from above.

## Ship

On one `pm/<next-lower>-<slug>` branch (e.g. `pm/sprint-02-combat-loop`),
commit with the `Sprint: <NEXT>` trailer, push, and open the PR. Report the PR
URL. The other roles don't start until it merges. Tell Brian to run
`/project-sync` once it merges, so the board shows the new sprint.
