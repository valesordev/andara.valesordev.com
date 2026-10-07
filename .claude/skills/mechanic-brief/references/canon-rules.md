<!-- GENERATED from plugins/andara-design/src/skills/mechanic-brief/references/canon-rules.md by `make build`; edit the source, not this file. -->
# Andara canon rules

Generated from `shared/andara-canon.md`. Apply every rule below.

## Canon rules (every Andara's World role)

**Where canon lives.** Notion, under `Solo7 › The Hearth › Andara's World`, is authoritative. The content repo's (`valesordev/andara.solo7.media`) `docs/lore/` is a read-only mirror of *approved* pages, written only by `/lore-sync`. If the two disagree, Notion wins and the mirror is stale: say so.

| Page | Role |
|---|---|
| World Foundation | premise, megacities, frontier, tone |
| Cosmology & the Awakening | nomic field, rifts, Andara's fate, terminology |
| Content Development Tracker | the backlog: sections §1–§19, development order, content targets |

**Status line.** Every canon page opens with `**Status:** <state>`:

- `draft`: being worked. Not canon.
- `proposed`: ready for Brian's decision. Not canon.
- `vX.Y — approved canon`: canon. **Only Brian sets this.** An agent never writes "approved", even when asked to "just mark it done". Ask him to change the line himself, or to confirm in the session that he approves it, and quote that confirmation in the change.

**Changing approved canon.** Only after Brian decides the change: edit the approved page in place, tag each changed line `[PROPOSED]` until he bumps the version on the status line, and add a `**Revisions:**` entry (version, date, what changed). Notion page history is the revert path. Never create a separate amendment page. Don't run `/lore-sync` between an edit and its version bump.

**Never invent canon silently.** Anything not already in an approved page is a proposal. Tag it inline `[PROPOSED]` and list it under *Open questions*. Names, species, factions, places, dates, and mechanics all count. Placeholder names are marked `(placeholder)`.

**Information hierarchy.** Every fact has a layer: **Public → Rumor → Specialist → Secret → Deep truth**. Player-facing text (room descriptions, dialogue, item text, lore entries) may state Public and Rumor facts, hint at Specialist and Secret ones, and **never state a Deep truth outright**. Design docs label each layer explicitly.

**Terminology register.** The word a speaker uses for the field marks who they are: *nomic* (megacity, technical, academic), *the Fifth* (street, secular), *Andara's* (devotional, folk). Someone who won't say her name is a Penitent or wants you to think so. Dates are `A.A.` (After Awakening). Check new terms against the Cosmology terminology table and the content repo's `docs/glossary.md`.

**Tone guardrails.** Five centuries on, this is people's world, not a ruin they're mourning. Technology, magic, and psionics are three overlapping ways to interact with reality, not tiers. Development is uneven. Mundane things outnumber extraordinary ones. Reveal the world through play, not exposition.

**Tracker discipline.** Work is pulled from the Content Development Tracker in its §19 phase order. When a proposal lands, list the tracker items it resolves, partially addresses, and adds (as the Cosmology page's *Tracker impact* does). An agent never ticks a tracker box; Brian does, when he approves.

**Boundary with Creative Coach.** Skill practice (drawing drills, writing exercises, craft study) belongs to the `creative-coach` plugin. This plugin is production for the game. If Brian asks to *learn* a technique, hand off.

**Outward-facing actions** (publishing a Notion page, pushing to the content or server repo, opening issues, posting images anywhere shared) need confirmation. Drafting Notion pages as `draft` or `proposed` under Andara's World doesn't.
