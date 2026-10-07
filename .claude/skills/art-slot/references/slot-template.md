# Slot index and slot file

## art/<imprint>/slots.md

```markdown
# <Imprint> — art slots

| Slot | Kind | Used on | Provenance | Status | Outputs |
|---|---|---|---|---|---|
| hero | illustration | home hero; section art (crops) | generated | installed | dist/<imprint>/art/hero.png, hero.json |
| og | illustration | Open Graph background | — | brief | dist/<imprint>/og.png |
| mark | mark | everywhere | hand | locked | identity/<imprint>/mark.svg |
```

Kinds: `illustration`, `mark` (marks, small cuts, wordmarks, lockups).
Provenance: `generated`, `hand`, or `—` (not yet filled).
Status: `brief → prompted → review → installed` (marks: `locked` once Brian locks them).
The rows with `generated` provenance are the backlog for hand-made replacements.

## art/<imprint>/slots/<slot>.md

```markdown
# <slot> — <imprint>

**Status:** brief | prompted | review | installed
**Kind:** illustration | mark  · **Basis:** AD v<n> | positioning.md @ <short commit>

## Spec
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
- Source: art/<imprint>/source/<slot>.<ext>
- Tool: <ChatGPT image model, as reported> · Date: <YYYY-MM-DD> · Prompt: candidate <letter> above
- Basis: AD v<n> (illustration) | positioning.md @ <short commit> (a mark made before the imprint has an approved art direction)
- Edits: <none | what was changed after generation, by whom>
- Treatment: `python3 art/treat/<name>.py <args>`
```
