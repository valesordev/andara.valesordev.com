---
name: art-slot
description: Fill one art slot for an imprint with generated artwork. Spec and brief the slot, write candidate prompts for Brian's ChatGPT project, review the candidates after treatment, then install the selected one as a source with its treatment, build outputs, and provenance. Marks, small cuts, and lockups go through the same flow and end as SVG on the kit's grid. Also swaps a slot to Brian's hand-made replacement. Trigger on "/art-slot", "we need a hero for System 9", "prompts for the valesordev OG image", "here are the hero candidates", "generate a mark for solo7productions", "I drew the hero, swap it in". Requires the visual-designer role. Not for an imprint's overall style (art-direction), tokens or type (visual-designer, directly), Andara concept art (andaras-world concept-sheet), or building the site (the site repo).
argument-hint: "<imprint> <slot> [candidates | select <letter> | hand <file>]"
allowed-tools: Bash(.claude/bin/role:*) Bash(make:*) Bash(python3 art/treat/*) Bash(inkscape:*) Bash(potrace:*) Bash(git log:*)
---

# Art slot

Request: `$ARGUMENTS`

## Preflight
0. Run `.claude/bin/role require visual-designer ${CLAUDE_SESSION_ID}`. If it
   fails, stop and tell Brian to run `/role visual-designer`.
1. Read `art/<imprint>/slots.md` and the slot file `art/<imprint>/slots/<slot>.md`
   if it exists. The request and the slot's `Status` say which stage you're in.
2. Read the imprint's positioning and tokens. For an **illustration** slot, read
   `art/<imprint>/direction.md`. If it isn't approved, stop and recommend
   `/art-direction <imprint>`. A **mark** slot needs only a locked direction
   statement in `positioning.md`. Its provenance basis is then that file's
   commit, not an AD version.
3. Check that `.gitignore` covers `art/*/candidates/`. If it doesn't, ask Brian
   before adding it (unowned path). Candidates are never committed.

## 1. Open the slot (Status: `brief`)
1. Add a row to `art/<imprint>/slots.md` (`references/slot-template.md`). Slot
   ids are kebab-case and stable, because sites reference their outputs by them.
2. **Spec** in the slot file: canvas, aspect, crops per breakpoint, the empty
   zone for type, theme behaviour, and output files. If an existing brief covers
   this slot (for example `art/valesordev/brief.md` for `hero`), move its spec
   and done-when criteria into the slot file and leave a one-line pointer in the
   old file.
3. **Brief:** what it shows, the must-keeps, what's free to vary, and its
   done-when list.
4. **Vary.** Pick one axis and define 3–4 candidates (A–D), each a one-line thesis.
5. **Prompts.** One copy-ready block per candidate, per `references/prompt-rules.md`.

Output: the theses, the prompt blocks, and the run instructions: "In ChatGPT
project *<Imprint> — Art*, paste each block into a new message, attach the
listed files, and save each result to `art/<imprint>/candidates/<slot>-<letter>.png`."
Set Status `prompted`.

## 2. Review candidates (Status: `review`)
1. Match files to candidates by name. Ask about any that don't match.
2. **Treat before judging.** Run the direction's treatment on each candidate into
   `art/<imprint>/candidates/treated/`. Composite each result over the
   background token for every theme the imprint has, and view it at the sizes
   the spec names. For a mark, view it at 16, 32, and 64 px.
3. Review with `references/review.md`. Add the table to the slot file, and
   recommend one candidate, an iteration (revised prompts for the closest
   one), or rejecting all of them.
4. Brian selects. Record his pick and reason in his words. The others become
   `Rejected` lines with the reason. Their files stay uncommitted.

## 3. Install (Status: `installed`)
- **Illustration:** copy the selected raw image to `art/<imprint>/source/<slot>.png`.
  Write the treatment command into the slot file, and add the slot to the
  Makefile so `make <imprint>` builds `dist/<imprint>/art/<slot>.*` from the
  source, with the source file as the rule's prerequisite (`references/build.md`).
  Never write `dist/` by hand.
- **Mark, small cut, wordmark, lockup:** follow `references/marks.md`. The
  deliverable is SVG in `identity/<imprint>/`, and the raster candidate goes to
  `art/<imprint>/source/` as its provenance.
- Fill the slot's **Provenance** block, and set the `slots.md` row to
  `generated · installed`.
- Run `make <imprint>`. View the outputs, check them against the spec's
  done-when list, and list any that fail.
- If the imprint has a handoff brief, bump its REV line and name the slot.
- Remind Brian to upload the source to the ChatGPT project's files, so later
  slots can stay consistent with it.

## 4. Swap to hand-made (`hand <file>`)
Brian's replacement goes to `art/<imprint>/source/<slot>.<kra|svg|png>`. Rename
the generated source to `<slot>.generated.png`, which no rule reads, and keep it
until he says to delete it. In the same change, point the slot's Makefile rule at
the new source. A `.kra` or `.svg` gets an export step into
`dist/<imprint>/art/src/<slot>.png` ahead of the treatment
(`references/build.md`), so `make <imprint>` rebuilds from the editable file and
never from an exported copy someone has to remember to refresh. Run the same
treatment, dropping any flag that only patched the generated image (such as
`ink.py --moon`). Keep the output names, and set Provenance to `hand` with the
date. Run `make <imprint>` from clean (`rm -rf dist/<imprint>/art`) to prove the
rule. Sites don't change.

## Close
One next action: run the prompts, select from A–D, check the installed slot
on the site, or the next slot in `slots.md`. If you changed files, they go on
a branch with a PR labeled `role:visual-designer` after `/pre-pr`.
