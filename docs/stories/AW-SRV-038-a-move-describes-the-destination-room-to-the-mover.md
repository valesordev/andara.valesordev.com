---
id: AW-SRV-038
title: A move describes the destination Room to the mover
epic: EPIC-03
component: server
type: feature
status: review
size: S
depends_on: [AW-SRV-003, AW-SRV-036]
blocks: [AW-SRV-041]
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
- An in-Zone `move`: the apply emits `RoomDescribed` for the destination Room to the mover only
  (`ScopeEntities`), after its `CharacterArrived`, with the fields `look` produces.
- A cross-Zone `move`: the target Zone's `Arrive` apply emits it, so the description reads the
  target Zone's content. **`AW-SRV-036` builds this**, since every `Arrive` describes the Room it
  lands in. This story asserts it for `move` and changes no `Arrive` code.
- Bystanders' Events are unchanged: they read `leaves` and `arrives`, not the description.
- Golden recordings, and any test that counts the mover's Events, updated in the same PR.
- `scripts/stack_play.sh` is SRE's (CLAUDE.md §2), not architecture's. Its walk assertion
  searches forward through the transcript, so the extra description passes with no script change
  (AC-6). Its comment "A move describes no Room" goes stale, and SRE updates it.

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
6. **Given** `make stack-play` **when** it runs, with `scripts/stack_play.sh` unchanged, **then**
   it passes. Its walk still finds `Market Plaza`, then `<name> leaves north.`, then `Town Hall`,
   in that order. The order within the move (no `look` between the arrival and the description)
   is AC-1's, asserted on the Event stream, not on the rendered transcript.
7. **Given** a cross-Zone `move` whose target Room a Content Swap removed before the `Arrive`
   applies **when** it lands at the fallback Room **then** the mover reads `EntityRelocated` and
   then the fallback Room's `RoomDescribed`. That's `AW-SRV-036` AC-10's rule, asserted here for
   `move`.

## Interface contract

- No protocol change. `RoomDescribed` is the existing Event, emitted to the mover's Session with
  the same fields `look` produces.
- `andara-cli play` is unchanged: it already renders `RoomDescribed`.

## Data / state impact

None. The log's Commands are unchanged, and the extra Event isn't state.

## Observability requirements

- **Metrics / Logs / Traces / Alerts:** none new. The Event is counted wherever Events already are.
- *(SRE observability review, 2026-09-28.)* Expected shift, stated so the §8 check doesn't read it
  as a regression: `andara_events_emitted_total{type="room_described"}` rises by one per successful `move`. `andara_stream_events_sent_total` rises by one
  per mover. The per-tick cost is one Event per move, under the 50 ms budget's noise floor at the
  sizing fixture. The §8 record states the before and after rate on `make stack-play`, and states
  that `andara_tick_duration_seconds` p99 didn't move.

## Test plan

- **Unit:** the `move` apply, in-Zone and cross-Zone, asserts the mover's Event order (AC-1, AC-2),
  the bystanders' (AC-3), and the refusal (AC-4).
- **Integration:** determinism on replay (AC-5), and `make stack-play` (AC-6).
- **Manual/operator:** `andara-cli play`, then `north`: the Town Hall's description appears without
  a `look`.

## Definition of done

CLAUDE.md §8.

## Open questions

- **Settled at contract review (2026-09-28):** the description follows `CharacterArrived` on the
  same Tick. It doesn't replace it. The mover's stream keeps every Event it has today, so the wire
  only gains. What `andara-cli play` prints for the mover's own arrival is unchanged by this story.

## Contract review (architecture, 2026-09-28)

SRE's observability review is in `docs/feedback/AW-SRV-038-move-describes-room.md`. The story is
`ready`.

1. **No protocol change is confirmed.** `RoomDescribed` (`event.proto`) already carries what a
   `look` shows, and the new emission is addressed to the actor only, as `look`'s is. It's an
   Event, not a new message or a new field.
2. **The cross-Zone half moves to `AW-SRV-036`.** A cross-Zone `goto` needs the `Arrive` apply to
   describe the Room, and an `Arrive` can't tell which verb produced it. So one change serves
   both, and it lands with the earlier story in the sprint. This story now depends on
   `AW-SRV-036`, and keeps AC-2 as a `move` assertion.
3. **`scripts/stack_play.sh` is SRE's**, not architecture's. It passes unchanged: its assertion
   searches forward. AC-6 no longer claims an ordering the script doesn't check. AC-1 asserts that
   ordering on the Event stream.
4. **AC-7 adds the fallback landing**, since that's a way a mover arrives that the draft didn't
   cover.
5. **The mover's own `CharacterArrived` stays.** Dropping it would take an Event off the wire,
   which changes the protocol for no gain. A client can choose what to print.

## Implementation record (2026-09-30)

On `impl/aw-srv-038-move-describes`. An in-Zone `move`'s apply now emits `RoomDescribed` for the
destination, to the mover alone (`ScopeEntities`), after its `CharacterArrived`. It uses the same
`describeTo` a `look` and an `Arrive` use. The cross-Zone half came with AW-SRV-036's `Arrive`
change, and it's asserted here for `move`. Notes, and what's for SRE, are in
`docs/feedback/AW-SRV-038-move-describes-room.md`, "Implementation, 2026-09-30".

| AC | Covered by | Result |
|----|------------|--------|
| 1 | `sim` `TestMove_InZone`: left, arrived, then `RoomDescribed` of Town Hall on the same Tick, to alice alone, equal (`proto.Equal`) to what her `look` then returns. `events` `TestScope_RoomMove`: the mover's stream is those three. Mutation-checked: without the emit, six tests fail | pass |
| 2 | `sim` `TestMove_CrossZone`: the target Zone's `Arrive` apply emits the description of the Forest Trail, to alice alone | pass |
| 3 | `events` `TestScope_RoomMove`: the bystander in the source Room sees one `CharacterLeft`, the one in the destination one `CharacterArrived`, and neither sees a `RoomDescribed`. `TestClientRefOnlyToOwnSession`: the description carries the mover's `client_ref` to the mover only | pass |
| 4 | `sim` `TestMove_NoSuchExit`: the rejection is the only Event | pass |
| 5 | `sim` `TestMove_ReplayIsIdentical`: two Engines on the same log, in-Zone and cross-Zone moves with their `Arrive`s, emit equal Events and agree on the State Hash at every tick | pass |
| 6 | `make stack-play` with `scripts/stack_play.sh` unchanged, on a compose stack built from this branch at 249e025 with fresh volumes. `stack-smoke` and `stack-linkdead` pass too, and the transcript shows each move's own description ahead of the `look`'s (SRE on #278) | pass |
| 7 | `sim` `TestArrive_IntoAGoneRoomLandsAtTheFallback`: a cross-Zone `move` into a gone Room reads `EntityRelocated`, then the fallback's `RoomDescribed` | pass |

Tests that counted a mover's Events are updated in this PR: `sim`'s `TestMove_InZone`,
`TestValidateFailure_SkipsApply` and `TestStep_BuildsConsumedContexts`, and `events`'
`TestScope_RoomMove`, `TestOrdering` and `TestClientRefOnlyToOwnSession`.

Every `make check` target passes. `TestSnapshotCopyStaysInsideTheStallBudget` (#172) failed under
full-suite load on three runs, and passed five times alone and with its package. The suite outside
`simtest` passes, and so does every target after `test`, each run on its own.

## §8 review (architecture, 2026-09-30): stays `review` on SRE's record only

Against `main` at `3f06943`. Merged in #278. The PR's `check`, `stack`, `cli-release` and 3-OS
`determinism` are green. `main`'s run on the merge was in progress at this review. Re-run here: the
`sim` and `events` tests the record names.

Every AC passes on the implementation record's evidence:
- ACs 1–5 and 7: `sim`'s `TestMove_InZone`, `TestMove_CrossZone`, `TestMove_NoSuchExit`,
  `TestMove_ReplayIsIdentical` and `TestArrive_IntoAGoneRoomLandsAtTheFallback`, and `events`'
  `TestScope_RoomMove` and `TestClientRefOnlyToOwnSession`.
- AC-6: `make stack-play` on a stack built from the branch, SRE on #278, plus the PR's `stack` job.

**Mutation-checked in this review:** stubbing out the in-Zone `describeTo` fails six tests,
`TestMove_InZone`, `TestValidateFailure_SkipsApply`, `TestStep_BuildsConsumedContexts`,
`TestScope_RoomMove`, `TestOrdering` and `TestClientRefOnlyToOwnSession`. AC-2's and AC-7's code
is `AW-SRV-036`'s, already mutation-checked in that story's review.

Checklist: no protocol change, as the contract says. No config, no migration, no new term. No
`[ASSUMPTION]`. The renderer golden (`events.jsonl`) is left as it is, and that's right: it's a
catalogue of Event kinds, not one mover's stream.

**The arrival rule isn't here.** `docs/feedback/AW-SRV-036-goto.md` recommended this story as the
carrier for Brian's `<name> has arrived.` and the `from_direction` rule. #278 merged before that
ruling did. So the bystander still reads `arrives from the in.` on Purgatory's `out`, and the rule
still needs a carrier. That's PM's, and it doesn't reopen this story.

**Not holding, SRE's:** `scripts/stack_play.sh`'s comment "A move describes no Room" is stale now.

**What closes it:** SRE's §8 instrumentation record, with the shift the Observability section
predicts measured on `make stack-play`:
- `andara_events_emitted_total{type="room_described"}`, before and after;
- `andara_stream_events_sent_total`, before and after;
- `andara_tick_duration_seconds` p99, unmoved.

The "before" already includes the cross-Zone moves' descriptions since #274, so only the in-Zone
delta is this story's. Architecture then moves the story to `done` without another pass.

## §8 instrumentation check (2026-10-01, SRE): satisfied

On `sre/sprint-03-srv036-srv038-verify`. The rates the Observability section asks for come from
`make stack-play`'s walk (the script up to its server-restart step, so that no counter resets), read
exactly from the server's `/metrics` before and after. Two builds were compared, each on fresh
compose volumes: `447559e`, the commit before #278 merged, and `main` at `b9cc8e6`.

| | before #278 | after #278 |
|---|---:|---:|
| `andara_events_emitted_total{type="room_described"}` | +15 | **+18** |
| `andara_stream_events_sent_total` | +44 | **+47** |
| move verbs submitted (`out` 4, `north` 2, `south` 1, `west` 2) | 9 | 9 |
| `no_such_exit` refusals | 2 | 2 |
| `andara_tick_duration_seconds` p99 over the walk | ≤ 1 ms (first bucket) | ≤ 1 ms (first bucket) |

**+3, not +7, and that's the contract.** Seven moves succeeded, but four were cross-Zone `out`s
from Purgatory. A cross-Zone arrival was already described by `AW-SRV-036`'s `Arrive`, which
`447559e` contains, and this story's Interface contract says it changes no `Arrive` code. What it adds
is the in-Zone description, and the walk had exactly three successful in-Zone moves (`north` ×2,
`south` ×1). So the shift is one `room_described`, and one stream Event to the mover, per successful
in-Zone move. Tick p99 didn't move. *(The Observability section's "one per successful `move`" was
written before `AW-SRV-036` existed. Measured against a pre-036 build it would be one per move.)*

**`stack_play.sh` now proves the move's own description** (the contract review's "For SRE"). The
stale comment is replaced. Between `<A> leaves north.` and the `west` rejection, the walk requires
two `Town Hall` lines: the move's own description, then the `look`'s. Mutation-checked: on the
`447559e` build it fails with `Town Hall described 1 time(s) … want 2`, and on `main` it passes.
