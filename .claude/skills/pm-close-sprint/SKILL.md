---
name: pm-close-sprint
description: Close out the active sprint and plan the next one. Records final story status, writes and runs the demo, triages issues and feedback, plans SPRINT-NN+1, and opens one pm/sprint PR. Run it once the architecture, SRE, and implementation roles have stopped and reported.
disable-model-invocation: true
allowed-tools: Bash(.claude/bin/role:*)
---

# PM: close out a sprint and plan the next

Follow the sprint-boundary steps in your role file. Run this only once the
architecture, SRE, and implementation roles have stopped and reported, with every sprint story either `done` or
`blocked`.

## Preflight

0. Run `.claude/bin/role require pm ${CLAUDE_SESSION_ID}`. If it
   fails, stop and tell Brian to run `/role pm` first.
1. Run `git fetch origin` and work from `origin/main` (repo §11 session start).
2. Find the one `docs/sprints/SPRINT-*.md` with `Status: active`. Its number is
   **NN** and the next sprint is **NN+1**, zero-padded (SPRINT-02 → SPRINT-03).
   If there isn't exactly one active sprint, stop and report what you found.
   If no sprint file exists at all, stop and point Brian to `/pm-start-sprint`.
3. Read the session-start documents in the order repo §11 gives them.
4. If a SPRINT-NN story is still `ready`, `in-progress`, or `review` with no
   blocker explaining it, the working roles may not be finished. Tell Brian which
   stories, and ask whether to proceed before closing anything.

## Steps

1. **Close-out.** For every SPRINT-NN story, record its final status on
   `origin/main` and the PR that merged it. Anything not `done` is carryover:
   say why, citing the feedback file, PR, or blocker, not a guess. List any
   status defects, meaning merged work whose frontmatter doesn't match. Report
   them; don't fix them.
2. **Demo.** Write `docs/sprints/SPRINT-NN-demo.md` for the demo goal
   SPRINT-NN set. Then run every step yourself on a clean checkout of
   `origin/main`. If this clone hasn't run `make bootstrap`, run it first.
   Record what actually happened. If a step fails, the demo goal wasn't met:
   say so in the close-out and don't write around it. Mark any step that isn't
   a make target or an `andara-cli` command as `§9 defect → AW-INF-NNN`, with an
   SRE story behind it.
3. **Triage** the GitHub issues opened this sprint and the unanswered
   `docs/feedback/` items. For each one, say which role owes it and whether it
   belongs in SPRINT-NN+1.
4. **Plan SPRINT-NN+1** the way `/pm-start-sprint` does: demo goal, groom only
   what it needs, keep it small enough to finish. Put carryover first, then any
   §9 defect stories, then the next demoable slice.

## Checkpoint: stop before writing SPRINT-NN+1

Send Brian one message containing:

- the close-out: done, carried over, defects
- whether the demo passed, step by step
- SPRINT-NN+1's demo goal and its story list per role
- any game-design questions (up to 3, per repo §6)

Wait for his answer, then write.

## Write

5. On one `pm/sprint-<NN+1>-<slug>` branch, as one PR:
   - the close-out edited into `SPRINT-NN.md`, with `Status: closed`
   - `docs/sprints/SPRINT-NN-demo.md`
   - `docs/sprints/SPRINT-<NN+1>.md` with `Status: active`
6. Run `make backlog status check`. Fix what it reports in files you own. If it
   fails on something you don't own, report it and don't touch it.
7. Commit with the `Sprint: SPRINT-<NN+1>` trailer, push, and open the PR.
   Report the PR URL. The other roles don't start until it merges.
