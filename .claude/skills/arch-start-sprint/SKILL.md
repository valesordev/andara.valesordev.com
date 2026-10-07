---
name: arch-start-sprint
description: Work the active sprint as architecture, with stories as GitHub issues. Contract review of the sprint's drafts first (implementation waits on it), then the §8 review queue, then architecture's own backlog. Run once PM has activated the sprint; it also resumes a sprint already under way.
disable-model-invocation: true
allowed-tools: Bash(.claude/bin/role:*) Bash(.claude/bin/sprint-state:*) Bash(.claude/bin/story list:*) Bash(.claude/bin/story show:*)
---

# Architecture: work the active sprint

Follow the order of work in your role file.

## Preflight

0. Run `.claude/bin/role require architecture ${CLAUDE_SESSION_ID}`. If it
   fails, stop and tell Brian to run `/role architecture` first.
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
4. Check where the sprint stands. If `story list --sprint <n> --status draft`
   is empty, step 1 is done: go straight to step 2.

## Steps

1. **Contract review.** For every `draft` in the sprint, in Rank order: read
   it (`story show <ID> --comments 20`), check it against the ADRs, specs, and
   repo §5 and §6, and amend the body where it's wrong (`story body <ID>
   --body-file <f> --note <what changed>`). Then `story status <ID> ready`, or
   `story status <ID> blocked --note <what it waits on>`.
   Before moving a story to `ready`, check SRE's `Observability review (sre)`
   comment is there. If it isn't, ready the rest and say which stories wait
   on SRE. Implementation is waiting on this, so do it before anything else.
   Board changes take effect at once; no PR is needed. An ADR or spec change
   you make along the way goes in an `arch/` PR as usual.
2. **§8 review.** Take `story list --status review` oldest first. Move a story
   to `done` only when every §8 item holds:
   - The instrumentation item is SRE's to verify. If there's no
     `§8 instrumentation (sre)` comment yet, leave the story at `review` and
     `story comment <ID> --to sre`.
   - Otherwise record your review with `story comment <ID> --record "§8"`,
     then `story status <ID> done --note <PRs reviewed>`.
   - If an item fails, record it in the `§8` comment and leave the story at
     `review`.
3. **Your own backlog**: `story list --sprint <n> --lane architecture`, in
   Rank order. Start each one with `story start <ID>`, deliver it in an `arch/`
   PR carrying `Story: <ID>` and `role:architecture`, and the merge moves it to
   `review`.

As you reach the stories they concern, answer the comments addressed
`--to architecture` (`story comment <ID> --record "Answer"`).

## Stop

When nothing on your list can be picked up, stop. Report what's blocking each
remaining item: the story, its status, and what it waits on.
