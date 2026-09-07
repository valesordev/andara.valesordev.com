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
- A human-authorable source format for content, and the compile step to the canonical protobuf.
- `content publish` — compile, validate locally, upload blobs, create a version manifest.
- `content activate` — move the Active Pointer. Deliberately separate from publish, per `AW-SRV-013` AC-2.
- `content rollback` — activate a prior version, which is one pointer move.
- `content history` and `content diff` between versions, which the version chain makes possible.
- Clear reporting of the server's rejection findings, which are the same findings `content validate`
  produces locally.

### Out of scope
- The server-side publish path — `AW-SRV-013`.
- In-game building. `[NEEDS BRIAN]` on whether it is a design goal; it would publish through the same
  topics, so nothing here forecloses it.
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

- `[NEEDS BRIAN]` The authoring format. YAML is the obvious default and is diffable and familiar; a
  purpose-built DSL reads better for room descriptions and exits and is more work. This decides how
  pleasant world-building feels, which for a MUD is not a small thing.
- `[NEEDS BRIAN]` Whether activation should require a second approver. Publish and activate are already
  separate, so a two-person rule for a live world is nearly free here.
- `[NEEDS BRIAN]` Whether in-game building is a design goal, carried from ADR-0004.
