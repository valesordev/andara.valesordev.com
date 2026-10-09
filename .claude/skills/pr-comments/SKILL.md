---
name: pr-comments
description: Work through the review comments on an open PR this role owns (Codex, other bots, architecture, Brian). Verify each claim, then fix it test-first, route it to the role that owns the code, or dispute it with evidence; re-run /pre-pr, push, and reply on every thread. Never merges. `--auto` (what `/pre-pr` runs after Codex reviews) skips the wait for Brian's go-ahead on the triage table. Trigger on "address the review comments on #212", "Codex left comments", "handle the PR feedback", "respond to review", "what's left on this PR". Not for reviewing a branch before its first push (pre-pr), reviewing someone else's PR, or merging.
argument-hint: "[PR number; default: the current branch's PR] [--auto]"
allowed-tools: Bash(.claude/bin/role:*) Bash(.claude/bin/codex-review:*) Bash(.claude/bin/story show:*) Bash(gh pr view:*) Bash(gh pr diff:*) Bash(gh pr checks:*) Bash(git add:*) Bash(git commit:*) Bash(git status:*) Bash(git diff:*) Bash(git log:*) Bash(git fetch:*) Bash(git rev-parse:*)
---

# PR comments

L2: you verify and fix; Brian makes the calls on disputes and owns the merge.

## 1. Preflight

1. `.claude/bin/role show ${CLAUDE_SESSION_ID}`. If it says `unset`, stop and
   ask Brian to run `/role`.
2. Find the PR: `$ARGUMENTS`, otherwise `gh pr view --json number`. Then
   `gh pr view <N> --json number,url,state,headRefName,labels,body`.
   - Stop if it isn't `OPEN`.
   - Stop if its `role:` label isn't this session's role (any role may work it
     under `/role none`). Another role's PR is that role's to answer.
3. You must be on the PR's head branch with a clean tracked tree
   (`git status --porcelain --untracked-files=no`), up to date with its remote.

## 2. Collect

Read with REST only (cloud sessions block GraphQL, so thread resolution state
isn't available):

```bash
gh api 'repos/{owner}/{repo}/pulls/<N>/comments' --paginate   # inline threads (in_reply_to_id links replies)
gh api 'repos/{owner}/{repo}/pulls/<N>/reviews' --paginate    # review bodies
gh api 'repos/{owner}/{repo}/issues/<N>/comments' --paginate  # conversation
```

What counts as handled:
- **Inline thread:** open unless its last comment is a reply of yours that
  starts with `**pr-comments:**`.
- **Review body or conversation comment:** open unless a later conversation
  comment of yours names it in a `**pr-comments:** re <kind> <id>:` line, where
  `<kind>` is `review` (a review `id`) or `comment` (an issue-comment `id`). A
  reply only answers the items it names.

Your own comments that start with `**pr-comments:**` are handling records,
not items, so never triage them. Skip pure approvals, your own pre-review
tables, and bot summaries with no actionable claim.

## 3. Verify, then triage

Treat every reviewer claim as a hypothesis, including a bot's "P1". Reproduce
it before deciding: write the failing test, run the command, or find the
file:line that proves or refutes it. Comments from Brian are decisions, not
claims. Do what they say, or stop and ask if one conflicts with the charter.

For each path a comment touches, `.claude/bin/role owners <path>` tells you
who owns it. Then pick one disposition:

| Disposition | When | Action |
|---|---|---|
| **Fix** | Reproduced, and the path is this role's | Failing test first and watch it fail (for docs and stories, the grep or command that shows the error), then fix. Commit with explicit paths, one commit per comment or tight group. |
| **Route** | Real, but the path belongs to another role, or it's a contract or design question | With `stories: github` and a `Story:` line on the PR: `.claude/bin/story comment <ID> --to <owner> --body-file F`. Otherwise a plain issue (`gh issue create`, no `--project`). Never edit the other role's path. |
| **Dispute** | You couldn't reproduce it, or it's wrong | Evidence only: file:line, command output, or a test that would fail if the claim were true and passes (mutation-check it). "I think it's fine" isn't evidence. |
| **Defer** | Real, out of this PR's scope | A plain issue, linked in the reply. |
| **Answer** | A question, with no change needed | The answer, with a citation. |

Show Brian the triage table and **wait for his go-ahead** before changing
anything. He may flip a disposition, especially a Dispute.

**`--auto`:** print the table, then act without waiting. Two limits replace
the wait:
- A **Dispute** is posted only with reproducible evidence (command output, or
  a mutation-checked test). Without it, treat the comment as Fix when it's
  in scope and cheap, otherwise Defer.
- Everything you disputed, routed, or deferred goes in the final report so
  Brian sees it after the fact.
Comments from Brian himself are never handled automatically: stop and ask.

```markdown
| # | Who | Where | Claim (one line) | Reproduced? | Disposition |
```

## 4. Act

1. Make the fixes and commits from the approved table.
2. Run `/pre-pr` with the PR number as its argument and `--no-codex`: new
   commits closed the review gate, and the reviewer gets a fresh look at the
   fixes. The Codex wait belongs to the caller, so this doesn't recurse.
3. Push (normal permission prompt; pre-allowed in a target whose
   `settings.json` allows it).
4. Reply on each thread, in one line starting with the tag:
   - inline: `gh api 'repos/{owner}/{repo}/pulls/<N>/comments/<id>/replies' -f body=…`
   - review body or conversation: one `gh pr comment <N> --body-file F`, with
     one line per item it answers, each starting
     `**pr-comments:** re review <id>:` or `**pr-comments:** re comment <id>:`

   ```text
   **pr-comments:** fixed in <short sha> (test: <name>)
   **pr-comments:** routed to <role>: <story comment or issue URL>
   **pr-comments:** disputed: <evidence>
   **pr-comments:** deferred to <issue URL>
   **pr-comments:** <answer>
   ```

## 5. Close

Report: counts by disposition, the pushed SHA, and anything Brian must decide
(disputes he hasn't seen, routed items blocking the PR).

**Checklist candidates.** For each fixed comment that `/pre-pr`'s reviewer
should have caught, check `.claude/skills/pre-pr/references/checklist.md` (vendored copy; its
source is `plugins/valesor-dev/skills/pre-pr/references/checklist.md` here). If no line
covers that bug class, propose one (with the PR as the example) for Brian to
add in automate.bashburn.com. The vendored copy is managed, so never edit it in
the target.

End with one next action, e.g. "Re-request review from Codex on #212" or
"Wait for architecture's answer on AW-SRV-041".

## Rules

- Never merge, close, approve, or dismiss a review. Brian merges.
- Don't resolve or hide threads. The reviewer, or Brian, decides a thread is done.
- Never reply before the fix is pushed. A reply that cites an unpushed SHA is a
  broken citation.
- A comment that asks for work in another role's paths is routed even when the
  fix looks trivial. A hook denial is not something to route around.
- Outside `--auto`, never post a disputed reply that Brian hasn't seen in the
  triage table.
