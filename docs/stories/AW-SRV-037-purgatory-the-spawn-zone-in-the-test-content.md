---
id: AW-SRV-037
title: Purgatory — the spawn Zone in the test content
epic: EPIC-02
component: server
type: feature
status: review
size: S
depends_on: [AW-SRV-014]
blocks: [AW-INF-024, AW-INF-021]
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
- `content/fixtures/town/`: the dev fixture's Content Language source, which `AW-INF-021`'s
  `make content-seed` publishes as pack `town`. It's the `.aw` files of the spec corpus's
  `valid/town/` case, copied once, plus `purgatory.aw` for the Zone above. From then on it's its
  own copy: the corpus case doesn't change, and doesn't have to track the fixture
  (`docs/feedback/AW-INF-021-dev-content-store.md`, item 3).
- `TestDevFixtureSourceMatchesTestContent`: compiles `content/fixtures/town/` with the embedded
  `andara.core` and holds every Zone Definition and `town.*` Template byte-equal to
  `testdata/content/valid/`.

### Out of scope
- Changing `character.spawn_room` anywhere: `AW-INF-024`.
- Publishing the fixture to any store: `AW-INF-021`.
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
5. **Given** `content/fixtures/town/` **when** `make check` runs **then**
   `TestDevFixtureSourceMatchesTestContent` compiles it with no `error` findings, and each of its
   four Zone Definitions and its `town.*` Templates is byte-equal to the same file in
   `testdata/content/valid/`.
6. **Given** a one-character change to `testdata/content/valid/purgatory.json` alone **when**
   `make check` runs **then** that test fails, naming the file.

## Interface contract

- Zone `purgatory`, title `Purgatory`, `fallback start`.
- Room `purgatory/start`, title `Purgatory`, description
  `Placeholder: Purgatory's description is Brian's to write.`
- Exits: `out -> town.plaza`. Nothing else.
- Fixture source: `content/fixtures/town/`, with `pack.aw` declaring `pack town requires
  andara.core@1`, the corpus case's `town.aw`, `docks.aw`, `wilds.aw`, `items.aw` and `npcs.aw`,
  and a new `purgatory.aw`. The compiled output's `source` fields name these paths.
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
  (AC-1, AC-2). `TestDevFixtureSourceMatchesTestContent`, and its mutation check (AC-5, AC-6).
- **Integration:** a Character placed in `purgatory/start` walks `out` to `town/plaza` (AC-3).
  The existing suite passes unchanged (AC-4).
- **Manual/operator:** none. Nothing spawns in Purgatory until `AW-INF-024`.

## Definition of done

CLAUDE.md §8.

## Open questions

- **Resolved 2026-09-27 (Brian):** the Room ID `purgatory/start` and the Exit direction `out`
  stand, and the description stays a placeholder. After `AW-INF-024` makes Purgatory the spawn Room,
  renaming either means renaming it in every environment's `character.spawn_room` too.

## Contract review (architecture, 2026-09-28)

SRE's observability review is in `docs/feedback/AW-SRV-037-purgatory.md`: no change. The story is
`ready`.

1. **The fixture's Content Language source is added here** (feedback item 3 in
   `docs/feedback/AW-INF-021-dev-content-store.md`). `AW-INF-021` publishes `dev`'s fixture from
   Content Language, and `testdata/content/valid/` is compiled JSON. So the source needs a home
   that isn't the spec corpus, and a test that holds the two equal. `content/` is
   implementation's, and this story already adds Purgatory. AC-5 and AC-6 are new.
2. **`AW-INF-019`'s AC-4 is supplied by this story's merge**, even though the PR also changes tests
   and `content/`. Neither is rendered by the Application. The PR also builds an image, so `dev`
   rolls for the digest too (AC-5 there allows two rolls). The AC-4 evidence is therefore not
   "the StatefulSet rolled". It's the `andara-content` ConfigMap's data equal to `main`'s, and the
   pod template's `checksum/content` annotation changed, within the deadline. SRE records both.
   It has to be observed before `AW-INF-021` lands, since that story stops rendering the ConfigMap
   on `dev`. The sprint's order already does that (SRE item 3 before item 8).
3. **The Exit is `out -> town.plaza`,** which is the Content Language's cross-Zone form. The JSON
   form is `to_zone: town, to_room: plaza`. The source and the compiled file agree because AC-5
   holds them equal.
