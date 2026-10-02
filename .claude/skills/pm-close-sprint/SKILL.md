---
name: pm-close-sprint
description: Close out the active sprint, whatever its number, and plan the next one in the same PR. Records final story status, writes and runs the demo, triages issues and feedback, then plans SPRINT-NN+1 with the shared planning procedure. Run it once the architecture, SRE, and implementation roles have stopped and reported. If no sprint is active, it stops and points to /pm-start-sprint.
disable-model-invocation: true
allowed-tools: Bash(.claude/bin/role:*) Bash(.claude/bin/sprint-state:*) Bash(.claude/bin/project-mirror:*)
---

# PM: close out the active sprint and plan the next

Follow the sprint-boundary steps in your role file. Run this only once the
architecture, SRE, and implementation roles have stopped and reported.

## Preflight

0. Run `.claude/bin/role require pm ${CLAUDE_SESSION_ID}`. If it fails, stop
   and tell Brian to run `/role pm` first.
1. Run `git fetch origin` and work from `origin/main` (repo §11 session start).
2. Run `.claude/bin/sprint-state`:

   | state | Do |
   |---|---|
   | `active` | continue: **NN** = `current`, **NEXT** = `next` (numbers come from the tool, never from memory or examples) |
   | `closed` or `planned` | stop: nothing is active to close. Point Brian to `/pm-start-sprint` to plan `next` |
   | `first` | stop: no sprint exists yet. Point Brian to `/pm-start-sprint` |
   | `invalid` / error | stop and report the output verbatim; don't repair sprint files |

3. Check for an open PM PR (`gh pr list --state open --json headRefName,url --jq '.[] | select(.headRefName | startswith("pm/"))'`). If
   one already closes NN or plans NEXT, stop and report its URL.
4. Read the session-start documents in the order repo §11 gives them.
5. List NN's stories from `docs/sprints/<NN>.md` (every backlog section plus
   "Contract review" and "Carryover"). If any is still `ready`, `in-progress`,
   or `review` with no blocker explaining it, the working roles may not be
   finished. Tell Brian which stories, and ask whether to proceed before
   closing anything.
6. **Sync NN's board.** Run the Sync steps of
   `.claude/skills/project-sync/SKILL.md`. Brian started this skill, so apply
   without asking again. Then run `.claude/bin/project-mirror --offline` and
   put its `Current (NN): …` progress line in the close-out. If the repo isn't
   configured for a Project, skip this step.

## Close

1. **Close-out.** For every NN story, record its final status on `origin/main`
   and the PR that merged it. Anything not `done` is carryover: say why, citing
   the feedback file, PR, or blocker, not a guess. List any status defects,
   meaning merged work whose frontmatter doesn't match. Report them; don't fix
   them.
2. **Demo.** Write `docs/sprints/<NN>-demo.md` for the demo goal NN set. Then
   run every step yourself on a clean checkout of `origin/main`. If this clone
   hasn't run `make bootstrap`, run it first. Record what actually happened.
   If a step fails, the demo goal wasn't met: say so in the close-out and don't
   write around it. Mark any step that isn't a make target or an `andara-cli`
   command as `§9 defect → AW-INF-NNN`, with an SRE story behind it.
3. **Triage** the GitHub issues opened during NN and the unanswered
   `docs/feedback/` items. For each one, say which role owes it and whether it
   belongs in NEXT.

## Plan NEXT

Read `.claude/skills/pm-start-sprint/references/plan-sprint.md` and follow it
with NEXT, CARRYOVER = the close-out's carryover, and DEFECTS = the demo's §9
defects. At its checkpoint, lead your message to Brian with:

- the close-out: done, carried over, defects
- whether the demo passed, step by step

## Ship

On one `pm/<next-lower>-<slug>` branch (e.g. `pm/sprint-03-trade-routes`), as
one PR:

- the close-out edited into `docs/sprints/<NN>.md`, with `Status: closed`
- `docs/sprints/<NN>-demo.md`
- `docs/sprints/<NEXT>.md` with `Status: active`

Run `make backlog status check`. Commit with the `Sprint: <NEXT>` trailer,
then confirm `.claude/bin/sprint-state --ref HEAD` reports `state: active` and
`current: <NEXT>`. Push and open the PR. Report the PR URL. The other
roles don't start until it merges. Tell Brian to run `/project-sync` once it
merges. That is NN's final sync: it records the close-out and shows NEXT as
Current.
