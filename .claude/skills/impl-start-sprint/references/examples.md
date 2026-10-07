# Examples

**Prompt:** (implementation role, SPRINT-07 active) "/impl-start-sprint"
**Behavior:** Nothing is carried over as `in-progress`. `story list --sprint 7 --lane implementation` puts AW-SRV-061 first: it's `ready`, and its blocker AW-SRV-058 is at `done`. The agent branches `impl/aw-srv-061-snapshot-fields` from `origin/main` and runs `story start AW-SRV-061`. It builds to the issue's acceptance criteria and interface contract, runs `/pre-pr`, and opens the PR with `Story: AW-SRV-061` and `role:implementation`. It records `--record "Verification"` citing commits, then takes the next ready story.

**Prompt:** (implementation role, resuming) "/impl-start-sprint"
**Behavior:** AW-SRV-055 is still `in-progress`. The agent reads its thread with `--comments 30` and finds that PR #398 already merged the projector half. It builds only what's left, on a fresh branch.

**Prompt:** (implementation role) "/impl-start-sprint" while all its stories are `draft`
**Behavior:** Architecture's contract review isn't done. The agent stops and says so.

**Prompt:** (implementation role) A story's contract names a field that conflicts with ADR-021.
**Behavior:** The agent runs `story comment <ID> --to architecture` with what conflicts and why, and moves on to the next story it can pick up. It doesn't edit the contract.

**Non-trigger:** "Address the Codex comments on #402" → `/pr-comments 402`.
