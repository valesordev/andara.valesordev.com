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

## 3b. Warnings are dropped when the compile fails, which `errors.md` does not say

**Finding.** `errors.md` rule 7 says warnings do not *fail* a compile. It does not say what happens
to them when something else does. The corpus does: not one of the 32 `invalid/semantic/` sidecars
carries a warning, and several of the cases would produce one under the compiler's own rules —
`multiple-findings` leaves Room `h` joined to nothing once both of Room `r`'s Exits fail to resolve,
and `unknown-direction` leaves a one-way Exit whose reverse is absent.

**Assumption taken.** A compile that reports an error reports only errors. A warning is advice about
content the compiler otherwise accepted, and a compile that failed has not accepted anything —
"no Exit joins Room h to any other Room" on a pack whose Exits did not resolve is a statement about
the compiler's own half-finished analysis, not about the source.

**Why it matters.** Without the rule, every invalid case's sidecar would need to enumerate the
warnings that fall out of its errors, which would make each one a test of the resolver's recovery
order rather than of the finding it exists to cover.

## 3c. An `extends` cycle is reported once, at its lowest-named member

**Finding.** `semantics.md` §4 says a cycle names every Template in it. It does not say how many
findings a cycle produces, nor which member carries the position. `invalid/semantic/extends-cycle/`
has three mutually-extending Templates and one finding, at `t.aw:1:10` — `p.A`.

**Assumption taken.** One finding per cycle, positioned at the **lowest-named** member. A Builder
fixing one cycle should not read three findings that describe it from three angles.

The corpus cannot distinguish "lowest-named" from "first in the file": `p.A` is both. Sorted order
was chosen because it does not depend on which file the Builder happened to put the declaration in,
which is the same property `semantics.md` §2 asks of the compiled output.

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


## 5. `pack_mismatch` cannot be raised by `Compile(dir, core, deps)` alone

**Finding.** `semantics.md` §2 says a `pack` declaration "naming something other than the pack being
compiled is `pack_mismatch`". The story's contract sketch is
`Compile(dir string, core *Pack, deps []*Pack)`, so the only candidate for "the pack being compiled"
is the directory — and the corpus rules that out twice over:

| Case | Directory | Declares | Expected |
|------|-----------|----------|----------|
| `valid/core` | `core` | `pack andara.core` | compiles clean |
| `valid/minimal` | `minimal` | `pack p` | compiles clean |
| `valid/town` | `town` | `pack town` | compiles clean |
| `invalid/semantic/pack-mismatch` | `pack-mismatch` | `pack elsewhere` | `pack_mismatch` |

69 of the 71 cases declare `pack p` out of a directory named for what the case covers. A
directory-name rule would fail all of them; no rule derivable from the directory distinguishes
`elsewhere` from `p`.

**Why it matters.** AC-1 requires the sidecar to match. Without an expected pack name from
somewhere, the case cannot be satisfied and the code path is never exercised by the corpus.

**Assumption taken.** `Compile` keeps the contract's signature and delegates to `CompileOpts` with
an `Options{Pack string}`. Empty — the ordinary case, a Builder compiling their own working copy —
skips the check entirely; a caller that already knows the pack id, such as the publish gate checking
that a pack published as `town` declares itself `town`, supplies it. That is the reading under which
`pack_mismatch` has a job at all.

The conformance harness then needs the name per case, and derives it from a convention read off the
corpus rather than stated by it: `valid/core` is `andara.core`, `valid/town` is `town`, everything
else is `p` (`content/lang/conformance.go`, `corpusPack`).

**What we would like.** A marker file, the way `corpus/pending/` already carries `PENDING` — a
one-line `PACK` naming the pack each case is compiled as. Then the harness reads it instead of
inferring it, and adding a case that is not `p` stops being a silent trap. Alternatively, confirm
that `pack_mismatch` belongs to the publish gate rather than to `compile`, and the corpus case moves
out of `invalid/semantic/` to wherever that is tested.

## 6. `orphan_room`: the compiler and the loader do not agree, and the corpus is the wider rule

**Finding.** `sim.BuildWorld` flags a Room with **no inbound intra-Zone Exit**
(`server/sim/build.go`). The corpus requires something wider:

| Case | Shape | Corpus expects |
|------|-------|----------------|
| `valid/warn-missing-reverse-exit` | `loft` has a one-way chute down to `cellar`; nothing reaches `loft` | `missing_reverse_exit` only — **no** `orphan_room` on `loft` |
| `valid/warn-orphan-room` | `island` has no Exit either way | `orphan_room` on `island` |
| `valid/minimal`, `desc-adjacent`, `string-escapes`, `components-on-carriers` | one Room, no Exits | no finding at all |

The loader's rule would raise `orphan_room` on `loft` and on all four one-Room Zones.

**Assumption taken.** The compiler warns when a Room has no **incident** intra-Zone Exit in either
direction, and never in a Zone with a single Room. A Room a Builder can walk out of is joined to the
Zone; the warning is for the Room that is joined to nothing, and warning that the only Room in a Zone
is unreachable is advice nobody can act on.

**Why it matters, beyond this story.** `AW-CLI-002` AC-4 requires the CLI, the conformance harness,
and `AW-SRV-013`'s publish gate to produce **identical** diagnostics. Today they cannot: the same
content gets `orphan_room` from the loader and not from the compiler. One of the two has to move, and
this is a decision rather than a bug fix — the loader's rule has been shipped through three `done`
stories and is what `--strict-orphans` promotes.

**Recommendation.** The loader adopts the corpus's rule. `AW-SRV-001` AC-6 already words it as "any
other Room in that Zone", which the incident-Exit reading satisfies more literally than the inbound
one, and it is the reading that stops a one-Room Zone and a one-way chute from producing warnings a
Builder would learn to ignore. That is a change to `server/sim` and therefore to a `done` story's
behaviour, so it is architecture's call and not taken here.

## 7. `content fetch-core` and `content decompile --pack/--version` have no RPC to call

**Finding.** The story puts `content fetch-core` "over `Admin`" and
`content decompile --pack ID --version N` behind exit code `3` for "server unreachable". No RPC in
`docs/specs/protocol/andara/admin/v1/` serves a pack's blobs or a published `ContentVersion` —
`AdminService` today is server info, accounts, invites, registration, and agent accounts. Serving a
published version is `AW-SRV-013`'s and `AW-CLI-003`'s, and both are downstream of this story
(`AW-CLI-003` is in this story's `blocks`).

**Assumption taken.** Neither command invents the RPC, because defining it would be this lane
writing the contract:

- `content fetch-core` exits `3` with `core_fetch_unavailable`, naming `AW-SRV-013` and offering
  `--from <dir>`, which populates the cache from a pack directory on disk — the shipped seed under
  `content/core`, or a checkout. That is enough to make AC-5 and AC-6 real and to make the cache
  path exercised rather than theoretical.
- `content decompile` takes `--path` and decompiles a pack already on disk. `--pack` and `--version`
  are left for `AW-CLI-003` to add against the RPC it defines.

AC-4's round trip is therefore verified in-process against `Output` and against the corpus's
`roundtrip/` cases, not against a throwaway Redpanda as the test plan's integration line asks. The
compiled → source → compiled identity is fully checked; what is not checked is the transport.

## 8. `deps` is accepted and unused

`Compile(dir, core, deps)` takes dependency packs. In v1 a Template resolves within its own pack or
`andara.core` and nowhere else (`semantics.md` §4), and cross-pack Exits do not exist (§3), so there
is nothing for a dependency to resolve. The parameter is kept — it is the contract's, and the day
pack dependencies arrive the signature does not move — and a non-empty `deps` currently changes
nothing. Recorded so it is not mistaken for an implemented feature.

## 9. `server/sim` gained two exported accessors

`ComponentFieldKind` and `ComponentFieldNames`, beside the existing `ComponentTypes`. The compiler
validates a Builder's field names and value kinds against the registry the loader enforces rather
than a copy of it (`semantics.md` §3, ADR-0004), and `componentSpec.fields` was unexported. Additive;
no behaviour moved.

## 10. `content/` is not in `CLAUDE.md`'s ownership list

The story places the package at `content/lang` "(repo root, shared by `admin/cli` and
`server/content`)". `CLAUDE.md` §2's implementation-lane list names `server/`, `cli/`, and
`client/`. `content/` already existed for the `andara.core` seed, and `content/lang` is Go source
that runs in the game, so it is implementation by the "runs **in** the game" rule — but the list
does not say so. Worth a one-word edit when `CLAUDE.md` is next touched; no action taken here, since
`CLAUDE.md` is not a path this lane writes.

## 11. Measurements

- **AC-7**, 2,000 Rooms across 16 Zones: **20 ms** uninstrumented, **122 ms** under `-race`, against
  a 5 s budget — measured on the **workstation**, not on the kind box the criterion names. At 250×
  headroom the kind-box number is a formality, and the test runs in CI on the CI runner either way;
  the same posture `AW-SRV-006` took on its own budget. `TestSizingPackCompilesInBudget`. The
  threshold moves with the build for the reason `server/simtest/stallfactor_race_test.go` records —
  CI runs `make check`, so a `!race` test never runs outside a laptop.
- **AC-1**: 69 corpus cases agree, 6 pending skipped. `make content-conformance`, in `make check`.
- The check was shown to be non-vacuous by breaking the compiler twice: reversing the Exit sort
  produced 7 failures, dropping the canonical trailing LF produced 110.
- **AC-8** was shown to be non-vacuous by adding an import of `server/store`: depguard and
  `TestImportsNothingFromServerButSim` each failed, and each named the package.

## 12. `compiled → source → compiled` is not byte-identity for a pack that had comments

**Finding.** `semantics.md` §8 states two directions and qualifies only one of them:

> **Compiled → source → compiled is identity.** `decompile` of a published version produces `.aw`
> source that recompiles to the same blobs, byte for byte.
>
> **Source → compiled → source is identity for canonical source only.**

The first is unqualified, and it does not hold for any pack whose source carried comments.
`TemplateDefinition.source` is `{file, line}`, `decompile` output is comment-free, and dropping
comments moves every line after them. `corpus/valid/town/npcs.aw` opens `template Guard` on line 30,
behind 29 lines of commentary; the decompiled file opens it on line 5, so the recompiled
`town.Guard.json` differs from the published one in `source.line` and in nothing else.

**Why it matters.** It is the operational direction — "how a Builder who has lost their working copy
gets it back" — and a Builder who decompiles, recompiles, and diffs against what is published will
see every Template move. The difference is cosmetic and provenance-only, but a publish gate that
compared compiled blobs for equality would reject the result.

**Assumption taken.** Implemented as specified, and the qualification is treated as applying to both
directions: the round trip is byte-identity for **canonical** source, which is comment-free, and is
identity in everything but `SourceRef.line` otherwise. The corpus agrees — both `roundtrip/` cases are
authored canonically, so the corpus cannot distinguish the two readings, and `AW-CLI-006` AC-4 passes
under either.

**Recommendation.** §8's first bullet gains the same qualification its second has. The alternative —
making `decompile` preserve line numbers by padding with blank lines — would produce source no
Builder would want, to satisfy a field that is documented as "provenance a finding quotes, **not** a
path the loader opens" (§6).

## 13. `Decompile` needs the core pack to un-flatten, so the API grew a variant

**Finding.** Recovering what a Template's own file declared means subtracting the parent's flattened
set from the child's. For a parent inside the pack that set is in `Output.Templates`; for a parent in
`andara.core` it is not, and every inherited Component then looks like the subtype's own.

The contract sketch is `Decompile(out *Output) (map[string][]byte, error)`, which cannot reach a
parent it was not handed.

**Why it matters.** Every Builder pack extends `andara.core`, so this is the ordinary case, not an
edge. `decompile` of `corpus/valid/town/` emitted

```
template Merchant extends andara.core.Npc {
  component andara.core.Behavior { name: "town.merchant" }
  component andara.core.Memory {}      // inherited from andara.core.Npc; Merchant never wrote it
}
```

AC-4 does not catch it: an empty Component body is a no-op rather than a reset (`semantics.md` §5),
so the restatement recompiles to the same Components. Neither `roundtrip/` case extends a core
Template that carries Components, so the corpus does not catch it either.

**Assumption taken.** `Decompile(out)` keeps the contract's signature and delegates to
`DecompileWith(out, core *Pack)` with a nil core. The CLI passes the core pack it already loaded, and
`TestDecompileSubtractsTheCoreParent` asserts both halves — that `Memory` is absent with the core
pack and present without it, so the reason the parameter exists is visible from the test.

**Worth a corpus case.** A `roundtrip/` case whose Template extends `andara.core.Npc` would have
caught this from the corpus rather than from a reviewer.

---

## 14. §8 review (2026-09-24, architecture)

The story stays `review`. The record is in the story under "§8 pass (2026-09-24)".

### For implementation
1. The `valid/` decompile → recompile → compare pass (`source` masked) in `content/lang/conformance.go`,
   run by `make check`. This is `SPRINT-01` implementation item 2, and it closes this story and
   `AW-CLI-005` together.
2. `admin/README.md`: drop "`content` arrives with `AW-CLI-002`". Add `content compile`, `fmt`,
   `decompile` and `fetch-core` to the Commands table, and document `--cache`/`ANDARA_CONTENT_CACHE`,
   `fetch-core --from`, and `decompile --path`.

### For PM
3. **No story defines the RPC that §7 above needs.** The review of `AW-CLI-005` accepted "`AW-SRV-013` /
   `AW-CLI-003` own the transport". But `AW-SRV-013`'s Admin sketch has `HasBlobs`, `PublishBlob`,
   `PublishVersion`, `ApproveVersion`, `ActivateVersion` and `ListVersions`, and nothing that returns
   a published version's blobs. Without one, three things have no server to talk to:
   - `content decompile --pack/--version` (this story's AC-4)
   - `content fetch-core` over Admin (AC-5)
   - `AW-CLI-002`'s `fetch-core` hint

   Recommendation: `AW-SRV-013` gains a read RPC (a `GetVersion` → manifest, plus a streamed
   `GetBlob` by hash; the blobs are immutable and hash-keyed, so the read side is cacheable
   forever). Architecture writes that contract at `AW-SRV-013`'s contract review once it is in a
   sprint. That story then inherits AC-4's and AC-5's published-version halves from here.

### Implementation, 2026-09-26: item 2 delivered

On `impl/aw-cli-006-admin-readme`, `admin/README.md` now has the following:
- The four `content` commands in the Commands table.
- A `content` section with every flag, including `--cache` and `ANDARA_CONTENT_CACHE` with their
  precedence, `fetch-core --from`/`--version`, and `decompile --path`/`--out`.
- The three `error.code`s the commands add.

It points at §7 and §14 item 3 for the missing read RPC, and the stale "`content` arrives with
`AW-CLI-002`" is gone. Item 1 was delivered in #75. Item 3 stays PM's.

Review of #99: the README said a `core_version_mismatch` finding names the cached version. From
the CLI it can't, because `loadCore` looks up only the pinned version and a miss reports "no cached
copy". The "cache holds" message is reachable only from `lang.Compile` given a different core. The
README now says only the required version is reported.

