---
id: AW-SRV-034
title: Loader and compiler agree on orphan_room and duplicate_direction
epic: EPIC-02
component: server
type: bug
status: done
size: S
depends_on: [AW-SRV-001, AW-CLI-006]
blocks: [AW-CLI-002, AW-SRV-037]
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
loader refuses them too, but as `malformed_file` ("duplicate exit direction"), so the two report
different codes for one finding. *(Corrected 2026-09-29, architecture, from implementation's
feedback: this said the loader "sorts and keeps both". It never did.)*
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
  `z.aw:4:3: orphan_room` with chain `z`, `loft`, sorted ahead of the existing
  `missing_reverse_exit`. *(Corrected 2026-09-29: `loft`'s `room` keyword is on line 4, not 2.
  Architecture writes the sidecar, since `docs/specs/` is architecture's.)*
- `errors.md` prose, by architecture in the same merge (2026-09-29): `duplicate_direction` moves from
  §3.1 to §3.2, and the §3.3 note about the chute case is replaced. The sidecar is different: it has to
  change with the compiler, or `make check` goes red in between.

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
   `duplicate_direction` on the second Exit, and it's the only finding for that Zone: neither
   reports `orphan_room` for the Room the refused Exit named (`errors.md` §1 rule 7, which binds
   the loader too). `BuildWorld` returns no World. The loader's detail names the Direction and both
   targets, as the compiler's message does. *(Amended 2026-09-29, architecture, from the review
   of #161.)*
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

None. A pack with a duplicated Direction was already refused at boot, as `malformed_file`. After
this lands it's refused as `duplicate_direction`, which is the same outcome with a new code. None of
the 17 Zone files under `content/` and `testdata/` has one (checked while writing this story).
*(Corrected 2026-09-29, with the Context.)*
Builder packs in `valesordev/andara.solo7.media` compile unchanged, apart from the new code on an
already-fatal duplicate and `orphan_room` on a Room nothing enters, which is a warning, exit `0`. The
corpus's `invalid/semantic/duplicate-direction/` is compiler input, not loader input.

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

## Contract amendment (architecture, 2026-09-29): AC-3 after the review of #161

Implementation had started, so this is recorded here and in the feedback file.
- **Only errors when the load fails.** `TestBuildWorld_LoaderAgreesWithCompiler`'s "two Exits
  north" case expected `duplicate_direction` *and* `orphan_room` for `yard`. The compiler reports
  only the error (`errors.md` §1 rule 7), so the two still disagreed on exactly the input AC-3
  names. Rule 7 now binds the loader too, and AC-3 says so. A strict-mode orphan is an error, so
  AC-5 is unchanged.
- **The loader's detail names both targets.** `errors.md` §3.2 states it, and `AW-SRV-013`'s
  publish gate shows the loader's detail to a Builder. Detail isn't part of the three-way
  equivalence (code, position and chain), but it's the contract's text.

## §8 review (architecture, 2026-09-30): stays `review` on SRE's record only

Against `main` at `79fd622`. Merged in #162 (`7f403f2`). `check` and `stack` on `7f403f2` were
cancelled because #157 superseded them 25 s later, and they're green on `3593d46`, which contains
this story. Re-run in this review: the `server/sim`, `server/content`, `server/boot` and
`content/lang` tests, and `make content-conformance` (72 cases agree).

| AC | Evidence | Result |
|----|----------|--------|
| 1 | `TestBuildWorld_LoaderAgreesWithCompiler` "a Room you can leave but never enter" (`[missing_reverse_exit, orphan_room]`, orphans `[loft]`); corpus `valid/warn-missing-reverse-exit/expected.errors`; `TestOrphanIsInbound` "the chute" | pass |
| 2 | the one-Room cases of the same test (no Exits; an Exit out of the Zone); `TestOrphanIsInbound` "a Zone of one Room" | pass |
| 3 | "two Exits north": exactly `[duplicate_direction]`, no `orphan_room` for `yard`, nil World, detail names both targets; corpus `invalid/semantic/duplicate-direction` | pass |
| 4 | `make content-conformance` in `CHECK_TARGETS` and a `ci.yaml` step | pass |
| 5 | "chute under strict_orphans": `[orphan_room]` only, nil World. The config key → option path is `TestLoadContent_StrictOrphansRefusesToBoot` | pass |

Mutation-checked in a scratch copy. Dropping the loader's one-Room exemption fails the one-Room
cases and `TestLoadContent_ValidThreeZones`. Returning every finding instead of only the refusals
fails "two Exits north" and "chute under strict".

Checklist: tests run in CI (`make test`, `make content-conformance`). No config, no migration, no
new domain term. No `[ASSUMPTION]`. `AW-CLI-002`'s Open questions name this story closed.
`duplicate_direction` is in `AllErrCodes`, so its failure series is pre-seeded.

**Not holding the story, for implementation** (`docs/feedback/AW-SRV-034-loader-compiler-agree.md`):
- `server/README.md`'s refusal list lacks `duplicate_direction`. The `content.strict_orphans` row
  doesn't state the one-Room exemption, and the refusal section doesn't state that a refused load
  reports only its errors. That's DoD's "documented in the component README", owed on the next
  `server/README.md` touch.
- The "two Exits north" case doesn't assert the finding's line, which is the second Exit's. The
  code uses it (`build.go:290`), and the corpus case pins it on the compiler side. It's a gap in
  the loader test, not a defect.

**What closes it:** SRE's §8 instrumentation record. The story specifies no new series, and the
`orphan_room` warn and the validation-failure counter are `AW-SRV-001`'s. Once SRE records that
here, architecture moves the story to `done` without another pass.

## §8 instrumentation check (2026-09-30, SRE): satisfied

On `sre/sprint-03-review-verify`. The story adds no instrument. Its obligation is that the two
findings are logged as every other finding is, so the check is those lines on a real backend. The
compose stack's server image at `79fd622` ran `--validate-only` twice, with
`ANDARA_SERVICE_NAME=andara-server-verify`, over a copy of `testdata/content/valid/` plus one Zone
`z`, shipping to the stack's collector:

| Content | Exit | Findings (stdout) | Local Loki, `{service_name="andara-server-verify"}` |
|---------|------|-------------------|-----|
| `loft` -down-> `cellar`, nothing into `loft` (AC-1) | `0` | `missing_reverse_exit` on `z/loft`, `orphan_room` on `z/loft`, and Purgatory's `missing_reverse_exit`. No `orphan_room` on the one-Room `purgatory` (AC-2) | `\| code="orphan_room"`: one `warn` line, `zone=z`, `room=loft`, `file`, `trace_id` |
| `hall` with two Exits `north`, to `yard` and `shed` (AC-3) | `1` | `duplicate_direction` alone: `duplicate exit direction "north": to z/yard, and again to z/shed`. No `orphan_room` | `\| code="duplicate_direction"`: one `error` line, same fields |

`duplicate_direction` is in `sim.AllErrCodes`, so `AW-SRV-013`'s `andara_content_validation_failures_total{code}`
pre-seeds it. Nothing is carried forward.

## §8 close (architecture, 2026-09-30): `done`

SRE's instrumentation record (2026-09-30, above) is accepted: the story's findings reach a real
backend with their required fields, and nothing is carried forward. It was the only item the
architecture review left owed. Every checklist item now holds.
