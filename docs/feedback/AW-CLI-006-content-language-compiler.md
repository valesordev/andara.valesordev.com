<!--
SPDX-FileCopyrightText: 2026 Valesor Development
SPDX-License-Identifier: CC-BY-SA-4.0
-->

# Feedback — AW-CLI-006 Content Language compiler, formatter, and decompiler

Implementation findings that deviate from, or decide something left open by, the story and the
`AW-CLI-005` specification. Raised for architecture review; the implementation proceeds under the
assumptions recorded here so the branch is reviewable against a stated position rather than
against a guess.

Story: `docs/stories/AW-CLI-006-content-language-compiler-formatter-and-decompiler.md`
(`status: ready`, read 2026-09-23)
Spec: `docs/specs/content-language/v1/` — all four documents `status: ready` as of `202e51a`
(AC-10 accepted 2026-09-23; `desc`, `->`, and PascalCase Template names stand)

---

## 1. `duplicate_pack` is positioned at the *first* declaration; every other `duplicate_*` at the second

**Finding.** `errors.md` §3.1 says `duplicate_pack` reports "both files" but does not say which
one carries the position. The corpus does, and it disagrees with the rest of the family:

| Case | Sources (sorted) | Sidecar position | Which occurrence |
|------|------------------|------------------|------------------|
| `duplicate-pack` | `other.aw`, `pack.aw` | `other.aw:1:1` | **first** |
| `duplicate-zone` | `a.aw`, `b.aw` | `b.aw:1:1` | second |
| `duplicate-room` | `z.aw` | `z.aw:4:3` | second |
| `duplicate-template` | `a.aw`, `b.aw` | `b.aw:1:10` | second |
| `duplicate-component-type-room` | `z.aw` | `z.aw:4:5` | second |

Both files in `duplicate-pack/` hold the identical line `pack p requires andara.core@1`, so the
position is not distinguishing the "wrong" one by content — `other.aw` simply sorts before
`pack.aw`.

**Why it matters.** AC-1 is a byte comparison against the sidecar. A compiler that applied the
`duplicate_*` family's rule uniformly would emit `pack.aw:1:1` and fail the corpus.

**Assumption taken.** `duplicate_pack` is a **pack-level** finding (its chain is empty, unlike
every other `duplicate_*`), so it is reported once at the first `pack` declaration in
sorted-file order, with both files named in the message. The other `duplicate_*` codes name a
declaration that lost to one that was kept, so they report at the loser — the second.

**What would change it.** If the intent was "the declaration that is not in `pack.aw`", say so
and the rule becomes a special case on the canonical filename instead; the corpus cannot tell
the two readings apart today because it has exactly one case.

## 2. `encoding` reports one finding for the whole pack, not one per file

**Finding.** `invalid/encoding/crlf/` has CRs in both `pack.aw` and `z.aw`, and its sidecar
carries a single finding at `pack.aw:1:30`.

**Assumption taken.** Encoding is checked before the grammar is reached (corpus README), file by
file in sorted order, and the **first** violation is reported and the compile stops. A per-file
sweep would emit `z.aw:1:13` as well and fail the corpus.

## 3. Chain content varies by code in ways `errors.md` §1 does not enumerate

**Finding.** `errors.md` says a Room or Exit finding is `["<zone>", "<room>"]` "and the Exit's
direction where the finding is on an Exit". The corpus is more specific than that, and two codes
extend the chain past the carrier:

| Code | Chain |
|------|-------|
| `unknown_direction` | `zone, room` — **no** direction, because the direction is the offending value |
| `unknown_room`, `unknown_zone`, `duplicate_direction`, `missing_reverse_exit` | `zone, room, direction` |
| `orphan_room`, `duplicate_room`, `duplicate_declaration` | `zone, room` |
| `duplicate_component_type` | `zone, room` (or `pack.Template`) — **no** component type |
| `invalid_component_field` | `zone, room, <component type>` (or `pack.Template, <component type>`) |
| `removed_by_subtype`, `unknown_behavior` | the full inheritance chain, root first, ending at the declaring Template |
| `extends_cycle` | the cycle, with the entry Template repeated as the last element |
| `chain_too_deep` | every Template in the chain, including the one past the bound |

**Assumption taken.** Implemented exactly as the corpus reads. Recorded because §1's prose is
the normative statement and it is narrower than the corpus it governs; a reader implementing
from §1 alone would get four of these wrong.

## 4. Position conventions, extracted from the corpus rather than from `errors.md` rule 1

**Finding.** Rule 1 says the position "names the token the Builder must look at — the offending
value, not the declaration that contains it". For roughly half the codes the offending value *is*
the declaration keyword, and which one is not derivable from the rule.

Recorded here as the table the implementation was written to, because it is the part of the
contract that is checked byte-for-byte and stated only by example:

| Code | Token |
|------|-------|
| `pack_missing` | `1:1` of the first file in the pack, sorted |
| `pack_mismatch` | the declared pack name |
| `core_version_mismatch` | the version integer after `@` |
| `duplicate_pack` | the `pack` keyword (see §1) |
| `duplicate_zone`, `duplicate_room` | the `zone` / `room` keyword of the second |
| `duplicate_template`, `extends_cycle`, `chain_too_deep` | the Template **name**, not the `template` keyword |
| `template_head` | the second head keyword when both; the Template name when neither |
| `unresolved_extends` | the reference after `extends` |
| `duplicate_declaration` | the `desc` / `fallback` keyword of the second |
| `duplicate_direction`, `missing_reverse_exit` | the `exit` keyword |
| `orphan_room` | the `room` keyword |
| `unknown_direction` | the direction identifier |
| `unknown_room`, `unknown_zone` | the **whole** exit target reference, including the `<zone>.` half |
| `unknown_component_type`, `duplicate_component_type` | the component reference / the `component` keyword respectively |
| `invalid_component_field` | the field **name** when unknown; the **value** when the kind is wrong |
| `float_literal`, `unknown_behavior`, `unknown_sense`, `fallback_missing` | the offending literal or identifier |
| `invalid_escape` | the backslash inside the string, not the string's opening quote |
| `removed_by_subtype` | the `remove` keyword |
| `encoding` | the offending byte (`1:1` for a BOM, the CR's column for a CR) |

