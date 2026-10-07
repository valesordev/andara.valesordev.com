# Examples

**Prompt:** "/art-direction valesordev"
**Behavior:** Loads art-direction after `role require visual-designer`. Reads `positioning.md` (Instrument),
`tokens/valesordev.json`, the moodboard notes, and the existing hero brief. Drafts `art/valesordev/direction.md`:
subject is desert landscape with one juniper, no people; rendering is pen-and-ink line and hatch; value/colour is
ink-only on transparency, painted with `--color-fg`; treatment `art/treat/ink.py`. Recommends each section with one
alternative (e.g. "flat silhouette instead of hatch: repeats more reliably, loses the drawing quality"). Marks open
items `[PROPOSED]`. Renders nothing until Brian approves.

**Prompt:** "Approved, ship AD v1."
**Behavior:** Sets `**Status:** AD v1 — approved 2026-10-05`, adds the changelog line, and outputs the rendered
ChatGPT block with "Paste into ChatGPT → Project *Valesor Development — Art* → Instructions, replacing everything.
Version: AD v1." Next action: `/art-slot valesordev hero`.

**Prompt:** "The System 9 renders keep coming out glossy."
**Behavior:** Revision. Cites the rejected candidates in `art/system9studios/slots/` and proposes tightening
Rendering ("matte, no specular highlights, no reflections") and adding a treatment step (posterize to the token
ramp). Bumps to AD v<n+1> only after Brian approves, and recommends re-running the last slot to compare.

**Prompt:** "Make solo7.media look like <named artist>."
**Behavior:** Declines the named style and asks which qualities he wants (palette, line, light) to capture in the
direction instead.

**Non-triggers:** "Prompts for the S9 hero" → art-slot. "Pick the body font for System 9" → visual-designer,
directly. "Update Andara's house style" → andaras-world house-style.
