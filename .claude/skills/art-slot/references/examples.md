# Examples

**Prompt:** "/art-slot valesordev hero"
**Behavior:** After `role require visual-designer`, checks that `art/valesordev/direction.md` is approved
(stops and recommends `/art-direction valesordev` if not). Opens the slot: adds `hero` to `slots.md`, moves the
spec and done-when list from `art/valesordev/brief.md` into `slots/hero.md` (4800 × 2000, phone crop is the right
1200 × 2000, left third empty, ink on alpha), and leaves a pointer in `brief.md`. Axis: tree silhouette. A:
juniper bent hard to the left; B: upright, split trunk; C: low and wide on a rock shelf; D: dead snag beside a
young tree. Emits four prompt blocks asking for clean black line and hatch on plain paper, since the treatment is
`ink.py`, saved as `art/valesordev/candidates/hero-A.png` … `-D.png`.

**Prompt:** "Here are the hero candidates."
**Behavior:** Runs `python3 art/treat/ink.py` on each into `candidates/treated/`, composites over `#F4F1EA` and
`#0F1115` with the ink recoloured, and views each at 600 px and in the phone crop. The review table shows C has
pseudo-text in the rock texture (Tells ✗) and B's canopy crosses x 2400 (Composition ~). Recommends A. Waits for
Brian's selection.

**Prompt:** "A. The bend reads as wind, which is the point."
**Behavior:** Records the selection with his reason, copies `hero-A.png` to `art/valesordev/source/hero.png`, adds
the Makefile rule for `dist/valesordev/art/hero.png` and `hero.json`, fills Provenance (generated, ChatGPT, date,
AD v1), runs `make valesordev`, checks the done-when list, bumps the REV line in `handoff/valesordev.md`, and
reminds him to upload `hero.png` to the ChatGPT project files.

**Prompt:** "Generate a mark for solo7productions."
**Behavior:** A mark slot, so it needs only the direction statement in `copy/solo7productions/positioning.md`. If
none is locked, stops and says positioning comes first. Otherwise it writes 2–3 mark-sheet prompts on one
construction axis. After Brian picks a cell, it rebuilds the mark as grid geometry per `marks.md`, renders it at
16–512 beside the Solo7 glyph, records the vertices and provenance in the identity README, and leaves locking to Brian.

**Prompt:** "I drew the hero in Krita, swap it in." (attaches `hero.kra` export)
**Behavior:** Swap: the export goes to `source/hero.png`, the old one is kept as `hero.generated.png`, the same
treatment runs without `--moon`, Provenance becomes `hand`, and the `slots.md` row changes. Outputs keep their
names, so the site needs no change.

**Non-triggers:** "The renders all look too glossy" → art-direction. "Lock the S9 type" → visual-designer,
directly. "Concept the Andara gate" → andaras-world concept-sheet.
