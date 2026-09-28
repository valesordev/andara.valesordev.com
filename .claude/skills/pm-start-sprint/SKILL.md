---
name: pm-start-sprint
description: Start SPRINT-01, the first sprint, when no sprint file exists yet. Snapshots origin/main into carryover, picks a demo goal, grooms only what it needs, and opens the pm/sprint-01 PR. Not for later sprints; use pm-close-sprint for those.
disable-model-invocation: true
allowed-tools: Bash(.claude/bin/role:*)
---

# PM: start the first sprint

This is the first sprint, so there's nothing to close out and no demo to write
for a previous sprint. Follow the "First sprint" note in your role file.

## Preflight

0. Run `.claude/bin/role require pm ${CLAUDE_SESSION_ID}`. If it
   fails, stop and tell Brian to run `/role pm` first.
1. Run `git fetch origin` and work from `origin/main` (repo §11 session start).
2. List `docs/sprints/`. If any `SPRINT-*.md` already exists, stop. Tell Brian
   this skill is only for SPRINT-01 and that `/pm-close-sprint` is the right one.
3. Read the session-start documents in the order repo §11 gives them.

## Steps

1. **Carryover snapshot.** Snapshot `origin/main` into SPRINT-01's carryover:
   every story `in-progress`, every story at `review` (these become
   architecture's §8 list), and any status defects you find. Use the sources in
   your role file's "Status reporting" section. Don't infer anything from prose.
2. **Demo goal.** Choose the milestone gate from `docs/roadmap.md`, or the slice
   of one, that is the nearest thing an operator can actually run once the
   carryover and the fewest new stories land. Say why you chose it, and say what
   it is not. It can build on the existing M1 gate, `make stack-play`.
3. **Groom** only what that goal needs. Pull `ready` stories as they are. Write
   new stories in full at `draft`, and list them under "Contract review" for
   architecture. A story that needs a decision no ADR covers stays out of the
   sprint, with its question in `docs/feedback/`.
4. **Size.** Keep the sprint small enough to finish: the §8 queue plus one
   demoable slice, not everything that's ready.

## Checkpoint: stop before writing

Before you write any file, send Brian one message containing:

- the demo goal you chose, why, and what it is not
- the story list per role, in pickup order
- any game-design questions (up to 3, per repo §6)

Wait for his answer, then write.

## Write

5. Write `docs/sprints/SPRINT-01.md` in the role file's sprint format, with
   `Status: active`.
6. Run `make backlog status check`. Fix what it reports in files you own. If it
   fails on something you don't own, report it and don't touch it.
7. Commit on `pm/sprint-01-<slug>` with the `Sprint: SPRINT-01` trailer, push,
   and open the PR. Report the PR URL. The other roles don't start until it merges.
