# Examples

**Prompt:** (visual-designer) "/brand-audit valesordev freeze https://app.notion.com/p/3f26a339c86181249193d3b987def934"
**Behavior:** After `role require visual-designer`, fetches the page's image block, saves it untouched as
`brands/valesordev/references/target-homepage.png`, and records URL, file name, 1586 × 992, date, and sha256 in
`references/README.md`. Then audits: samples paper (~#FAFAF7), ink (#111), muted grey, rule grey, and one vermilion
accent; types the roles (display serif for the wordmark, headline, and project names; monospace for metadata, body
blurbs, and CTAs; serif subheads); notes hairline rules doing the layout, a three-column "what / license / what
belongs" band, and a `ProjectRow` (serif name, mono blurb, STACK/LICENSE/REPO metadata, vermilion arrow). Classifies
the engraving style and ink-only rendering as core, the juniper and moon as hero, and the three-column band as a
candidate. Lists tells: "fction", the repo URLs on `github.com/valesor/…`. Every line `[PROPOSED]`. Opens the PR
with each core item as a checkbox and stops.

**Prompt:** "Audit the System 9 target. Is the radio telescope part of the identity?"
**Behavior:** Classifies the telescope as hero, citing the Notion page's note that it's imagery, not the identity,
and proposes the structural treatment around it (navy ground, bone type, small uppercase metadata, thin rules) as
core. Flags that "thin rules" also appears in the Valesor core: proposes how S9's rules differ (weight, colour)
so the imprints don't converge.

**Prompt:** "Audit approved, with the band moved to core."
**Behavior:** Records Brian's change in Decisions, flips every line to `[DECIDED <date>]` on the same branch, and
names the next action: derive tokens from the decided palette and type.

**Prompt:** "Just generate the tokens from the screenshot."
**Behavior:** Declines to skip the gate: tokens come from the approved audit. Offers to run the audit now and keep
the PR small so review is quick.

**Non-triggers:** "Write the illustration system" → art-direction. "Does the built homepage match the brand?" →
brand-qa. "Review these Andara concepts" → andaras-world art-review.
