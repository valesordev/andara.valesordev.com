---
id: AW-INF-024
title: Every environment spawns new Characters in Purgatory
epic: EPIC-01
component: infra
type: infra
status: review
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
  - both players walk `out`, then `look`, before the existing steps. B goes first, and its
    `look` reading `Market Plaza` is the existing "B is in the plaza" wait. Then A walks `out` and
    `look`s, and the existing plaza-to-hall steps follow. A move describes no Room today (the
    script's own comment), so the `look` after `out` is what reads the plaza.
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
3. **Given** `make stack-play` **when** it runs **then** it passes. Each player's transcript shows
   `Purgatory` first, then `Market Plaza` from the `look` after `out`, then A's existing walk
   north. B's transcript shows A's arrival line in the plaza (its wording is whatever `AW-SRV-037`
   AC-3 leaves it) before `<A> leaves north.`.
4. **Given** `make stack-linkdead` **when** it runs **then** it passes, with both players in
   `town/plaza` for the drop and reconnect.
5. **Given** each environment after this story **when** its server boots **then** it reaches
   Ready. The spawn Room must exist, and a server whose content lacks it exits `1` naming it
   (`AW-SRV-014`'s existing rule and test). So a Ready pod in each environment is the evidence
   that its content has `purgatory/start`: the compose stack (AC-2), `local` (`make helm-test`),
   and `dev` after the merge rolls it.

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

- **Unit:** `helm-test` asserts AC-1 on each environment's render, and a `local` pod reaching
  Ready (AC-5).
- **Integration:** the `stack` workflow runs `stack-play` and `stack-linkdead` (AC-3, AC-4).
- **Manual/operator:**
  ```
  make down VOLUMES=1 && make up && make build
  make stack-play            # transcript: Purgatory, then Market Plaza
  ```

## Definition of done

CLAUDE.md §8.

## Open questions

- **Ordering with `AW-INF-021`.** Until `AW-INF-021` lands, `dev` reads `testdata/content/valid/`
  from a ConfigMap, which has Purgatory once `AW-SRV-037` merges. After it, `dev`'s spawn Room is
  in the store's `town` pack (`content/fixtures/town/`, which `AW-SRV-037` also adds). Either
  order works, and neither needs this story to change.

- **Resolved 2026-09-28 (architecture): `prod` changes too**, since Brian said "all characters".
  `prod` serves no players yet.

## Contract review (architecture, 2026-09-28)

SRE's observability review is in `docs/feedback/AW-INF-024-purgatory-spawn.md`: no change. The
story is `ready`.

1. **Both players walk out, not just A.** `stack_play.sh` waits for B to read `Market Plaza` before A
   moves, so that B witnesses A. With Purgatory as the spawn Room, B spawns there too, and the wait
   would never pass. B walks out first and A second, so B is in the plaza to see A arrive.
2. **`look` after `out`.** A move describes no Room until `AW-SRV-038`, so the plaza is read from a
   `look`. If `AW-SRV-038` lands first, the move's own description appears before the `look`. The
   script's ordered matching finds the first `Market Plaza` either way.
3. **AC-5 is now observable per environment.** As written, it restated `AW-SRV-014`'s rule and
   tested nothing new. A Ready pod is the per-environment evidence, since boot refuses a missing
   spawn Room.
4. **`prod` changes too.** It serves no players, and its content is the same test content, so the
   `[ASSUMPTION]` holds with nothing to resolve. It was never a contract question.

## Implementation record (SRE, 2026-09-30)

On `sre/aw-inf-024-purgatory-spawn`.

| AC | How | Result |
|----|-----|--------|
| 1 | `helm_test.test_spawn_room_is_purgatory`: `ANDARA_CHARACTER_SPAWN_ROOM=purgatory/start` in the `local`, `dev` and `prod` renders and in the compose server's environment. Where the chart renders content (`local`, `dev`), `andara-content` holds `purgatory/start`. Mutation-checked: `town/plaza` in `prod.yaml`, and then in the compose file, each fails it naming the environment | pass |
| 2 | `stack-play` on a fresh compose stack: `character list` shows `<A>  dormant  purgatory/start` | pass |
| 3 | `stack-play`'s client half: A's transcript reads `Purgatory`, then `Market Plaza` after `out`, then `leaves north` and `Town Hall`. B's reads `<A> arrives from the in.` before `<A> leaves north.` | **the client half passes. The Go half, `TestLive_M1Gate`, fails** on the plaza spawn. It's implementation's (`docs/feedback/AW-INF-024-purgatory-spawn.md`), and this PR waits for it |
| 4 | `make stack-linkdead` on the same stack: both walk `out`, then drop, mark, reconnect and quit in `town/plaza` | pass |
| 5 | The compose server is Ready (AC-2's run). `local` renders the Room (AC-1), and the `kind` workflow installs it. `dev` is observed after the merge rolls it | compose pass; `local` in CI; `dev` owed after merge |

**The arrival wording** out of Purgatory is `arrives from the in.` The Exit is one-way, and the
move rule names the reverse of `out`. AC-3 accepts whatever AW-SRV-037 left, so both gates match
`arrives( from the <dir>)?.`. The prose question is Brian's (feedback file).

**Contract gap:** the scope missed `internal/smoke/m1_test.go`, which asserts the spawn Room too.
It's routed to implementation, with a spawn-agnostic fix so it can land before this PR.
