# Examples

**Prompt:** (visual-designer role, SPRINT-01 active) "/design-start-sprint"
**Behavior:** Scans the sprint's threads first and finds `**For visual-designer:**` on S7M-VAL-012 (implementation:
"ProjectRow arrow on hover?"). `brands/valesordev/brand/visual-language.md` doesn't say, and it's a spec detail, so
it adds "arrow shifts 2 px right, no colour change" to the ProjectRow spec on `design/s7m-val-012-row-hover`, opens
the PR, and replies on the story citing `visual-language.md#projectrow`. Then takes S7M-VAL-003 (`ready`, the
Valesor visual audit): branches `design/s7m-val-003-visual-audit`, runs `story start S7M-VAL-003`, runs
`/brand-audit valesordev`, opens the PR with `Story: S7M-VAL-003` and `role:visual-designer`, and records
`--record "Verification"`.

**Prompt:** (visual-designer role) "/design-start-sprint" when implementation asks which accent red to use
**Behavior:** The palette is Brian's call. Brings him one recommendation with the sampled hex and contrast ratios,
records his answer in the tokens on a `design/` branch, and replies on the story with the PR.

**Prompt:** (visual-designer role) "/design-start-sprint" with no active sprint
**Behavior:** `sprint-state` reports `state: planned`. Stops and reports it: PM hasn't activated the sprint.

**Non-triggers:** "Make the S9 hero" outside a sprint → `/art-slot system9studios hero`. "Address the review comments
on #31" → `/pr-comments 31`.
