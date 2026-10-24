---
name: readiness-scan
description: Scan the sprint's ready and review stories for what will stop the role that picks them up (acceptance criteria that can't run yet, records a move still needs, spec or contract inconsistencies, decisions not recorded where they belong), report them grouped by the role that has to clear each, say which role to run first, and mark failing ready stories blocked. Runs inside the shared sprint planning procedure (/pm-start-sprint, /pm-close-sprint), and on its own mid-sprint. `--report` never writes, for /sprint-run's dispatcher. Trigger on "/readiness-scan", "what's going to block the lanes", "is the sprint ready to run", "which role should I run first". Not for planning or activating a sprint (pm-start-sprint), working a story (the role's start-sprint skill), or the §8 review itself (arch-start-sprint).
argument-hint: "[--report] [--sprint N]"
allowed-tools: Bash(.claude/bin/role:*) Bash(.claude/bin/sprint-state:*) Bash(.claude/bin/story list:*) Bash(.claude/bin/story show:*) Bash(.claude/bin/story check:*) Bash(git fetch:*) Bash(git ls-files:*) Bash(git grep:*)
---

# Readiness scan

L2: you find and evidence the gaps, and mark what fails; the owner decides what
to do about them and which role runs next.

Each blocker is found once, here, by the role about to plan, instead of
mid-session by whichever role picks the story up. Read only: the only writes are
the `blocked` moves in step 4, after the owner's go-ahead.

## 1. Preflight

1. Mode. With `--report` (or when `/sprint-run` reads this file), nothing is
   written: skip step 4. Otherwise `.claude/bin/role require pm ${CLAUDE_SESSION_ID}`
   must pass; if it doesn't, stop and say to run `/role pm`, or to pass `--report`.
2. `git fetch origin`, and judge files as they are on `origin/main`
   (`git grep … origin/main`, `git ls-files`), not a stale checkout.
3. The sprint: `--sprint N`, otherwise `current` from `.claude/bin/sprint-state`
   (inside the planning procedure, NEXT). Its stories:
   `story list --sprint <n> --status ready` and `--status review`.

## 2. Check each story

`story show <ID> --comments 30` for each. Every finding names the **owning
role**: the role that has to act to clear it, from `.claude/bin/role owners
<path>` for a file, or the role a record or decision belongs to.

| Check | `ready` stories | `review` stories |
|---|---|---|
| **Runnable ACs** | Every acceptance criterion can be executed today: each make target, script, test, endpoint or file it names exists on `origin/main` (`git grep`, `git ls-files`), and each story it needs (`Blocked by:`, or named in the AC) is `done`. An AC that waits on something unbuilt is a finding, owned by whoever builds it. | Same, for the ACs §8 will run. |
| **Records** | `story check <ID> in-progress`. | `story check <ID> done`: each missing record, with the role that owes it. |
| **Spec and contract** | What the story's role can cheaply detect in the files the story touches: in `.proto` messages, duplicate field numbers, or a number or name that's `reserved`; a spec section, ADR, or path the contract cites that doesn't exist; two sections of the contract that contradict each other. Owned by the doc's owner (usually architecture). | Not checked: the work is merged. |
| **Decisions** | Each decision the story rests on ("approved", "decided", "per the owner", a choice between options) is written in the doc that owns it: the ADR, spec, or brand doc says it. One that exists only in a comment or in chat is a finding, owned by that doc's owner. | Same. |

Report a check you couldn't run (no proto files, a doc you can't read) as
**not checked**, never as passed. Evidence is a command and its output, or a
file:line, not an impression.

## 3. Report

```markdown
| Role to clear it | Story | Status | Gap | Evidence |
|---|---|---|---|---|
```

Group rows by role, the role with the most `ready` stories waiting on it first:
those gate lanes. Break a tie by the most stories waiting in all. Then:

- **Run first:** that role, and why (n ready stories wait on it; m review stories wait on its records).
- **Clean:** the stories with no finding, which lanes can pick up now.
- **Not checked:** anything you couldn't check, and why.

## 4. Mark (not with `--report`)

Show the table and **wait for the owner's go-ahead**. They may drop a finding.
Inside the planning procedure, the scan's table is part of its checkpoint
message, and this step runs with its writes.

For each `ready` story with a finding:

1. `story status <ID> blocked --note "<gap>; <owning role> clears it"`.
2. `story comment <ID> --to <owning role> --body-file <f>`: the gap, its
   evidence, and what clears it. That is how the role finds it in its queue.

Lanes skip a `blocked` story. Architecture moves it back with
`story status <ID> ready` once the gap is cleared.

`review` stories are reported, never moved: their work is merged, they stay
on architecture's §8 queue, and `story status … done` already refuses while a
record is missing. A finding on one gets the step 2 comment only.

## 5. Close

End with one next action: run the **Run first** role (for example `/role sre`,
then `/sre-start-sprint`), or, when everything is clean, start the lanes
(`/sprint-run`).

## Rules

- Never change a story's body, fields, or links here, and never fix a gap
  yourself, even one in PM's own paths. Report it, mark it, hand it off.
- Never block a story on a hunch. No evidence, no finding.
- Never move a `review` story.
