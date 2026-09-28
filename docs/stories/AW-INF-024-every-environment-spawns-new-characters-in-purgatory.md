---
id: AW-INF-024
title: Every environment spawns new Characters in Purgatory
epic: EPIC-01
component: infra
type: infra
status: draft
size: S
depends_on: [AW-SRV-037]
blocks: [AW-INF-023]
lane: sre
risk: low
---

## Context

Brian decided (2026-09-26) that every new Character spawns in Purgatory. `AW-SRV-037` adds the Zone
to the test content without moving the spawn Room. This story moves it, in every environment and in
every script that asserts on it, in one pull request so `main` stays green.

The spawn Room is configuration (`character.spawn_room`, `AW-SRV-014`). Its value today:
- `values/dev.yaml` and `values/prod.yaml` set `town/plaza`;
- compose and `local` take the server default, also `town/plaza`.

After this story, each environment sets it explicitly, so the server default stops mattering.
`scripts/stack_play.sh` asserts a new Character reads `town/plaza` as `dormant  town/plaza` and sees
`Market Plaza` first, and `make stack-linkdead` (`AW-INF-017`) places both players there. Both learn
that a Character arrives in Purgatory and walks `out` to the plaza.

## User story

As an operator, I want every environment to spawn new Characters in Purgatory, so that the spawn
point is the same everywhere, and the gates prove the path a real player takes.

## Scope

### In scope
- `character.spawn_room: purgatory/start` in `values/local.yaml`, `values/dev.yaml`,
  `values/prod.yaml` and the compose server's environment.
- `stack_play.sh`:
  - a new Character is `dormant  purgatory/start` before its first `play`;
  - its first `look` reads `Purgatory`;
  - it walks `out` to `Market Plaza` before the existing plaza-to-hall steps.
- `stack_linkdead.sh` (`AW-INF-017`): both players walk `out` before the linkdead steps, which stay
  in `town/plaza`.
- The dev fixture pack (`AW-INF-021`) includes Purgatory, so `dev`'s spawn Room exists in its store.
- The SPRINT-01 demo's expected output isn't edited, because it records a past run. The next demo
  written uses Purgatory.

### Out of scope
- Purgatory's content: `AW-SRV-037`.
- The server's compiled-in default. It stays `town/plaza`, which is `AW-SRV-014`'s default and
  implementation's to change. Every environment overrides it after this story.
- Moving new Characters out of Purgatory automatically. That's `[NEEDS BRIAN]` (glossary, Start
  Location).

## Acceptance criteria

1. **Given** each environment's rendered configuration (`make k8s-dry` for `local`, `dev` and
   `prod`, plus the compose file) **when** it's read **then** `ANDARA_CHARACTER_SPAWN_ROOM` is
   `purgatory/start` in all four.
2. **Given** a fresh compose stack **when** `character create` and then `character list` run
   **then** the list shows `<name>  dormant  purgatory/start`.
3. **Given** `make stack-play` **when** it runs **then** it passes. Its transcript shows `Purgatory`
   first, then `Market Plaza` after `out`, then the existing walk north.
4. **Given** `make stack-linkdead` **when** it runs **then** it passes, with both players in
   `town/plaza` for the drop and reconnect.
5. **Given** a server whose loaded content lacks `purgatory/start` **when** it boots **then** it
   exits 1, naming the spawn Room. That's `AW-SRV-014`'s existing rule; this AC only confirms each
   environment's content has the Room.

## Interface contract

- Configuration: `character.spawn_room` / `ANDARA_CHARACTER_SPAWN_ROOM` = `purgatory/start`,
  explicit in every environment. It's an existing key, with no new keys.
- Make targets unchanged. `stack-play` and `stack-linkdead` keep their names, exit codes, and final
  lines.

## Data / state impact

A dormant Character keeps its position, so existing Characters stay where they are. Only Characters
that have never been bound start in Purgatory. A local stack from before `AW-SRV-037` needs
`make down VOLUMES=1` anyway (that story's Data/state).

## Observability requirements

- **Metrics / Logs / Traces / Alerts:** none new.

## Test plan

- **Unit:** `helm-test` asserts AC-1 on each environment's render.
- **Integration:** the `stack` workflow runs `stack-play` and `stack-linkdead` (AC-3, AC-4).
- **Manual/operator:**
  ```
  make down VOLUMES=1 && make up && make build
  make stack-play            # transcript: Purgatory, then Market Plaza
  ```

## Definition of done

CLAUDE.md §8.

## Open questions

- `[ASSUMPTION]` `prod` changes too, since Brian said "all characters". `prod` serves no players
  yet.
