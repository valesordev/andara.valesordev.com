---
name: impl-start-sprint
description: Work the active sprint as implementation, with stories as GitHub issues. Finishes carried-over in-progress stories first, then takes the implementation backlog in Rank order, one fresh impl/ branch per story. Run once architecture's contract review has readied stories; it also resumes a sprint already under way.
disable-model-invocation: true
allowed-tools: Bash(.claude/bin/role:*) Bash(.claude/bin/sprint-state:*) Bash(.claude/bin/story list:*) Bash(.claude/bin/story show:*)
---

# Implementation: work the active sprint

## Preflight

0. Run `.claude/bin/role require implementation ${CLAUDE_SESSION_ID}`. If it
   fails, stop and tell Brian to run `/role implementation` first.
   Then confirm the repo has switched over: `.claude/roles/_repo.md` has
   `stories: github` and `sprints: project`. If it doesn't, stop. This skill
   works the GitHub board, and this repo still keeps stories in files.
1. Run `git fetch origin` and work from `origin/main` (repo §11 session start).
   Read `.claude/skills/role/references/stories-on-github.md`.
2. Run `.claude/bin/sprint-state`. Unless it reports `state: active`, stop and
   report its output: PM hasn't activated a sprint yet. The active sprint is
   `current`; its goal is in the `current` sprint issue, and its stories are
   `story list --sprint <n>`.
3. Read the session-start documents in the order repo §11 gives them.
4. If every story in `story list --sprint <n> --lane implementation` is still
   `draft`, architecture's contract review isn't done. Stop and say so.

## Carryover first

Start with any of your stories that is already `in-progress`. Read it and its
thread (`story show <ID> --comments 30`), and build only what's left. Don't
redo what's already merged. Anything a comment hands to architecture stays
architecture's.

## Then the backlog

Take `story list --sprint <n> --lane implementation` in Rank order: the first
story that's `ready` with every blocker (`Blocked by:`) at `review` or later.
For each story:

- use a fresh `impl/<story-id>-<slug>` branch from `origin/main`
- `story start <ID>` when you begin
- meet the Acceptance criteria and Interface contract in the issue body exactly
- put `Story: <ID>` in the PR body and in a commit trailer, and label the PR
  `role:implementation`. The merge moves the story to `review`. Never move it
  there yourself.
- once the PR is open, record what you verified:
  `story comment <ID> --record "Verification" --body-file <f>`, citing commits

If a story is wrong or can't be built as written, `story comment <ID> --to
architecture` with what and why, and move on to the next story you can pick up.

## Stop

When nothing on your list can be picked up, stop. Report what each remaining
story is waiting on: the story, its status, and the blocker or comment
holding it.
