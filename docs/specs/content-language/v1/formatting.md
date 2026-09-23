---
spec: content-language
version: v1
status: ready
story: AW-CLI-005
---

# Content Language v1 — canonical formatting

What `andara-cli content fmt` produces, so that there is one answer and layout stops being a thing
anyone argues about. Normative with [semantics.md](semantics.md).

Two properties hold, and `AW-CLI-006` AC-3 asserts both: **`fmt` is idempotent** — a second run is a
no-op — and **`fmt` never changes meaning**. Formatting is the only thing it touches.

---

## 1. `fmt` does not reorder

This is the load-bearing decision in this document, so it is first.

`fmt` reorders nothing. Not Rooms within a Zone, not Exits within a Room, not declarations within a
file, not Components within a Template. A Builder who lays out a Zone as the walk through it — plaza,
then the lane north of it, then the docks it opens onto — keeps that layout, and `fmt` fixes the
whitespace around it.

The alternative was tempting because this document exists to give `fmt` one answer, and sorting is
one answer. It is the wrong one: a Zone file is read far more often than it is diffed, narrative
order is how a builder holds a map in their head, and gofmt has never reordered a Go file either. The
canonical *output* is sorted regardless (semantics.md §7) — the compiler sorts every `repeated` field
on the way out, so determinism and the State Hash never depended on the source order.

**Canonical order is a separate, stronger property** that only `decompile` output has. See §6.

---

## 2. Whitespace

| | |
|---|---|
| Indent | two spaces per nesting level; never a tab |
| Line ending | LF; no CR anywhere |
| End of file | exactly one LF; no blank line before it |
| Trailing whitespace | none, on any line, including inside a comment |
| Encoding | UTF-8, no BOM |

A block whose body is empty is `{}` on the opening line, with one space before it and none inside:

```
component andara.core.Dark {}
template Entity kind entity {}
```

A block with a body puts `{` at the end of the opening line and `}` alone on a line at the opening
line's indent:

```
room square "Market Square" {
  desc "Stalls crowd the cobbles."
  exit north -> lane
}
```

---

## 3. Spacing within a line

Exactly one space between tokens, and **no column alignment anywhere**:

```
exit north -> lane
exit east -> docks.pier
component andara.core.Behavior { name: "town.merchant" }
```

not

```
exit north -> lane
exit east  -> docks.pier            // aligned
```

Alignment is rejected because it makes one edit touch every line around it: adding
`exit northeast -> copse` to an aligned block rewrites the whole block, and a Builder's real diff
disappears into whitespace. The story's own contract sketch was written aligned; this supersedes it,
and it is one of the things to say out loud in the syntax review.

A single-field Component fits on one line if it fits in the line budget; otherwise one field per
line:

```
component andara.core.Behavior { name: "town.merchant" }

component andara.core.Behavior {
  name: "town.merchant"
  chatty: false
}
```

`fmt` does not collapse a multi-line Component that fits, and does not expand a one-liner that fits.
Both forms are canonical, and which one a Builder wrote is information — a Component written open is
usually one they are still working on. `decompile` always emits the one-liner when it fits (§6).

Field assignment is `name: value` — no space before the colon, one after. There are no commas
between fields; `perceives` is the only comma-separated list, written `[sight, sound]` with no space
inside the brackets and one after each comma.

---

## 4. Prose

`desc` wraps at **100 columns**, breaking between string literals, with continuation literals
aligned under the first:

```
  desc "Stalls crowd the cobbles, and the smell of tar comes up from the harbour"
       "whenever the wind turns. Nobody here will meet your eye for long."
```

Adjacent literals are joined with a single space (semantics.md §1), so the break points carry no
meaning and `fmt` may move them freely. This is the one place `fmt` rewrites a value's *layout*
without touching the value — and the reason the language spells prose this way rather than with a
multi-line literal, which could not be re-wrapped without changing what it says.

`fmt` breaks only at an existing space in the joined text. It never breaks inside a word, never
breaks a literal that has no space in it, and never merges a literal that a Builder split at a
sentence boundary if the result would exceed the budget.

100 columns, not 80, because room descriptions are prose and 80 puts four words on a line.

---

## 5. Blank lines and comments

- Exactly one blank line between top-level declarations; none before the first or after the last.
- Inside a block, a run of blank lines collapses to one. A blank line immediately after `{` or
  immediately before `}` is removed.
- Comments are **preserved**. A comment on its own line is re-indented to the line it precedes. A
  trailing comment keeps its line, separated from the code by two spaces:

```
zone market "The Market" {
  // The square is the spawn Room for the dev content.
  room square "Market Square" {
    exit east -> docks.pier  // cross-Zone
  }
}
```

`fmt` never moves a comment across a declaration, never joins two comments, and never re-wraps one.
A comment is the one thing in the file the formatter has no way to understand, so it does the minimum
that keeps indentation honest.

Comments are dropped by *compile*, not by `fmt` — what preserves them through publication is the
source blob (semantics.md §6, §8).

---

## 6. Canonical source, and what `decompile` emits

`fmt`-clean is not the same as canonical. **Canonical source** is `fmt`-clean, comment-free, and in
canonical order, and it is what `decompile` produces and what the round-trip contract is stated over
(semantics.md §8).

Canonical order:

| Level | Order |
|-------|-------|
| Files | `pack.aw`, then one file per Zone named `<zone-id>.aw`, then Templates grouped by `source.file`'s basename or `templates.aw` |
| Within a Zone | `fallback`, then Components sorted by type, then Rooms sorted by id |
| Within a Room | `desc`, then Exits sorted by direction string, then Components sorted by type |
| Within a Template | Components sorted by type |

Exits sort **lexicographically by direction string** — `east`, `north`, `south` — because that is
the order they are already in on the way out (semantics.md §7) and the order the loader produces
(`TestBuildWorld_ExitsSortedByDirection`). Compass order is for listing the twelve in an error
message, not for ordering content; canonical source and canonical output agree, which is what makes
the round trip a byte comparison.

`decompile` additionally emits the one-line form of any Component that fits in 100 columns, since it
has no record of how the Builder wrote it.

A pack authored in canonical order round-trips file for file. A pack that is merely `fmt`-clean
round-trips to the same *content* in canonical files, which is why the corpus's round-trip cases are
authored canonically — it makes AC-9 a byte comparison rather than a semantic one.

---

## Acceptance criteria

1. `fmt` is idempotent on every file in the corpus: a second run changes nothing (`AW-CLI-006` AC-3).
2. Every `.aw` under `corpus/valid/`, `corpus/pending/`, and `corpus/invalid/semantic/` is
   `fmt`-clean — `content fmt --check` exits `0` over the corpus.
3. Every `.aw` under `corpus/roundtrip/` is canonical per §6, and `compile` then `decompile`
   reproduces it byte for byte (`AW-CLI-005` AC-9).
4. `fmt` does not reorder: a corpus case whose Rooms are in non-alphabetical order stays that way,
   and its expected output is sorted regardless.
5. No corpus `.aw` has a tab, a CR, a trailing space, or a missing final LF.

## Out of scope

- **The formatter** — `AW-CLI-006`. This says what it produces, not how.
- **Editor support.** Syntax highlighting, an LSP, an on-save hook: none of them exist and none of
  them is this story's.
- **A line-length limit on anything but prose.** A long Component field value is not re-wrapped;
  there is nowhere to break it.
- **`--strict` or a lint pass.** Warnings are `AW-CLI-002`'s and `AW-CLI-006`'s; `fmt` reformats and
  reports nothing.
