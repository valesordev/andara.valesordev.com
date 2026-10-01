---
id: AW-SRV-036
title: goto — a Builder jumps to any Room
epic: EPIC-03
component: server
type: feature
status: review
size: M
depends_on: [AW-SRV-003, AW-SRV-014]
blocks: [AW-INF-023, AW-SRV-038, AW-SRV-041]
lane: implementation
risk: medium
---

## Context

A Builder who publishes a new pack has a Zone that nothing links to yet. A Builder can only publish
packs they hold (`AW-SRV-013`), so they can't add an Exit to it from someone else's Zone. Brian
decided (2026-09-26): **Builders get a `goto` command** to jump to a Zone to test it. Until the base
content exists, an Exit from Purgatory, the spawn Zone (`AW-SRV-037`), is the other way in.

`goto` is a game command. It goes through the same pipeline as `move`: parse, authorize, validate,
apply, and emit Events (CLAUDE.md §10). Only the `authorize` stage knows it's a Builder's. The
Gateway's verb table already gates a verb by role (`auth.VerbRoles`), so gating is a table entry.
A jump to another Zone crosses Partitions the way a cross-Zone `move` does (`AW-SRV-003` AC-9): the
source Zone applies the departure and `Produce`s an `Arrive` to the target Zone. So `goto` adds a
verb and a Command, not a new transport.

## User story

As a builder, I want to type `goto <zone>/<room>` in `andara-cli play` and be standing in that Room,
so that I can test a Zone I just activated without walking to it, or before anything links to it.

## Scope

### In scope
- The verb `goto <zone>/<room>`. It's gated to `builder` in the verb table, and `goto <room>` means
  a Room in the current Zone.
- A new `LoggedCommand` arm, `Goto goto = 19`, `{ target_zone_id = 1, target_room_id = 2 }`,
  applied in the source Zone. It's pinned in `docs/specs/protocol/andara/log/v1/log.proto`.
- Validate: the target Zone and Room exist in the World in effect. A missing one is a validate
  failure naming the reference, and the Character doesn't move.
- Apply:
  - within one Zone, relocate and emit `CharacterLeft` and `CharacterArrived`, both with an empty
    direction;
  - across Zones, emit `CharacterLeft` in the source and `Produce` an `Arrive` with an empty
    `from_direction`, `origin_zone_id` and `origin_room_id` set, as a cross-Zone `move` does.
- `goto` updates the Session's Binding exactly as `move` does: the Room for an in-Zone jump, and
  transit then the new Zone for a cross-Zone one.
- After arrival, the Room is described to the jumper: one `RoomDescribed`, addressed to the actor
  only (`ScopeEntities`), emitted after its `CharacterArrived` in the same apply.
  - In-Zone, the `Goto` apply emits it.
  - Across Zones, **the `Arrive` apply emits it**, for every `Arrive`. An `Arrive` doesn't know
    whether a `move` or a `goto` produced it, so this also gives a cross-Zone `move` its
    description (`AW-SRV-038` AC-2). An `Arrive` that lands at the Zone's fallback Room, because a
    swap removed the target, describes the fallback Room after its `EntityRelocated`.
- `andara-cli play` renders an empty `to_direction` as `<name> leaves.` The arrival already renders
  as `<name> arrives.`

### Out of scope
- `goto` for `game_master` or for an Entity other than one's own Character. Later, with the Game
  Master's powers.
- `goto <character>` (jump to a player). Not asked for.
- A Room-by-name search. The target is always an ID.
- Moving new Characters out of Purgatory to a start location. That's a `[NEEDS BRIAN]` mechanic
  (glossary, Start Location).

## Acceptance criteria

1. **Given** a Character whose Account holds `builder`, standing in `town/plaza` **when** they submit
   `goto docks/pier` **then**:
   - the next `RoomDescribed` is `The Pier`;
   - a bystander in `town/plaza` reads `<name> leaves.`;
   - a bystander in `docks/pier` reads `<name> arrives.`;
   - after the Session quits, `character list` shows the Character at `docks/pier`.
2. **Given** the same Builder **when** they submit `goto hall` in `town` **then** they're in
   `town/hall`, and the Command stayed in `town`'s Partition (no `Arrive` produced).
3. **Given** a Character whose Account lacks `builder` **when** they submit `goto docks/pier`
   **then** the Submit is refused at `authorize`:
   - it returns `PERMISSION_DENIED` with `ErrorInfo.reason` `not_authorized` and the detail
     `you may not goto`, the pipeline's existing denial;
   - no log offset is consumed;
   - one audit record is written (`auth.Authorizer`).
4. **Given** `goto nowhere/room` or `goto town/nowhere` **when** it's applied **then** validation
   fails with `unknown_zone` or `unknown_room` at stage `validate`, the Character hasn't moved, and
   the client receives a `CommandRejected` Event with that code and the message
   `there is no room <zone>/<room>`, naming the resolved reference.
5. **Given** `goto` with no argument **when** it's parsed **then** it's refused at `parse` with
   `missing_argument` (`arg` = `target`). **Given** a malformed reference (an empty part, more
   than one `/`, or characters outside the Content Language's ID set) **then** it's
   `invalid_argument`. Both return `INVALID_ARGUMENT` with the detail `usage: goto <zone>/<room>`,
   and neither reaches the log.
6. **Given** a `goto` to the Room the Character is already in **when** it's applied **then** it's a
   no-op with a fresh `look`, and no `CharacterLeft` or `CharacterArrived` is emitted.
7. **Given** a replay of a log containing cross-Zone `goto`s **when** it's recovered **then** the
   State Hash matches. `goto` is deterministic and reads no wall clock.
8. **Given** a `goto` into a Zone of a pack activated seconds earlier **when** it runs after that
   pack's Content Swap has applied **then** it succeeds. If it runs before, it's `unknown_zone`,
   and a retry after the swap succeeds.
9. **Given** an Operator acting as a Builder's Account **when** they `goto` **then** it succeeds,
   and the audit trail carries the real actor (`AW-SRV-008`'s act-as rule). **Given** an Account
   holding `operator` but not `builder`, not acting as anyone **then** `goto` is refused as in
   AC-3. Roles are a set, not a ladder.
10. **Given** a cross-Zone `goto` whose target Room is removed by a Content Swap applied before the
    `Arrive` **when** the `Arrive` applies **then** the Character lands in the target Zone's
    fallback Room, with `EntityRelocated{reason: room_removed}` and then that Room's
    `RoomDescribed`. It's never lost (`AW-SRV-012`).

## Interface contract

```protobuf
// Pinned in andara/log/v1/log.proto (contract review, 2026-09-28).
message Goto {
  string target_zone_id = 1;   // resolved before logging from "<zone>/<room>" or the current Zone
  string target_room_id = 2;
}
// LoggedCommand.command gains:  Goto goto = 19;
```

- Verb table entry: `{name: "goto", kind: goto, role: builder}`. No abbreviation and no aliases,
  so a player can't trigger it by typing a prefix of another verb.
- Syntax: `goto <zone>/<room>` | `goto <room>`, lowercase IDs as the Content Language names them.
- Events reused unchanged: `CharacterLeft{to_direction: ""}`, `CharacterArrived{from_direction: ""}`,
  and `RoomDescribed`.
- Errors, following `AW-SRV-003`'s taxonomy:

  | Stage | Condition | Code | Surface |
  |-------|-----------|------|---------|
  | parse | no argument | `missing_argument` | `INVALID_ARGUMENT` |
  | parse | malformed reference | `invalid_argument` | `INVALID_ARGUMENT` |
  | authorize | no `builder` | `not_authorized` | `PERMISSION_DENIED`, audited |
  | validate | unknown Zone or Room | `unknown_zone`, `unknown_room` | `CommandRejected` Event |

  No new code. `unknown_zone` is an apply-stage code today (a Command naming a Zone the World
  lacks). `goto` adds the pair `{stage="validate", code="unknown_zone"}`, and it's pre-seeded on
  `andara_command_rejected_total` with the others.

- `andara-cli play`: an empty `to_direction` renders `<name> leaves.`

## Data / state impact

`log.proto` gains one arm. It's additive, and `buf breaking` passes. A server rolled back past this
story can't apply a log containing `Goto`. That's the same forward-only rule every new arm has, and
no `dev` World predates M2.

## Observability requirements

*(SRE observability review, 2026-09-28. The first draft named a verb label on
`andara_command_rejected_total` and a "handoff series". Neither exists. Corrected against
`server/command/metrics.go` and `server/tickloop/loop.go`.)*

- **Metrics:** none new.
  - `andara_commands_total{verb}` and `andara_command_duration_seconds{verb,phase}` gain one bounded
    `verb` value, `goto`.
  - `andara_command_rejected_total` is labelled `{stage,code,pre_log}`, not by verb. A `goto`
    refusal counts there under its stage and code.
    - The post-log codes `unknown_zone` and `unknown_room` already exist.
    - The existing pre-log codes are `missing_argument` and `invalid_argument` at `parse`, and
      `not_authorized` at `authorize`. The contract's `usage` is not a code today.
    - If architecture keeps `usage`, it's added to `command.PreLogCodes`, so it's pre-seeded. If
      not, AC-5 uses the existing codes. Either way, the code set stays closed.
  - A cross-Zone `goto` produces an `Arrive`, whose apply in the target Zone counts under that
    record's own verb in `andara_command_duration_seconds{phase="post_log"}`, as a cross-Zone
    `move`'s does.
  - Room and Zone IDs are never labels (CLAUDE.md §7).
- **Logs:**
  - No new line. The tick's existing `debug` `command applied` line carries `verb=goto`, `actor`,
    `session_id`, `code`, `stage` and `trace_id`. It gains `from_room` and `to_room`, for `goto`
    only.
  - The authorize denial is the existing audited line, and counts on
    `andara_privileged_actions_total{action="authorize"}`.
- **Traces:**
  - The existing `command.execute` → `command.parse`, `command.authorize` at the Gateway, then
    `command.apply` in the source Zone, with `from_room` and `to_room` as span attributes.
  - A cross-Zone `goto`'s `Arrive` carries the `Goto` Command's `trace_id`, as `move`'s does
    (`server/sim/verbs.go`). The target Zone's `command.apply` then lands in the same trace, under
    the Gateway's root.
  - That's parentage, not a span link, and one trace shows both halves of the jump.
- **Alerts:** none.

## Test plan

- **Unit:** parse (both forms, malformed), the verb-table role gate, validate against a World
  missing the Zone or Room, same-Room no-op (AC-6), and the renderer's empty direction.
- **Integration:** against Redpanda, through the Gateway:
  - a cross-Zone `goto` with a bystander on each side (AC-1);
  - a same-Zone `goto` (AC-2);
  - a non-Builder refused (AC-3);
  - replay with a matching State Hash (AC-7).
- **Manual/operator:**
  ```
  andara-cli account set-roles <id> --role builder
  andara-cli play --character Aldric
  > goto docks/pier       # The Pier
  > goto town/plaza       # Market Plaza
  ```

## Definition of done

CLAUDE.md §8.

## Open questions

- **Consistent with Brian's 2026-09-27 decision** that a move describes the destination Room to
  the mover (`AW-SRV-038`): `goto` already emits `RoomDescribed` for the target Room.

- **Settled at contract review (2026-09-28):** a Builder can `goto` any Room in the World, not
  only Rooms in packs they hold. Pack scoping (`AW-SRV-013`) governs writes to the content store.
  Standing in a Room isn't one, and testing a link into someone else's Zone needs it.
- **Resolved 2026-09-30 (Brian, relayed on #273):** a bystander reads `<name> has arrived.` for any
  arrival that didn't come through an Exit leading back, a `goto` among them. This story ships the
  old `<name> arrives.`, per its contract. The new text, and the rule for when `from_direction` is
  empty (`event.proto`, amended 2026-09-30), are a carrier story's, whose AC supersedes AC-1's
  bystander text. See `docs/feedback/AW-SRV-036-goto.md`.

## Contract review (architecture, 2026-09-28)

SRE's observability review is in `docs/feedback/AW-SRV-036-goto.md`. The story is `ready`.

1. **`Goto goto = 19` is pinned in `log.proto`**, with its two fields. The file's "from 19 upward"
   note now reads 20. `AW-SRV-009`'s sketch claims 17–19 for other arms. That sketch is stale
   (17 and 18 went to `ContentSwap` and `MarkLinkdead`), and its numbers are re-pinned at its own
   review.
2. **No `usage` code.** AC-5 uses the existing parse codes, `missing_argument` and
   `invalid_argument`, with `usage: goto <zone>/<room>` as the detail. The code set stays closed,
   as SRE asked.
3. **AC-3 uses the pipeline's existing denial:** `not_authorized`, detail `you may not goto`. The
   draft's `goto requires builder` isn't a string the pipeline produces.
4. **`unknown_zone` at `validate`** is a new stage-and-code pair, not a new code. It's pre-seeded.
5. **Every `Arrive` describes the Room it lands in.** A cross-Zone `goto` needs it, and an
   `Arrive` can't tell a `goto` from a `move`. So this story delivers `AW-SRV-038`'s cross-Zone
   half, and `AW-SRV-038` now depends on this one. The fallback landing (AC-10) describes the
   fallback Room.
6. **A successful `goto` isn't audited** (SRE's question). It's a `LoggedCommand`, so the log
   already records it durably, with actor, Session and trace. The audit topic records what the
   log doesn't: authorization decisions and Admin actions. Revisit when `goto` reaches
   `game_master`, or can target another Entity.
7. **The Binding moves the way `move` moves it.** Without that, the next Command routes to the old
   Zone and is `actor_not_found`. AC-1's roster check now reads after quit, since the roster is
   written at unbind (`AW-SRV-014`).
8. **An Operator without `builder` is refused**, because roles are a set (AC-9). Brian's demo path
   is his Builder Account, or `--as` it.
9. **Linkdead** (`AW-SRV-015`) needs nothing. A linkdead Character submits nothing, and one that
   jumped and then dropped is linkdead where it stands.

## Implementation record (2026-09-30)

On `impl/aw-srv-036-goto`.
- **Before the log:** `goto` is a verb-table entry gated to `builder`, with no abbreviation and no
  alias, and a `room_ref` argument kind. Parse builds the `Goto` arm, and the pipeline fills a bare
  Room's Zone from the Binding.
- **After the log:** `sim.applyGoto` validates the target against the World in effect. An in-Zone
  jump relocates and describes. A cross-Zone jump leaves through an `Arrive` with no direction, and
  every `Arrive` now describes where it lands.
- **Bindings:** they follow the `CharacterLeft` and `CharacterArrived` that `goto` emits, exactly as
  for `move`.

Decisions are in `docs/feedback/AW-SRV-036-goto.md`, "Implementation, 2026-09-30".

| AC | Covered by | Result |
|----|------------|--------|
| 1 | `sim` `TestGoto_CrossZone`: a departure with no direction, an `Arrive` with origin, trace, Session and `client_ref`, then an arrival and `RoomDescribed` of The Pier. `smoke` `TestLive_Goto`: bystanders in the plaza and at the pier, and the roster at `docks/pier` after quit | pass; `TestLive_Goto` passes on a compose stack built from this branch at 9553a5a, with both the `town/plaza` spawn and `purgatory/start` (#269 merged in locally), alongside `stack-smoke`, `stack-play` and `stack-linkdead` (SRE on #274) |
| 2 | `command` `TestGoto_ParsesBothFormsAndFillsTheZone`: `goto hall` goes to `town`'s Partition carrying `town/hall`. `sim` `TestGoto_InZone`: no `Arrive` is produced, and the events are left, arrived, then described. `TestLive_Goto` checks the Partition on the stack | pass, and on the stack (SRE on #274) |
| 3 | `command` `TestGoto_NeedsBuilder`: a player is refused `not_authorized`, `you may not goto`, audited, with no offset consumed. `TestLive_Goto` checks `PERMISSION_DENIED` with reason `not_authorized` on the wire. Mutation-checked: without the role, the test fails | pass |
| 4 | `sim` `TestGoto_UnknownTarget`: `unknown_zone` and `unknown_room` at `validate`, `there is no room <zone>/<room>`, and nothing moves | pass |
| 5 | `command` `TestGoto_ParseRefusals`: `missing_argument` (`arg` `target`) and six malformed forms as `invalid_argument`, each with the detail `usage: goto <zone>/<room>`. No prefix reaches `goto`, and nothing is logged | pass |
| 6 | `sim` `TestGoto_SameRoomIsALook`: one `RoomDescribed`, nothing moved | pass |
| 7 | `sim` `TestGoto_ReplayMatchesTheStateHash`: two Engines agree on the State Hash at every tick, across cross-Zone jumps and their `Arrive`s | pass |
| 8 | `sim` `TestGoto_SeesAZoneOnceItsSwapHasApplied`: `unknown_zone` before `docks@1`'s swap, and the same `goto` succeeds after it | pass |
| 9 | `command` `TestGoto_NeedsBuilder`: an Operator without `builder` is refused, and an Operator acting as a Builder's Account (`auth.Store.ActAs`'s Principal shape) goes through | pass |
| 10 | `sim` `TestGoto_IntoAGoneRoomLandsAtTheFallback`: `EntityRelocated{room_removed}` then the fallback's `RoomDescribed`. Mutation-checked: without the `Arrive`'s description, the cross-Zone tests fail | pass |

**Instrumentation**, asserted by `tickloop` `TestPipeline_GotoLogsBothEnds`:
- The `command applied` line and the `command.apply` span carry `from_room` and `to_room` for
  `goto`, and for no other verb.
- `andara_command_duration_seconds{verb="goto",phase="post_log"}` counts.
- `goto` is pre-seeded on `andara_commands_total` from the verb table.
- `{validate, unknown_zone}` was already pre-seeded, as every post-log code is at both stages.

`make check` passes. `TestSnapshotCopyStaysInsideTheStallBudget` (#172) failed twice under
full-suite load, and passed alone on this branch and on `main`.

**The arrival text.** The Open questions `[ASSUMPTION]` about bystander wording is answered by Brian
(2026-09-30, recorded on #273): a bystander reads `<name> has arrived.` for a `goto`. This branch
still renders `<name> arrives.`, per the contract as written, until architecture amends it.

## §8 review (architecture, 2026-09-30): stays `review` on SRE's record only

Against `main` at `d996066`. Merged in #274 (`81c9e60`). The PR's `check`, `stack`, `cli-release` and
3-OS `determinism` are green. `TestLive_Goto` passed in `main`'s `stack` run 36789975363, and again
in this review against the compose stack at `d996066` (1.96 s). Every named test re-ran green.

| AC | Evidence | Result |
|----|----------|--------|
| 1 | `sim/goto_test.go` `TestGoto_CrossZone`; `TestLive_Goto`: bystanders get empty `to_direction` and `from_direction`, the jumper `RoomDescribed`, the roster `docks/pier` after quit | pass, as written (the wording moves, see Open questions) |
| 2 | `command/goto_test.go` `TestGoto_ParsesBothFormsAndFillsTheZone`; `TestGoto_InZone`; smoke | pass |
| 3 | `TestGoto_NeedsBuilder`: `not_authorized`, one audit record, nothing logged; `PERMISSION_DENIED` live | pass |
| 4, 5, 6 | `TestGoto_UnknownTarget`; `TestGoto_ParseRefusals` (six malformed forms, no abbreviation); `TestGoto_SameRoomIsALook` | pass |
| 7 | `TestGoto_ReplayMatchesTheStateHash`, two Engines in lockstep, on the 3-OS determinism matrix | pass. Recovery doesn't depend on the verb, so lockstep equality stands in for a recovered replay, and the test plan's Redpanda replay isn't required |
| 8 | `sim/content_test.go` `TestGoto_SeesAZoneOnceItsSwapHasApplied`, with a real swap | pass |
| 9 | `TestGoto_NeedsBuilder`, with a Principal shaped as `auth.Store.ActAs` returns it. The real actor in the audit trail is `AW-SRV-008`'s `act_as` record, and a successful `goto` isn't audited (contract review, item 6) | pass |
| 10 | `TestGoto_IntoAGoneRoomLandsAtTheFallback`, with the `Arrive`'s Room rewritten in place of a swap | pass. It gives the same state at apply, and the swap path is `AW-SRV-012`'s |

Mutation-checked, and all nine caught:
- the builder role dropped, or changed to `operator`;
- `Abbrev: true`;
- the pipeline's Zone fill removed;
- a cross-Zone goto relocated in-Zone;
- `Arrive` no longer describing;
- a goto `Arrive` given a direction;
- the `unknown_zone` check removed;
- `Jump` not copied.

The wire matches: `LoggedCommand.goto = 19`, and `Goto` fields 1–2 in `gen/`. The sim imports no clock,
network or OS, and `Jump` rides only on `Outcome`, so it's not state and not hashed.

**Implementation's decisions** (`docs/feedback/AW-SRV-036-goto.md`), all accepted:
- The bare Room's Zone is filled after authorize, before logging. The log still carries both fields.
  The `log.proto` comment and the contract sketch said "the parser", and now say the Gateway,
  before the log.
- Lowercasing predates this story and applies to every verb.
- Every `Arrive` describes where it lands, as contract-review item 5 said. That delivers
  `AW-SRV-038`'s cross-Zone half (below).
- `sim.Outcome.Jump` is internal. On a cross-Zone jump that falls back, the source Zone's line
  names the intended Room, not the fallback. That's accepted, and the README could say so.

**For `AW-SRV-038`** (PM and implementation):
- Its AC-2, a cross-Zone `move` describes, is already true on `main` (`TestMove_CrossZone`).
- AC-7's code is in place, but no `move`-specific test exists.
- What's left is the in-Zone `applyMove` description (AC-1) and AC-3 to AC-6.
- SRE's before-and-after `room_described` rate on `stack-play` already includes the cross-Zone
  moves, so only the in-Zone delta is 038's.

**Not holding** (implementation's file): `andara-cli sim` renders an empty `to_direction` as
`X leaves .` (`admin/cli/simcmd.go`), and goto's events reach it now.

**What closes it:** SRE's §8 instrumentation record:
- `andara_commands_total{verb="goto"}` and `andara_command_duration_seconds{verb="goto"}`;
- `andara_command_rejected_total`'s `not_authorized` and `unknown_zone` series;
- `andara_privileged_actions_total{action="authorize"}` for the denial;
- the `command applied` line's `from_room` and `to_room`. It logs at `debug`, and compose runs at
  `info`, so SRE observes it with the level raised or rules the unit assertion enough;
- the `command.apply` span's `from_room` and `to_room`, with the cross-Zone `Arrive`'s
  `command.apply` under the same trace.

Architecture then moves the story to `done` without another pass.

## §8 instrumentation check (2026-10-01, SRE): satisfied

On `sre/sprint-03-srv036-srv038-verify`, against the compose stack built from `main` at `b9cc8e6`
with `ANDARA_LOG_LEVEL=debug` and fresh volumes. A Builder (`set-roles --role builder`) played
`goto docks/pier` from Purgatory, then `goto pier`, `goto nowhere/x` and a bare `goto`. A player
with no role played `goto docks/pier`.

| Signal | Backend | Observed |
|--------|---------|----------|
| `andara_commands_total{verb="goto"}` | local Prometheus | `0` before (pre-seeded), `4` after |
| `andara_command_duration_seconds{verb="goto"}` | local Prometheus | `pre_log` and `post_log` counts of 3 each (the parse refusal never reaches either) |
| `andara_command_rejected_total` | local Prometheus | `{parse, missing_argument}` 1, `{authorize, not_authorized}` 1, and `{validate, unknown_zone}` 1. The last is the new pair the contract pre-seeds |
| `andara_privileged_actions_total{action="authorize"}` | local Prometheus | `0` → `1`, the player's refusal |
| `debug` `command applied` | local Loki | `verb=goto` with `actor`, `session_id`, `trace_id`. `from_room=purgatory/start`, `to_room=docks/pier` on the jump. The refusal carries `stage=validate`, `code=unknown_zone`. The player's refusal is the `info` `command rejected` line with `code=not_authorized` |
| Trace | local Tempo (`95284008…`) | `command.execute` (`verb=goto`) → `command.parse`, `command.authorize`, then `command.apply` (`verb=goto`, Partition 32, `from_room`, `to_room`), and the target Zone's `command.apply` (`verb=arrive`, Partition 11). All are under the same parent: one trace for both halves of the jump, by parentage. The in-Zone `goto pier` applies on Partition 11 only |

**Noted, not holding the story.** All of a `play` session's Commands share one trace ID, so this
trace holds the bind, the `look` and all three `goto`s. It's `play`'s behavior, not this story's,
and §7's per-Command parentage still holds within it. And `goto pier` from `docks/pier` named the
Room the Builder was already in, so this run didn't observe a true in-Zone jump between two
Rooms. The unit and smoke tests cover that (`TestLive_Goto`).
