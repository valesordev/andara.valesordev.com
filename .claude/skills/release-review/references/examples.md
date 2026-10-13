# Examples

**Prompt:** (product role, game server repo, R1 at `shipping`) "/release-review"
**Behavior:** `role require product` passes. The agent reads R1 and its briefs, `story list` for `GS-*` and `--prefix GC`, the newest `SPRINT-*-demo.md`, and the content repo's latest cycle close-out. FEAT-01's signal (log out, come back, the world is as you left it) holds: SPRINT-06's demo step 4. FEAT-04's signal doesn't: `GC-ZON-007` (raider templates) is `in-progress` and `GS-SRV-061` (damage events) is `ready`. It recommends `no-go` with the shortest path (GS-SRV-061 to implementation, GC-ZON-007 to content), writes `docs/product/reviews/R1-2026-11-20-01.md`, and opens a PR. The next action is the top blocker and the role that pulls it.

**Prompt:** (product role) "can we ship R1 without trading"
**Behavior:** FEAT-02 (trading) is `cut-first`, and every `must` signal holds. The agent recommends `cut`: ship without FEAT-02, which costs nothing in R1's promise. It says the ship call and the outward-facing invite are the owner's, and that the cut goes through `/release-plan` once they accept it.

**Non-trigger:** "where are we in the sprint" → `project-sync`.
**Non-trigger:** "announce R1 on the site" → the owner; outward-facing.
