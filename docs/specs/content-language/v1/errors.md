---
spec: content-language
version: v1
status: draft
story: AW-CLI-005
---

# Content Language v1 — error contract

ADR-0009 chose a purpose-built language over YAML on one argument: *a Builder gets a compile error
naming a line instead of a load failure naming a field.* It then named the bill — **"error messages
are a product feature"** — and observed that a compiler with bad errors is worse than YAML with bad
errors, because at least YAML's failures are familiar. This document is that feature's specification.

Normative with [semantics.md](semantics.md). [grammar.ebnf](grammar.ebnf) is what is well-formed;
this is what a failure says.

---

## 1. Shape

Every finding carries a position, a stable code, a message, and the declaration chain that led to it.

```go
// CONTRACT SKETCH — not an implementation; AW-CLI-006 owns the package
type Severity int   // SeverityError, SeverityWarning

type Diagnostic struct {
    File     string     // as the Builder can open it: --path joined with the path inside the pack
    Line     int        // 1-based
    Col      int        // 1-based, in runes, not bytes
    Code     string     // the table in §3; the same string sim.ErrCode uses
    Message  string     // one sentence, no period, names the offending value
    Chain    []string   // the declaration chain, outermost first; empty when not chain-scoped
    Severity Severity
}
```

Rendered, human (`AW-CLI-006` AC-2, `AW-CLI-002` AC-1):

```
town/npcs.aw:14:3: removed_by_subtype cannot remove Component "andara.core.Memory"
  andara.core.Entity
  andara.core.Npc
  town.Merchant
```

Rendered, JSON (`--output json`): `AW-CLI-001`'s error envelope with `diagnostics: []`, each object
carrying the fields above, `severity` as `"error"` or `"warning"`.

### Rules

1. **Position is always real.** `line` and `col` name the token the Builder must look at — the
   offending value, not the declaration that contains it. A finding with `line: 0` is a defect.
2. **Column counts runes**, so a `desc` with an em dash in it does not shift every column after it.
3. **End of input is the position one past the final character.** A file that ends mid-declaration
   reports at end-of-file rather than at the opening brace; `scripts/content_grammar_check.py`
   computes it the same way and the syntax sidecars say so.
4. **Every finding is printed, not just the first.** A Builder fixing ten broken exits should need
   one compile, not ten — the rule `AW-SRV-001` already set for boot. Parsing is the exception: a
   syntax error stops the parse of that file, so a file produces at most one `syntax_error`. Other
   files in the pack are still parsed and still report.
5. **Findings are sorted** by file, then line, then column, then code. Two compiles of the same
   source produce the same diagnostics in the same order, which is what makes the `.errors` sidecars
   a byte comparison and the three-way equivalence (`AW-CLI-002` AC-4) checkable.
6. **The message never blames the Builder for a rule they cannot see.** Where a vocabulary is closed
   and server-defined — Component types, Directions, senses — the message states that and either
   lists the permitted set or says where to file the request. This is why `unknown_component_type`
   is three clauses long: without them a Builder re-checks their spelling forever for a type that
   was never going to exist (ADR-0010 decision 7).
7. **Warnings do not fail the compile.** Exit `0` with warnings on stderr; `--strict` is
   `AW-CLI-006`'s to add if it is ever wanted.

### Chain

`Chain` is the declaration path to the finding, outermost first:

- a Template finding — the inheritance chain, root first, ending at the Template that triggered it
- a Room or Exit finding — `["<zone-id>", "<room-id>"]`, and the Exit's direction where the finding
  is on an Exit
- a pack-level finding — empty

The chain is what makes a merge error fixable. A value that appears in none of the files a Builder
wrote is exactly what `extends` plus field-level merge produces (ADR-0010 decision 9), so
"which ancestor did this come from" has to be in the finding rather than in a follow-up command.

---

## 2. Codes are shared with the loader, not parallel to it

`AW-CLI-002` AC-4 requires that the CLI, `make content-conformance`, and `AW-SRV-013`'s publish gate
produce **identical diagnostics — code, position, and chain**. That is only possible if the compiler
and the loader speak one taxonomy, so:

**The codes are `sim.ErrCode` strings, in the shipped `snake_case`, and the compiler adds to that
set rather than running a second one beside it.**

The story's interface sketch listed `E_SYNTAX`, `E_UNRESOLVED`, `E_CYCLE`, `E_DEPTH`, `E_REMOVE`,
`E_DUP_COMPONENT`, `E_UNKNOWN_COMPONENT`, `E_DIRECTION`, `E_FALLBACK`, `E_FLOAT`, `E_CORE_VERSION`
"plus every `AW-SRV-001` code". Half of those already exist under other names — `E_DIRECTION` is
`unknown_direction`, `E_UNKNOWN_COMPONENT` is `unknown_component_type`, `E_DUP_COMPONENT` is
`duplicate_component_type`, `E_DEPTH` is `chain_too_deep` — shipped in `server/sim/errors.go` and
reachable from three `done` stories. A parallel `E_*` set would mean every finding has two names and
the equivalence check compares a mapping table rather than a value. Recorded as a correction on the
story; `AW-CLI-002`, `AW-CLI-006`, and `AW-SRV-016` carry the same rename.

Three groups follow from that.

---

## 3. The codes

### 3.1 Raised only by the compiler

New in this spec. Each is a failure that exists in source and not in compiled output, so the loader
has no occasion to raise it.

| Code | Raised when | The message says |
|------|-------------|------------------|
| `syntax_error` | the parse fails | what was expected at that position, and what was found |
| `encoding` | a BOM, a CR, or invalid UTF-8 | which, and that source is UTF-8 with LF |
| `invalid_escape` | a string escape outside `\n`, `\"`, `\\` | the escape, and the three that exist |
| `float_literal` | a decimal where `int64` is required | that the schema has no float, and to use an integer in fixed units (ADR-0007 rule 3) |
| `pack_missing` | no `pack` declaration in the pack | that one file must declare the pack |
| `duplicate_pack` | more than one `pack` declaration | both files |
| `pack_mismatch` | the declared pack is not the pack being compiled | both names |
| `core_version_mismatch` | `requires andara.core@N` against a cache of another version, or none | both versions, and `content fetch-core` |
| `template_head` | a Template declares both `kind` and `extends`, or neither | that a root states its kind and a subtype inherits it |
| `extends_cycle` | `extends` forms a cycle | every Template in the cycle, in order |
| `removed_by_subtype` | a `remove` form | the ancestor that defined it, the substitutability rule, and `enabled: false` |
| `duplicate_declaration` | a second `desc` in a Room or a second `fallback` in a Zone | both positions |
| `duplicate_direction` | two Exits in one Room with the same Direction | the Direction and both targets |

### 3.2 Raised by the compiler, defined by the loader

`sim.ErrCode` values the compiler raises at compile time so the Builder hears about them with a
`file:line:col` instead of at boot. The loader still raises every one of them: the compiler is not a
gate the server trusts, and hand-written content reaching the store still has to be refused.

| Code | Owner | Raised when |
|------|-------|-------------|
| `unknown_room` | `AW-SRV-001` | an Exit target names no Room in the target Zone |
| `unknown_zone` | `AW-SRV-001` | an Exit target names no Zone in the pack, or names another pack |
| `duplicate_room` | `AW-SRV-001` | two Rooms in one Zone share an id |
| `duplicate_zone` | `AW-SRV-001` | two Zones in one pack share an id |
| `unknown_direction` | `AW-SRV-021` | a Direction outside the closed twelve — the message lists all twelve |
| `unknown_component_type` | `AW-SRV-021` | a Component type the server registry does not carry — the message says types are server-defined and where to file the request |
| `duplicate_component_type` | `AW-SRV-021` | two Components of one type on one Room, Zone, or Template |
| `invalid_component_field` | `AW-SRV-021` | a field the registry does not declare on that type, or one declared with a different kind |
| `unresolved_extends` | `AW-SRV-022` | `extends` names a Template absent from the pack and from `andara.core` |
| `duplicate_template` | `AW-SRV-022` | two Templates in one pack share a name |
| `chain_too_deep` | `AW-SRV-022` | a chain over `sim.MaxChainDepth` (16) — the message names every Template in it |

### 3.3 Warnings

Exit `0`. Both are legal content that is far more often a mistake than an intention, and the posture
is `AW-SRV-001`'s: legal, never silent.

| Code | Owner | Emitted when |
|------|-------|--------------|
| `orphan_room` | `AW-SRV-001` | a Room no Exit in its Zone reaches. A Builder mid-work |
| `missing_reverse_exit` | `AW-SRV-021` | an Exit whose reverse is absent — names both Rooms. A chute is real; forgetting the way back is more common |

`AW-CLI-002` adds a third at validate time — the *inert Component* warning ADR-0010 asks for, on a
Component no system and no bound Behavior reads. It is that story's because it needs the system
registry, which the compiler does not have.

### 3.4 Pending a field

Specified, in the grammar, and not yet reachable because the protobuf field does not exist
(semantics.md §9). Their corpus cases are under `corpus/pending/`.

| Code | Gated on | Raised when |
|------|----------|-------------|
| `fallback_missing` | `AW-SRV-012` | `fallback` names a Room the Zone does not declare, or a Zone declares none |
| `unknown_sense` | `AW-SRV-029` | a sense outside the server registry — the message lists the permitted ones and says senses are server-defined |
| `unknown_behavior` | `AW-SRV-016` | `andara.core.Behavior{ name }` names no Behavior the pack declares |

`AW-SRV-016` names this finding `E_UNRESOLVED`; it is `unknown_behavior` under §2's rule, and that
story carries the rename.

### 3.5 Loader-only — never raised by the compiler

Listed so that the taxonomy is complete and so that nobody wires a compiler case for a code that
cannot have one.

| Code | Why the compiler cannot raise it |
|------|--------------------------------|
| `malformed_file` | the loader's JSON parse failure. The compiler's equivalent is `syntax_error`, on `.aw` |
| `unsupported_format_version` | `format_version` is emitted by the compiler, never authored — a Builder has no way to claim one |
| `no_zones_found` | an empty World is a *server* configuration error (`AW-SRV-001` AC-9). A Template-only pack such as `andara.core` is legal content |
| `unflattened_template` | `resolved: false` would be a compiler bug, not a source error |
| `chain_mismatch` | the loader's check on compiler output — a chain that is not the parent's chain plus self |
| `invalid_provenance` | likewise: provenance naming a field the Template does not carry |

The last three are the server checking the compiler. That they exist is the reason this story
specifies the compiler's output rather than the compiler defining it (CLAUDE.md §2).

---

## 4. The `.errors` sidecar

Each invalid corpus case pairs its source with a `.errors` file holding the findings a conforming
compiler produces, in order:

```
town/npcs.aw:14:3: removed_by_subtype
  andara.core.Entity
  andara.core.Npc
  town.Merchant
town/zones.aw:8:5: unknown_direction
  town
  square
```

- One finding per unindented line: `file:line:col: code`. The path is relative to the case
  directory.
- Chain entries are indented two spaces beneath their finding, in order.
- `#` starts a comment line.
- Warnings carry the code and are marked by nothing else — a `.errors` file for a case that also
  compiles is how a warning case is expressed, and the case then has expected output too.

**The sidecar deliberately does not carry the message text.** AC-3 compares code, position, and
chain; pinning wording would mean every improvement to an error message is a corpus change, and
error messages are meant to improve. What holds the wording is §3's "the message says" column and
review — a weaker check, honestly weaker, and the right trade for a feature whose whole value is
that it reads well.

---

## Acceptance criteria

1. Every code in §3 appears in the corpus at least once — `make content-conformance` fails naming
   any that does not (`AW-CLI-005` Definition of done).
2. Every code in §3.2 is a string that exists in `server/sim/errors.go`; no code in §3.1 collides
   with one.
3. Every `.errors` sidecar parses as §4 and its findings are in sorted order.
4. `corpus/invalid/syntax/` cases carry exactly one finding, always `syntax_error` — checked by
   `make content-grammar-check`, which also asserts the position against the grammar.
5. No finding in the corpus has `line: 0`.

## Out of scope

- **Message wording.** §3 fixes what a message must say, not how it says it. `AW-CLI-006` writes the
  strings and `AW-CLI-002` renders them.
- **Rendering.** Human and JSON output shapes are `AW-CLI-001`'s envelope and `AW-CLI-006`'s to emit.
- **Exit codes.** `AW-CLI-002` and `AW-CLI-006` own theirs; this document only fixes that warnings
  do not fail a compile.
- **The inert-Component warning** — `AW-CLI-002`, which has the system registry the compiler lacks.
- **Localization.** One language, English, until someone asks.
