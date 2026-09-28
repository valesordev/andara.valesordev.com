---
name: arch-start-sprint
description: Work the active sprint as architecture. Contract review of the sprint's drafts first (implementation waits on it), then the §8 review queue, then architecture's own backlog. Run after the PM sprint PR merges; it also resumes a sprint already under way.
disable-model-invocation: true
allowed-tools: Bash(.claude/bin/role:*) Bash(.claude/bin/sprint-state:*)
---

# Architecture: work the active sprint

Follow the order of work in your role file.

## Preflight

0. Run `.claude/bin/role require architecture ${CLAUDE_SESSION_ID}`. If it
   fails, stop and tell Brian to run `/role architecture` first.
1. Run `git fetch origin` and work from `origin/main` (repo §11 session start).
2. Run `.claude/bin/sprint-state`. Unless it reports `state: active`, stop and
   report its output: PM hasn't planned a sprint yet, or the sprint files need
   PM's attention. The active sprint is `current`, `docs/sprints/<current>.md`.
3. Read the session-start documents in the order repo §11 gives them.
4. Check where the sprint stands. If every story on the "Contract review" list
   is already past `draft` on `origin/main`, or an open `arch/` PR covers them,
   step 1 is done: go straight to step 2.

## Steps

1. **Contract review.** Go through every story on the sprint's "Contract
   review" list against the ADRs, specs, and repo §5 and §6. Amend what's
   wrong, then move each story to `ready`, or to `blocked` with the reason, all
   in one PR. Before moving a story to `ready`, check the SRE observability
   review is in (the story's feedback file, or SRE's amendments to its
   Observability requirements section). If it isn't, move the rest and say
   which stories wait on SRE. Implementation is waiting on this, so open that
   PR before you start anything else. Report its URL.
2. **§8 review.** Take the `review` queue oldest first, one `arch/…-review`
   branch per batch. Move a story to `done` only when every §8 item holds. The
   instrumentation item is SRE's to verify: if the story's §8 record has no SRE
   verification yet, leave it at `review` and note it in the feedback file
   under an SRE heading. If an item fails, write it up in the story's feedback
   file and leave the story at `review`.
3. **Your own backlog**, in the order the sprint lists it. Move each story
   through `in-progress` and `review` the way implementation does.

As you reach the stories they concern, answer any `docs/feedback/` items
addressed to architecture.

## Stop

When nothing on your list can be picked up, stop. Report what's blocking each
remaining item: the story, its status, and what it waits on.
