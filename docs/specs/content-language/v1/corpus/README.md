# Content Language v1 — conformance corpus

ADR-0009 made the language a **second schema surface** and named the consequence in the same
breath: protobuf compatibility is machine-checkable with `buf breaking`, and language compatibility
is not — *unless we build a corpus of source files that must keep compiling*. This is that corpus.
It is the equivalent of `buf breaking` for the decision, and ADR-0009 required it to exist from the
language's first release.

It is also how `AW-CLI-006` is verified against something other than itself. Every expected output
here is hand-authored; none of it was produced by a compiler.

## What runs it

| Target | Checks | Needs |
|--------|--------|-------|
| `make content-grammar-check` | AC-1, plus the corpus's own structure and the canonical form of every expected blob | `lark` — in `make check` today |
| `make content-conformance` | AC-2, AC-3, AC-9 — source against expected output and expected errors | a compiler; `AW-CLI-006` wires it up |

## Layout

```
core/ … is under valid/                 the andara.core pack, at version 1
valid/<case>/                           sources + expected/ — compiles clean
  <case>/expected/<zone-id>.json        one ZoneDefinition
  <case>/expected/templates/<name>.json one TemplateDefinition
  <case>/expected.errors                only on a warning case: it compiles and warns
pending/<case>/                         specified, waiting on a protobuf field
  <case>/PENDING                        the gating story id, first thing on the first line
roundtrip/<case>/                       canonical source; compile → decompile is byte identity
invalid/syntax/<case>.aw + .errors      a flat file that never becomes a pack
invalid/semantic/<case>/                sources + expected.errors
invalid/encoding/<case>/                wrong before a grammar is reached
```

A **case is a directory**, and the directory is a pack, because `Compile(dir, core, deps)` takes a
directory: a case that were a single file could not exercise name resolution across files, which is
where the interesting failures are. `invalid/syntax/` is the exception — those never reach a
resolver, so they are flat files.

The case directory's **name is the pack directory's name**, which is what `TemplateDefinition.source`
records as its first path segment (`merge-three-deep/t.aw`). `make content-grammar-check` asserts it,
and asserts that the line named really does open that Template.

Every case but `core` resolves against `valid/core/` as **`andara.core@1`**.

Every case is compiled **as pack `p`** and declares `pack p`, except the two anchors, which are
compiled as the packs they reproduce: `valid/core/` as `andara.core` and `valid/town/` as `town`.
That name is the "pack being compiled" of semantics.md §2, which is what makes
`invalid/semantic/pack-mismatch/` (declares `elsewhere`) a finding rather than a skip. A new case
that declares anything else fails `make content-conformance` with an unexpected `pack_mismatch`. It
fails loudly, so there is no marker file; add one if a third exception ever appears.

`roundtrip/core-parent/` extends `andara.core.Npc`, a core Template that carries a Component, so a
`decompile` that restated what the Template inherited, rather than what it wrote, fails the byte
comparison.

## The two anchors

Two cases are not invented. They reproduce compiled output that is already in the repository, which
is what stops the corpus from being a description of whatever the corpus author imagined:

| Case | Reproduces | Held by |
|------|------------|---------|
| `valid/core/` | `content/core/templates/*.json`, byte for byte | `TestCoreSeedMatchesFixture` |
| `valid/town/` | `testdata/templates/templates/town.{Merchant,Guard,Lantern}.json`, byte for byte | `AW-SRV-022`'s loader fixtures |

Their sources are written so each declaration lands on the exact line the shipped `source` field
already names — `core/entity.aw:1`, `town/npcs.aw:12`, `town/npcs.aw:30`, `town/items.aw:3`. Those
line numbers were chosen by `AW-SRV-022` before this language had a grammar; the corpus fits the
grammar to them rather than the other way round.

`valid/town/` also carries the three Zones of `testdata/content/valid/`. Its expected `*.json` are
**not** byte-identical to those files: the loader fixtures are hand-written loader *inputs* written
with one-line exits, and this corpus holds compiler *outputs* in canonical form (semantics.md §7).
Same content, different provenance.

`valid/town/town.aw` is the file AC-10 puts in front of Brian.

## Expected output

Canonical JSON, exactly as semantics.md §7 defines it: `formatVersion` first then field-number order,
two-space indent, sorted `repeated` fields, LF, trailing LF. `make content-grammar-check` asserts the
bytes.

**Source blobs are not duplicated here.** A pack publishes its `.aw` sources alongside the compiled
output (`src/<path>.aw`, ADR-0009), and those blobs are byte-for-byte the case's own inputs — holding
a second copy would be a fixture that can drift from the file beside it.

## `.errors` sidecars

```
z.aw:3:10: unknown_direction
  z
  r
```

One finding per unindented line as `file:line:col: code`, its chain indented two spaces beneath,
findings in the order a conforming compiler emits them (errors.md §1 rule 5). Paths are relative to
the case directory. `#` starts a comment.

**No message text**, deliberately: AC-3 compares code, position, and chain, and pinning wording would
make every improvement to an error message a corpus change. errors.md §3 says what each message must
convey; review holds it.

Positions here were computed from the source token rather than counted by hand, because errors.md
rule 1 says the position names the offending token.

## Pending cases

A pending case is a **specified construct waiting on a protobuf field** (semantics.md §9) — not a
failing case and not a skipped test. `make content-grammar-check` parses them like any other source;
`make content-conformance` skips them, printing the count and each case's gating story. The day the
field lands, the story that landed it moves the case into `valid/`.

| Case | Waiting on |
|------|------------|
| `fallback`, `fallback-missing` | `AW-SRV-012` — `ZoneDefinition.fallback_room` |
| `perceives`, `unknown-sense` | `AW-SRV-029` — `ExitDefinition.perceives` |
| `unknown-behavior` | `AW-SRV-016` — the Python pack layout a Behavior name resolves against |
| `merge-partial-fields` | `AW-SRV-021` — a core Component with more than one field |

The last one is worth reading. The registry's only field-bearing Component is
`andara.core.Behavior{name: string}`, so **ADR-0010 decision 4's field-by-field merge cannot be
exercised across two fields by any valid content today**, and neither can the language's `int64` and
`bool` literals. That is not a gap in the language; it is ADR-0010's own predicted consequence —
*"component design becomes the mechanics-ceiling decision, made continuously"* — arriving early, and
it belongs to the core vocabulary question both `AW-SRV-021` and `AW-SRV-022` carry for Brian.

## Coverage

| | Cases | `.aw` files |
|---|------:|------------:|
| `valid/` | 16 | 40 |
| `pending/` | 6 | 12 |
| `roundtrip/` | 2 | 5 |
| `invalid/syntax/` | 19 | 19 |
| `invalid/semantic/` | 30 | 60 |
| `invalid/encoding/` | 2 | 3 |

Every one of the 29 codes in errors.md §3 appears at least once. Every production in `grammar.ebnf`
is exercised by a valid case.

## Adding a case

1. Make the directory. Give it a name that says what it covers, not what number it is.
2. Write the sources. Keep a case to one idea — the anchors and `town` are the only large ones on
   purpose, because a large case that fails tells you less than a small one that fails.
3. Write `expected/` or `expected.errors` **by hand**. Never from a compiler's output: a corpus
   whose expectations come from the thing it checks is a description, not a check.
4. Run `make content-grammar-check`.
5. If the case needs a field that does not exist, it belongs in `pending/` with a `PENDING` marker
   naming the story — not in `valid/`, and not left out.
