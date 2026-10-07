---
name: role
description: Set this session's role (e.g. pm, product, architecture, sre, implementation, visual-designer, or none) and load its charter, then start an optional task given after the role; or show the current role when run with no argument. Brian runs it at session start. Never invoke it on your own or to change roles mid-task.
argument-hint: "[role|none] [task]"
disable-model-invocation: true
allowed-tools: Bash(.claude/bin/role:*)
---

# Set this session's role

Request: `$ARGUMENTS`
Session: `${CLAUDE_SESSION_ID}`

The first word of the request is the role. Anything after it is a **task**
for this session.

## If no role was given

Run `.claude/bin/role show ${CLAUDE_SESSION_ID}` and `.claude/bin/role list`.
Report the current role and the roles this repo defines, then stop.

## Otherwise

1. Run `.claude/bin/role set <role> ${CLAUDE_SESSION_ID}`, with only the
   first word of the request as `<role>`. Never pass the task text to the
   command.
2. If it exits non-zero, report its output and stop. Don't pick a role yourself.
3. Its output is this session's **role charter**. From now until Brian runs
   `/role` again, it binds together with the repo's `CLAUDE.md`. Where the
   charter narrows the repo file, the charter wins. Where they conflict, stop
   and flag it.
4. Path ownership is enforced by a hook. If an edit is denied, don't work
   around it (no `sed`, `tee`, or shell redirection into that path). Follow the
   role's routing rule instead.
5. Run the session-start steps the repo's `CLAUDE.md` prescribes.
6. Reply with one short block:
   - role and branch prefix
   - the role's skills
   - what the active sprint says this role picks up next, or why it can't pick anything up

   With no task, wait for Brian.

7. With a task, start on it now, as this role and within its charter. If
   the task is another role's work, belongs to no sprint, or needs a skill
   that refuses this role, say so and wait instead of starting.
