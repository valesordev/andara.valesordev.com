---
id: AW-CLI-010
title: The compiler reports unportable names offline
epic: EPIC-05
component: cli
type: feature
status: draft
size: S
depends_on: [AW-SRV-013, AW-CLI-002]
blocks: []
lane: implementation
risk: low
---

## Context

Since #322 (#267's fix), the publish gate refuses any blob path with an element that Windows treats
as a device name: `CON`, `PRN`, `AUX`, `NUL`, `CONIN$`, `CONOUT$`, `COM1`–`COM9` and
`LPT1`–`LPT9`. The superscript digits `¹ ² ³` count as digits too. The match ignores case, trailing
spaces, and anything after the first `.`. Windows is a release target for `andara-cli`, and
`content fetch` writes blobs to disk by these paths, so the gate is right to refuse them. But the
language allows such names, so a Builder only finds out at publish. Offline `content validate` and
the Content Repository's `check` can't see it.

The compiler builds blob paths from three sources:
- a Zone ID, giving `<zone>.json`;
- a Template name, giving `templates/<pack>.<Name>.json`, where the element before the first `.`
  is the pack name, so a pack named `aux` trips every one of its Templates;
- source paths, giving `src/<path>`.

This story makes the compiler report the same rule offline, with a position, using the gate's
predicate rather than a copy of it. A source file name containing `:` (legal on Linux and macOS,
refused by the gate) is covered too, by the same predicate (architecture, 2026-10-01). Architecture asked for it after #322. Until it ships, the
Builder's Guide section 9 documents the gap (#323). It isn't on the demo path.

## User story

As a builder, I want `content validate` to tell me a name won't publish because Windows reserves it,
so that I find out at my desk rather than at publish.

## Scope

### In scope
- One shared predicate for "some supported platform can't write this blob path element" (a Windows
  device name, a colon, or a NUL), used by both the publish gate (`server/content.UnsafeBlobPath`)
  and the compiler. Placement is implementation's
  choice, but it must be one function, not two copies.
- A compile error, `unportable_name`, for each declaration or file whose blob path would contain an
  unportable element, in three cases:
  - a Zone ID;
  - a pack name, when the pack declares a Template;
  - a source file or directory name under `--path`.
- The code `unportable_name`, whose `errors.md` §3.1 row architecture adds at contract review.

### Out of scope
- Changing what the gate refuses. The gate's rule from #322 is the contract, and this story only
  moves it earlier.
- The gate's absolute-path and `..` checks. Zone IDs and pack names can't produce them, because
  the identifier grammar is `[a-z][a-z0-9_]*`, and source paths are relative to `--path` by
  construction. (`unsafe_source_path` is `AW-CLI-003`'s guard on blobs it fetches, not a compile
  check.)

## Acceptance criteria

1. **Given** a Zone declared `zone aux { … }` **when** `content validate --path` runs **then** it
   exits `1`, with one diagnostic at the Zone ID's position, naming `aux` and the blob path
   `aux.json`.
2. **Given** a pack declared `pack con` that declares a Template **then** the compile fails with one
   diagnostic at the pack name's position. That's one per pack, not one per Template.
3. **Given** a source file `src/zones/nul.aw` or a directory `src/lpt1/` **then** the compile fails
   with one diagnostic per offending file, at `file:1:1`, naming the path element.
4. **Given** names that merely contain a device name (`console`, `auxiliary`, `com10`, `lpt0`)
   **then** there's no diagnostic.
5. **Given** a case or extension variant (`Aux`, `COM¹`, `con.aw`, `nul.v2`) **then** it's reported,
   as the gate would refuse it.
6. **Given** a source file `src/zones/a:b.aw` **then** the compile fails with `unportable_name` at
   `file:1:1`, naming the element and the colon rule.
7. **Given** a table of names, covering every form the gate refuses for an element (each device name and
   variant, a colon, a NUL) **then** a test holds the compiler's verdict equal to `UnsafeBlobPath`'s
   for the same blob path, for every name. That proves one rule.
8. **Given** the conformance corpus **then** it has a case for each of ACs 1–3 and 6, with a `.errors`
   sidecar, and `make content-conformance` passes.

## Interface contract

- **Diagnostic code: `unportable_name`** (architecture, 2026-10-01), in `errors.md` §3.1, raised
  only by the compiler. Its row is architecture's:
  - **trigger:** a Zone ID, a pack name with Templates, or a source path whose blob path some
    supported platform can't write. That's a Windows device name in any case with any extension, a
    colon, or a NUL.
  - **severity:** `error`. It refuses the compile, because the gate would refuse the publish.
- **Message:** names the offending name and the platform rule it breaks. For example: `aux is a
  Windows device name, so aux.json can't be published; rename it`, or `a:b.aw contains ':', which
  Windows can't write; rename it`.
- **Positions:**
  - a Zone ID: the `zone` declaration's ID (AC-1);
  - a pack name: the `pack` line, once per pack (AC-2);
  - a source path: the file, at line 1, column 1 (AC-3, AC-6).
- **Chain:** the existing carrier table. The Zone for a Zone ID, and empty for a pack name or a file.
- **Shared predicate:** something like `UnportableElement(element string) bool`, covering #322's
  `windowsDevice` plus `:` and NUL. Both the gate and the compiler call it.

## Data / state impact

None. Nothing that publishes today changes. A pack that would already be refused at publish is now
refused at compile.

## Observability requirements

None. It's a compile-time diagnostic in a local CLI. `content validate`'s existing `cli.command` span
and diagnostic output carry it (`AW-CLI-002`). There's nothing new to measure.

## Test plan

- **Unit:** ACs 1–5, plus the shared-verdict table (AC-6).
- **Integration:** the corpus cases (AC-7). `AW-CLI-002`'s three-way equivalence still holds, since
  the gate refuses the same names, and its fixture covers one.
- **Manual/operator:**
  ```
  andara-cli content validate --path ./mypack   # with zone aux: expect exit 1, the diagnostic at the Zone ID
  ```

## Definition of done

CLAUDE.md §8, plus: the Builder's Guide section 9's interim note (#323) can be replaced by a link to
the new `errors.md` row. That edit is architecture's, in `docs/builders/`.

## Open questions

1. **Resolved 2026-10-01 (architecture):** the code is `unportable_name`, with the row, positions and
   chain in the Interface contract. Architecture adds the `errors.md` row at contract review.
2. `[ASSUMPTION]` One diagnostic per pack for AC-2, rather than one per Template, because the pack
   name is the one thing to rename.
3. **Resolved 2026-10-01 (architecture): a colon in a source file name** is folded in, under the same
   code (AC-6).
