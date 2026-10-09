# Examples

**Prompt:** "/art-slot valesordev hero"
**Behavior:** After `role require visual-designer`, checks that `brands/valesordev/art-direction/illustration-system.md`
is approved (stops and recommends `/art-direction valesordev` if not). Opens the slot: adds `hero` to
`assets/slots.md` with composition `hero-wide` (2.4:1, left third empty for type, ink on alpha), and seeds the brief
from the visual audit's hero-specific items (the wind-bent juniper on a rock shelf, the moon upper right, the ridge
receding right). Axis: tree silhouette. A: juniper bent hard to the left; B: upright, split trunk; C: low and wide on
a rock shelf; D: dead snag beside a young tree. Emits four prompt blocks asking for clean black line, hatch, and
stipple on plain paper, since the treatment is `brands/_tools/treat/ink.py`, saved as
`brands/valesordev/assets/candidates/hero-A.png` … `-D.png`.

**Prompt:** "Here are the hero candidates."
**Behavior:** Runs `python3 brands/_tools/treat/ink.py` on each into `assets/candidates/treated/`, composites over the
paper token with the ink recoloured, and views each at 600 px and in the phone crop. The review table shows C has
pseudo-text in the rock texture (Tells ✗) and B's canopy crosses into the type zone (Composition ~). Recommends A.
Waits for Brian's selection.

**Prompt:** "A. The bend reads as wind, which is the point."
**Behavior:** Records the selection with his reason, copies `hero-A.png` to
`brands/valesordev/assets/illustration/hero-wide/hero.png`, fills Provenance (generated, ChatGPT, date, AD v1), and
adds the Makefile rule for `brands/valesordev/dist/art/hero.png`. Makefile is sre's in this repo, so the rule goes
in the PR body with `story comment <ID> --to sre`. Runs `make valesordev` once the rule lands, checks the done-when
list, names `sites/valesordev.com` in the PR body, and reminds him to upload `hero.png` to the ChatGPT project files.

**Prompt:** "Generate a mark for solo7productions."
**Behavior:** A mark slot, so it needs only the direction statement in an approved
`brands/solo7productions/brand/foundation.md`. If it isn't approved, stops and says the foundation comes first.
Otherwise it writes 2–3 mark-sheet prompts on one construction axis (S+7 monogram, slash/cut, maker's mark). After
Brian picks a cell, it rebuilds the mark as grid geometry per `marks.md`, renders it at 16–512 beside the sibling
marks, records the vertices and provenance in `assets/marks/README.md`, and leaves locking to Brian.

**Prompt:** "I drew the hero in Krita, swap it in." (attaches `hero.kra`)
**Behavior:** Swap: `hero.kra` goes to `assets/illustration/hero-wide/`, the old source is kept as
`hero.generated.png`, the rule gains the Krita export step, the same treatment runs without `--moon`, Provenance
becomes `hand`, and the `slots.md` row changes. Outputs keep their names, so the site needs no change.

**Non-triggers:** "The renders all look too glossy" → art-direction. "What's intentional in the S9 target?" →
brand-audit. "Lock the S9 type" → visual-designer, directly. "Concept the Andara gate" → andaras-world concept-sheet.
