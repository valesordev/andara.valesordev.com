---
spec: content-language
version: v1
status: ready
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

Rendered, JSON (`--output json`): the array of diagnostics alone on stdout, each object carrying
the fields above, with `severity` as `"error"` or `"warning"`. Failures that aren't diagnostics use
`AW-CLI-001`'s error envelope. *(Amended 2026-09-30 at `AW-CLI-002`'s §8 review.)*

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
   files in the pack are still parsed and still report. Encoding is the other exception, and a
   stronger one: it is checked before any grammar is reached, file by file in sorted order, and the
   **first** violation is the only finding — the compile stops there (`invalid/encoding/crlf/` has
   CRs in two files and one finding).
5. **Findings are sorted** by file, then line, then column, then code. Two compiles of the same
   source produce the same diagnostics in the same order, which is what makes the `.errors` sidecars
   a byte comparison and the three-way equivalence (`AW-CLI-002` AC-4) checkable.
6. **The message never blames the Builder for a rule they cannot see.** Where a vocabulary is closed
   and server-defined — Component types, Directions, senses — the message states that and either
   lists the permitted set or says where to file the request. This is why `unknown_component_type`
   is three clauses long: without them a Builder re-checks their spelling forever for a type that
   was never going to exist (ADR-0010 decision 7).
7. **Warnings do not fail the compile.** Exit `0` with warnings on stderr; `--strict` is
   `AW-CLI-006`'s to add if it is ever wanted. **A compile that reports an error reports only
   errors.** A warning is advice about content the compiler accepted, and a failed compile accepted
   nothing — an `orphan_room` that exists only because a broken Exit did not resolve describes the
   compiler's half-finished analysis, not the source. No `invalid/` sidecar carries a warning.
   **The loader follows the same rule** (`AW-SRV-034`, 2026-09-29): when `BuildWorld` reports an
   error it reports no warning, so a publish or a boot that fails shows the Builder what the
   compile showed. A warning promoted to an error by `content.strict_orphans` is an error.
8. **A duplicate is reported at the one that lost.** `duplicate_zone`, `duplicate_room`,
   `duplicate_template`, `duplicate_component_type`, `duplicate_declaration`, and
   `duplicate_direction` land on the second occurrence in sorted order, the first being the one that
   was kept. `duplicate_pack` is pack-level (its chain is empty), so it is reported once, at the
   **first** `pack` declaration in sorted file order, with both files in the message.
9. **A cycle is one finding**, positioned at its lowest-named member, whichever file that is in.
10. **The publish gate reports what the publisher can fix** (`AW-SRV-013`, ruled 2026-10-03, #312).
    The gate builds one World from every active pack's blobs and the publisher's, and refuses on any
    error in it. What it **reports** is narrower:
    1. **Incumbents first.** The active packs' blobs are inputs before the publisher's, so in a
       cross-pack clash rule 8's "the one that lost" is the publisher's declaration. The active
       pack's Zone is kept whole, and a publish never breaks a pack that is already active.
    2. **Only the publisher's own blobs.** A finding, a warning included, whose `file` isn't in the
       version being published is not reported. Another pack's warning is shown when that pack
       publishes, and not at every publish after it.
    3. **Root only, across packs.** A cross-pack `duplicate_zone` drops the whole losing Zone and
       reports once. Its Rooms aren't reported as `duplicate_room`, and an Exit that targets the
       dropped Zone's id isn't reported as `unknown_zone` or `unknown_room`. They show after the
       clash is fixed. Two files of one pack declaring the same Zone keep rule 4: `duplicate_room`
       is reported too, because the Builder fixes both in the same files.
    4. **The message names both packs.** A cross-pack `duplicate_zone` reads
       `ZoneID <id> declared in pack <publisher> and in active pack <other>@<version>`. It lands on
       the publisher's `zone` keyword (rule 8), which the CLI places on the publisher's source.
    5. **An error the publish causes in another pack's blobs** (a new version that removes a Zone
       another pack exits into) still refuses the publish. Its `file` is kept, its message begins
       `in pack <other>:`, and its `line` and `col` are `0`, the pack-level case. It is never printed
       as the publisher's file. No instance is known. If one is seen, it goes to architecture.

    The counts follow the report: `validation_failures_total{code}` and the audit record's
    `findings_count` count reported findings, and the refusal's `warn` line carries the first
    reported finding's code. Rule 1's "a finding with `line: 0` is a defect" has this one exception,
    and the corpus is unaffected, since it compiles one pack at a time.

### Where the position lands

Rule 1, per code. The corpus fixes every row byte for byte; this table is the statement of it.

| Code | Token |
|------|-------|
| `pack_missing` | `1:1` of the first file in the pack, sorted |
| `pack_mismatch` | the declared pack name |
| `core_version_mismatch` | the version integer after `@` |
| `duplicate_pack` | the `pack` keyword (rule 8) |
| `duplicate_zone`, `duplicate_room` | the `zone` / `room` keyword of the second |
| `duplicate_template`, `extends_cycle`, `chain_too_deep` | the Template **name**, not the `template` keyword |
| `template_head` | the second head keyword when both; the Template name when neither |
| `unresolved_extends` | the reference after `extends` |
| `duplicate_declaration` | the `desc` / `fallback` keyword of the second |
| `duplicate_direction`, `missing_reverse_exit` | the `exit` keyword |
| `orphan_room` | the `room` keyword |
| `unknown_direction` | the direction identifier |
| `unknown_room`, `unknown_zone` | the **whole** exit target reference, including any `<zone>.` half |
| `unknown_component_type` | the component reference |
| `duplicate_component_type` | the `component` keyword |
| `invalid_component_field` | the field **name** when the field is unknown; the **value** when its kind is wrong |
| `float_literal`, `unknown_behavior`, `unknown_sense`, `fallback_missing` | the offending literal or identifier. For `fallback_missing` on a Zone that declares no `fallback`, there is none, so it is the Zone's `zone` keyword (decided 2026-09-25) |
| `invalid_escape` | the backslash, not the string's opening quote |
| `removed_by_subtype` | the `remove` keyword |
| `encoding` | the offending byte — `1:1` for a BOM, the CR's own column for a CR |

### Chain

`Chain` is the declaration path to the finding, outermost first. It names the **carrier** — the
declaration the offending token sits in — and stops short of the offending value, because the
position already points at that. There are four carriers:

| Carrier | Chain |
|---------|-------|
| Zone | `<zone>` |
| Room | `<zone>`, `<room>` |
| Exit | `<zone>`, `<room>`, `<direction>` |
| Template | `<pack>.<Name>` — the Template alone, not its ancestry |

Every raisable code, with the chain it carries. There is no fallback rule, and a code missing from
this table is a defect in this document:

| Code | Chain |
|------|-------|
| `syntax_error`, `encoding`, `pack_missing`, `duplicate_pack`, `pack_mismatch`, `core_version_mismatch` | empty — pack-level, or found before any declaration is |
| `duplicate_zone`, `fallback_missing` | Zone |
| `duplicate_room`, `orphan_room` | Room |
| `duplicate_declaration` | Room for a second `desc`; Zone for a second `fallback` |
| `unknown_direction` | Room — the direction *is* the offending value, so the Exit carrier would restate it |
| `unknown_room`, `unknown_zone`, `unknown_sense`, `duplicate_direction`, `missing_reverse_exit` | Exit — its direction is not in dispute |
| `duplicate_template`, `template_head`, `unresolved_extends` | Template |
| `invalid_escape`, `float_literal`, `unknown_component_type`, `duplicate_component_type` | whichever carrier holds the literal or the Component: Zone, Room, or Template. Never the Component type |
| `invalid_component_field` | the Component's carrier, then the Component type |
| `removed_by_subtype`, `unknown_behavior` | the inheritance chain, root first, ending at the declaring Template |
| `extends_cycle` | the cycle from its lowest-named member, that member repeated as the last entry |
| `chain_too_deep` | every Template in the chain, including the one past the bound |

*Corrected 2026-09-24 (`AW-CLI-005` review):* this section said an Exit finding carries "the Exit's
direction", which `unknown_direction`'s own sidecar contradicts, and rules 4, 7, 8, and 9, the
position table, and the chain table were stated only by the corpus. At PR #58's review the chain
table gained a row for every raisable code, each checked against its sidecar. `AW-CLI-006` was written to the corpus
(`docs/feedback/AW-CLI-006-content-language-compiler.md` §1–§4); the prose now says what the corpus
already fixed.

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
| `core_version_mismatch` | `requires andara.core@N` when neither the embedded core nor the cache holds `N` | both versions, and the remedy for the direction of the skew: with `N` above the embedded core, the `andara-cli` release that embeds `andara.core@N`; with `N` below it, the usual case after a core bump, changing `requires` to the embedded number in the file that declares the pack (`pack.aw` in the canonical layout). For a *published* version (`validate --pack --version`), which can't be edited, the remedy is the release that embeds `N` in either direction (amended 2026-09-28; direction added 2026-10-01, #308; the declaring file and the published case 2026-10-02, #335) |
| `template_head` | a Template declares both `kind` and `extends`, or neither | that a root states its kind and a subtype inherits it |
| `extends_cycle` | `extends` forms a cycle | every Template in the cycle, in order |
| `removed_by_subtype` | a `remove` form | the ancestor that defined it, the substitutability rule, and `enabled: false` |
| `duplicate_declaration` | a second `desc` in a Room or a second `fallback` in a Zone | both positions |

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
| `fallback_missing` | `AW-SRV-012` | `fallback` names a Room the Zone does not declare, or a Zone declares none. *(Moved from §3.4 on 2026-09-25, when field 6 landed.)* |
| `duplicate_direction` | `AW-SRV-034` | two Exits in one Room with the same Direction — the message names the Direction and both targets. *(Moved from §3.1 on 2026-09-29: the loader raised it as `malformed_file` until `AW-SRV-034` gave it this code.)* |

### 3.3 Warnings

Exit `0`. Both are legal content that is far more often a mistake than an intention, and the posture
is `AW-SRV-001`'s: legal, never silent.

| Code | Owner | Emitted when |
|------|-------|--------------|
| `orphan_room` | `AW-SRV-001` | a Room no Exit from another Room in its Zone reaches — inbound, as `AW-SRV-001` AC-6 has it; a Room's Exit to itself does not count, and a Zone of one Room never warns. A Builder mid-work |
| `missing_reverse_exit` | `AW-SRV-021` | an Exit whose reverse is absent — names both Rooms. A chute is real; forgetting the way back is more common |

**`orphan_room` was decided at the `AW-CLI-005` review (2026-09-24),** and `AW-SRV-034` made the
loader and the compiler apply it alike. A Room you can leave but never enter is unreachable, and
unreachable is what the warning is for. The corpus's `valid/warn-missing-reverse-exit/` is that case:
`loft`, which the chute leaves and nothing enters, carries both warnings.

`AW-CLI-002` adds a third at validate time — the *inert Component* warning ADR-0010 asks for, on a
Component no system and no bound Behavior reads. It is that story's because it needs the system
registry, which the compiler does not have.

### 3.4 Pending a field

Specified, in the grammar, and not yet reachable because the protobuf field does not exist
(semantics.md §9). Their corpus cases are under `corpus/pending/`.

| Code | Gated on | Raised when |
|------|----------|-------------|
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
| `zone_removed` | a version that drops a Zone the World in effect has (`AW-SRV-012`). It compares the version with the live World, which a compile of one pack never sees. Removing a Zone isn't supported, so the fix is to keep the Zone. Raised at activation and on reload, as `validation` |
| `spawn_room_removed` | a version whose World lacks `character.spawn_room` while the World in effect has it (`AW-SRV-012`). It depends on server configuration, not on the source. The fix is to keep the Room, or have an Operator move `character.spawn_room` first. Raised at activation and on reload, as `validation` |

`unflattened_template`, `chain_mismatch` and `invalid_provenance` are the server checking the
compiler. That they exist is the reason this story specifies the compiler's output rather than the
compiler defining it (CLAUDE.md §2).

*(`zone_removed` and `spawn_room_removed` added 2026-09-30, from
`docs/feedback/AW-INF-023-builders-guide.md` item 5. They were in `sim.AllErrCodes` and nowhere in
this taxonomy.)*

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
