# Examples

**Prompt:** (implementation, on `impl/s7m-val-012-homepage`) "/brand-qa valesordev diff"
**Behavior:** `role show` prints `implementation`, so it proceeds. Reads `brands/valesordev/qa.md` and the tokens,
runs the site preview, and views the homepage at 1440 and 390 px, light theme, JS off. Finds a literal `#3366CC` in
the footer CSS (off-token, fail, implementation), a `box-shadow` on project rows (anti-pattern, fail,
implementation), and a "Get started" CTA (voice, fail, implementation). Also finds the spec doesn't say how the
row arrow behaves on hover, so it sends `story comment S7M-VAL-012 --to visual-designer`. Fixes its own three fails
before `/pre-pr`, and records `--record "Brand QA"`.

**Prompt:** (visual-designer) "/brand-qa system9studios seed"
**Behavior:** No `qa.md` yet. Seeds it from the template, the approved S9 foundation, and the Notion v0.1
acceptance criteria ("recognizable without the radio telescope", "no HUD brackets, neon, or interface cosplay"),
marks two unsettled items `[PROPOSED]`, and opens a `design/` PR for Brian.

**Prompt:** (implementation) "/brand-qa solo7productions https://<preview-url>" with no `qa.md` in the kit
**Behavior:** Stops: there are no approved rules to check against. Asks with `story comment <ID> --to
visual-designer` for `qa.md`.

**Prompt:** (pm) "/brand-qa valesordev"
**Behavior:** `role show` prints `pm`. Stops and says the skill runs as visual-designer or implementation.

**Non-triggers:** "Audit the new target image" → brand-audit. "Review these hero candidates" → art-slot.
"Review the branch before I push" → pre-pr.
