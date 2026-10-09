---
name: art-slot
description: Fill one art slot for an imprint with generated artwork. Spec and brief the slot against one of the illustration system's composition types, write candidate prompts for Brian's ChatGPT project, review the candidates after treatment, then install the selected one as a source in the imprint's brand assets with its treatment, build outputs, and provenance. Marks, small cuts, and lockups go through the same flow and end as SVG on the kit's grid. Also swaps a slot to Brian's hand-made replacement. Trigger on "/art-slot", "we need a hero for System 9", "prompts for the valesordev OG image", "here are the hero candidates", "generate a mark for solo7productions", "I drew the hero, swap it in". Requires the visual-designer role. Not for an imprint's overall style (art-direction), reading a target image (brand-audit), tokens or type (visual-designer, directly), Andara concept art (andaras-world concept-sheet), or building pages (implementation).
argument-hint: "<slug> <slot> [candidates | select <letter> | hand <file>]"
allowed-tools: Bash(.claude/bin/role:*) Bash(make:*) Bash(python3 brands/_tools/treat/*) Bash(inkscape:*) Bash(potrace:*) Bash(git log:*)
---

# Art slot

Request: `$ARGUMENTS`

Paths below are relative to `brands/<slug>/` unless they start with `brands/`.

## Preflight
0. Run `.claude/bin/role require visual-designer ${CLAUDE_SESSION_ID}`. If it
   fails, stop and tell Brian to run `/role visual-designer`.
1. Read `assets/slots.md` and the slot file `assets/slots/<slot>.md` if it
   exists. The request and the slot's `Status` say which stage you're in.
2. Read `brand/foundation.md` and `tokens/tokens.json`. For an **illustration**
   slot, read `art-direction/illustration-system.md`. If it isn't approved,
   stop and recommend `/art-direction <slug>`. A **mark** slot needs only a
   direction statement in an approved `brand/foundation.md`. Its provenance
   basis is then that file's commit, not an AD version.
3. Check that `.gitignore` covers `brands/*/assets/candidates/`. If it doesn't,
   ask Brian before adding it (unowned path). Candidates are never committed.

## 1. Open the slot (Status: `brief`)
1. Add a row to `assets/slots.md` (`references/slot-template.md`). Slot ids
   are kebab-case and stable, because components and sites reference their
   outputs by them.
2. **Spec** in the slot file: the composition type from the illustration system
   (its aspect and empty zone), canvas, crops per breakpoint, theme behaviour,
   and output files.
3. **Brief:** what it shows, the must-keeps, what's free to vary, and its
   done-when list. When the frozen target has this image, the audit's
   hero-specific items for it are the brief's starting point.
4. **Vary.** Pick one axis and define 3–4 candidates (A–D), each a one-line thesis.
5. **Prompts.** One copy-ready block per candidate, per `references/prompt-rules.md`
   and the imprint's `art-direction/prompt-template.md`.

Output: the theses, the prompt blocks, and the run instructions: "In ChatGPT
project *<Imprint> — Art*, paste each block into a new message, attach the
listed files, and save each result to `brands/<slug>/assets/candidates/<slot>-<letter>.png`."
Set Status `prompted`.

## 2. Review candidates (Status: `review`)
1. Match files to candidates by name. Ask about any that don't match.
2. **Treat before judging.** Run the illustration system's treatment on each
   candidate into `assets/candidates/treated/`. Composite each result over the
   background token for every theme the imprint has, and view it at the sizes
   the spec names. For a mark, view it at 16, 32, and 64 px.
3. Review with `references/review.md`. Add the table to the slot file, and
   recommend one candidate, an iteration (revised prompts for the closest
   one), or rejecting all of them.
4. Brian selects. Record his pick and reason in his words. The others become
   `Rejected` lines with the reason. Their files stay uncommitted.

## 3. Install (Status: `installed`)
- **Illustration:** copy the selected raw image to
  `assets/illustration/<composition>/<slot>.png`. Write the treatment command
  into the slot file, and add the slot's rule to the brand's make include,
  `brands/<slug>/brand.mk`, so `make <slug>` builds
  `brands/<slug>/dist/art/<slot>.*` from the source, with the source file as the
  rule's prerequisite (`references/build.md`). Never write `dist/` by hand.
  The root Makefile (sre's) includes every `brand.mk`. If it doesn't yet, ask
  with `story comment <ID> --to sre`; don't edit it.
- **Mark, small cut, wordmark, lockup:** follow `references/marks.md`. The
  deliverable is SVG in `assets/marks/`, and the raster candidate goes to
  `assets/marks/source/` as its provenance.
- Fill the slot's **Provenance** block, and set the `slots.md` row to
  `generated · installed`.
- Run `make <slug>`. View the outputs, check them against the spec's
  done-when list, and list any that fail.
- If a built page uses the slot, name the site in the PR body.
- Remind Brian to upload the source to the ChatGPT project's files, so later
  slots can stay consistent with it.

## 4. Swap to hand-made (`hand <file>`)
Brian's replacement goes next to the generated source as
`<slot>.<kra|svg|png>`. Rename the generated source to `<slot>.generated.png`,
which no rule reads, and keep it until he says to delete it. In the same
change, point the slot's Makefile rule at
the new source. A `.kra` or `.svg` gets an export step into
`brands/<slug>/dist/art/src/<slot>.png` ahead of the treatment
(`references/build.md`), so `make <slug>` rebuilds from the editable file and
never from an exported copy someone has to remember to refresh. Run the same
treatment, dropping any flag that only patched the generated image (such as
`ink.py --moon`). Keep the output names, and set Provenance to `hand` with the
date. Run `make <slug>` from clean (`rm -rf brands/<slug>/dist/art`) to prove
the rule. Sites don't change.

## Close
One next action: run the prompts, select from A–D, check the installed slot
on the site, or the next slot in `slots.md`. If you changed files, they go on
a `design/` branch with a PR labeled `role:visual-designer` after `/pre-pr`.
