---
id: AW-SRV-036
title: goto — a Builder jumps to any Room
epic: EPIC-03
component: server
type: feature
status: draft
size: M
depends_on: [AW-SRV-003, AW-SRV-014]
blocks: [AW-INF-023]
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
- A new `LoggedCommand` arm, `Goto { target_zone_id, target_room_id }`, applied in the source
  Zone. Its number is the next free one, assigned at contract review.
- Validate: the target Zone and Room exist in the World in effect. A missing one is a validate
  failure naming the reference, and the Character doesn't move.
- Apply:
  - within one Zone, relocate and emit `CharacterLeft` and `CharacterArrived`, both with an empty
    direction;
  - across Zones, emit `CharacterLeft` in the source and `Produce` an `Arrive` with an empty
    `from_direction`, as a cross-Zone `move` does.
- After arrival, an automatic `look` in the target Room, as spawn does.
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
   - `character list` later shows the Character at `docks/pier`.
2. **Given** the same Builder **when** they submit `goto hall` in `town` **then** they're in
   `town/hall`, and the Command stayed in `town`'s Partition (no `Arrive` produced).
3. **Given** a Character whose Account lacks `builder` **when** they submit `goto docks/pier`
   **then** the Submit is refused at `authorize`:
   - it returns `PERMISSION_DENIED` with `goto requires builder`;
   - no log offset is consumed;
   - one audit record is written (`auth.Authorizer`).
4. **Given** `goto nowhere/room` or `goto town/nowhere` **when** it's applied **then** validation
   fails with `unknown_zone` or `unknown_room` naming the reference, the Character hasn't moved, and
   the client receives a `CommandRejected` Event naming the code.
5. **Given** `goto` with no argument, or a malformed reference **when** it's parsed **then** it's
   refused at `parse` with `usage: goto <zone>/<room>`, before the log.
6. **Given** a `goto` to the Room the Character is already in **when** it's applied **then** it's a
   no-op with a fresh `look`, and no `CharacterLeft` or `CharacterArrived` is emitted.
7. **Given** a replay of a log containing cross-Zone `goto`s **when** it's recovered **then** the
   State Hash matches. `goto` is deterministic and reads no wall clock.
8. **Given** a `goto` into a Zone of a pack activated seconds earlier **when** it runs after that
   pack's Content Swap has applied **then** it succeeds. If it runs before, it's `unknown_zone`,
   and a retry after the swap succeeds.
9. **Given** an Operator acting as a Builder's Account **when** they `goto` **then** it succeeds,
   and the audit trail carries the real actor (`AW-SRV-008`'s act-as rule).

## Interface contract

```protobuf
// CONTRACT SKETCH — not an implementation. andara/log/v1/log.proto
message Goto {
  string target_zone_id = 1;   // resolved at parse from "<zone>/<room>" or the current Zone
  string target_room_id = 2;
}
// LoggedCommand.command gains:  Goto goto = <next free, assigned at review>;
```

- Verb table entry: `{name: "goto", kind: goto, role: builder}`. No abbreviation and no aliases,
  so a player can't trigger it by typing a prefix of another verb.
- Syntax: `goto <zone>/<room>` | `goto <room>`, lowercase IDs as the Content Language names them.
- Events reused unchanged: `CharacterLeft{to_direction: ""}`, `CharacterArrived{from_direction: ""}`,
  and `RoomDescribed`.
- Errors, following `AW-SRV-003`'s taxonomy:

  | Stage | Condition | Code |
  |-------|-----------|------|
  | parse | no or malformed argument | `usage` |
  | authorize | no `builder` | `PERMISSION_DENIED` |
  | validate | unknown Zone or Room | `unknown_zone`, `unknown_room` |

- `andara-cli play`: an empty `to_direction` renders `<name> leaves.`

## Data / state impact

`log.proto` gains one arm. It's additive, and `buf breaking` passes. A server rolled back past this
story can't apply a log containing `Goto`. That's the same forward-only rule every new arm has, and
no `dev` World predates M2.

## Observability requirements

- **Metrics:** `andara_commands_total`, `andara_command_rejected_total` and
  `andara_command_duration_seconds` gain the verb `goto` wherever they label by verb: one bounded
  value. The cross-Zone path counts on the existing handoff series as a `move` does.
- **Logs:** `debug` `goto applied` with `session_id`, `character_id`, source and target Room. The
  authorize denial goes through the existing audited line.
- **Traces:** the existing command spans. A cross-Zone `goto` links the source apply to the
  target's `Arrive` apply, as `move` does.
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

- `[ASSUMPTION]` A Builder can `goto` any Room in the World, not only Rooms in packs they hold.
  Reading a Room isn't a write, and testing a link into someone else's Zone needs it.
- `[ASSUMPTION]` Bystanders see the ordinary `<name> leaves.` and `<name> arrives.`, with no special
  wording for a jump. The wording is Brian's to change, and it doesn't affect the wire.
