# Examples

**Prompt:** (pm role, after SPRINT-05 closed) "/pm-start-sprint"
**Behavior:** `role require pm` passes, and `_repo.md` has `stories: github` and `sprints: project`. `sprint-state` reports `state: closed`, `current: 5`, `next: 6`. The agent reads `docs/sprints/SPRINT-05-closeout.md` and `-demo.md` from `origin/main`. It takes the carryover list and re-checks it on the board, so AW-SRV-044 drops out because it's now `done`. It adds the `§9 defect → AW-INF-051` from the demo. Then it plans SPRINT-06 with the shared procedure, creates the sprint issue at `sprint:planned`, and opens a `pm/` PR for Brian.

**Prompt:** (pm role) "/pm-start-sprint" while SPRINT-06 is still `sprint:active`
**Behavior:** `state: active`. The agent stops and points to `/pm-close-sprint`, which closes 06 and plans 07.

**Prompt:** (pm role, in a repo whose `_repo.md` lacks `stories: github`) "start the next sprint"
**Behavior:** The agent stops: the repo hasn't switched to stories on GitHub, and this skill only works the board.

**Non-trigger:** "Close out the sprint" → `/pm-close-sprint`.
**Non-trigger:** (architecture role) "/pm-start-sprint" → `role require pm` fails, and the agent tells Brian to run `/role pm` first.
