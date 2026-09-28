---
name: sre-start-sprint
description: Work the active sprint as SRE. Observability review of the sprint's drafts first (architecture's contract review waits on it), then instrumentation verification for stories at review, then SRE's own backlog. Run after the PM sprint PR merges; it also resumes a sprint already under way.
disable-model-invocation: true
allowed-tools: Bash(.claude/bin/role:*)
---

# SRE: work the active sprint

Follow the order of work in your role file.

## Preflight

0. Run `.claude/bin/role require sre ${CLAUDE_SESSION_ID}`. If it fails, stop
   and tell Brian to run `/role sre` first.
1. Run `git fetch origin` and work from `origin/main` (repo §11 session start).
2. Find the one `docs/sprints/SPRINT-*.md` with `Status: active`. If there's
   none, PM hasn't planned one yet: stop and say so.
3. Read the session-start documents in the order repo §11 gives them, then
   every SLO doc in `docs/specs/slo/` and the runbook index.
4. Check where the sprint stands. If every story on the "Contract review" list
   already has an SRE observability review, step 1 is done: go straight to
   step 2.

## Steps

1. **Observability review.** For every story on the sprint's "Contract review"
   list, check its Observability requirements section against repo §7:
   - metric names, types, labels, and cardinality bounds (no unbounded labels)
   - required log fields, including the correlation ID
   - span names and parentage
   - symptom-based alerts tied to an SLO, each with its runbook entry

   Amend only that section, in one `sre/` PR for the batch, and record the
   review in each story's feedback file under an Architecture heading.
   Architecture's contract review waits on this, so open the PR first and
   report its URL. Don't change status; that's architecture's.
2. **Instrumentation verification.** For each story at `review`, verify its §7
   instrumentation is emitting against a real backend (`make up`), or record
   exactly which series have no caller yet, per repo §8. Write the result into
   the story's §8 record on an `sre/…-verify` branch. Architecture moves the
   story to `done`.
3. **Your own backlog**, in the order the sprint lists it. Move each story
   through `in-progress` and `review` the way implementation does.

As you reach the stories they concern, answer any `docs/feedback/` items
addressed to SRE.

## Stop

When nothing on your list can be picked up, stop. Report what's blocking each
remaining item: the story, its status, and what it waits on.
