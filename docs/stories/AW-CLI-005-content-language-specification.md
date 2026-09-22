---
id: AW-CLI-005
title: Content Language v1 — grammar, semantics, error contract, and conformance corpus
epic: EPIC-05
component: cli
type: feature
status: in-progress
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
2. **Given** a case under `corpus/valid/` **when** compiled by a conforming compiler **then** the
   output equals the case's `expected/` blobs byte for byte — canonical JSON, sorted `repeated`, no
   maps. *(Corrected from `*.pb`; see Corrections.)*
3. **Given** a case under `corpus/invalid/semantic/` **when** compiled **then** the findings equal its
   `expected.errors` sidecar: code, `file:line:col`, and chain, in order.
4. **Given** a Template overriding a Component field **when** compiled **then** the flattened output
   carries the merged Component and the manifest records the originating ancestor per field
   (ADR-0010 §9); the corpus has a three-deep chain proving it.
5. **Given** a Template that attempts to remove a Component or a field **when** compiled **then**
   `removed_by_subtype` names the ancestor that defined it. The language has `remove component X` and
   `remove field f from X` **only so that this error can be reached**; a rule with no way to break it
   is a rule a Builder cannot be taught.
6. **Given** an `extends` cycle or a chain deeper than `MAX_DEPTH` (16) **when** compiled **then**
   `extends_cycle` / `chain_too_deep` names every Template in the chain.
7. **Given** a Component type the server registry does not carry **when** compiled **then**
   `unknown_component_type` says component types are server-defined and where to file a request
   (ADR-0010 §7).
8. **Given** an Exit with a Direction outside the closed set **when** compiled **then**
   `unknown_direction` lists the permitted twelve.
9. **Given** every expected blob **when** decompiled and recompiled **then** the bytes are identical;
   **given** every `.aw` under `corpus/roundtrip/` — canonical source — **then** compile → decompile
   reproduces it byte for byte. *(Made precise; comments survive publication, not compilation. See
   Corrections.)*
10. **Given** the spec **when** Brian reads `corpus/valid/town/` **then** it is accepted as something a
    Builder would write, recorded as a resolved question here. **Open.**
11. **Given** `make content-grammar-check` **when** `make check` runs **then** it passes: every corpus
    file agrees with the grammar, every expected blob is canonical bytes, every
    `TemplateDefinition.source` names a line that opens that Template, and every code in `errors.md`
    has a case. *(Added: AC-1 needed a target, and CLAUDE.md §9 makes a documented procedure without
    one a defect.)*

## Interface contract

The spec is the contract and it now exists: `docs/specs/content-language/v1/`. This section is kept
as the story's summary of it. The sketch below is **superseded by `corpus/valid/town/`**, which is
the real thing and what AC-10 puts in front of Brian; two lines of it were wrong and are worth
naming rather than quietly editing — `fallback` needs a field `AW-SRV-012` has not landed, and
`andara.core.Dark { enabled: false }` sets a field a marker Component does not declare, which is now
the corpus case `invalid/semantic/invalid-component-field-name/`.

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
| identifiers | `[a-z][a-z0-9_]*` for packs, Zones, Rooms, Directions, senses, field names; `[A-Z][A-Za-z0-9]*` for Template and Component type names. Every dotted reference is lowercase segments ending in one PascalCase segment |
| strings | double-quoted, `\n \" \\` escapes only |
| numbers | `int64`; decimals are `float_literal` — the schema has none (ADR-0007 rule 3) |
| comments | `//` to end of line; retained by `fmt`, dropped by compile |
| output | canonical JSON: `<zone-id>.json`, `templates/<pack>.<Name>.json`, one blob per declaration, plus the source blobs at `src/<path>.aw` with `media_type: text/x-andara` |
| errors | `sim.ErrCode` strings, shared with the loader rather than a parallel `E_*` set. Compiler-native: `syntax_error`, `encoding`, `invalid_escape`, `float_literal`, `pack_missing`, `duplicate_pack`, `pack_mismatch`, `core_version_mismatch`, `template_head`, `extends_cycle`, `removed_by_subtype`, `duplicate_declaration`, `duplicate_direction`. Reused from `sim`: `unknown_room`, `unknown_zone`, `duplicate_room`, `duplicate_zone`, `unknown_direction`, `unknown_component_type`, `duplicate_component_type`, `invalid_component_field`, `unresolved_extends`, `duplicate_template`, `chain_too_deep`. Warnings: `orphan_room`, `missing_reverse_exit`. Pending a field: `fallback_missing`, `unknown_sense`, `unknown_behavior`. Full table in `errors.md` §3 |


### Corrections made while writing the spec (2026-09-22)

Each of these contradicted something already shipped, so the spec follows the artifact and the story
records the change rather than the spec quietly disagreeing with its own story.

- **Error codes are `sim.ErrCode` strings, not a parallel `E_*` set.** `AW-CLI-002` AC-4 requires the
  CLI, the conformance run, and `AW-SRV-013`'s gate to produce *identical* diagnostics, and half the
  sketch's codes already exist under other names in `server/sim/errors.go` — `E_DIRECTION` is
  `unknown_direction`, `E_UNKNOWN_COMPONENT` is `unknown_component_type`, `E_DUP_COMPONENT` is
  `duplicate_component_type`, `E_DEPTH` is `chain_too_deep`. Two names per finding would make the
  equivalence check compare a mapping table rather than a value. `AW-CLI-002` AC-5, `AW-CLI-006`
  AC-5/AC-6, and `AW-SRV-016`'s `E_UNRESOLVED` carry the rename, applied in this pass.
- **Expected output is canonical JSON, not `.pb`.** The pack blob format the loader reads *is*
  protojson (`server/content` calls `protojson.Unmarshal`; every shipped pack is `.json`), binary
  protobuf is not canonical by default, and a corpus whose expected output can only be produced by
  the compiler it checks is not a check. Canonical form is pinned byte-exactly and reproduces all
  eleven compiled fixtures already in the repo: `formatVersion` first, then field-number order,
  two-space indent, LF.
- **Exits sort lexicographically, not in compass order.** Written as compass order first; the loader
  sorts by the direction string (`TestBuildWorld_ExitsSortedByDirection`, `want = [east north south]`)
  and a compiler emitting a different order would make `AW-SRV-021` AC-5's byte-identical
  serialization a property of the loader's re-sort. Compass order is for *listing* the twelve in an
  error message.
- **Identifier casing is split by what the identifier names** — the reconciliation `AW-SRV-022` asked
  for. Lowercase for packs, Zones, Rooms, Directions, senses, and field names; PascalCase for Template
  and Component type names. The seed is not renamed: it is on disk, held byte-identical by
  `TestCoreSeedMatchesFixture`, and reachable from a `done` story.
- **AC-9's round trip is made precise.** "Comments dropped by compile" and "compile → decompile
  reproduces the source" contradicted each other as written. Compiled → source → compiled is identity;
  source → compiled → source is identity for *canonical* source. Comments survive **publication**, not
  compilation — the source blob under `src/` is byte-for-byte what the Builder wrote, which is what
  `content fetch` returns.
- **`fmt` does not reorder, and `fmt`-clean is not canonical.** gofmt's posture: narrative Room order
  is how a builder holds a map in their head. Canonical order is the stronger property `decompile`
  output has, and it is what AC-9 is stated over.
- **Three constructs are specified but gated on a field** — `fallback` (`AW-SRV-012`), `perceives`
  (`AW-SRV-029`), Behavior-name resolution (`AW-SRV-016`). They are in the grammar rather than
  deferred, because a language that has to grow a keyword later is worse than one specified and
  waiting, and because ADR-0009's compatibility mechanism — a corpus that must keep compiling —
  cannot protect a construct the corpus does not contain. Their cases live in `corpus/pending/`.
- **AC-5 had no source form.** A language with no removal syntax makes ADR-0010 decision 5
  unreachable and unteachable, so `remove component X` and `remove field f from X` exist **only to be
  rejected**, with the rule and the `enabled: false` alternative in the message. Same technique as the
  `FLOAT` terminal and the permissive `template_head`.
- **`AW-SRV-021` has no `duplicate_direction`.** The compiler rejects two Exits with the same
  Direction in one Room; the loader does not, so hand-written JSON reaching the store is not refused
  for it. A small gap, not this story's to close — see Open questions.

## Data / state impact

None; a specification. It constrains `andara.content.v1`: any construct without a field is a schema
change first.

## Observability requirements

None at runtime. The corpus runs in CI as `make content-conformance` (`AW-CLI-006` wires it); a spec
change that breaks a corpus pair fails the build naming the pair.

## Test plan

- The corpus *is* the test plan. Delivered: **57 valid `.aw` files** across 24 cases (16 `valid/`,
  6 `pending/`, 2 `roundtrip/`) covering every grammar production, and **82 invalid files** across 51
  cases (19 syntax, 30 semantic, 2 encoding) covering all 29 raisable error codes at least once.
- `make content-grammar-check` — added by this story, in `make check` — runs AC-1 and AC-11 today.
- `make content-conformance` (added by `AW-CLI-006`) runs AC-2, AC-3, and AC-9, and skips
  `corpus/pending/` printing each case's gating story.
- Two cases are anchored to compiled output already in the repository rather than invented:
  `corpus/valid/core/` reproduces `content/core/templates/*.json` and `corpus/valid/town/`
  reproduces `testdata/templates/templates/town.*.json`, byte for byte, source lines included.
- Manual: Brian reads `corpus/valid/town/` (AC-10).

## Definition of done

CLAUDE.md §8, plus: every error code in `errors.md` has at least one corpus file — mechanical, in
`make content-grammar-check`, which also refuses a code the corpus uses and `errors.md` does not
declare; AC-10 recorded.

The four spec documents stay `status: draft` until AC-10 lands. **This story stays `in-progress`
until then too, deliberately**: `review` counts as a dependency met, so flipping it would put
`AW-CLI-006` in the implementation lane's `next` against a spec whose own frontmatter says `draft` —
a compiler written before the syntax is pinned, which is the failure the `AW-CLI-003` split existed
to prevent. Whether merging this branch moves the story to `review` — unblocking `AW-CLI-006` ahead
of the syntax review — or holds it is Brian's call, attached to AC-10.

## Open questions

- `[NEEDS BRIAN]` **Syntax review (AC-10)** — the one thing holding this story out of `done`. Read
  `docs/specs/content-language/v1/corpus/valid/town/`. The three choices most worth disagreeing with:
  `desc` as the keyword for a Room's prose, `->` as the exit arrow, and PascalCase Template names.
  Only the last has a cost attached — changing it means renaming the shipped `andara.core` seed and
  the fixtures `TestCoreSeedMatchesFixture` holds against it.

- **Resolved 2026-09-22 (inherited from `AW-SRV-022`, 2026-09-18):** the identifier grammar admits
  the names on disk. Casing is split by what the identifier names, so `Merchant`, `Npc`, `Entity`,
  `Character`, and `Item` are legal and the seed is not renamed. `MAX_DEPTH` is `sim.MaxChainDepth`
  = 16, and `corpus/valid/chain-max-depth/` compiles at exactly 16 while
  `corpus/invalid/semantic/chain-too-deep/` fails at 17.

- **Resolved 2026-09-22 (inherited from `AW-SRV-029`, 2026-09-19):** the keyword is `perceives`, on
  the exit clause — `exit north -> yard perceives [sound, sight]`, sorted in the compiled output,
  validated against the same server registry the loader uses, `unknown_sense` when it is not. It is
  specified and in the grammar; `ExitDefinition.perceives` does not exist yet, so its two cases wait
  in `corpus/pending/` with `AW-SRV-029` named.

- **For `AW-SRV-012`:** `fallback <room>` is the keyword, one per Zone, `fallback_missing` when it
  names a Room the Zone does not declare. `corpus/pending/fallback/` and
  `corpus/pending/fallback-missing/` move to `corpus/valid/` and `corpus/invalid/semantic/` in the
  story that lands `ZoneDefinition.fallback_room`. Worth knowing while that story is `next`: until
  the field exists, **every Zone authored in this language is one that a server requiring
  `fallback_room` would refuse**, which is why the keyword is specified now rather than in a v2.

- **For `AW-SRV-021`:** the core Component vocabulary is thinner than the language. The registry's
  only field-bearing Component is `andara.core.Behavior{name: string}`, so **ADR-0010 decision 4's
  field-by-field merge cannot be exercised across two fields by any valid content**, and neither can
  the language's `int64` and `bool` literals — both are specified and unreachable.
  `corpus/pending/merge-partial-fields/` holds the case, written against ADR-0010's own
  `Aggro{threshold, enabled}` example, and moves to `corpus/valid/` when a Component with two fields
  and an off switch exists. This is that story's `[NEEDS BRIAN]` vocabulary question arriving with a
  concrete consequence attached rather than as a preference.

- **For `AW-SRV-021` (small):** the loader has no `duplicate_direction`. Two Exits with the same
  Direction in one Room is a compile error here and silently accepted by `sim.BuildWorld`, which
  sorts and keeps both. Hand-written JSON reaching the store is not refused for it. Not this story's
  to fix — filing it here rather than editing the loader, per the lane rule.

- **For `AW-SRV-016`:** its `E_UNRESOLVED` is `unknown_behavior`, renamed in this pass. The resolution
  rule — what a Behavior name resolves *against* — is that story's, because the Python pack layout
  does not exist yet. `corpus/pending/unknown-behavior/` waits on it.

- **For `AW-CLI-006`:** `lang.Diagnostic` needs a `Severity`. Its sketch has none, and
  `orphan_room` and `missing_reverse_exit` are warnings that must not fail a compile (errors.md §1
  rule 7). Also: Go's `protojson` deliberately injects non-deterministic whitespace, so emitting
  canonical bytes means re-serializing through a deterministic encoder rather than trusting
  `protojson.MarshalOptions{Indent: "  "}`.

- **Resolved 2026-09-22:** `[ASSUMPTION] MAX_DEPTH 16` is no longer an assumption — it is
  `sim.MaxChainDepth`, exported by `AW-SRV-022` for exactly this, and the corpus pins both sides of
  the boundary.

- `[ASSUMPTION]` Source blobs are published at `src/<path>.aw` with `media_type: text/x-andara`
  (ADR-0009), so `content fetch` returns exactly what the Builder wrote, comments included. `src/`
  keeps them clear of a loader that globs `*.json` at the pack root. `AW-CLI-003` owns `fetch` and
  may move them; nothing in the language depends on the prefix.
