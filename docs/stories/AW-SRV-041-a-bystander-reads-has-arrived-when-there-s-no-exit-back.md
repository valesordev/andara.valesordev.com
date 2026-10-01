---
id: AW-SRV-041
title: A bystander reads has arrived when there's no Exit back
epic: EPIC-02
component: server
type: feature
status: draft
size: S
depends_on: [AW-SRV-036, AW-SRV-038]
blocks: []
lane: implementation
risk: low
---

## Context

A bystander watching a Character arrive reads `<name> arrives from the <dir>.`, where `<dir>` is
the reverse of the Direction the mover took. That's wrong whenever the destination Room has no Exit
back that way. Purgatory's one-way `out` gives `arrives from the in.`, and a `goto` or a first bind
gives a bare `<name> arrives.`. Brian decided (2026-09-30) that an arrival with no way back reads
`<name> has arrived.`.

Architecture pinned the rule in `event.proto` at the review of `AW-SRV-036` (#279):
`CharacterArrived.from_direction` is set only when the destination Room has an Exit in that
Direction leading back to the Room the mover left. Architecture recommended `AW-SRV-038` as the
carrier, but #278 merged it before the ruling did, so the rule gets its own story. The source is
`docs/feedback/AW-SRV-036-goto.md`, "For PM: the carrier". It isn't on SPRINT-03's demo path.

## User story

As a player, I want an arrival to name the Direction someone came from only when there's a way back
that way, so that what I read matches the Room I'm standing in.

## Scope

### In scope
- In `server/sim`:
  - `applyMove` (within one Zone) sets `CharacterArrived.from_direction` per the rule;
  - `applyArrive` (across Zones) sets it the same way, deciding from the destination Zone's World
    at that tick, against `Arrive.origin_zone_id` and `origin_room_id`.
- In `admin/cli` (`render.go`, `simcmd.go`) and `sim`'s text rendering, an empty `from_direction`
  renders as `<name> has arrived.`.

### Out of scope
- The log. `Arrive.from_direction` keeps the reverse Direction ("the Direction it came through"),
  unchanged.
- Tightening the `stack-play` and `stack-linkdead` patterns. They already accept both texts (#269).
  SRE may tighten them afterwards.
- What the mover sees. That's `AW-SRV-038`, which is done.

## Acceptance criteria

1. **Given** a `move` through an Exit whose destination Room has the reverse Exit back to the origin
   Room **when** it applies **then** `CharacterArrived.from_direction` is the reverse Direction, and
   the bystander reads `<name> arrives from the <dir>.`, unchanged.
2. **Given** a `move` through a one-way Exit (Purgatory's `out` into the plaza) **then**
   `from_direction` is empty, and the bystander reads `<name> has arrived.`.
3. **Given** a `goto`, or a first bind into Purgatory **then** `from_direction` is empty, and the
   bystander reads `<name> has arrived.`.
4. **Given** a cross-Zone `move`, with and without a reverse Exit in the destination Zone **then**
   ACs 1 and 2 hold, decided by the destination Zone at `Arrive`.
5. **Given** the change **when** `make stack-play` and `make stack-linkdead` run **then** both pass
   with the new text.
6. **Given** a destination Room with an Exit in the reverse Direction that leads somewhere other than
   the origin Room **then** `from_direction` is empty. "Back" means back to where the mover was.

## Interface contract

- `CharacterArrived.from_direction` (`event.proto` field 4), as its comment pins it: set iff the
  destination Room has an Exit in that Direction whose target is the origin Room. It's decided where
  the event is emitted, from the World at that tick.
- Rendering: non-empty gives `<name> arrives from the <dir>.`, and empty gives `<name> has arrived.`.
  This applies to `andara-cli play` and to `sim`'s text output alike.
- No log, protocol-field or State Hash change. Events aren't hashed, so replaying an older log
  changes only the text a bystander reads.

## Data / state impact

None. The log is unchanged, and so is the State Hash.

## Observability requirements

- **Metrics:** none new. It changes an event's field value on existing paths.
- **Logs:** none new.
- **Traces:** none new. `command.apply` already covers `move`, `goto` and bind.
- **Alerts:** none.

## Test plan

- **Unit:** `sim` covers ACs 1–4 and 6, each asserting both `from_direction` and the rendered text.
  It includes Purgatory's `out`, a `goto`, a first bind, and a reverse-Direction Exit to a third
  Room.
- **Integration:** `make stack-play` and `make stack-linkdead` (AC-5).
- **Manual/operator:** with two `andara-cli play` sessions in Purgatory, one goes `out`, and the
  other, waiting in the plaza, reads `<name> has arrived.`.

## Definition of done

CLAUDE.md §8.

## Open questions

None. The rule is architecture's (#279), and the text is Brian's (2026-09-30).
