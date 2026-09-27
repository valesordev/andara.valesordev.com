---
id: AW-SRV-038
title: A move describes the destination Room to the mover
epic: EPIC-03
component: server
type: feature
status: draft
size: S
depends_on: [AW-SRV-003]
blocks: []
lane: implementation
risk: low
---

## Context

Today a mover reads `<name> leaves north.` and `<name> arrives from the south.`, then has to type
`look` to see where they are. Brian decided (2026-09-27): **a move describes the destination Room to
the mover.** This was SPRINT-02's game-design question 1.

`move` is `AW-SRV-003`'s verb. Its apply emits `CharacterLeft` in the source Room and
`CharacterArrived` in the destination, or `Produce`s an `Arrive` to the target Zone when the Exit
crosses a Zone (AC-9). `look` emits `RoomDescribed` to the actor. This story makes the arrival emit
the same `RoomDescribed` to the mover, in the Zone that applies the arrival. The Event already exists
and `andara-cli play` already renders it, so this is a server change with no new protocol.

`AW-SRV-036` (`goto`) already describes the destination to the jumper. This story brings `move` into
line, so every way a Character arrives shows the Room.

## User story

As a player, I want to see the Room I walk into, so that I don't have to type `look` after every
step.

## Scope

### In scope
- An in-Zone `move`: the apply emits `RoomDescribed` for the destination Room to the mover, after
  its `CharacterArrived`.
- A cross-Zone `move`: the target Zone's `Arrive` apply emits it, so the description reads the
  target Zone's content.
- Bystanders' Events are unchanged: they read `leaves` and `arrives`, not the description.
- Golden recordings, and any test that counts the mover's Events, updated in the same PR.
- `scripts/stack_play.sh` is architecture's. Its assertions search forward through the transcript,
  so an extra description should still pass (AC-6). If it doesn't, the contract review names the
  script change and who lands it.

### Out of scope
- An arrival that isn't a move: a bind, a reconnect, or a relocation. A bind already describes the
  Room.
- A brief mode, or any per-player setting to turn it off.
- Changing the `RoomDescribed` Event's fields.

## Acceptance criteria

1. **Given** a Character in `town/plaza` with a `north` Exit to `town/hall` **when** `move north` is
   applied **then** the mover's stream carries `CharacterLeft`, then `CharacterArrived`, then
   `RoomDescribed` for `town/hall`, all on the same Tick.
2. **Given** a cross-Zone Exit **when** the mover takes it **then** the `RoomDescribed` comes from
   the target Zone's apply of `Arrive`, and describes the target Room.
3. **Given** a bystander in the source or destination Room **when** the move applies **then** its
   Events are the same as before this story. It reads no `RoomDescribed`.
4. **Given** a `move` refused with `no_such_exit` **when** it's applied **then** there's no
   `RoomDescribed`, as before.
5. **Given** a replay of the same log **when** it runs **then** the Events and the State Hash are
   identical (§4 determinism). The description is an Event, not state.
6. **Given** `make stack-play` **when** it runs **then** it passes, and its transcript shows
   `Town Hall` straight after `<name> arrives from the south.`, with no `look` between them.

## Interface contract

- No protocol change. `RoomDescribed` is the existing Event, emitted to the mover's Session with
  the same fields `look` produces.
- `andara-cli play` is unchanged: it already renders `RoomDescribed`.

## Data / state impact

None. The log's Commands are unchanged, and the extra Event isn't state.

## Observability requirements

- **Metrics / Logs / Traces / Alerts:** none new. The Event is counted wherever Events already are.

## Test plan

- **Unit:** the `move` apply, in-Zone and cross-Zone, asserts the mover's Event order (AC-1, AC-2),
  the bystanders' (AC-3), and the refusal (AC-4).
- **Integration:** determinism on replay (AC-5), and `make stack-play` (AC-6).
- **Manual/operator:** `andara-cli play`, then `north`: the Town Hall's description appears without
  a `look`.

## Definition of done

CLAUDE.md §8.

## Open questions

- `[ASSUMPTION]` The description follows `CharacterArrived` on the same Tick, rather than
  replacing the mover's own `arrives from` line. Whether the mover still reads their own arrival is
  a rendering detail architecture may settle at contract review.
