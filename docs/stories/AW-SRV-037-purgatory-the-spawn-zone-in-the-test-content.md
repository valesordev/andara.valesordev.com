---
id: AW-SRV-037
title: Purgatory — the spawn Zone in the test content
epic: EPIC-02
component: server
type: feature
status: draft
size: S
depends_on: [AW-SRV-014]
blocks: [AW-INF-024]
lane: implementation
risk: low
---

## Context

Brian decided (2026-09-26) that every new Character spawns in **Purgatory**, a Zone they wait in
after creation before moving to their start location. Until the base content exists, Purgatory's
way out is an Exit into the test town. Today `character.spawn_room` is `town/plaza`, the dev
fixture's Room (Brian, 2026-09-21), and `testdata/content/valid` has no Purgatory.

This story adds Purgatory to the test content and changes nothing else, so no existing assertion
moves. `AW-INF-024` then points every environment's `character.spawn_room` at it, and updates the
scripts that assert on the spawn Room, in one pass. Splitting it this way keeps `main` green: adding
a Zone breaks nothing, and the switch lands with the assertions it changes.

## User story

As a builder, I want new Characters to start in Purgatory with a way out to the test town, so that
the spawn point is a place of its own, not a Room in whatever content happens to be loaded.

## Scope

### In scope
- `testdata/content/valid/purgatory.json`: Zone `purgatory` with one Room, `purgatory/start`
  titled `Purgatory`, and `fallback start`.
- One Exit, `out -> town.plaza`. It's one-way, as Brian asked ("just having an exit from the start
  Zone"), so it carries the `missing_reverse_exit` warning.
- Every test that counts the valid fixture's Zones or Rooms, or lists its Zones, updated for the
  fourth Zone.
- The fixture's byte-identical copies, if any hold it (`TestCoreSeedMatchesFixture`'s pattern),
  updated with it.

### Out of scope
- Changing `character.spawn_room` anywhere: `AW-INF-024`.
- The dev fixture pack in `dev`'s store: `AW-INF-021`, which includes Purgatory.
- Moving a Character from Purgatory to a start location automatically. That's `[NEEDS BRIAN]`
  (glossary, Start Location).
- Purgatory's prose. The description stays a placeholder (Brian, 2026-09-27).

## Acceptance criteria

1. **Given** `testdata/content/valid` **when** the server boots with `content.source=dir` **then**
   it loads four Zones, including `purgatory` with the Room `purgatory/start`, and
   `andara_content_zones_loaded` is `4`.
2. **Given** that load **when** its findings are read **then** the only new one is
   `missing_reverse_exit` on `purgatory/start`'s `out` Exit, at `warning`, and boot succeeds.
3. **Given** a Character placed in `purgatory/start` **when** it submits `out` **then** it arrives
   in `town/plaza`, and a bystander there reads its arrival. `from_direction` is whatever the
   existing `move` rule sets for an Exit with no reverse. This story doesn't change it.
4. **Given** the server's default `character.spawn_room` (`town/plaza`) **when** `make check` and
   the `stack` workflow run **then** both pass unchanged. No existing assertion moves in this story.

## Interface contract

- Zone `purgatory`, title `Purgatory`, `fallback start`.
- Room `purgatory/start`, title `Purgatory`, description
  `Placeholder: Purgatory's description is Brian's to write.`
- Exits: `out -> town.plaza`. Nothing else.
- No configuration, protocol, or schema change.

## Data / state impact

A compose stack or `local` cluster built before this story has a log whose genesis swap names the
old fixture's digest. Recovery rebuilds `content.source=dir` content from the directory and
compares, so it halts with a digest mismatch (`AW-SRV-012`). The recovery is the one it names:
`make down VOLUMES=1` locally. `dev` reads the store after `AW-INF-021`, so it isn't affected.

## Observability requirements

- **Metrics / Logs / Traces / Alerts:** none new. AC-1 and AC-2 read the existing
  `andara_content_zones_loaded` and `andara_content_load_warnings_total{kind}`.

## Test plan

- **Unit:** the loader on the valid fixture: four Zones, and one `missing_reverse_exit` warning
  (AC-1, AC-2).
- **Integration:** a Character placed in `purgatory/start` walks `out` to `town/plaza` (AC-3).
  The existing suite passes unchanged (AC-4).
- **Manual/operator:** none. Nothing spawns in Purgatory until `AW-INF-024`.

## Definition of done

CLAUDE.md §8.

## Open questions

- **Resolved 2026-09-27 (Brian):** the Room ID `purgatory/start` and the Exit direction `out`
  stand, and the description stays a placeholder. After `AW-INF-024` makes Purgatory the spawn Room,
  renaming either means renaming it in every environment's `character.spawn_room` too.
