---
id: AW-CLI-010
title: The compiler reports Windows device names offline
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
predicate rather than a copy of it. Architecture asked for it after #322. Until it ships, the
Builder's Guide section 9 documents the gap (#323). It isn't on the demo path.

## User story

As a builder, I want `content validate` to tell me a name won't publish because Windows reserves it,
so that I find out at my desk rather than at publish.

## Scope

### In scope
- One shared predicate for "this blob path element is a Windows device name", used by both the
  publish gate (`server/content.UnsafeBlobPath`) and the compiler. Placement is implementation's
  choice, but it must be one function, not two copies.
- A compile error for each declaration or file whose blob path would contain a device name, in
  three cases:
  - a Zone ID;
  - a pack name, when the pack declares a Template;
  - a source file or directory name under `--path`.
- The diagnostic code that architecture adds to `errors.md` §3.1 at contract review.

### Out of scope
- Changing what the gate refuses. The gate's rule from #322 is the contract, and this story only
  moves it earlier.
- Other path checks the gate makes (absolute, `..`, colon, NUL). Zone IDs and pack names can't
  produce them, because the identifier grammar is `[a-z][a-z0-9_]*`. Source paths are relative to
  `--path` by construction. A source **file name containing `:`** (legal on Linux and macOS) would
  still pass offline and be refused at publish. That's the same gap as this story's, and it's Open
  question 3. (`unsafe_source_path` is `AW-CLI-003`'s guard on blobs it fetches, not a compile
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
6. **Given** a table of names **then** a test holds the compiler's verdict equal to
   `UnsafeBlobPath`'s for the same blob path, for every name. That proves one rule.
7. **Given** the conformance corpus **then** it has a case for each of ACs 1–3, with a `.errors`
   sidecar, and `make content-conformance` passes.

## Interface contract

- **Diagnostic code:** architecture names it and adds it to `errors.md` §3.1 (raised only by the
  compiler) at contract review. PM's placeholder is `reserved_name`. Its severity is `error`, and it
  refuses the compile, because the gate would refuse the publish.
- **Message:** `<name> is a name Windows reserves for a device, so <blob path> can't be published;
  rename it`.
- **Position:** the Zone ID token (AC-1), the pack name token (AC-2), or `file:1:1` (AC-3).
- **Shared predicate:** something like `ReservedDeviceName(element string) bool`, with the exact
  semantics of #322's `windowsDevice`. Both the gate and the compiler call it.

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

1. **For architecture: the code's name and its `errors.md` row** (§3.1). `reserved_name` is a
   placeholder.
2. `[ASSUMPTION]` One diagnostic per pack for AC-2, rather than one per Template, because the pack
   name is the one thing to rename.
3. **For architecture: a colon in a source file name.** The gate refuses it, and the compiler
   doesn't report it. Fold it into this story's code, or rule it out of scope.
