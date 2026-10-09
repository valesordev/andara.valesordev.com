---
name: art-direction
description: Develop, revise, or export an imprint's illustration system (stable rules for subject matter, rendering, value and colour against the tokens, line, light, treatment, exclusions, and the approved composition types), kept separate from per-image prompts, in the imprint's art-direction folder, and render it as the instructions for that imprint's ChatGPT project. Trigger on "/art-direction", "set the illustration system for System 9", "the valesordev renders all look too X", "give me the ChatGPT project instructions for solo7.media", "what compositions do we allow". Requires the visual-designer role. Not for one image or mark (art-slot), reading a target image (brand-audit), tokens or type decisions (visual-designer, directly), or Andara's concept-art house style (andaras-world house-style).
argument-hint: "<slug> [revise | export]"
allowed-tools: Bash(.claude/bin/role:*) Bash(git log:*) Bash(git fetch:*)
---

# Art direction (illustration system)

Request: `$ARGUMENTS`

## Preflight
0. Run `.claude/bin/role require visual-designer ${CLAUDE_SESSION_ID}`. If it
   fails, stop and tell Brian to run `/role visual-designer`.
1. Resolve the slug (`brands/<slug>/`). Read its `brand/foundation.md`,
   `brand/visual-language.md`, the approved `references/visual-audit.md`,
   `tokens/tokens.json`, and the existing `art-direction/` files.
2. If the imprint has no approved visual audit or foundation, and no colour
   tokens, stop. The illustration system derives from those, so they come first
   (`/brand-audit <slug>`).

## Develop or revise
1. **Evidence first.** Gather the audit's image observations (which were
   classified core, candidate, or hero-specific), the slots already selected
   or rejected (`assets/slots/`), and Brian's reasons. A revision cites at
   least one image or finding.
2. **Rules, not prompts.** `illustration-system.md` holds what stays true for
   every image. Anything specific to one composition (this tree, this moon)
   belongs in a slot's prompt, not here. If an audit item was classified
   hero-specific, it stays out of the system.
3. **Decide section by section** with `references/direction-template.md`. For
   each open section, recommend one option in plain visual terms and name one
   alternative with its trade-off. Test each against:
   - **Tokens:** can the treatment map it onto the imprint's palette and themes?
   - **Distinctness:** does it stay clear of the sibling imprints' languages?
     Read their `illustration-system.md` files.
   - **Generator reliability:** will a ChatGPT image model hit it repeatedly?
     Flat, graphic, limited-palette styles repeat well. Fine hatching, exact
     geometry, and legible text don't, so treatment or SVG handles those.
4. **Compositions.** Define the approved composition types (for example
   `hero-wide` 2.4:1, `banner-wide`, `square-specimen` 1:1, `vertical-plate`)
   with aspect, empty zone, and use. Slots name one of them.
5. **Draft** `art-direction/illustration-system.md`, plus `prompt-template.md`
   (the slot prompt skeleton for this imprint) and `negative-examples.md`
   (what drifted and why, with images). Attribute decisions to Brian, and
   mark undecided items `[PROPOSED]`.
6. **Version.** When Brian approves (in the session or at PR review; quote
   him), set `**Status:** AD v<n> — approved <YYYY-MM-DD>` and add a changelog
   line: what changed, why, and which images motivated it.
7. **Render** the ChatGPT project instructions (below).

## Export
Fill `references/chatgpt-project.md` from the approved illustration system,
under 1,500 characters, in one fenced block, followed by: "Paste into ChatGPT →
Project *<Imprint> — Art* → Instructions, replacing everything. Version:
AD v<n>." Brian never edits the instructions in ChatGPT, or the two drift.
Never render from a draft.

## Output
Decisions made, `[PROPOSED]` items, the new version (if any), the rendered
block (if approved), and one next action, usually "`/art-slot <slug> <slot>`
with AD v<n>", or "re-run <slot> to compare" after a revision. Changed files go
on a `design/` branch, with a PR labeled `role:visual-designer` after `/pre-pr`.
