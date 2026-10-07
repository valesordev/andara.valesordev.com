# Examples

## Triggers
**Prompt:** "/mechanic-spec nomic-strain" (brief at `proposed`, all decisions decided by Brian)
**Behavior:** Requires the game-designer role in the server repo. Writes `docs/mechanics/nomic-strain.md`: state (strain 0..cap per character), numbered rules, a tunables table with `[PROPOSED]` starting values, scenarios like "NS-S02 boundary: Given strain = cap−1 and a cast costing 1, when the character casts, then strain = cap and the cast succeeds; NS-S03: Given strain = cap, when they cast, then the cast is refused with reason `strain_cap`", at least one two-actor scenario, feasibility flags, shippable slices. Opens a PR labeled `role:game-designer` after `/pre-pr`.

## Stops
**Prompt:** "Spec the rift tolls." (brief has two open decisions)
**Behavior:** Lists the open decisions, doesn't write the spec.

**Prompt:** `/mechanic-spec …` in the content repo.
**Behavior:** Stops: specs live in the server repo; finish the brief here and run it in a server session.

## Non-triggers
- "What are my options for strain?" → mechanic-brief.
- "Cut stories from this spec" → server PM.
