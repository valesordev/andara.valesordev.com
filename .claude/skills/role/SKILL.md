---
name: role
description: Set this session's role (e.g. pm, product, architecture, sre, implementation, visual-designer, or none) and load its charter, then start an optional task given after the role; or show the current role when run with no argument, or the repo's role catalog with `help`. The owner runs it at session start. Never invoke it on your own or to change roles mid-task.
argument-hint: "[role|none|help] [task]"
disable-model-invocation: true
allowed-tools: Bash(.claude/bin/role:*), EnterWorktree
---

# Set this session's role

Request: `$ARGUMENTS`
Session: `${CLAUDE_SESSION_ID}`

The first word of the request is the role. Anything after it is a **task**
for this session.

## If the first word is `help`

Run `.claude/bin/role help`, show its output as it is (a markdown table), and
stop. `help` is never a role: don't run `role set help`, and treat anything after
it as nothing to start.

## If no role was given

Run `.claude/bin/role show ${CLAUDE_SESSION_ID}` and `.claude/bin/role list`.
Report the current role and the roles this repo defines, then stop.

## Otherwise

1. Run `.claude/bin/role set <role> ${CLAUDE_SESSION_ID}`, with only the
   first word of the request as `<role>`. Never pass the task text to the
   command.
2. If it exits non-zero, report its output and stop. Don't pick a role yourself.
3. If the output starts with `Worktree: <name> (enter it …)`, call
   EnterWorktree with that `name` now, before any other step. The session then
   works in `.claude/worktrees/<name>`, on a branch from `origin/main`, and the owner
   keeps the checkout they launched from. Create the role's branch there when work
   begins. If EnterWorktree fails, report it and stop: never work in the owner's
   checkout instead. `Worktree: this session already works in …` needs nothing.
   No `Worktree:` line (`/role none`, a cloud session) means stay where you are.
   Scratch files (downloads, rendered configs, notes, PR bodies) stay in
   Claude Code's scratchpad directory, which still works from the worktree.
   Name each scratchpad file by its full literal path, every time, and never
   through a shell variable (`F=…; sed -n 1,9p $F`): the worktree guard
   refuses commands built from a computed value. Never copy scratch files
   into the worktree (`.local/` or any other ignored directory included),
   because an edit there is outside the role's `writes`, so each one waits
   on the owner's approval.
4. The rest of the output is this session's **role charter**. From now until the owner runs
   `/role` again, it binds together with the repo's `CLAUDE.md`. Where the
   charter narrows the repo file, the charter wins. Where they conflict, stop
   and flag it.
5. Path ownership is enforced by a hook. If an edit is denied, don't work
   around it (no `sed`, `tee`, or shell redirection into that path). Follow the
   role's routing rule instead.
6. Run the session-start steps the repo's `CLAUDE.md` prescribes.
7. Reply with one short block:
   - role and branch prefix
   - the worktree it works in, if any
   - the role's skills
   - what the active sprint says this role picks up next, or why it can't pick anything up

   With no task, wait for the owner.

8. With a task, start on it now, as this role and within its charter. If
   the task is another role's work, belongs to no sprint, or needs a skill
   that refuses this role, say so and wait instead of starting.
