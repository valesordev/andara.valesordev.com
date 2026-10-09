# Slot index and slot file

## brands/<slug>/assets/slots.md

```markdown
# <Imprint> — art slots

| Slot | Kind | Composition | Used on | Provenance | Status | Outputs |
|---|---|---|---|---|---|---|
| hero | illustration | hero-wide | home hero; section art (crops) | generated | installed | dist/art/hero.png, hero.json |
| og | illustration | banner-wide | Open Graph background | — | brief | dist/og.png |
| mark | mark | — | everywhere | hand | locked | assets/marks/mark.svg |
```

Kinds: `illustration`, `mark` (marks, small cuts, wordmarks, lockups).
Provenance: `generated`, `hand`, or `—` (not yet filled).
Status: `brief → prompted → review → installed` (marks: `locked` once Brian locks them).
The rows with `generated` provenance are the backlog for hand-made replacements.

## brands/<slug>/assets/slots/<slot>.md

```markdown
# <slot> — <imprint>

**Status:** brief | prompted | review | installed
**Kind:** illustration | mark  · **Composition:** <type> · **Basis:** AD v<n> | brand/foundation.md @ <short commit>

## Spec
- Composition: <type from illustration-system.md>
- Canvas: <W × H px>, aspect <a:b>, <colour mode / alpha>
- Crops: <breakpoint → region>
- Empty zone: <where type sits; nothing busy there>
- Themes: <how it renders in each: painted with a token, duotone, fixed>
- Outputs: <dist paths and sizes>

## Brief
Shows: <one paragraph>
Must keep: <bullets>
Free: <bullets>
Done when: <observable checks: reads at N px, crop is complete alone, passes threshold check…>

## Candidates
Axis: <variation axis>
| | Thesis | File | Verdict |
|---|---|---|---|
| A | … | candidates/<slot>-A.png | |

### Prompts
<one block per candidate, per prompt-rules.md>

## Review
<table from review.md, recommendation>

## Selection
<letter> — Brian, <date>: "<his reason>". Rejected: B (<reason>), …

## Provenance
- Provenance: generated | hand
- Source: assets/illustration/<composition>/<slot>.<ext> | assets/marks/source/<slot>.png
- Tool: <ChatGPT image model, as reported> · Date: <YYYY-MM-DD> · Prompt: candidate <letter> above
- Basis: AD v<n> (illustration) | brand/foundation.md @ <short commit> (a mark made before the imprint has an approved art direction)
- Edits: <none | what was changed after generation, by whom>
- Treatment: `python3 brands/_tools/treat/<name>.py <args>`
```
