---
name: art-direction
description: Develop, revise, or export an imprint's art direction (subject matter, rendering, value and colour against the tokens, line, light, composition, treatment, exclusions) in the brand kit, and render it as the instructions for that imprint's ChatGPT project. Trigger on "/art-direction", "set the art direction for System 9", "the valesordev renders all look too X", "give me the ChatGPT project instructions for solo7.media", "what's the current art direction". Requires the visual-designer role. Not for one image or mark (art-slot), tokens or type decisions (visual-designer, directly), or Andara's concept-art house style (andaras-world house-style).
argument-hint: "<imprint> [revise | export]"
allowed-tools: Bash(.claude/bin/role:*) Bash(git log:*) Bash(git fetch:*)
---

# Art direction

Request: `$ARGUMENTS`

## Preflight
0. Run `.claude/bin/role require visual-designer ${CLAUDE_SESSION_ID}`. If it
   fails, stop and tell Brian to run `/role visual-designer`.
1. Resolve the imprint (`valesordev`, `system9studios`, `solo7productions`,
   `solo7media`). Read its positioning (`copy/<imprint>/positioning.md`), tokens
   (`tokens/<imprint>.json`), identity README, moodboard `notes.md`, and the
   existing `art/<imprint>/direction.md` if there is one.
2. If the imprint has no locked direction statement and no colour tokens, stop.
   Art direction derives from those, so they come first.

## Develop or revise
1. **Evidence first.** Gather the moodboard keeps and why they were kept, the
   slots already selected or rejected (`art/<imprint>/slots/`) and Brian's
   reasons. A revision cites at least one image or finding.
2. **Decide section by section** with `references/direction-template.md`. For
   each open section, recommend one option in plain visual terms and name one
   alternative with its trade-off. Test each against:
   - **Tokens:** can the treatment map it onto the imprint's palette and both
     themes (or its single theme)?
   - **Distinctness:** does it stay clear of the sibling imprints' languages?
     Read their `direction.md` files.
   - **Generator reliability:** will a ChatGPT image model hit it repeatedly?
     Flat, graphic, limited-palette styles repeat well. Fine hatching, exact
     geometry, and legible text don't, so treatment or SVG handles those.
3. **Draft** `art/<imprint>/direction.md`. Attribute decisions to Brian, and
   mark undecided items `[PROPOSED]`.
4. **Version.** When Brian approves (in the session; quote him), set
   `**Status:** AD v<n> — approved <YYYY-MM-DD>` and add a changelog line:
   what changed, why, and which images motivated it.
5. **Render** the ChatGPT project instructions (below).

## Export
Fill `references/chatgpt-project.md` from the approved direction, under
1,500 characters, in one fenced block, followed by: "Paste into ChatGPT →
Project *<Imprint> — Art* → Instructions, replacing everything. Version:
AD v<n>." Brian never edits the instructions in ChatGPT, or the two drift.
Never render from a draft.

## Output
Decisions made, `[PROPOSED]` items, the new version (if any), the rendered
block (if approved), and one next action, usually "`/art-slot <imprint> <slot>`
with AD v<n>", or "re-run <slot> to compare" after a revision.
