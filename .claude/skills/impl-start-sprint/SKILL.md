---
name: impl-start-sprint
description: Work the active sprint as implementation. Finishes carried-over in-progress stories first, then takes the implementation backlog in order, one fresh impl/ branch per story. Run after architecture's contract-review PR merges; it also resumes a sprint already under way.
disable-model-invocation: true
allowed-tools: Bash(.claude/bin/role:*)
---

# Implementation: work the active sprint

## Preflight

0. Run `.claude/bin/role require implementation ${CLAUDE_SESSION_ID}`. If it
   fails, stop and tell Brian to run `/role implementation` first.
1. Run `git fetch origin` and work from `origin/main` (repo §11 session start).
2. Find the one `docs/sprints/SPRINT-*.md` with `Status: active`. If there's
   none, PM hasn't planned one yet: stop and say so.
3. Read the session-start documents in the order repo §11 gives them.
4. If every story you could pick is still `draft`, architecture's contract
   review hasn't merged. Stop and say so.

## Carryover first

Start with any story the sprint carries over as `in-progress`. Read the story
and its feedback file in `docs/feedback/`, and build only what's left. Don't
redo what's already merged. Anything the feedback file hands to architecture
stays architecture's.

## Then the backlog

Take your implementation backlog in the listed order: the first story that's
`ready` with every `depends_on` at `review` or later. For each story:

- use a fresh `impl/<story-id>-<slug>` branch from `origin/main`
- move it to `in-progress` in the first commit, with `make backlog status` in
  that same commit
- meet the Acceptance criteria and Interface contract exactly
- in the delivering PR, move it to `review` and fill in its verification record

If a story is wrong or can't be built as written, write it up in its feedback
file and move on to the next story you can pick up.

## Stop

When nothing on your list can be picked up, stop. Report what each remaining
story is waiting on: the story, its status, and the dependency or feedback
item holding it.
