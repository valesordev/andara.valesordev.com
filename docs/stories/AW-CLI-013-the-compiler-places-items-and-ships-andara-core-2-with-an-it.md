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
- **The names Component type, registered on the server**: in `server/sim/component.go`'s component
  registry, with `AW-CLI-012`'s field kinds. Component types are defined on the server (ADR-0010
  decision 7). The compiler (`sim.KnownComponentType`) and the server's loader both reject a type
  the registry doesn't hold, so this comes first.
- The parser, resolver and emitter in `content/lang` for `AW-CLI-012`'s placement and names rule,
  with its error codes.
- `AW-CLI-012`'s pending corpus cases move to `valid/` and `invalid/`. The anchors they change move
  with them:
  - `content/core/templates` (`TestCoreSeedIsReproducible`);
  - `testdata/templates` (`TestTownAnchorMatchesLoaderFixtures`).
- `content/core/` at version 2: the compiled `andara.core.Item` carrying the names Component,
  `VERSION` set to `2`, and a new line in `VERSIONS`. `andara-cli`'s embedded core becomes `@2`.
- `content/fixtures/town/`, the `dev` fixture, moves to `requires andara.core@2`: its `Lantern` gains
  names, and one `Lantern` is placed in `town/plaza`. So `dev` and `make stack-play` have an Item to
  find.

### Out of scope
- Item Instances, verbs and state in the server: `AW-SRV-047`. Until it lands, the server parses
  placements (the field is in `gen/` from `AW-CLI-012`) and doesn't instantiate them.
- The Content Repository's packs. Builders author those once `andara-cli` ships `@2`.

## Acceptance criteria

1. **Given** the corpus with `AW-CLI-012`'s cases moved out of `pending/` **when** `make check` runs
   **then** `content-conformance` passes with every new case, each error at its expected file, line
   and code, and no case is left pending on `AW-CLI-013`.
2. **Given** `content/core/VERSION` at `2` **when** `make check` runs **then** `core-versions-check`
   passes, `andara-cli version` names `andara.core@2`, and both anchor tests pass.
3. **Given** a pack that `requires andara.core@1` and places no Items **when** the `@2` binary
   validates it **then**:
   - with `andara.core@1` in the cache (`andara-cli content fetch-core --from <an @1 core>`), it
     validates;
   - with it not cached, it's `core_version_mismatch`, whose remedy is to change `requires` to `2`
     (`errors.md` §3.1).

   **Given** a pack built against `@2` **when** an `@1` `andara-cli` validates it **then**
   `core_version_mismatch` tells the Builder to upgrade `andara-cli` (#308's direction rule).
4. **Given** the `town` fixture with its placed `Lantern` **when** `make stack-play` runs **then** it
   passes. The fixture loads and the server ignores the placement, since the server parses the field
   but doesn't act on it until `AW-SRV-047`.

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
