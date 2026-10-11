---
name: design-start-sprint
description: Work the active sprint as visual-designer, with stories as GitHub issues. Answers brand questions addressed to visual-designer first (implementation waits on them), then finishes carried-over in-progress stories, then takes the visual-designer backlog in Rank order, one fresh design/ branch per story. Run once PM has activated the sprint; it also resumes a sprint already under way.
disable-model-invocation: true
allowed-tools: Bash(.claude/bin/role:*) Bash(.claude/bin/sprint-state:*) Bash(.claude/bin/story list:*) Bash(.claude/bin/story show:*)
---

# Visual designer: work the active sprint

## Preflight

0. Run `.claude/bin/role require visual-designer ${CLAUDE_SESSION_ID}`. If it
   fails, stop and tell Brian to run `/role visual-designer` first.
   Then confirm the repo has switched over: `.claude/roles/_repo.md` has
   `stories: github` and `sprints: project`. If it doesn't, stop. This skill
   works the GitHub board.
1. Run `git fetch origin` and work from `origin/main`. Read
   `.claude/skills/role/references/stories-on-github.md`.
2. Run `.claude/bin/sprint-state`. Unless it reports `state: active`, stop and
   report its output: PM hasn't activated a sprint yet. The active sprint is
   `current`; its goal is in the `current` sprint issue, and its stories are
   `story list --sprint <n>`.
3. Read the repo's `CLAUDE.md` and your role file's session-start steps.

## Brand questions first

Code never decides branding, so implementation stops on a story until you
answer. For every story in `story list --sprint <n>` (any lane), read the
thread (`story show <ID> --comments 30`) and find `**For visual-designer:**`
comments with no answer after them. For each:

- If the brand docs already decide it, reply citing `file#section`
  (`story comment <ID> --to <asking role> --body …`).
- If they don't and it's yours to decide (a spec detail, a token derived from
  an approved decision), change the brand doc on a `design/` branch, then reply
  with the PR and the `file#section`.
- If it's Brian's call (palette, type, marks, voice, direction), bring him one
  recommendation, then reply with his decision and the doc change.

## Carryover

Then any of your stories that's already `in-progress`. Read it and its thread,
and finish only what's left. Don't redo what's already merged.

## Then the backlog

Take `story list --sprint <n> --lane visual-designer` in Rank order: the first
story that's `ready` with every blocker (`Blocked by:`) at `review` or later.
For each story:

- use a fresh `design/<story-id>-<slug>` branch from `origin/main`
- `story start <ID>` when you begin
- use the skill the work calls for (`/brand-audit`, `/art-direction`,
  `/art-slot`, `/brand-qa seed`) and meet the Acceptance criteria in the
  issue body exactly. A decision that's Brian's stays his: the PR asks for it.
- put `Story: <ID>` in the PR body and in a commit trailer, and label the PR
  `role:visual-designer`. The merge moves the story to `review`. Never move it
  there yourself.
- once the PR is open, record what you verified:
  `story comment <ID> --record "Verification" --body-file <f>`, citing commits
  and the outputs you looked at

If a story is wrong or can't be done as written, `story comment <ID> --to
architecture` with what and why, and move on. Work that isn't in the sprint
goes to PM with `story comment <ID> --to pm` or a plain issue.

## Stop

When nothing on your list can be picked up and no brand question is waiting,
stop. Report the questions you answered, and what each remaining story is
waiting on: the story, its status, and the blocker, comment, or Brian decision
holding it.
