---
id: AW-CLI-013
title: The compiler places Items and ships andara.core@2 with an Item's names
epic: EPIC-05
component: cli
type: feature
status: draft
size: M
depends_on: [AW-CLI-012, AW-INF-029]
blocks: [AW-SRV-047]
lane: implementation
risk: medium
---

## Context

`AW-CLI-012` specifies how a Builder names an Item and places it in a Room, and that `andara.core`
gains the names Component as version 2. This story builds it in `content/lang`, ships the new core
seed, and makes `andara-cli content validate`, `compile` and `publish` accept it. It's the first
`andara.core` bump. So it inherits `AW-INF-021` AC-6, and it's the first real test of #308's
skew-direction remedy and `AW-INF-029`'s append-only `VERSIONS`.

## User story

As a builder, I want `andara-cli content validate` and `publish` to accept the Items I name and
place, and to tell me exactly what's wrong when I get one wrong, so that my pack reaches `dev`
with its Items in it.

## Scope

### In scope
- The parser, resolver and emitter in `content/lang` for `AW-CLI-012`'s placement and names
  Component, with its error codes.
- `content/core/` at version 2: the compiled `andara.core.Item` carrying the names Component,
  `VERSION` set to `2`, and a new line in `VERSIONS`. `andara-cli`'s embedded core becomes `@2`.
- `content/fixtures/town/`, the `dev` fixture, gains one placed Item, so `dev` and `make stack-play`
  have an Item to find.

### Out of scope
- The server: `AW-SRV-047`.
- The Content Repository's packs. Builders author those once `andara-cli` ships `@2`.

## Acceptance criteria

1. **Given** the `AW-CLI-012` corpus **when** `make check` runs **then** `content-conformance` passes
   with every new case, each error at its expected file, line and code.
2. **Given** `content/core/VERSION` at `2` **when** `make check` runs **then** `AW-INF-029`'s
   append-only check passes, and `andara-cli version` names `andara.core@2`.
3. **Given** a pack built against `andara.core@1` **when** `andara-cli content validate` runs with
   the `@2` binary **then** it validates. **Given** a pack built against `@2` **when** an `@1`
   `andara-cli` validates it **then** `core_version_mismatch` tells the Builder to upgrade
   `andara-cli` (#308's direction rule).
4. **Given** the `town` fixture with its placed Item **when** `make stack-play` runs **then** it
   passes. The server ignores placements until `AW-SRV-047`, which is why this criterion asserts
   only that the fixture still loads.

## Interface contract

As `AW-CLI-012` specifies. The error codes, the placement's compiled field, and the names
Component's schema are that story's. This story adds no CLI flags.

## Data / state impact

The first `andara.core` bump. `dev` publishes `andara.core@2` at boot on the first roll after merge
(`AW-INF-021` AC-6). Every Builder pack active before the roll stays active, since none was built
against a newer core.

## Observability requirements

- **Metrics / Traces / Alerts:** none added.
- **Logs:** none added. `AW-INF-021` AC-6's observation is the server's existing boot line
  (`content core: andara.core@2 published; activated`).

## Test plan

- **Unit:** the compiler's corpus runner, plus a test of each new error code.
- **Integration:** `content-conformance`, `core-versions-check`, `make stack-play`.
- **Manual/operator:** after merge, `andara-cli server info` against `dev` lists `andara.core@2`.

## Definition of done

CLAUDE.md §8, plus:
- **Inherited from `AW-INF-021` AC-6** (the first core bump): on the roll that carries `@2` to `dev`,
  the new pod's boot line reports `andara.core@2` published and activated, `server info` lists it
  once Ready, and every Builder pack active before the roll is still active. Its §8 record names the
  roll.

## Open questions

- None of its own. It builds `AW-CLI-012`'s decisions.
