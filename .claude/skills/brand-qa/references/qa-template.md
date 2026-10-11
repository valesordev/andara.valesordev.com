# brands/<slug>/qa.md

Seed from the imprint's brand docs and its Notion direction page. Every item is a yes/no question someone
can answer by looking at the page. Generic items below hold for every imprint unless its foundation says
otherwise; the imprint-specific section comes from that imprint's docs.

```markdown
# <Imprint> — brand QA

**Status:** draft | approved <YYYY-MM-DD> (PR #<n>)
**Sources:** brand/foundation.md @ <commit>, brand/voice.md @ <commit>, tokens/tokens.json @ <commit>

## Identity
- [ ] Does it read as <the foundation's character words> rather than <its "not" list>?
- [ ] Is the imprint named the way the foundation names it (e.g. "an imprint", not "a company")?
- [ ] Would it be mistaken for a sibling imprint? (It shouldn't.)

## Palette and type
- [ ] Is every colour a token, and is the palette still essentially <the decided set>?
- [ ] Are the only typefaces the decided families, each in its decided role?
- [ ] Is the accent used only where the visual language allows (e.g. links, one directional mark)?

## Layout
- [ ] Are rules and typography doing most of the layout work?
- [ ] Is whitespace preserved rather than filled?
- [ ] Do the canonical components look like their specs (correct-usage examples), not like variants?

## Imagery
- [ ] Is every illustration on the approved illustration system, from an installed slot with provenance?
- [ ] Is the hero's empty zone clear of detail behind the type, at every breakpoint?

## Anti-patterns (one hit fails)
- [ ] No gradients, shadows, glassmorphism, or rounded SaaS cards (unless the foundation allows one).
- [ ] No unnecessary UI, animation, or decorative ornament the foundation excludes.

## Copy
- [ ] Does the copy describe what the work is rather than sell its benefits?
- [ ] Does it use the voice's person (e.g. first-person singular) and vocabulary, and the approved CTAs?
- [ ] Are source and license visible where the foundation asks for them?

## Accessibility
- [ ] WCAG 2.1 AA contrast for every text/background pair, focus visible, targets ≥ 24 px, reduced motion respected.
- [ ] Fully readable with JavaScript off.

## <Imprint>-specific
- [ ] <items from this imprint's docs>
```
