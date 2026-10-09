# Examples

**Prompt:** (implementation role, AW-SRV-042 committed) "Push it and open the PR."
**Behavior:** `git push` is denied by the review gate ("HEAD … hasn't passed /pre-pr"). The agent runs /pre-pr: tree clean, `make check` green, then it spawns the reviewer with BASE/HEAD and the story. Round 1 returns a P1: the new projector test still passes with the lock removed, as shown by mutation in the reviewer's worktree. The agent adds an interleaving test, watches it fail, fixes the code, commits `internal/projector/…` by explicit path, re-runs `make check`, and spawns round 2, which comes back clean. It runs `role reviewed`, pushes, and opens the PR with `--label role:implementation` and a *Pre-review findings* table.

**Prompt:** (PM role) "/pre-pr SPRINT-04 plan"
**Behavior:** The diff is story files only. The reviewer skips the concurrency and deploy sections and says so. It checks citations: AW-INF-031 cites `andara_projector_lag_seconds`, and grep finds no such metric (the real one is `andara_projector_lag`). That's a P1, which the agent fixes. Round 2 is clean, the review is recorded, and the PR opens.

**Prompt:** (after round 4 with a P1 still open) —
**Behavior:** The agent doesn't run `role reviewed`. It reports the open P1 with the reviewer's evidence and its own attempts, and asks Brian how to proceed.

**Non-trigger:** "Address the Codex comments on #212" → `/pr-comments 212`, which runs /pre-pr itself before it pushes.

**Prompt:** (implementation role, PR #212 just pushed) "Push it, open the PR, and see it through review."
**Behavior:** After the push, `codex-review wait 212` polls the status comment for a row on HEAD. No row appears within 2 minutes, so it posts `@codex review` once; ten minutes is the ceiling. The row turns Completed with two inline findings, so the agent runs `/pr-comments 212 --auto`: it fixes both test-first, runs `/pre-pr --no-codex` on the fixes, pushes, replies on both threads, and waits again on the new HEAD. That round is clean. Before reporting it re-reads `gh pr view 212 --json state,mergedAt,headRefOid` and `codex-review status 212`, then reports "Codex clean at <sha>, PR open". It doesn't merge.

**Prompt:** (a branch whose PR #484 merged while the agent kept committing) "Push the new commits."
**Behavior:** `gh pr view` shows `MERGED`. The agent doesn't push. It branches from `origin/main`, cherry-picks the new commits, re-runs /pre-pr there, and opens a replacement PR.

**Prompt:** (Codex is down; `codex-review wait` returns `timeout` after 10 minutes)
**Behavior:** The agent reports "Codex hasn't reviewed <sha>" and names `@codex review` as the next action. It doesn't call the PR Codex-clean.
