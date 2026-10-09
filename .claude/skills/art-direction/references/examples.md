# Examples

**Prompt:** "/art-direction valesordev"
**Behavior:** Loads art-direction after `role require visual-designer`. Reads `brands/valesordev/brand/foundation.md`,
the approved `references/visual-audit.md` (core: pure black ink on white, engraving hatch and stipple, large negative
space; hero-specific: this juniper, this moon), and `tokens/tokens.json`. Drafts
`brands/valesordev/art-direction/illustration-system.md`: subject is high-desert landscape (wind-shaped juniper,
rock, low ridge, sparse scrub, a large moon), no people; rendering is pen-and-ink survey/engraving line, hatch, and
stipple, no gray wash; value is ink-only on transparency, painted with the ink token; treatment
`brands/_tools/treat/ink.py`. Composition types: `hero-wide` 2.4:1 (left third empty for type), `banner-wide`,
`square-specimen`, `vertical-plate`. Recommends each section with one alternative (e.g. "flat silhouette instead of
hatch: repeats more reliably, loses the engraving quality"). Keeps the specific juniper out of the system: that's
the hero slot's prompt. Marks open items `[PROPOSED]`. Renders nothing until Brian approves.

**Prompt:** "Approved, ship AD v1."
**Behavior:** Sets `**Status:** AD v1 — approved 2026-10-08`, adds the changelog line, and outputs the rendered
ChatGPT block with "Paste into ChatGPT → Project *Valesor Development — Art* → Instructions, replacing everything.
Version: AD v1." Next action: `/art-slot valesordev hero`.

**Prompt:** "The System 9 renders keep coming out glossy."
**Behavior:** Revision. Cites the rejected candidates in `brands/system9studios/assets/slots/`, adds them to
`negative-examples.md`, and proposes tightening Rendering ("matte, no specular highlights, no reflections") and
adding a treatment step (posterize to the token ramp). Bumps to AD v<n+1> only after Brian approves, and recommends
re-running the last slot to compare.

**Prompt:** "Make solo7.media look like <named artist>."
**Behavior:** Declines the named style and asks which qualities he wants (palette, line, light) to capture in the
illustration system instead.

**Non-triggers:** "Prompts for the S9 hero" → art-slot. "What's in the Valesor target?" → brand-audit. "Pick the
body font for System 9" → visual-designer, directly. "Update Andara's house style" → andaras-world house-style.
