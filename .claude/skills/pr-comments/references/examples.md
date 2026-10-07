# Examples

**Prompt:** (implementation role, on `impl/aw-srv-041`) "Codex left comments on #212, deal with them."
**Behavior:** PR #212 is open with `role:implementation`. Three open threads. The agent tries each claim:
1. "P1: `drain()` reads `q.pending` without the lock." An interleaving test fails, so it's a **Fix** in `internal/projector/`.
2. "Config key `projector.max_lag` is undocumented in `docs/specs/projector.md`." `role owners docs/specs/projector.md` says `architecture`, so it's **Route**, via `story comment AW-SRV-041 --to architecture`.
3. "P2: the retry loop never exits on context cancel." A test with a cancelled context returns within 10ms, and the test fails when the `ctx.Done()` case is mutated out, so it's **Dispute**, citing `internal/projector/retry.go:58` and the test name.

It shows the triage table and waits. Brian approves. The agent fixes #1 test-first, commits it by explicit path, runs `/pre-pr 212` (clean), and pushes. Then it replies on all three threads with `**pr-comments:** …` lines. It closes by noting that the checklist already covers unguarded shared state, so there's no candidate, and gives the next action: re-request review from Codex.

**Prompt:** (PM role) "Address architecture's review on the SPRINT-04 close-out PR."
**Behavior:** Architecture says `docs/sprints/SPRINT-04-closeout.md` cites the make target `make chaos`, which doesn't exist. The agent greps the Makefile and confirms the target is missing (the real one is `make fault-inject`). `docs/sprints/` is PM's, so it fixes the citation, commits, runs `/pre-pr`, pushes, and replies. Citations are already on the checklist, so it proposes no new line and notes that `/pre-pr` missed one it covers.

**Prompt:** (architecture role) "Handle the comments on #215." (#215 carries `role:implementation`)
**Behavior:** The agent stops: #215 belongs to implementation. It suggests running this from an implementation session, or under `/role none`.

**Non-trigger:** "Review my branch before I push it" → `/pre-pr`.
**Non-trigger:** "Merge #212 once the comments are done" → the agent handles the comments, then tells Brian the PR is ready for him to merge. It never merges.
