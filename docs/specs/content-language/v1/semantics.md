---
spec: content-language
version: v1
status: draft
story: AW-CLI-005
---

# Content Language v1 — semantics

What a parse *means*. `grammar.ebnf` says what is well-formed, this says what it compiles to,
[errors.md](errors.md) says what a failure reports, and [formatting.md](formatting.md) says what
`fmt` produces. Together they are the contract `AW-CLI-006` implements and the
[corpus](corpus/README.md) enforces.

The language is a front end to `andara.content.v1` and nothing more (ADR-0009). Every construct
below names the protobuf field it produces. **A construct with no field is not a language feature,
it is a schema change first** (`AW-SRV-020`'s rules), which is why §9 exists.

---

## 1. Lexical structure and identifiers

Source is UTF-8, LF, no BOM, `.aw`. A BOM or a CR is `encoding` (errors.md) rather than a mystery
in the first token.

Identifier casing is split by what the identifier names:

| Class | Pattern | Names |
|-------|---------|-------|
| `LOWER_ID` | `[a-z][a-z0-9_]*` | pack segments, Zone ids, Room ids, Directions, senses, Component field names, Template kinds |
| `UPPER_ID` | `[A-Z][A-Za-z0-9]*` | Template names, Component type names |

This is the reconciliation `AW-SRV-022` asked for on 2026-09-18. The story's original grammar said
`[a-z][a-z0-9_]*` for everything, while the shipped seed and fixtures write `Merchant`, `Npc`,
`Entity`, `Character`, `Item` — names that are on disk under `content/core/templates/` and
`testdata/templates/`, held byte-identical by `TestCoreSeedMatchesFixture`, and reachable from a
`done` story. **The grammar admits the names rather than the seed being renamed**, because the
alternative is an architecture document editing implementation fixtures to match a sketch.

The split earns its keep beyond that. Every dotted reference in the language is lowercase segments
ending in exactly one PascalCase segment:

```
andara.core.Npc          pack `andara.core`, Template `Npc`
andara.core.Behavior     pack `andara.core`, Component type `Behavior`
town.Merchant            pack `town`, Template `Merchant`
docks.pier               Zone `docks`, Room `pier` — all lowercase, so not a Template
```

So "which part is the pack" is answered by looking, not by a rule — which is the property ADR-0010
decision 6 wanted from namespacing in the first place. `TemplateDefinition.name` says "the pack is
everything before the last dot"; casing makes that visible.

**There are no reserved words.** `entity`, `room`, `remove`, `perceives` and the rest are keywords
only in the position the grammar gives them, so a Room may be called `exit` and a Component field
`template`. A Builder should never have to learn a list of names they may not use for a room. This
is a constraint on `AW-CLI-006`'s parser — it must be contextual, which a recursive-descent parser
is by construction, and which is why the grammar check uses a dynamic lexer (`grammar.ebnf` header).

### Values

| Form | Produces | Notes |
|------|----------|-------|
| `"…"` | `ComponentField.string_value` | one line; escapes `\n`, `\"`, `\\` and no others |
| `-?(0\|[1-9][0-9]*)` | `ComponentField.int_value` | `int64`; one spelling per value — no `+1`, no `007`, no `1_000` |
| `true` / `false` | `ComponentField.bool_value` | |
| `3.5` | — | `float_literal`. `ComponentField` has no float member and may never grow one: a float in the State Hash makes the hash platform-dependent (ADR-0007 rule 3). A quantity that wants a fraction is an integer in fixed units. |

A field whose value is absent is not an empty string — the grammar has no such form, and
`FieldUnset` is rejected by the loader (`sim.FieldKind`).

### Prose

`desc` takes one or more adjacent string literals, **joined with a single space**:

```
desc "Stalls crowd the cobbles, and the smell of tar comes up"
     "from the harbour whenever the wind turns."
```

This is the one ergonomic decision the language makes for its own sake. ADR-0009 rejected YAML
partly because "indentation-sensitive multi-line strings are the single most reliable source of
'why is my text mangled'", and a MUD's content is mostly prose. Adjacency gives wrapping with no
indentation significance at all: the value is the same however the lines are broken, so `fmt` can
re-wrap freely (formatting.md §4) and `decompile` can produce one deterministic wrap. A paragraph
break is an explicit `\n`.

### Comments

`//` to end of line. **Retained by `fmt`, dropped by compile.** Comments are not in
`ZoneDefinition` or `TemplateDefinition` and the language will not add a field to hold them; what
preserves them is the source blob published beside the compiled output (ADR-0009, §6 below), which
is what `content fetch` returns. See §8 for why this does not contradict the round-trip contract.

---

## 2. Packs, files, and order

A **pack** is a directory. `Compile(dir, core, deps)` reads every `*.aw` under it, recursively, and
compiles them as one unit.

Exactly one `pack` declaration exists across the whole pack, in whichever file the Builder put it
in. None is `pack_missing`; two are `duplicate_pack`; one naming something other than the pack being
compiled is `pack_mismatch`.

```
pack town requires andara.core@1
pack andara.core                   // the core pack requires nothing; it is the root
```

`requires andara.core@N` becomes `ContentVersion.core_version` and is what turns version skew into
a legible error instead of a mystery (ADR-0010 decision 8). Compiling against a cache holding a
different version — or no cache — is `core_version_mismatch` naming both numbers.

**Order is not meaning.** Neither the order of files in the pack nor the order of declarations
within a file affects the compiled output: names resolve across the whole pack, a Template may
extend one declared later, and a Room may exit to one declared above it. What a Builder chooses is
how the source reads; `fmt` fixes the layout (formatting.md) and the canonical output is sorted
(§7). Two packs whose files differ only in declaration order compile to identical bytes, and the
corpus proves it.

Names are unique per kind across the pack, not per file: two Zones with the same id are
`duplicate_zone` naming both files, two Templates `duplicate_template`, two Rooms in one Zone
`duplicate_room`. A pack containing no `zone` declaration at all compiles to no Zones, which is
legal for a Template-only pack such as `andara.core`; it is the *server* that refuses to boot on an
empty World (`no_zones_found`, `AW-SRV-001` AC-9), not the compiler.

---

## 3. Zones, Rooms, and Exits

```
zone <id> "<name>" { … }   ->  ZoneDefinition{ id, name, rooms, components }
room <id> "<title>" { … }  ->  RoomDefinition{ id, title, description, exits, components }
desc "…"                   ->  RoomDefinition.description
exit <dir> -> <ref>        ->  ExitDefinition{ direction, to_zone, to_room }
```

`format_version` is emitted by the compiler, not authored: the Builder does not get to claim a
format version, and the compiler writes the one it emits (`1` for v1).

At most one `desc` per Room and at most one `fallback` per Zone — a second is
`duplicate_declaration`. A Room with no `desc` compiles with an empty `description`, which is legal
and which `content validate` does not warn about; an unwritten room is a Builder mid-work, the same
posture `AW-SRV-001` takes on orphans.

### Exit targets

`exit north -> lane` is a Room in this Zone: `to_zone` empty, `to_room` `lane`. Empty `to_zone`
meaning "the containing Zone" is `zone.proto`'s own convention and keeps the common case terse.

`exit east -> docks.pier` crosses a Zone boundary: `to_zone` `docks`, `to_room` `pier`. Cross-Zone
references are resolved as Zone+Room **value pairs, never pointers** — ADR-0001's seam invariant,
and what lets a Zone move to another process later without its exits being rewritten.

A target that does not resolve is `unknown_room`, or `unknown_zone` when the Zone half is the part
that is missing. Both are `AW-SRV-001` codes, raised here at compile time with a `file:line:col`,
which is the entire argument ADR-0009 made for Option C: *a compile error naming a line instead of a
load failure naming a field.*

**Cross-pack Exits do not exist.** A `to_zone` naming a Zone in another pack is `unknown_zone`. The
compiler resolves within the pack being compiled and nowhere else, which matches the loader's rule
for Templates (`AW-SRV-022` AC-3) and keeps a pack a self-contained unit of publication.

### Directions

The direction is a `LOWER_ID`, validated against the closed twelve in `docs/glossary.md`. Anything
else is `unknown_direction`, listing all twelve in the compass order `sim.Directions()` returns —
`north`, `northeast`, `east`, `southeast`, `south`, `southwest`, `west`, `northwest`, `up`, `down`,
`in`, `out`. That order is for *reading*; it is not the sort (§7).

It is deliberately not a set of grammar keywords. Closing the set in the *grammar* would report
`norht` as a parse error pointing at a token; closing it in *validation* reports it with the
permitted set, which is the difference between a Builder fixing a typo and a Builder guessing. It
is the same reason `zone.proto` keeps `direction` a string rather than an enum, and it means growing
the set is a glossary edit and a revalidation, never a language change.

Two Exits in one Room with the same direction are `duplicate_direction`. An Exit whose reverse is
absent is the warning `missing_reverse_exit`, naming both Rooms — legal, because a chute or a
trapdoor is a real thing, and never silent, because far more often it is a Builder forgetting the
way back.

### Components on Rooms and Zones

`component <pack>.<Type> { … }` inside a `room` or a `zone` produces a `ComponentValue` on that
Room or Zone (ADR-0010 decision 8, `AW-SRV-021`). At most one of each type per carrier — a second
is `duplicate_component_type`, because two of a thing has no override semantics and every answer to
"which one wins" is silent.

**Zone-level Components do not descend onto the Zone's Rooms.** A Zone carrying `Dark` does not make
its Rooms dark (`AW-SRV-021` AC-4, resolved by Brian 2026-09-14). Merging across a containment
boundary is a different rule from §5's inheritance merge and would want its own decision.

Component types are validated against the **server-defined registry** (`sim.ComponentTypes()`), not
against anything the pack declares: Builders compose Components, they do not create them (ADR-0010
decision 7). An unregistered type is `unknown_component_type`, and the message says types are
server-defined and where to file the request — otherwise a Builder re-checks their spelling forever
for a type that was never going to exist. A field the registry does not declare on that type, or one
declared with a different kind, is `invalid_component_field`.

The compiler carries **no component vocabulary of its own**. It reads the registry the loader
enforces, which is what makes the three-way equivalence of `AW-CLI-002` AC-4 possible at all: one
validator, quoted rather than copied (ADR-0004).

---

## 4. Templates

```
template <Name> kind <kind> { … }        ->  a root TemplateDefinition
template <Name> extends <Ref> { … }      ->  a subtype
```

`TemplateDefinition.name` is `<pack>.<Name>`: the pack is the declaring pack, always, so a Template
cannot be authored into a pack it does not belong to.

**Exactly one of `kind` and `extends`.** A root states its kind; a subtype inherits its parent's,
because a chain has one kind (`AW-SRV-022`: `andara.core.Item` is its own root rather than extending
`andara.core.Entity`, since an Item is not an Entity). Both, or neither, is `template_head`. The
kinds are `entity`, `item`, `behavior`, producing `ENTITY`, `ITEM`, `BEHAVIOR`; `TemplateKind` is
open by construction (ADR-0010 decision 1), so a new kind is an additive enum value and one keyword.

### `extends` resolution

A bare `Npc` resolves within the declaring pack. A dotted `andara.core.Npc` names another pack,
**which in v1 means `andara.core` and nothing else** — a reference to any other pack is
`unresolved_extends`. This is the compile-time twin of the loader's rule ("the loader resolves
ancestors within the same pack or the core pack and nowhere else", `AW-SRV-022`), and a compiler
that resolved more widely would emit content the server then refuses.

`unresolved_extends` names both the referring Template and the missing reference. Single inheritance
only: there is no syntax for a second parent, because composition already does the job multiple
inheritance would be there for (ADR-0010 decision 2).

### Chain

`TemplateDefinition.chain` is root first, self last; a root's chain is `[self]`. A chain longer than
**`MAX_DEPTH` = 16, self included** — `sim.MaxChainDepth`, exported so both sides reject at the same
depth — is `chain_too_deep`, and the finding names every Template in the chain in order. A cycle is
`extends_cycle`, likewise naming every Template in the cycle: "A extends B extends C extends A" is
the only message that makes a cycle fixable.

`resolved` is always `true` in emitted output. The server rejects `resolved: false` rather than
resolving late, because the sim carries no resolver (ADR-0010 decision 9) — so a compiler that
emitted an unflattened Template would be producing content nothing can load.

---

## 5. Components on Templates, and merge

A Template's `components` in the compiled output is the **flattened** set: every Component every
ancestor declares, merged down the chain, sorted by type, at most one of each type (ADR-0010
decisions 3 and 9).

Merge is **field by field**, nearest ancestor wins, and a subtype states only what changes:

```
template Npc kind entity {
  component andara.core.Behavior { name: "core.idle", chatty: false }
}
template Merchant extends Npc {
  component andara.core.Behavior { name: "town.merchant" }
}
// town.Merchant carries Behavior{ name: "town.merchant", chatty: false }
```

Restating every field to change one is the ergonomic failure the whole model exists to avoid
(ADR-0010 decision 4). Three consequences that have to be stated or they will be discovered:

- **Declaring a Component with an empty body is not a reset.** `component andara.core.Behavior {}`
  on a subtype merges nothing and the ancestor's fields survive unchanged. It is a no-op, and `fmt`
  leaves it alone rather than deleting it, because it is how a Builder marks "this type is mine to
  fill in later".
- **A subtype may introduce a Component the ancestors do not carry.** That is extension, and it is
  the normal case.
- **A subtype may never remove.** Not a Component, not a field. Substitutability is the point:
  anything holding a `SWORD` keeps working when handed a `FIRE_SWORD` (ADR-0010 decision 5). A
  `CURSED_SWORD` that cannot be wielded is `Wieldable{ enabled: false }`, not a Template with
  `Wieldable` deleted.

The language therefore has a `remove` form **that exists only to be rejected**:

```
remove component andara.core.Memory
remove field name from andara.core.Behavior
```

Both are `removed_by_subtype`, and the finding names the ancestor that defined the Component or
field, states the rule, and points at the off-switch alternative. A language with no removal syntax
at all would make the rule unteachable: a Builder would write *something*, get a parse error
pointing at a token, and learn nothing about why. This is ADR-0009's "error messages are a product
feature" applied to the one rule Builders are most likely to push on — ADR-0010 says so itself:
*"components need their own off switch, or Builders will ask for removal within the month."*

### List and nested fields

ADR-0010 decision 4 also fixes two rules for cases **v1 cannot express**: a list-valued field is
*replaced, not appended*, and merge is *shallow past one level of nesting*. `ComponentField` is a
`oneof` of `string`, `int64`, and `bool` — there is no list member and no nested message — so
neither rule has anything to bind to, and v1 invents no list literal or nested block to give them
one. They are recorded here so that the day `ComponentField` grows a list member, the merge rule is
already decided and the language change is additive rather than a new decision. `perceives` (§9) is
the only list in the language and it is an Exit clause, not a Component field.

### Provenance

`TemplateDefinition.provenance` carries one entry per `(component, field)` naming the **nearest
Template in the chain that set that value** — `self` for a value declared on the Template itself.
Sorted by `(component, field)`. Fields whose Component carries no fields produce no entries, which
is why a marker Component such as `andara.core.Memory` appears in `components` and nowhere in
`provenance`.

This is the answer to "why does this Template have `Aggro{threshold: 3}`", a question about a value
that may appear in none of the files the Builder wrote — which is exactly what merge makes possible
and what `AW-CLI-002`'s `inspect template` prints.

---

## 6. What a pack compiles to

`Compile` produces the blobs a `ContentVersion` manifests, sorted by path:

| Path in the pack | Content | `media_type` |
|------------------|---------|--------------|
| `<zone-id>.json` | one `ZoneDefinition` | `application/json` |
| `templates/<pack>.<Name>.json` | one `TemplateDefinition` | `application/json` |
| `src/<path>.aw` | the source, byte for byte as authored | `text/x-andara` |

One blob per declaration. The compiled paths are the loader's conventions, not new ones:
`server/content.LoadTemplatesDir` reads `<dir>/templates/*.json` and the dir loader reads
`<dir>/*.json` for Zones, which is why `templates/` is a subdirectory and Zones sit at the root.

Source blobs are published alongside the compiled output so `content fetch` returns exactly what the
Builder wrote, comments and all (ADR-0009). They are under `src/` so that they are grouped for
fetch and out of the way of a loader that globs `*.json`.

### `SourceRef`

`TemplateDefinition.source` is `{ file, line }`, where `file` is the source path prefixed with the
pack directory's own name:

```
content/core/entity.aw   compiled from content/core/   ->  source.file = "core/entity.aw"
town/npcs.aw             compiled from town/           ->  source.file = "town/npcs.aw"
```

This is not a choice; it is what is already on disk in `content/core/templates/*.json` and
`testdata/templates/`, which `AW-CLI-006`'s output must reproduce byte for byte. It is provenance a
finding quotes, **not a path the loader opens** — `content/core/README.md` says the same. Diagnostics
during compile name the path as the Builder can open it, from `--path`; `SourceRef` normalizes.

`ZoneDefinition` carries no `SourceRef` — there is no field for one, and adding one would be a
schema change under `AW-SRV-020`'s rules before it could be a language feature. The consequence for
`decompile` is in §8.

---

## 7. Canonical output

Compiled output is **canonical JSON**: protojson, with

1. **`formatVersion` first, then the remaining keys in field-number order.** Version first is how a
   versioned document is read, and it is the rule that reproduces every fixture on disk —
   `ZoneDefinition` (`formatVersion` is field 1, so the rule is vacuous) and `TemplateDefinition`
   (`formatVersion` is field 8, and every seed file puts it first) alike.
2. Two-space indent, one key or element per line, LF, a trailing LF, no trailing whitespace.
3. Fields at their default omitted; enums as their names (`"ENTITY"`).
4. Every `repeated` field sorted, **lexicographically by the value named**: `rooms` by id, `exits`
   by direction string, `components` by type, `fields` by name, `provenance` by
   `(component, field)`. `chain` is in chain order, root first, which is not a sort. No maps
   anywhere.

   Exits sort by the direction *string* — `east`, `north`, `south` — not in compass order. That is
   what the loader already does (`sim.BuildWorld`, held by `TestBuildWorld_ExitsSortedByDirection`),
   and the compiler emitting a different order than the loader produces would make "two loads of the
   same content serialize identically" (`AW-SRV-021` AC-5) a property of the loader's re-sort rather
   than of the content. Compass order is how the twelve are *listed* to a Builder (§3); it is not an
   ordering on content.

Byte-exactly: `json.dumps(obj, indent=2) + "\n"` over a document whose keys are in the order rule 1
gives. This is checkable and is checked — `make content-grammar-check` asserts it over every expected
file in the corpus, and the eleven compiled fixtures already in the repo satisfy it.

Rule 4 is not style. A Room's Component set feeds the State Hash, and an unsorted `repeated` field
or a map makes the hash depend on iteration order (ADR-0007 rule 3, `andara/log/v1/log.proto`). The
compiler sorts so that the loader never has to, and so that two compiles of the same source produce
identical bytes.

**Why JSON and not binary.** The story's AC-2 said `.pb`. Two things make that wrong and one make it
impossible. The pack blob format the loader actually reads is protojson — `server/content` calls
`protojson.Unmarshal` and the shipped packs are `.json`. Binary protobuf is not canonical by default,
so "byte for byte" would be a claim about a serializer rather than about content. And a corpus whose
expected outputs can only be produced by the compiler it is meant to check is not a check: hand-
authorable expected output is the entire point of specifying before implementing (CLAUDE.md §2).
Recorded as a correction on the story.

One note for `AW-CLI-006`: Go's `protojson` deliberately injects non-deterministic whitespace, so
emitting canonical output means marshalling and then re-serializing through a deterministic
encoder — not trusting `protojson.MarshalOptions{Indent: "  "}`.

---

## 8. The round-trip contract

Two directions, and they are not the same statement.

**Compiled → source → compiled is identity.** `decompile` of a published version produces `.aw`
source that recompiles to the same blobs, byte for byte. This is the one that matters
operationally: it is how a Builder who has lost their working copy gets it back (`AW-CLI-003`'s
`content fetch` and `AW-CLI-006`'s `content decompile`).

**Source → compiled → source is identity for canonical source only.** `decompile` output is
canonical (formatting.md) and carries no comments, so the round trip reproduces a `.aw` that is
`fmt`-clean and comment-free. Source that is formatted and comment-free round-trips to itself; source
with comments round-trips to itself minus the comments.

The story's AC-9 said "compile → decompile reproduces the source", which contradicts "comments
dropped by compile" as written. The resolution is that **comments survive publication, not
compilation**: the source blob under `src/` is byte-for-byte what the Builder wrote, so
`content fetch` returns the comments even though `decompile` cannot invent them. Recorded as a
correction on the story.

Because `ZoneDefinition` has no `SourceRef` (§6), `decompile` cannot know which file a Zone came
from. It therefore emits a **canonical file layout**, which is the layout `fmt` does not move things
into but which decompile always produces:

| Declaration | File |
|-------------|------|
| `pack` | `pack.aw`, alone |
| each Zone | `<zone-id>.aw` |
| each Template | grouped by `source.file`'s basename, or `templates.aw` when absent |

So a pack that was authored in this layout round-trips file for file, and one that was not
round-trips to the same *content* in canonical files. The corpus's round-trip cases are authored in
the canonical layout, which is what makes AC-9 a byte comparison rather than a semantic one.

---

## 9. Constructs the schema cannot yet hold

The language's own rule is that a construct with no protobuf field is a schema change first
(`AW-CLI-005` scope, `AW-SRV-020`). Three constructs are **decided, with their field numbers pinned
by a `ready` story, and not yet in `andara/content/v1`**. They are in the grammar, specified here,
and their corpus cases live under `corpus/pending/` with the story that unblocks each one named.

| Construct | Produces | Gated on | Code |
|-----------|----------|----------|------|
| `fallback <room>` | `ZoneDefinition.fallback_room` (field 6) | `AW-SRV-012` | `fallback_missing` |
| `perceives [<sense>, …]` | `ExitDefinition.perceives` (field 4) | `AW-SRV-029` | `unknown_sense` |
| `andara.core.Behavior{ name }` resolution | — (validation only) | `AW-SRV-016` | `unknown_behavior` |

They are in the grammar rather than deferred to v2 for one reason: **a language that has to grow a
keyword later is a worse outcome than a keyword that is specified and waiting.** ADR-0009's whole
compatibility mechanism is a corpus of source that must keep compiling, and the corpus cannot
protect a construct it does not contain. Specifying `fallback` now also closes a hole that would
otherwise be real: `AW-SRV-012` makes `fallback_room` *required*, so without the keyword every Zone
authored in v1 would be one the next server release refuses.

`make content-grammar-check` parses `corpus/pending/` like any other valid source.
`make content-conformance` (`AW-CLI-006`) skips it, printing the count and the gating story per case,
and each gating story's Definition of done picks up its own cases. **A pending case is not a failing
case and not a skipped test** — it is a specified construct waiting on a field, and the day the field
lands the case moves to `corpus/valid/` in the story that landed it.

### `fallback`

Names a Room in the same Zone. It is where an Entity is relocated when its Room is removed by a
content change, rather than the content change being refused (ADR-0004). One per Zone, required by
`AW-SRV-012`; naming a Room the Zone does not declare is `fallback_missing` — the one Room a Zone may
not delete.

### `perceives`

The senses that pass through an Exit **into the Room that owns it** (`AW-SRV-029`). The vocabulary is
server-defined and closed, like Component types: `sight`, `sound` to start. A sense outside the
registry is `unknown_sense`, listing the permitted ones and saying senses are server-defined. Sorted
in the compiled output, like every `repeated` field.

### Behavior names

`component andara.core.Behavior { name: "town.merchant" }` is the one seam between the Template
hierarchy and the Python class hierarchy an Agent runs (ADR-0010). `AW-SRV-022` records the name
without validating it and says `AW-CLI-006` validates it at compile; `AW-SRV-016` names the finding.
The check is that the name resolves to a Behavior the pack declares — but the pack layout for Python
is `AW-SRV-016`'s to define and does not exist yet, so v1 specifies the code and leaves the resolution
rule to that story.

---

## 10. What the language deliberately cannot say

Each of these is a decision, not an omission, and each has an ADR behind it. They are listed because
a Builder will eventually ask for every one of them, and the answer should be findable.

| Not in the language | Why | Where it lives instead |
|---------------------|-----|------------------------|
| Expressions, arithmetic, conditionals, loops | Components hold data; the sim never executes anything a Builder wrote (ADR-0005, ADR-0010) | Go systems, Python Behaviors |
| Functions, variables, macros, includes | Same. A grammar with these is a compiler with a runtime, and its behaviour becomes part of the state contract forever (ADR-0010's declined Option 2) | — |
| Defining new Component *types* | Decided against the ADR's own recommendation (ADR-0010 decision 7): every Component in the World is a type the server understands, so validation, the State Hash, projections, and the wire format have a closed vocabulary | a GitHub issue and a server release |
| Multiple inheritance | Composition already does the job it would be there for, without a diamond or a resolution order Builders must memorise (ADR-0010 decision 2) | Components |
| Removing an inherited Component or field | Substitutability (ADR-0010 decision 5) | `enabled: false` — an off switch on the Component |
| Two Components of one type on a carrier | Override semantics on a bag of duplicates have no good answer, and every bad answer is silent (ADR-0010 decision 3) | a list *inside* one Component, when `ComponentField` can hold one |
| Floats | A float in the State Hash makes the hash platform-dependent (ADR-0007 rule 3) | integers in fixed units |
| Cross-pack Exits, or `extends` across a Builder pack | A pack is the unit of publication and activation; resolving wider emits content the loader refuses (`AW-SRV-022` AC-3) | `andara.core` |
| Comments in compiled output | No field, and adding one would be a schema change to hold something no system reads | the source blob under `src/` |

---

## Acceptance criteria

These restate `AW-CLI-005`'s criteria as obligations on *this document*, checkable against the
corpus. The story's own numbering is preserved.

1. Every construct in `grammar.ebnf` has a meaning here, and every meaning here names the
   `andara.content.v1` field it produces or the §9 story that will add one.
2. The canonical output rules (§7) are stated byte-exactly and are satisfied by the eleven compiled
   fixtures already in the repo; `make content-grammar-check` asserts it over the corpus.
3. Every error this document names exists in [errors.md](errors.md) with a corpus case.
4. §5 fixes merge, and the corpus carries a three-deep chain whose expected `provenance` proves
   field-level origin.
5. §5 fixes `removed_by_subtype`, and the language has a source form that reaches it.
6. §4 fixes `extends_cycle` and `chain_too_deep` at `MAX_DEPTH` = 16, `sim.MaxChainDepth`.
7. §3 fixes `unknown_component_type` against the server registry, quoted rather than copied.
8. §3 fixes `unknown_direction` against the glossary's twelve.
9. §8 fixes both round-trip directions, and says which one comments survive.
10. `corpus/valid/town/` is readable as something a Builder would write — **open**, AC-10 is Brian's.

## Out of scope

- **The compiler, formatter, and decompiler** — `AW-CLI-006`. This document is what they conform to;
  it prescribes no parsing technique, no data structures, and no package layout.
- **The `andara-cli content` commands** — `AW-CLI-002` (`validate`, `inspect`) and `AW-CLI-003`
  (`publish`, `activate`, `rollback`, `fetch`).
- **Behavior code.** A Template names a Behavior; Python is `AW-SRV-016`.
- **The core Component and Template vocabulary.** Which properties Andara wants is game design and
  Brian's (`AW-SRV-021` and `AW-SRV-022` both carry the question). This document fixes how a
  vocabulary is *referenced*, never what is in it.
- **Growing the Direction set or the sense registry.** A glossary edit and a revalidation; no
  language change, by construction.
- **Any construct with no `andara.content.v1` field**, beyond the three in §9 whose fields are
  already decided and numbered.
