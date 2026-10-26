---
name: brand-audit
description: Reverse-engineer an imprint's target design image into the imprint's references/visual-audit.md (palette with sampled hex and approximate ratios, typography roles, spacing, composition, navigation, image behavior, mark, line weights, texture, hierarchy, repeated motifs), classifying every observation as core identity, candidate motif, or hero-specific, each a numbered proposal for the owner to check at PR review. Also freezes the target image from Notion into references/target-homepage.png. Trigger on "/brand-audit", "audit the Valesor target", "what's intentional in the S9 homepage", "freeze the new target image", "re-audit after the new mockup". Requires the visual-designer role. Not for writing tokens or brand docs from an approved audit (visual-designer, directly), the illustration system (art-direction), checking built pages (brand-qa), or a game's concept images (that game's concept-art role).
argument-hint: "<slug> [freeze <notion-url> | revise]"
allowed-tools: Bash(.claude/bin/role:*) Bash(git log:*) Bash(git fetch:*) Bash(python3 -c:*) Bash(magick:*) Bash(identify:*)
---

# Brand audit

Request: `$ARGUMENTS`

The target is evidence, not a spec. This audit turns what it shows into
decisions the owner makes, one observation at a time. Nothing downstream (tokens,
brand docs, components) may cite the target directly: it cites the approved
audit.

## Preflight
0. Run `.claude/bin/role require visual-designer ${CLAUDE_SESSION_ID}`. If it
   fails, stop and tell the owner to run `/role visual-designer`.
1. `git fetch origin`. Resolve the slug (`brands/<slug>/`). Read the imprint's
   Notion "Brand Kit Project Direction" page (the role file or `brand/README.md`
   links it), any existing `brand/` docs, and a previous `visual-audit.md`.

## Freeze the target (`freeze`, or when `references/target-homepage.png` is missing)
1. Fetch the image from the Notion page (the file block's download URL), and
   save it as `references/target-homepage.png`. Never re-encode or resize it.
2. Record its provenance in `references/README.md`: the Notion page URL, file
   name, pixel size, date fetched, and `sha256`.
3. A target that changes later is a new freeze in its own commit, and the
   audit gets a revision against it. Never overwrite silently.

## Audit
Open the image and look at it, at full size and zoomed into each region.
Never describe it from the Notion text alone. Fill `references/audit-template.md`:

1. **Palette:** sample every distinct colour (`python3 -c` with Pillow, or
   `magick … -unique-colors`, on regions you name), with hex values and
   approximate area ratios. Note anything that looks like a generator
   artefact (off-by-a-few greys, noise) rather than a colour decision.
2. **Typography:** each text role (wordmark, display, body, mono metadata,
   links, nav), with a likely typeface family or class, size ratio to body,
   weight, case, and tracking. Name candidates for open-source, self-hostable
   fonts, but choosing one is the owner's call.
3. **Layout:** grid and columns, max width, gutters, vertical rhythm, rule
   weights and where rules carry the layout.
4. **Composition and image behavior:** how the hero sits (bleed, mask, empty
   zone for type), crop behavior implied for mobile.
5. **Navigation, mark, components:** header, nav, CTAs, cards and rows,
   metadata blocks, footer. Each repeated pattern is a component candidate.
6. **Line, texture, hierarchy, motifs:** line weights, texture layers, the
   read order, and anything repeated.
7. **Copy:** headlines, CTA wording, and voice signals visible in the image.
8. **Tells:** pseudo-text, broken letterforms, impossible geometry, misspelt
   names, wrong URLs. These are never core.

## Classify
Every observation gets one class, marked `[PROPOSED P<n>]` (the next IDs
unused in `brands/<slug>/`, per the role's Proposals section), with one line
of reasoning:

- **core identity:** persists across every application of the brand
- **candidate motif:** worth testing in other applications before adopting
- **hero-specific:** belongs to this composition only (it goes to a slot brief,
  never to the brand docs)

Test each proposed core item against the Notion page's direction (its
"character", "not", and anti-pattern lists) and against the sibling
imprints' approved audits. A core item that matches a sibling's core is a
distinctness flag: say so.

## Ship
The audit goes in its own PR on a `design/` branch, labeled
`role:visual-designer`, after `/pre-pr`. The PR body has one checkbox per
proposal, core items first, each at the start of a line:
`- [ ] P<n>: <class> — <observation> (references/visual-audit.md#<section>)`.
The owner decides at PR review: a checked box approves that ID, and one still
unchecked at merge stays `[PROPOSED P<n>]`. Never tell them the audit is
approved when merged. Record a reclassification or change they ask for in the
audit's Decisions section, on the same branch. Checked IDs become
`[DECIDED <YYYY-MM-DD>, PR #<pr>, P<n>]` after the merge (the role's Proposals
section), never in the open PR.

Never write `tokens/`, `brand/visual-language.md`, or component specs from an
unmerged audit. Stop after the PR.

## Output
The PR link, the count of core / candidate / hero-specific items, the
distinctness flags, open questions for the owner (at most five, each with a
recommendation), and one next action, usually "review the audit PR", or after
merge, "derive tokens from the checked core palette and type".
