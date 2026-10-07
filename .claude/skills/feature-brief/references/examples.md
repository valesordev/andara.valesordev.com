# Examples

**Prompt:** (product role, Andara server repo) "/feature-brief first combat encounter"
**Behavior:** `role require product` passes. The agent reads `vision.md`, `releases.md`, and the newest sprint demo, which shows a player can move and talk to NPCs but not fight. It drafts `FEAT-04-first-fight.md`: outcome "a new player wins or flees their first fight against a raider near the village", a success signal tied to a demo step, scope that leaves out parties and loot tables, release R1 (`must`). Dependencies: `docs/mechanics/combat.md` (game-designer, proposed), raider templates (producer, needs a C-milestone story), and damage events in protocol v1 (open question to architecture). It decides "fleeing is in scope because a loss with no exit fails the first-session promise", records it under Decisions, and opens a `product/` PR listing that call.

**Prompt:** (product role) "revise FEAT-02, Brian wants trading out of R1"
**Behavior:** Moving scope between releases changes R1's promise only if trading was `must`. Here it's `cut-first`, so the agent moves FEAT-02 to R2, logs the change in the brief and `releases.md`, and notes that pm and producer pick it up at their next boundary.

**Non-trigger:** "plan the next sprint" → `pm-start-sprint` (pm role).
**Non-trigger:** "how much damage should a rusty blade do" → game-designer (`mechanic-brief`, `balance-model`).
