---
name: sre-start-sprint
description: Work the active sprint as SRE, with stories as GitHub issues. Observability review of the sprint's drafts first (architecture's contract review waits on it), then instrumentation verification for stories at review, then SRE's own backlog. Run once PM has activated the sprint; it also resumes a sprint already under way.
disable-model-invocation: true
allowed-tools: Bash(.claude/bin/role:*) Bash(.claude/bin/sprint-state:*) Bash(.claude/bin/story list:*) Bash(.claude/bin/story show:*)
---

# SRE: work the active sprint

Follow the order of work in your role file.

## Preflight

0. Run `.claude/bin/role require sre ${CLAUDE_SESSION_ID}`. If it
   fails, stop and tell Brian to run `/role sre` first.
   Then confirm the repo has switched over: `.claude/roles/_repo.md` has
   `stories: github` and `sprints: project`. If it doesn't, stop. This skill
   works the GitHub board, and this repo still keeps stories in files.
1. Run `git fetch origin` and work from `origin/main` (repo §11 session start).
   Read `.claude/skills/role/references/stories-on-github.md`.
2. Run `.claude/bin/sprint-state`. Unless it reports `state: active`, stop and
   report its output: PM hasn't activated a sprint yet. The active sprint is
   `current`; its goal is in the `current` sprint issue, and its stories are
   `story list --sprint <n>`.
3. Read the session-start documents in the order repo §11 gives them, then
   every SLO doc in `docs/specs/slo/` and the runbook index.
4. Check where the sprint stands. If every `draft` in the sprint already has
   an `Observability review (sre)` comment, step 1 is done: go straight to
   step 2.

## Steps

1. **Observability review.** For every `draft` in the sprint, check its
   Observability requirements section against repo §7:
   - metric names, types, labels, and cardinality bounds (no unbounded labels)
   - required log fields, including the correlation ID
   - span names and parentage
   - symptom-based alerts tied to an SLO, each with its runbook entry

   Amend only that section (`story body <ID> --body-file <f> --note
   "observability"`: keep every other section byte-for-byte). Then record
   `story comment <ID> --record "Observability review"` with what you checked
   and changed. Architecture's contract review waits on this, so do it first.
   Don't change status; that's architecture's.
2. **Instrumentation verification.** For each story at `review`
   (`story list --status review`), verify its §7 instrumentation is emitting
   against a real backend (`make up`), or record exactly which series have no
   caller yet, per repo §8. Record it with `story comment <ID> --record
   "§8 instrumentation"`. Architecture moves the story to `done`.
3. **Your own backlog**: `story list --sprint <n> --lane sre`, in Rank order.
   Start each one with `story start <ID>`, and deliver it in an `sre/` PR
   carrying `Story: <ID>` and `role:sre`. The merge moves it to `review`.

As you reach the stories they concern, answer the comments addressed
`--to sre`.

## Stop

When nothing on your list can be picked up, stop. Report what's blocking each
remaining item: the story, its status, and what it waits on.
