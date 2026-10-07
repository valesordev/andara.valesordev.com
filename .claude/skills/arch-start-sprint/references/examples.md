# Examples

**Prompt:** (architecture role, SPRINT-07 just activated) "/arch-start-sprint"
**Behavior:** `sprint-state` reports `state: active`, `current: 7`. Four stories are `draft`. In Rank order, the agent reads each with `story show <ID> --comments 20` and checks it against the ADRs and specs. It fixes AW-SRV-061's interface contract with `story body … --note "Interface: snapshot field names per ADR-021"`. Three stories have SRE's `Observability review (sre)` comment and move to `ready`. The fourth is left as `draft`, and the agent says it waits on SRE. Then it works the §8 queue and its own backlog.

**Prompt:** (architecture role, mid-sprint) "/arch-start-sprint"
**Behavior:** No drafts remain, so contract review is done. `story list --status review` returns AW-SRV-058, which has a `§8 instrumentation (sre)` comment and passes every §8 item. The agent records `--record "§8"` and runs `story status AW-SRV-058 done --note "PR #402"`. AW-SRV-059 has no SRE record, so it stays at `review` with a `story comment --to sre`.

**Prompt:** (architecture role) "/arch-start-sprint" before PM activates the sprint
**Behavior:** `state: planned`. The agent stops and reports the output: PM hasn't activated a sprint yet.

**Non-trigger:** "Review this ADR draft" → ordinary architecture work, not the sprint loop.
