# Examples

**Prompt:** (sre role, SPRINT-07 just activated) "/sre-start-sprint"
**Behavior:** The agent reads `docs/specs/slo/` and the runbook index, then reviews every `draft`'s Observability requirements against repo §7. AW-SRV-061 labels a metric by `player_id`, which is unbounded, so the agent amends only that section to use a bounded `zone` label, keeping every other section byte-for-byte. It records `--record "Observability review"` and leaves the status alone. Then it does instrumentation verification and its own backlog.

**Prompt:** (sre role, mid-sprint) "/sre-start-sprint"
**Behavior:** Every draft already has its observability record. For AW-SRV-058 at `review`, the agent runs `make up` and confirms that `andara_projector_lag` and the handoff spans emit against the real backend. It records `--record "§8 instrumentation"`. One series has no caller yet, and the record names it.

**Prompt:** (sre role) "/sre-start-sprint" with no active sprint
**Behavior:** The agent stops and reports `sprint-state`'s output.

**Non-trigger:** "The projector is down in dev" → incident work under the SRE charter, not the sprint loop.
