---
name: mechanic-brief
description: Turn an approved Andara's World canon passage into a mechanic brief in the content repo's docs/design/briefs/ (design intent, the decisions to make with options, trade-offs, degenerate strategies, feasibility flags) and teach the game-theory concepts that bear on the choice. Trigger on "how should casting strain work as a mechanic", "what mechanic does this canon imply", "brief the portal travel mechanic", "explain the game theory behind X", "what are my options for the faction reputation system". Requires the content repo. Not for the formal server spec (mechanic-spec), numeric balance checks (balance-model), changing canon (world-builder), or content sheets and .aw (content-developer).
---

# Mechanic brief

Read `references/canon-rules.md` first, then `references/game-theory-primer.md` for the concepts you may need. The brief is where the owner decides; it is also where they learn.

## Inputs
- The canon page(s) at `approved` (`docs/canon/` on `origin/main`) and the passage the mechanic expresses. If canon is `draft`/`proposed`, stop and route to world-builder.
- The tracker items involved.
- Any existing brief or spec for the mechanic (`docs/design/briefs/` here, or `docs/mechanics/` in the server repo).
- Server constraints: CLAUDE.md, ADRs, `docs/specs/`, `docs/roadmap.md`, the builder reference. In the content repo, read them with `gh api repos/valesordev/andara.valesordev.com/contents/<path> -H "Accept: application/vnd.github.raw"`.

## Steps
1. **Anchor.** Quote the canon passage. State the fantasy it creates and the **design intent** in one line: what play it should encourage, what it should discourage.
2. **List the decisions.** The questions the owner must answer for the mechanic to exist (what is the resource, who pays, when does it reset, can it be traded). Order them by how much they constrain the rest.
3. **Options.** For each decision give 2–4 options with consequences for play, canon, content load, and server cost. Recommend one and say why.
4. **Teach.** For each decision where a concept from the primer changes the answer, name it, explain it in terms of this mechanic, and show how it separates the options. Concepts that don't change a choice stay out.
5. **Stress it.** For the recommended combination, find the degenerate strategy (what a rational player does every time), the exploit across actors (two players, a player and an NPC faction), and the failure of the intended fantasy. Propose a counter or mark it accepted.
6. **Feasibility.** Mark each requirement `exists`, `needs engine work`, or `needs Content Language work`. Don't assume.
7. **Write it** to `docs/design/briefs/<slug>.md` (frontmatter `title`, `kind: brief`) on a `design/<slug>` branch with `**Status:** draft` (open decisions) or `proposed` (all decided, awaiting the owner's approval), using `references/template.md`. Mark unapproved facts and numbers `[PROPOSED]`. Run `make check-lore`, commit, and push and open the PR only after the owner confirms.

Never write "approved". The owner does, or confirms it in the session; quote the confirmation.

## Output
The file and its PR, and in chat: path and PR URL (or "not pushed"), decisions open and decided (by the owner), concepts taught, canon questions routed to world-builder, feasibility flags. One next action.
