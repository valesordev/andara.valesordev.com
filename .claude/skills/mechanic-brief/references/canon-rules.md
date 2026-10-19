<!-- GENERATED from plugins/andara-design/src/skills/mechanic-brief/references/canon-rules.md by `make build`; edit the source, not this file. -->
# Andara canon rules

Generated from `shared/andara-canon.md`. Apply every rule below.

## Canon rules (every Andara's World role)

**Where canon lives.** The content repo (`valesordev/andara.solo7.media`) is authoritative, on `main`. There is no other copy. `.claude/bin/lore-check --list` prints every page with its kind and status. This plugin carries no setting content. Read it at the start of a session that touches canon:

- **The world foundation page** (`docs/canon/`): premise, regions, and the **tone guardrails**.
- **The cosmology page** (`docs/canon/`): how the world works and the **terminology table**, including which word marks which kind of speaker (the register).
- `docs/tracker.md`: the content tracker (backlog, its sections, development order, content targets).
- `docs/glossary.md`: terms already used in packs.

Read them from `origin/main` (`git fetch origin` first): a branch can be behind. If you can't reach the content repo (no checkout, no access), say so and stop before any canon judgment. Never fill a gap from memory or from this plugin's examples.

**Where work goes.**

| Kind (`kind:`) | Path | Owner role |
|---|---|---|
| `canon` | `docs/canon/<slug>.md` | world-builder |
| `tracker` | `docs/tracker.md` | the owner (producer proposes) |
| `sheet` | `docs/sheets/<type>/<slug>.md` (`npc`, `faction`, `item`, `creature`, `conflict`, `mystery`, `hook`, `zone`) | content |
| `concept` | `docs/art/CA-NNN-<slug>.md`, images `docs/art/img/CA-NNN-<letter>.png` (Git LFS) | concept-art |
| `house-style` | `docs/art/house-style.md` | concept-art |
| `brief` | `docs/design/briefs/<slug>.md` | game-designer |

Every file opens with frontmatter (`title`, `kind`), then a `**Status:** <state>` line, then the body. Slugs are kebab-case. `docs/art/inbox/` is the exception: images the owner dropped in under any name, waiting for concept-art to file them on a sheet. They aren't visual reference, so never cite or attach one. `.claude/bin/lore-check` validates all of it in `make check`.

**Status line.**

- `draft`: being worked. Not canon.
- `proposed`: ready for the owner's decision. Not canon.
- Approved: `vX.Y — approved canon` (canon), `approved` (sheet, brief), `HS vN — approved` (house style). A concept sheet stays `draft`; its candidates are marked `Selected` only on the owner's selection in the session. **Only the owner sets an approved status.** An agent never writes an approved status, even when asked to "just mark it done". Ask the owner to change the line themselves, or to confirm in the session that they approve it, and quote that confirmation in the status line and the commit message.

Drafts and proposals merge to `main` with their status line, so later sessions and other roles can read them. Approved means approved on `main`: build on nothing else.

**Changing approved canon.** Only after the owner decides the change, and only on a branch: edit the approved page in place, tag each changed line `[PROPOSED]`, and add a `**Revisions:**` entry (version, date, what changed). The PR stays open until the owner bumps the version on the status line in it; merging it is the approval. Git history is the revert path. Never create a separate amendment page.

**Never invent canon silently.** Anything not already in an approved page is a proposal. Tag it inline `[PROPOSED]` and list it under *Open questions*. Names, species, factions, places, dates, and mechanics all count. Placeholder names are marked `(placeholder)`.

**Information hierarchy.** Every fact has a layer: **Public → Rumor → Specialist → Secret → Deep truth**. Player-facing text (room descriptions, dialogue, item text, lore entries) may state Public and Rumor facts, hint at Specialist and Secret ones, and **never state a Deep truth outright**. Design docs label each layer explicitly. The canon pages say which layer each fact is in.

**Terminology register.** Check every setting term against the cosmology page's terminology table and the glossary, and flag a term used out of register for its speaker or document type. A new term is `[PROPOSED]` until a canon page approves it.

**Tone guardrails.** Hold drafts, prompts, and reviews to the world foundation page's tone guardrails, and name the guardrail when you flag a drift.

**Tracker discipline.** Work is pulled from the content tracker in its phase order. When a proposal lands, list the tracker items it resolves, partially addresses, and adds (a *Tracker impact* section). An agent never ticks a tracker box; the owner does, when they approve.

**Boundary with Creative Coach.** Skill practice (drawing drills, writing exercises, craft study) belongs to the owner's `creative-coach` plugin, not this one. This plugin is production for the game. If the owner asks to *learn* a technique, hand off.

**Outward-facing actions** (pushing a branch, opening a PR or issue, posting images anywhere shared) need confirmation. Writing and committing on your own branch doesn't.
