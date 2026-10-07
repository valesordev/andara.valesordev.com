---
name: pm-close-sprint
description: Close out the active sprint and plan the next one, with stories and sprints as GitHub issues. Records final story status, writes and runs the demo, triages issues and comments addressed to PM, closes the sprint issue, then plans SPRINT-NN+1 with the shared planning procedure. Run it once the architecture, SRE, and implementation roles have stopped and reported. If no sprint is active, it stops and points to /pm-start-sprint.
disable-model-invocation: true
allowed-tools: Bash(.claude/bin/role:*) Bash(.claude/bin/sprint-state:*) Bash(.claude/bin/story list:*) Bash(.claude/bin/story show:*)
---

# PM: close out the active sprint and plan the next

Follow the sprint-boundary steps in your role file. Run this only once the
architecture, SRE, and implementation roles have stopped and reported.

## Preflight

0. Run `.claude/bin/role require pm ${CLAUDE_SESSION_ID}`. If it fails, stop
   and tell Brian to run `/role pm` first.
   Then confirm the repo has switched over: `.claude/roles/_repo.md` has
   `stories: github` and `sprints: project`. If it doesn't, stop. This skill
   works the GitHub board, and this repo still keeps stories in files.
1. Run `git fetch origin` and work from `origin/main` (repo §11 session start).
   Read `.claude/skills/role/references/stories-on-github.md`.
2. Run `.claude/bin/sprint-state`:

   | state | Do |
   |---|---|
   | `active` | continue: **NN** = `current`, **NEXT** = `next` (numbers come from the tool, never from memory or examples) |
   | `closed` or `planned` | stop: nothing is active to close. Point Brian to `/pm-start-sprint` |
   | `first` | stop: no sprint exists yet. Point Brian to `/pm-start-sprint` |
   | `invalid` / error | stop and report the output verbatim; don't repair sprint issues |

3. Check for an open PM PR. If one already closes NN, stop and report its URL.
4. Read the session-start documents in the order repo §11 gives them.
5. List NN's stories: `story list --sprint <nn>`. If any is still `ready`,
   `in-progress`, or `review` with no blocker or comment explaining it, the
   working roles may not be finished. Tell Brian which stories, and ask
   whether to proceed before closing anything.

## Close

1. **Close-out** in `docs/sprints/<NN>-closeout.md`. For every NN story,
   record its final status and the PR that merged it. Anything not `done` is
   carryover: say why, citing the comment, PR, or blocker, not a guess. List
   any status defects: merged work whose story is behind, or a red
   story-merge run (`gh run list --workflow story-merge.yml`). Report them;
   don't fix them.
2. **Demo.** Write `docs/sprints/<NN>-demo.md` for the demo goal in NN's
   sprint issue. Then run every step yourself on a clean checkout of
   `origin/main`. If this clone hasn't run `make bootstrap`, run it first.
   Record what actually happened. If a step fails, the demo goal wasn't met:
   say so in the close-out and don't write around it. Mark any step that isn't
   a make target or an `andara-cli` command as `§9 defect → AW-INF-NNN`, with
   an SRE story behind it.
3. **Triage** GitHub issues opened during NN, `content-gap` issues, and story
   comments addressed `--to pm`. For each one, say which role owes it and
   whether it belongs in NEXT.

## Plan NEXT

Read `.claude/skills/pm-start-sprint/references/plan-sprint.md` and follow it
with NEXT, CARRYOVER = the close-out's carryover, and DEFECTS = the demo's §9
defects. At its checkpoint, lead your message to Brian with:

- the close-out: done, carried over, defects
- whether the demo passed, step by step

## Ship

After Brian's answer, in this order:

1. On one `pm/<next-lower>-<slug>` branch, commit the close-out, the demo, and
   any roadmap or glossary change. Run `make check`, then `/pre-pr`, push, and
   open the PR with `--label role:pm`.
2. `story sprint close <nn> --note "close-out: <PR URL>"`.
3. The planning procedure's *Write* step, which ends with `story sprint activate`.

Report the PR URL, the new sprint issue, and `sprint-state`'s output.
