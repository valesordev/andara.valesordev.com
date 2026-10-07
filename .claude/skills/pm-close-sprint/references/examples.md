# Examples

**Prompt:** (pm role, the other roles have stopped and reported) "/pm-close-sprint"
**Behavior:** `sprint-state` reports `state: active`, `current: 6`, `next: 7`. `story list --sprint 6` shows AW-CLI-012 at `in-progress`, with an architecture comment saying it's blocked on ADR-019. The agent writes `docs/sprints/SPRINT-06-closeout.md`, which records each story's final status and PR and lists AW-CLI-012 as carryover, citing that comment. It writes the demo, runs every step on a clean `origin/main` checkout, and records what happened. One step needs a hand-run `kubectl` command, so it marks it `§9 defect → AW-INF-NNN`. It triages issues and `--to pm` comments, closes the sprint issue, plans SPRINT-07, and opens a `pm/` PR.

**Prompt:** (pm role) "/pm-close-sprint" while AW-SRV-050 is `ready` with no comment
**Behavior:** Preflight step 5. The agent tells Brian that AW-SRV-050 is still `ready` with no blocker or comment, so implementation may not be finished. It asks whether to proceed before closing anything.

**Prompt:** (pm role) "/pm-close-sprint" with no active sprint
**Behavior:** `state: closed`. The agent stops and points to `/pm-start-sprint`.

**Non-trigger:** "Plan the first sprint" → `/pm-start-sprint`.
