---
name: brand-qa
description: Check a built page, screenshot, component, or branch diff against an imprint's brand QA checklist (qa.md), its tokens (no off-token colour, font, or spacing values), WCAG 2.1 AA, and the anti-patterns in its brand docs, and report a pass/fail table with each fix routed to the owning role; seeds qa.md from the template when an imprint has none. Trigger on "/brand-qa", "does this look Valesor", "brand check the homepage", "QA the project row against the kit", "is this on-brand before I open the PR". Requires the visual-designer or implementation role. Not for reviewing a target image (brand-audit), generated art candidates (art-slot), general code review (pre-pr), or changing the brand rules themselves (visual-designer, directly).
argument-hint: "<slug> [<url | path | screenshot> | diff | seed]"
allowed-tools: Bash(.claude/bin/role:*) Bash(git diff:*) Bash(git fetch:*) Bash(git merge-base:*) Bash(git log:*) Bash(make:*)
---

# Brand QA

Request: `$ARGUMENTS`

## Preflight
0. Run `.claude/bin/role show ${CLAUDE_SESSION_ID}`. It must print
   `visual-designer` or `implementation`. Otherwise stop and tell Brian to run
   `/role visual-designer` or `/role implementation`.
1. Resolve the slug (`brands/<slug>/`). Read `qa.md`, `brand/foundation.md`,
   `brand/visual-language.md`, `brand/voice.md`, `tokens/tokens.json`, and the
   component specs the target uses.
2. If `qa.md` doesn't exist: as visual-designer, seed it (below) and stop. As
   implementation, stop and ask for it with `story comment <ID> --to
   visual-designer`. Never QA against rules nobody approved.

## Seed (`seed`, visual-designer only)
Fill `references/qa-template.md` from the imprint's brand docs and its Notion
direction page's checklist. Every item must be observable on a page. Mark
items the docs don't settle `[PROPOSED]`. It ships in a `design/` PR for
Brian's review.

## Check
Decide what you're checking: a running page (local `make` preview or a
preview deploy URL), a screenshot, a component, or `diff` (changes since
`git merge-base origin/main HEAD` under `brands/` and `sites/`).

1. **Look at it.** Render the page at desktop and phone widths, in every theme
   the imprint has, and with JavaScript off. Never judge a page from its source alone.
2. **Checklist:** each `qa.md` item, pass or fail, with the evidence (a
   region of the screenshot, a selector, a line).
3. **Tokens:** search the diff or built CSS for literal colours, font
   families, and spacing values that aren't tokens. Each one is a defect,
   unless it's in `tokens/` itself.
4. **Accessibility:** WCAG 2.1 AA contrast for every text/background pair
   used (from the tokens), visible focus, target size, real headings and
   landmarks, `prefers-reduced-motion`.
5. **Anti-patterns:** each item in the foundation's "not" list (e.g.
   gradients, shadows, rounded card UI, marketing CTAs). One hit fails.
6. **Copy:** CTA wording and voice against `brand/voice.md`.

## Report
```markdown
| # | Check | Result | Evidence | Fix | Owner |
|---|---|---|---|---|---|
| 1 | Palette is ink, paper, one accent | ✗ | footer link uses #3366CC | use --color-accent | implementation |
```

Owners: `implementation` for code and page fixes, `visual-designer` for a
rule that's missing or wrong. Rules that need Brian are marked as such.
Don't fix another role's files. As implementation, fix your own fails before
the PR, and send rule gaps with `story comment <ID> --to visual-designer`. As
visual-designer, report fails on the story with `story comment <ID> --to
implementation` and fix your own docs.

When the check is for a story, record it with
`story comment <ID> --record "Brand QA" --body-file <report>`.

## Output
The table, a one-line verdict (pass, or the count of fails by owner), and one
next action.
