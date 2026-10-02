# AW-INF-023 — the Builder's Guide: its tooling

PM, 2026-09-29. This follows the story's "Blocked by" items 2 and 3, which recommended splitting the
tooling out. PM wrote the split as two new stories, both at `draft`:

- `AW-CLI-009`, implementation: `andara-cli content reference [--output human|json]`. It prints the
  Directions, Component types, embedded core Templates and diagnostic codes, all from the code. It
  depends on `AW-CLI-002`.
- `AW-INF-028`, SRE: `make builder-reference`, `make builder-reference-check` and
  `make guide-check`. It depends on `AW-CLI-009`.

`AW-INF-023` now depends on both, and its `status` is unchanged.

## For architecture

### 1. Proposal: move AC-1 and AC-2 to AW-INF-028 (a contract change, so it's yours)

PM's scope decision is to keep both new stories out of SPRINT-03. The sprint's implementation list
is already ten items long, with `AW-SRV-013` and `AW-CLI-003` as big as the sprint. `AW-INF-028` also
can't merge until Brian changes the `.claude/` generated-files list upstream (its Open questions,
item 1). The demo, AC-3's walk-through, needs neither tool.

So that the guide ships this sprint and the checks follow in SPRINT-04, PM proposes:

- moving AC-1, AC-2, the "Also in scope" bullets for the two targets, and the two target lines of the
  Interface contract from `AW-INF-023` to `AW-INF-028`, which already restates them;
- dropping `AW-CLI-009` and `AW-INF-028` from `AW-INF-023`'s `depends_on` if you accept. PM added
  them as the split requires, and they hold the guide behind SPRINT-04 work until you decide;
- giving section 7 an interim form until `AW-INF-028` lands. For example, it links to the glossary's
  Direction entry, `semantics.md`, `content/core/` and `errors.md` §3, and says the tables are
  coming.

If you'd rather keep AC-1 and AC-2 on the guide, the alternative is to pull both stories into
SPRINT-03, behind `AW-CLI-002` (implementation item 6) and Brian's upstream change. The guide
would then be the last thing to close.

### 2. A `--help` exit code can't show that a command exists

AC-1 says every `andara-cli` command in the guide "exits 0 for `--help`". But
`andara-cli content bogus --help` exits `0` today. It prints the help for `content`, because an
unknown trailing word falls back to the nearest parent. PM checked this against `main` at 910f785.
So `AW-INF-028` AC-5 resolves the path from the binary's own command tree, through Cobra's
`__complete`. A line then fails if it leaves a positional word after a command that has
subcommands. If AC-1 stays on the guide, it wants the same wording.

### 3. Section 7's "what triggers it, and the usual fix"

The guide's scope promises each diagnostic code's trigger and usual fix. `AW-CLI-009`'s JSON has
code, severity and `raised_by` only, and the code has no prose to offer. `AW-INF-028` renders each
row with a link to `errors.md` §3, which already has a trigger column. That keeps the rule that the
guide explains and links, never restates. Confirm that this meets section 7, or say where the
trigger and fix text should come from.

### 4. The `builder-reference` help line names `errors.md`

Your Interface contract's help line says the tables come from "…andara.core, and errors.md".
`builder-reference` reads only `AW-CLI-009`'s JSON, and it links to `errors.md` rather than reading it.
`AW-INF-028` keeps your line verbatim for now. Should it change?

### 5. Two loader codes aren't in `errors.md`

`zone_removed` and `spawn_room_removed` (`sim.ErrCode`, `AW-SRV-012`) aren't in `errors.md` §3. They
refuse an activation or a reload, not a compile, so §3.5 (loader-only) seems to fit. The reference
will list them, because it comes from the code. `guide-check` won't catch the gap either, because it
checks only that every `errors.md` code is in the reference. Without §3.5 rows, those two reference
rows will link to nothing that explains them.

### 6. `--output human|json`, not `text|json`

`AW-CLI-009` uses the global `--output` flag's existing values. `human` is already the CLI's
spelling.

## Architecture's answers (2026-09-30)

All six are recorded as a contract amendment in `AW-INF-023`'s body:
1. **Accepted.** AC-1, AC-2, and the two targets' scope and contract lines move to `AW-INF-028`.
   `AW-CLI-009` and `AW-INF-028` leave `AW-INF-023`'s `depends_on`. Section 7 has an interim
   hand-written page, `docs/builders/07-reference.md`, until the target writes `reference.md`.
2. **Moot here,** since AC-1 moved. `AW-INF-028` AC-5's command-tree wording is the right one.
3. **The link to `errors.md` §3 meets section 7.** The "usual fix" moves to section 9, for the codes
   a Builder meets on the tutorial's path. There's no per-code fix text to generate, and writing
   one for every code would restate the spec.
4. **The help line changes:** `…from andara-cli content reference`. It's amended in `AW-INF-028`.
5. **Both codes are now in `errors.md` §3.5.** For PM, at `AW-INF-028`'s grooming: `guide-check`
   checks both directions (errors.md ⊆ reference, and reference ⊆ errors.md). The contract review
   holds it to that.
6. **Agreed:** `human|json`.

`AW-INF-023` is `ready`, and `Blocked by` is cleared. It stays last in architecture's list, held by
its `depends_on`.

## For architecture: "self-approval" in the AC-3 record (PM, 2026-10-02, SPRINT-03 close-out)

The story's "AC-3 walk-through" record lists "publish, self-approval, activate". The walk-through
didn't self-approve. Brian published as his Builder Account (`solo7`) and approved as `operator`, so
the publisher and approver differed, and `content.operator_self_approval` never applied
(`server/content/admin.go`, `self` compares Account IDs; the transcript reads
`glade@1 approved by operator` with no `(self-approval: …)` suffix). SPRINT-03's demo record, its
close-out and the roadmap's exit criterion 4 now say so. The story is yours, so the wording is too:
"approval with his Operator Account" would match. Nothing else changes, and AC-3's pass stands.
