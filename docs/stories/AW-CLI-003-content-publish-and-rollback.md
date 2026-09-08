---
id: AW-CLI-003
title: andara-cli content publish, activate, and rollback with a human-authorable format
epic: EPIC-05
component: cli
type: feature
status: draft
size: M
depends_on: [AW-CLI-001, AW-CLI-002, AW-SRV-013]
blocks: []
assignee: cursor
risk: medium
---

> `status: draft` — unblocked by ADR-0004 and scoped. Groomed to `ready` when M3 approaches.

## Context

This is the Builder's whole workflow. ADR-0004 put content in the data layer so Builders need no
repository access; this command is how they reach it.

It also carries a cost ADR-0007 named explicitly: **protobuf is not hand-authorable.** The canonical
format is protobuf, and if the only way to write a Zone is to write protobuf text, Builders will hate this
and the decision to keep them out of the repository will have bought nothing. Defining a human-friendly
authoring format that compiles to the canonical one is in this story's scope, not a later nicety.

## User story

As a builder, I want to write a Zone in a format I can read, publish it, see it live, and roll it back if
I was wrong, so that world-building is a fast loop rather than a release process.

## Scope

### In scope
- The **grammar specification** for the Content Language (ADR-0009), including the Game Type
  hierarchy from ADR-0010, and the compile step to the canonical protobuf. The compiler resolves
  `extends` chains and emits flattened definitions with the chain retained in the manifest. The decision to build a purpose-built text language rather than map YAML onto
  the protobuf is made; the syntax is this story's to design, with a Builder in the room.
- `content publish` — compile, validate locally, upload blobs, create a version manifest.
- `content activate` — move the Active Pointer. Deliberately separate from publish, per `AW-SRV-013`
  AC-2, and now also because activation requires a second approver: this command **surfaces** the
  approval state and renders the rejection when an unapproved version is activated. It enforces
  nothing — the server is the security boundary (`AW-SRV-013`).
- `content rollback` — activate a prior version, which is one pointer move.
- `content history` and `content diff` between versions, which the version chain makes possible.
- Clear reporting of the server's rejection findings, which are the same findings `content validate`
  produces locally.

### Out of scope
- The server-side publish path — `AW-SRV-013`.
- In-game building — a design goal as of 2026-09-07, for Admins and Builders rather than players, with
  its own epic (`EPIC-11`) and explicitly not Phase 1. It publishes through the same three topics, so
  nothing here forecloses it. The one thing this story should avoid is accumulating assumptions that
  make `andara-cli` the *only* writer, because it is now known not to be.
- Enforcement of the two-person rule — `AW-SRV-013`.
- Concurrent editing and conflict resolution. ADR-0004 is explicit that last-pointer-move-wins is the
  whole concurrency model, and the CLI should make that visible rather than hide it.

## Acceptance criteria (known now; completed at grooming)

1. **Given** content in the authoring format **when** `content publish` runs **then** it compiles,
   validates locally, uploads only blobs the server does not already have, and creates a version manifest
   — **without** moving the Active Pointer.
2. **Given** a published version **when** `content activate --version N` runs **then** the pointer moves
   and the change is visible in the running world without a deploy.
3. **Given** a bad activation **when** `content rollback` runs **then** the previous version is active
   again, and the bad version remains published and inspectable.
4. **Given** local validation passing but server-side validation failing **when** publish runs **then** the
   server's findings are printed in the same format as local ones, and the exit code distinguishes "your
   content is wrong" from "the server is unreachable", per `AW-CLI-001`'s taxonomy.
5. **Given** `content history <pack>` **when** it runs **then** every version ever published is listed with
   author, timestamp, and which is active.
6. **Given** `content diff <pack> N M` **when** it runs **then** it shows what changed between two versions
   at the level of Zones, Rooms, and Exits — not as a protobuf byte diff, which would be useless.
7. **Given** the authoring format **when** a Builder writes a Zone by hand **then** it round-trips: compile
   to canonical, decompile back, and produce the same source. Without this, the format is write-only and
   `content diff` is the only way to read anything back.

## Interface contract

To be written at grooming, including the authoring format itself — which is the substantial design work in
this story and should not be decided in passing.

## Data / state impact

None locally beyond a compile cache. All state changes go through `AW-SRV-013`'s audited server-side path.

## Observability requirements

Per `AW-CLI-001`. The one addition worth naming: `content activate` is a change to a live world, so the
command prints the pack, both version numbers, and the author, and waits for confirmation unless `--yes` is
passed. A live-world mutation should be hard to do by accident.

## Test plan

Round-trip of the authoring format (AC-7); publish-without-activate; rollback asserting the newer version
survives; server-rejection formatting matching local formatting; blob dedup asserted by upload count.

## Definition of done

CLAUDE.md §8, plus: the authoring format has a specification in `docs/specs/schema/`, and the round-trip
test gates merges — a format that cannot be read back is not an authoring format.

## Open questions

- **Resolved 2026-09-07 (Brian): a purpose-built, text-based language**, recorded as ADR-0009. Text
  is explicit — diffable, greppable, reviewable. What this story now owes is the grammar itself, plus
  three things ADR-0009 flags as the difference between a compiler that helps and one that does not:
  error messages that name a line rather than a field, a formatter so that Builders do not argue about
  layout, and a corpus of source files that must keep compiling, which is the only mechanical check
  the language will have against breaking changes.
- **Resolved 2026-09-07 (Brian): activation requires a second approver.** Not as a compliance control
  — as a way to let more people contribute content while keeping a human moderation step in front of
  the live World. `AW-SRV-013` enforces; this command surfaces.
- **Resolved 2026-09-07 (Brian): in-game building is a design goal**, for Admins and Builders, not
  players. `EPIC-11`, not Phase 1.
- **Gated by ADR-0010** (`proposed`, 2026-09-08), which specifies the Game Type hierarchy this
  grammar must express: single inheritance, value overrides, new values, no removal, and no functions
  — those live in Python Behaviors (ADR-0005). This story does not reach `ready` until that ADR is
  accepted, because a language that cannot express subtyping has to change to add it, and changing
  this language is a breaking change to everything already authored.
