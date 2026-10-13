---
name: product-triage
description: Work the product role's queue: open issues labeled `product` (answer, fold into a brief or release, or close with the reason) and the handoffs still owed from merged `role:product` PRs ("Release changes (after merge)": one `release-change` issue per planner repo). Trigger on "/product-triage", "triage the product issues", "what is waiting on product", and when the sprint coordinator starts the product lane. Requires the product role. Not for writing one feature's brief (feature-brief), re-cutting releases on request (release-plan), release readiness (release-review), or cutting stories (pm, producer).
argument-hint: "[issue numbers]"
disable-model-invocation: true
allowed-tools: Bash(.claude/bin/role:*) Bash(.claude/bin/story list:*) Bash(.claude/bin/story show:*) Bash(gh issue list:*) Bash(gh issue view:*) Bash(gh issue comment:*) Bash(gh issue close:*) Bash(gh issue create:*) Bash(gh pr list:*) Bash(gh pr view:*) Bash(git fetch:*)
---

# Product: triage the queue

Queue: `$ARGUMENTS` (when empty, everything below).

## Preflight

1. `.claude/bin/role require product ${CLAUDE_SESSION_ID}`. If it fails, stop and
   tell the owner to run `/role product` first.
2. `git fetch origin`; work from `origin/main`. Read `docs/product/vision.md`,
   `releases.md`, and the briefs your queue touches. You own `docs/product/` and
   nothing else.

## 1. Issues labeled `product`

`gh issue list --label product --state open --json number,title,body,updatedAt`.
For each issue, decide and leave a comment saying what you decided and why:

- **Answer** it. Comment the answer, then close the issue.
- **Fold** it into a brief or release: change `docs/product/` on a `product/`
  branch (use `/feature-brief` or `/release-plan` for the content), comment with
  what changed and the PR, and close the issue ("folded into <PR>; reopen if the
  PR is rejected"). The PR is where the owner vetoes it.
- **Close** it with the reason (out of scope, already decided at `file#section`,
  duplicate of #N).

Every issue in the queue gets a comment or is closed: an issue you read and left
alone is a failure, because the next run can't tell it was seen. If it needs
the owner (the vision, adding or dropping a release, anything outward-facing), bring
them one recommendation in the comment and leave the issue open; it comes back to
you when someone comments on it.

## 2. Handoffs owed

`gh pr list --state merged --label role:product --json number,url,body --limit 10`.
For each PR whose body has a "Release changes (after merge)" section, check each
planner repo it names for a `release-change` issue linking the PR
(a person runs `gh issue list --repo <planner> --label release-change` for the planner repos; a lane can't).
Open the ones that are missing, one per planner, linking the PR (the PR's URL in
the issue body: that is how the handoff is recognised as done).
A coordinator lane writes in this repo only: if a handoff belongs in another
repo, stop and end with `LANE RESULT: blocked: handoff to <repo>` (a person
opens it). Name PRs and issues by number, not by URL.

## 3. Ship

If `docs/product/` changed, open one PR from the `product/` branch: label
`role:product`, with the calls you made listed under "Decided this session" so
the owner can veto them in review. Take it through `.claude/skills/pre-pr/SKILL.md`,
including section 6 (Codex). Never merge it.

## Stop

Your last message is one line: `LANE RESULT: <PR url>` when you opened a PR,
`LANE RESULT: triaged <n>` when you handled `n` issues or handoffs without one,
`LANE RESULT: nothing to do`, or `LANE RESULT: blocked: <reason>`. Then list what
the owner decides next.
