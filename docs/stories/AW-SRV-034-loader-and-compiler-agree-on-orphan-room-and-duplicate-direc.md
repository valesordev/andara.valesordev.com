---
id: AW-SRV-034
title: Loader and compiler agree on orphan_room and duplicate_direction
epic: EPIC-02
component: server
type: bug
status: ready
size: S
depends_on: [AW-SRV-001, AW-CLI-006]
blocks: [AW-CLI-002]
lane: implementation
risk: low
---

## Context

`AW-CLI-002` AC-4 needs the CLI, `make content-conformance`, and `AW-SRV-013`'s publish gate to give
**identical** diagnostics: code, position, and chain. The loader (`server/sim.BuildWorld`) and the
compiler (`content/lang`) disagree in two places today, and AC-4 can't hold until they agree.

**`orphan_room`.** The loader warns on a Room with no *inbound* Exit from another Room in its Zone.
That is `AW-SRV-001` AC-6 as written: "reachable by no Exit from any other Room in that Zone". The
compiler warns on a Room with no Exit in *either* direction, and never in a single-Room Zone. It
does that because the corpus's `valid/warn-missing-reverse-exit/` case holds only a
`missing_reverse_exit` for a Room (`loft`) whose one Exit leads out and that nothing enters. The
corpus departed from AC-6 without checking it, and `AW-CLI-006` was right to follow the corpus
(`docs/feedback/AW-CLI-006-content-language-compiler.md` §6). The feedback asked architecture to
decide. **Decided at the `AW-CLI-005` review (2026-09-24):** a Room you can leave but never enter is
unreachable, and unreachable is what the warning is for, so the loader's inbound rule stands. The
compiler's single-Room exemption is also right, because a Zone of one Room is entered across a Zone
boundary or at spawn and can never be entered from inside itself, so the loader takes it too. The
rule is now in `errors.md` §3.3.

**`duplicate_direction`.** The compiler rejects two Exits with the same Direction in one Room. The
loader accepts them: it sorts and keeps both, so hand-written JSON reaching the store is not refused.
`AW-CLI-005` filed this for `AW-SRV-021` as an Open question. It lands here because it is the same
three-way-equivalence gap.

## User story

As a Builder, I want the warnings and errors I see from `andara-cli` to be the ones the server
would raise, so that a pack that validates clean locally is not refused, or warned about, at publish
or boot.

## Scope

### In scope
- `server/sim`: the orphan check skips a Zone with exactly one Room. The inbound rule and the
  self-loop exclusion are unchanged.
- `server/sim`: `ErrDuplicateDirection ErrCode = "duplicate_direction"`, raised by `BuildWorld` on
  the second Exit with a Direction already used in that Room. It is fatal, like every other
  `duplicate_*` code.
- `content/lang`: the orphan check becomes inbound (a Room with no Exit into it from another Room
  in its Zone), keeping the single-Room exemption. Update the `warnOrphans` comment to cite this
  story instead of the corpus case.
- Corpus, in the same PR (a sidecar change is a compiler change, and `make check` must stay green):
  `docs/specs/content-language/v1/corpus/valid/warn-missing-reverse-exit/expected.errors` gains
  `z.aw:2:3: orphan_room` with chain `z`, `loft`, sorted ahead of the existing
  `missing_reverse_exit`.
- `errors.md`: `duplicate_direction` moves from §3.1 ("raised only by the compiler") to §3.2 with
  owner `AW-SRV-034`, and the note under §3.3 about the chute case is deleted.

### Out of scope
- Counting a cross-Zone Exit as inbound. That would stop the warning on a multi-Room Zone's entry
  Room, but the compiler sees one pack and the loader sees the World, so the two could no longer
  agree. It needs its own decision if the false positive shows up in real content.
- `--strict-orphans` / `content.strict_orphans`. Same key and behavior; it promotes whatever the
  rule finds.
- The inert-Component warning (`AW-CLI-002`).

## Acceptance criteria

1. **Given** a Zone of two Rooms where `loft` has one Exit down to `cellar` and nothing leads to
   `loft` **when** `sim.BuildWorld` runs **then** it warns `orphan_room` for `loft`, and `andara-cli
   content compile` on the same source warns `orphan_room` at `loft`'s `room` keyword with chain
   `z`, `loft`, and `missing_reverse_exit` on the Exit.
2. **Given** a Zone with exactly one Room and no Exits **when** either runs **then** neither warns
   `orphan_room`. The corpus's four one-Room valid cases stay finding-free.
3. **Given** a Room with two Exits `north` **when** either runs **then** both fail with
   `duplicate_direction` on the second Exit; `BuildWorld` returns no World.
4. **Given** `make content-conformance` **when** `make check` runs **then** all cases agree,
   including the amended `warn-missing-reverse-exit` sidecar.
5. **Given** `content.strict_orphans: true` **when** the AC-1 content boots **then** the load fails
   on `loft`, as `AW-SRV-001`'s strict mode always has.

## Interface contract

```go
// CONTRACT SKETCH — not an implementation
// server/sim/errors.go
ErrDuplicateDirection ErrCode = "duplicate_direction"

// content/lang: CodeDuplicateDirection = string(sim.ErrDuplicateDirection), moving it from the
// compiler-native block to the loader-defined block, as for every other shared code.
```

`orphan_room`, stated once for both implementations (`errors.md` §3.3): a Room is an orphan when its
Zone has more than one Room and no Exit from another Room in that Zone targets it.

## Data / state impact

None. A pack with a duplicated Direction that boots today will be refused after this lands. No such
pack exists in `content/`, `testdata/`, or the corpus. The unit suite proves that for the repo's
fixtures.

## Observability requirements

None new. `orphan_room` stays the same `warn` line from `AW-SRV-001`. `duplicate_direction` is logged
like every other validation finding.

## Test plan

- **Unit (`server/sim`):** AC-1, AC-2, AC-3, AC-5 as table cases beside the existing orphan tests.
  Any existing case that expects `orphan_room` on a one-Room Zone changes, and the PR says which
  cases.
- **Conformance:** AC-4, via `make content-conformance` in `make check`.
- **Unit (`content/lang`):** the chute and the one-Room Zone, alongside the conformance run.

## Definition of done

CLAUDE.md §8, plus: `AW-CLI-002`'s Open questions entry about equivalence names this story as
closed.

## Open questions

- None. The rule was decided at the `AW-CLI-005` review; see Context.
