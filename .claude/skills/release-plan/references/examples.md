# Examples

**Prompt:** (product role, Andara server repo, no `docs/product/` yet) "/release-plan what comes after phase 1"
**Behavior:** `role require product` passes. There's no `vision.md`, so the agent drafts it from the repo `CLAUDE.md`, `docs/roadmap.md`, and the Andara Notion pages, marking inferred lines `[ASSUMED]` (e.g. "first players are invited friends, not the public"). It opens a `product/vision` PR and asks Brian to confirm or correct each `[ASSUMED]` line. It stops there, and says the release plan follows once the vision merges.

**Prompt:** (product role, vision merged) "/release-plan"
**Behavior:** From the newest sprint demo, the agent states what a player can do today. It proposes R1 "First steps" (invited players walk, talk, and survive a first fight in the frontier village; bet: the text world holds a newcomer for a session) and R2. Coverage finds M0–M3 name no release and M4 has no player-facing feature for persistence. It decides to map M0–M4 to R1, with M2's persistence as an enabler of FEAT-01 (come back and find the world as you left it). It writes `releases.md` and opens the PR, listing under "Release changes (after merge)" one request for pm (add `Release: R1` to M0–M4) and one for producer (key C1 to R1's FEAT-04 raiders). It opens no issues yet. In the next product session, after Brian merged the PR, preflight finds no `release-change` issue linking it and opens both.

**Prompt:** (product role) "/release-plan drop R3, I'm not doing a public launch this year"
**Behavior:** Dropping a release is Brian's call, and he made it. The agent moves R3's features to `cut` or to R2 by their bet, logs the change, and lists release changes only for milestones that targeted R3, to open once the PR merges.

**Non-trigger:** "where are we on R1" → `release-review`.
**Non-trigger:** "add a milestone for the client" → pm (`docs/roadmap.md`).
