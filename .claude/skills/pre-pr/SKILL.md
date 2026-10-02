---
name: pre-pr
description: Gate a branch before it leaves the machine. Runs the repo's check command, then an adversarial review by a fresh-context reviewer subagent (races, silent-pass checks, over-matching predicates, citations that don't exist, stale copies), fixes P0/P1 findings test-first, and records HEAD as reviewed so `git push` and `gh pr create` are allowed. Trigger before any push or PR, when the review-gate hook denies one, or on "pre-pr", "review before the PR", "is this ready to push". Not for answering review comments on an open PR, and not for reviewing someone else's PR.
argument-hint: "[story id or note for the reviewer]"
allowed-tools: Bash(.claude/bin/role:*) Bash(git add:*) Bash(git commit:*) Bash(git status:*) Bash(git diff:*) Bash(git log:*) Bash(git fetch:*) Bash(git merge-base:*) Bash(git rev-parse:*)
---

# Pre-PR review gate

The review-gate hook denies `git push` and `gh pr create` until HEAD has been
through this skill. Every new commit needs a fresh pass, including fixes made
for PR comments.

## 1. Preflight

1. `.claude/bin/role show ${CLAUDE_SESSION_ID}`. If it says `unset`, stop and
   ask Brian to run `/role`.
2. `git status --porcelain --untracked-files=no` must be empty. If it isn't,
   commit the work first: stage explicit paths only (`git add <path> …`), never
   `-A`, `.` or `commit -a`. Leave changes you didn't make unstaged and say so.
3. `git fetch origin`, then `BASE=$(git merge-base origin/main HEAD)`. If
   `git diff --stat $BASE HEAD` is empty, stop: there is nothing to review.

## 2. Checks

Run the command `.claude/bin/role check-cmd` prints, from the repo root. Fix
failures and commit before going on. The reviewer reviews green code.

## 3. Adversarial review

Spawn one `general-purpose` subagent. Its prompt is the full text of
[references/reviewer.md](references/reviewer.md) followed by:

- `BASE` and `HEAD` SHAs, and the branch name
- the story or issue the branch implements (`$ARGUMENTS` if given, otherwise
  the story file the commits touch), so the reviewer can check acceptance
  criteria
- the check command

The reviewer is read-only on your branch. It reports findings; you fix them.

## 4. Fix loop (at most 4 rounds)

For each finding the reviewer returns:

| Severity | Action |
|---|---|
| P0, P1 | Fix now. If it's behavioral, write the failing test first and watch it fail. Commit with explicit paths. |
| P2 | Fix if it stays in scope. Otherwise list it under *Deferred* in the PR body. |
| P3 | Your call. List what you skip. |
| Disputed | You may reject a finding with evidence (file:line, command output). Record it under *Disputed* in the PR body. Never reject one silently. |

After fixing, re-run the checks and spawn a **new** reviewer on the new HEAD,
with the previous findings and your responses appended so it verifies them.
Stop when a round returns no P0/P1, or after round 4.

If P0/P1 findings remain after round 4, **don't** record the review. Stop and
give Brian the open findings. He decides.

## 5. Record and ship

1. `.claude/bin/role reviewed`. It refuses if tracked files have uncommitted
   changes.
2. Push and, for a new PR, open it with the role's label
   (`gh pr create --label role:<role> …`). Add this to the body:

   ```markdown
   ## Pre-review findings
   Rounds: <n>. Check command: `<cmd>` green at <short sha>.
   | Sev | Finding | Resolution |
   |---|---|---|
   | P1 | <file:line, one line> | fixed in <sha> / disputed: <evidence> / deferred |
   ```

   For an existing PR, post the same table as a PR comment after the push.

## Rules

- The check command, the push, and `gh pr create` aren't pre-allowed. Pushes
  and PRs are outward-facing, so they keep their normal permission prompt.
- Don't record a review you didn't run, and don't run `role reviewed` to get
  past the hook. That routes around a control in the same way a shell write
  into a denied path would.
- Don't reuse an old marker. If you amend or rebase, HEAD changes and the gate
  closes again. That's intended.
- Mutation testing happens in the reviewer's own worktree, never in yours.
