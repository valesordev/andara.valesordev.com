---
spec: content-language
version: v1
status: ready
story: AW-CLI-005
---

# Content Language v1

The language Builders author Zones, Rooms, and Templates in, and which compiles to canonical
`andara.content.v1` protobuf. ADR-0009 decided it exists and why; ADR-0010 decided the type system it
has to express; this directory is the specification, and `AW-CLI-006` is the compiler written to it.

**Status: `ready`.** AC-10 landed on 2026-09-23: Brian read
[`corpus/valid/town/`](corpus/valid/town/) as the Builder in the room and accepted the syntax as
written — `desc`, `->`, and PascalCase Template names stand. What remains open is listed under
[Open](#open) below; none of it is syntax.

```
pack town requires andara.core@1

zone town "Town" {
  room plaza "Market Plaza" {
    desc "A dusty square of packed earth."
    exit north -> hall
    exit east -> wilds.trail
    exit south -> docks.pier
  }
}

template Merchant extends andara.core.Npc {
  component andara.core.Behavior { name: "town.merchant" }
}
```

## The documents

| File | What it fixes |
|------|---------------|
| [`grammar.ebnf`](grammar.ebnf) | what is well-formed — normative, and machine-checked |
| [`semantics.md`](semantics.md) | what a parse means, and which protobuf field it produces |
| [`errors.md`](errors.md) | what a failure says: position, code, chain |
| [`formatting.md`](formatting.md) | what `fmt` produces, so there is one answer |
| [`corpus/`](corpus/README.md) | source paired with expected output or expected errors |

Read `semantics.md` first; the others are referenced from it.

## What checks it

`make content-grammar-check` — part of `make check` — builds the grammar with `lark` and parses every
corpus file, asserts that every syntax case fails at the position its sidecar names, that every
expected blob is canonical bytes, and that every `TemplateDefinition.source` names a line that really
opens that Template. It is the only acceptance criterion checkable before the compiler exists, which
is what "the contract before the code" (CLAUDE.md §2) buys.

`make content-conformance` — `AW-CLI-006` adds it — runs the corpus against a real compiler for the
output, the errors, and the round trip.

## The four decisions worth knowing before reading

1. **Identifier casing is split by what the identifier names.** Lowercase for packs, Zones, Rooms,
   Directions, and field names; PascalCase for Template and Component type names. Every dotted
   reference is lowercase segments ending in one PascalCase segment, so "which part is the pack" is
   answered by looking. This reconciles the story's original `[a-z][a-z0-9_]*` with the `Merchant`,
   `Npc`, `Entity` already on disk (semantics.md §1).
2. **Error codes are `sim.ErrCode` strings, shared with the loader.** Not a parallel `E_*` set: the
   three-way equivalence `AW-CLI-002` requires compares values, not a mapping table (errors.md §2).
3. **Expected output is canonical JSON, not binary protobuf.** It is the blob format the loader
   actually reads, and it is the only form a corpus can hand-author without the compiler it exists to
   check (semantics.md §7).
4. **Three constructs are specified and waiting on a protobuf field** — `fallback`, `perceives`, and
   Behavior-name resolution. They are in the grammar rather than deferred to v2, because a language
   that has to grow a keyword later is worse than one that is specified and waiting, and because the
   corpus cannot protect a construct it does not contain (semantics.md §9).

## Open

- ~~**AC-10, the syntax review.**~~ **Resolved 2026-09-23 (Brian): accepted as written.** `desc` for
  a Room's prose, `->` as the exit arrow, and PascalCase Template names all stand, so the shipped
  `andara.core` seed is not renamed.
- **The core Component vocabulary.** Not this specification's, and not changed by it — but its
  thinness is now visible here, because the corpus cannot exercise field-by-field merge or the
  language's `int64` and `bool` literals with a registry whose only field-bearing Component has one
  string field. See [`corpus/pending/merge-partial-fields/`](corpus/pending/merge-partial-fields/).
