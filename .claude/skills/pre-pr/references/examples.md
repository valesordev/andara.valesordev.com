# Examples

**Prompt:** (implementation role, AW-SRV-042 committed) "Push it and open the PR."
**Behavior:** `git push` is denied by the review gate ("HEAD … hasn't passed /pre-pr"). The agent runs /pre-pr: tree clean, `make check` green, then it spawns the reviewer with BASE/HEAD and the story. Round 1 returns a P1: the new projector test still passes with the lock removed, as shown by mutation in the reviewer's worktree. The agent adds an interleaving test, watches it fail, fixes the code, commits `internal/projector/…` by explicit path, re-runs `make check`, and spawns round 2, which comes back clean. It runs `role reviewed`, pushes, and opens the PR with `--label role:implementation` and a *Pre-review findings* table.

**Prompt:** (PM role) "/pre-pr SPRINT-04 plan"
**Behavior:** The diff is story files only. The reviewer skips the concurrency and deploy sections and says so. It checks citations: AW-INF-031 cites `andara_projector_lag_seconds`, and grep finds no such metric (the real one is `andara_projector_lag`). That's a P1, which the agent fixes. Round 2 is clean, the review is recorded, and the PR opens.

**Prompt:** (after round 4 with a P1 still open) —
**Behavior:** The agent doesn't run `role reviewed`. It reports the open P1 with the reviewer's evidence and its own attempts, and asks Brian how to proceed.

**Non-trigger:** "Address the Codex comments on #212" → `/pr-comments 212`, which runs /pre-pr itself before it pushes.
