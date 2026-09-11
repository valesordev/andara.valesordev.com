---
id: AW-CLI-005
title: Content Language v1 — grammar, semantics, error contract, and conformance corpus
epic: EPIC-05
component: cli
type: feature
status: ready
size: M
depends_on: [AW-SRV-020, AW-SRV-021, AW-SRV-022]
blocks: [AW-CLI-006]
lane: architecture
risk: high
---

## Context

ADR-0009 decided that content is authored in a purpose-built text language compiling to the canonical
protobuf, and gave `AW-CLI-003` the grammar. That story was `L` once ADR-0010 landed: a grammar, a
type system, a compiler, a formatter, a decompiler, and five CLI commands. This story is the
**specification** half, split out so the contract exists before the compiler is written (CLAUDE.md §2),
and so the compiler (`AW-CLI-006`) can be verified against something other than itself.

The language expresses ADR-0010: Templates in single inheritance with Components, merge-on-override,
extend-never-remove, components namespaced by pack, no functions (logic lives in Go systems and Python
Behaviors, ADR-0005), and it expresses `AW-SRV-021`'s Rooms and Zones with Components and the closed
Direction set. Its output is exactly `andara.content.v1` — the language adds nothing the schema cannot
hold.

## User story

As a builder, I want to write Zones, Rooms, and Templates in a format I can read, diff, and be corrected
on by line number, so that world-building is writing rather than data entry.

## Scope

### In scope
- `docs/specs/content-language/v1/`: `grammar.ebnf`, `semantics.md`, `errors.md`, `formatting.md`,
  and `corpus/` — source files paired with expected canonical output or expected errors.
- Lexical and syntactic grammar: a file is a sequence of declarations — `pack`, `zone`, `room`,
  `template`, `exit` inside `room`, `component` blocks inside any of them.
- Semantics: name resolution across files in a pack; `extends` resolution against `andara.core` and the
  pack; field-by-field merge; list replacement; depth bound; cycle rejection; Direction validation;
  cross-Zone Exit references; what a pack must contain to compile (`fallback` per Zone).
- Error contract: every error names `file:line:col`, a stable code, and the declaration chain that led to
  it; the codes extend `AW-SRV-001`'s `ValidationError` taxonomy so the three-way equivalence
  (`AW-CLI-002` AC-4) is code-for-code.
- Canonical formatting rules, so `fmt` has one answer.
- The round-trip contract: canonical protobuf → source → protobuf is identity; source → protobuf →
  source is identity for formatted source.
- Syntax reviewed with Brian as the Builder in the room before this story is done.

### Out of scope
- The compiler, formatter, decompiler — `AW-CLI-006`. The commands — `AW-CLI-003`.
- Behavior code. A Template references a Behavior by name; Python is `AW-SRV-016`.
- Any construct the schema cannot hold. If the language needs a field, the field is added to
  `andara.content.v1` first, under `AW-SRV-020`'s rules.

## Acceptance criteria

1. **Given** `grammar.ebnf` **when** every file under `corpus/valid/` is parsed by a generic EBNF tool
   **then** all parse, and every file under `corpus/invalid/syntax/` fails at the `file:line:col` its
   sidecar names.
2. **Given** `corpus/valid/*.aw` **when** compiled by a conforming compiler **then** the output equals
   the paired `*.pb` byte for byte — canonical encoding, sorted `repeated`, no maps.
3. **Given** `corpus/invalid/semantic/*.aw` **when** compiled **then** the errors equal the paired
   `*.errors` sidecar: code, `file:line:col`, and chain, in order.
4. **Given** a Template overriding a Component field **when** compiled **then** the flattened output
   carries the merged Component and the manifest records the originating ancestor per field
   (ADR-0010 §9); the corpus has a three-deep chain proving it.
5. **Given** a Template that attempts to remove a Component or a field **when** compiled **then** error
   `E_REMOVE` names the ancestor that defined it.
6. **Given** an `extends` cycle or a chain deeper than `MAX_DEPTH` (16) **when** compiled **then**
   `E_CYCLE` / `E_DEPTH` names every Template in the chain.
7. **Given** a Component type not defined by `andara.core` or a pack dependency **when** compiled
   **then** `E_UNKNOWN_COMPONENT` says component types are server-defined and where to file a request
   (ADR-0010 §7).
8. **Given** an Exit with a Direction outside the closed set **when** compiled **then** `E_DIRECTION`
   lists the permitted twelve.
9. **Given** every `.pb` in `corpus/valid/` **when** decompiled and recompiled **then** the bytes are
   identical; **given** every formatted `.aw` **then** compile → decompile reproduces the source.
10. **Given** the spec **when** Brian reads `corpus/valid/town.aw` **then** it is accepted as something a
    Builder would write, recorded as a resolved question here.

## Interface contract

The spec is the contract; this section fixes its skeleton so `AW-CLI-006` and `AW-CLI-002` can start
from it. Illustrative only — syntax is decided with Brian in AC-10.

```
// CONTRACT SKETCH — not an implementation; illustrative surface for docs/specs/content-language/v1
pack town requires andara.core@3

zone market "The Market" {
  fallback square
  component andara.core.Outdoor {}
  room square "Market Square" {
    desc "Stalls crowd the cobbles."
    exit north -> lane
    exit east  -> docks.pier            // cross-Zone reference
    component andara.core.Dark { enabled: false }
  }
}

template Merchant extends andara.core.Npc {
  component andara.core.Dialogue { greeting: "Fine wares!" }
  component andara.core.Behavior { name: "town.merchant" }   // Python, AW-SRV-016
}
```

| Property | Value |
|----------|-------|
| file extension | `.aw` |
| encoding | UTF-8, LF, no BOM |
| identifiers | `[a-z][a-z0-9_]*`, dotted for cross-pack references |
| strings | double-quoted, `\n \" \\` escapes only |
| numbers | `int64`; decimals are `E_FLOAT` — the schema has none (ADR-0007 rule 3) |
| comments | `//` to end of line; retained by `fmt`, dropped by compile |
| output | `ZoneDefinition` (`AW-SRV-021`), `TemplateDefinition` (`AW-SRV-022`), one blob per declaration, plus the source blobs with `media_type: text/x-andara` |
| errors | `E_SYNTAX`, `E_UNRESOLVED`, `E_CYCLE`, `E_DEPTH`, `E_REMOVE`, `E_DUP_COMPONENT`, `E_UNKNOWN_COMPONENT`, `E_DIRECTION`, `E_FALLBACK`, `E_FLOAT`, `E_CORE_VERSION`, plus every `AW-SRV-001` code |

## Data / state impact

None; a specification. It constrains `andara.content.v1`: any construct without a field is a schema
change first.

## Observability requirements

None at runtime. The corpus runs in CI as `make content-conformance` (`AW-CLI-006` wires it); a spec
change that breaks a corpus pair fails the build naming the pair.

## Test plan

- The corpus *is* the test plan: ≥ 30 valid files covering every grammar production, ≥ 40 invalid files
  covering every error code, and one pack (`corpus/valid/town/`) large enough to be a real example.
- `make content-conformance` (added by `AW-CLI-006`) runs AC-1–3 and AC-9.
- Manual: Brian reads `town.aw` (AC-10).

## Definition of done

CLAUDE.md §8, plus: every error code in `errors.md` has at least one corpus file; AC-10 recorded.

## Open questions

- `[NEEDS BRIAN]` Syntax review (AC-10). The sketch above is the starting point, not the answer.
- `[ASSUMPTION]` `MAX_DEPTH` 16. Deep enough for any hierarchy a Builder would want to read.
- `[ASSUMPTION]` Source blobs are published alongside compiled output (ADR-0009), so `content fetch`
  can return exactly what the Builder wrote.
