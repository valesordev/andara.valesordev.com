---
id: AW-SRV-047
title: Items in the World — placed at activation, get, drop, inventory, examine
epic: EPIC-02
component: server
type: feature
status: draft
size: M
depends_on: [AW-CLI-013, AW-SRV-012, AW-SRV-014, AW-SRV-007, AW-SRV-028]
blocks: []
lane: implementation
risk: medium
---

## Context

M4 needs Items (`docs/roadmap.md`), and content is about to define them (Brian, 2026-10-03). Today
the server loads Item Definitions (`TemplateKind_ITEM`, `AW-SRV-022`), but no Item exists in the
World: none can be placed, and no verb touches one. `AW-SRV-022` deferred "Items as placeable
instances in Rooms" to "the inventory story". This is that story. `AW-CLI-012` specifies how a
Builder names and places Items, and `AW-CLI-013` compiles them and ships `andara.core@2`.

Brian's decisions for the first Items (2026-10-03):
- **Verbs:** `get`, `drop`, `inventory` and `examine`. Wear and wield come with combat in M4.
- **Placement:** placed Items appear when their pack version is activated. A taken Item doesn't come
  back until the pack's next activation. Timed resets are a later story.
- **Limits:** none yet. Carrying is unlimited, there's no stacking, and each placed Item is one Item
  Instance.

The contract below is PM's proposal from the decided ADRs. Five questions it needs answered are
architecture's, in `docs/feedback/AW-SRV-047-items.md`, so the story stays `draft` until they are.

## User story

As a player, I want to see the Items in a Room, pick them up, carry them, put them down, and look at
them closely, so that the World has things in it and not only places.

## Scope

### In scope
- **Item Instances in world state:** in a Room, or carried by a Character. They live in Zone state,
  so snapshots, the state topic, replay and the State Hash cover them.
- **Placement at activation:** when a pack version is activated, each of its placements yields one
  Item Instance in its Room, at the tick boundary the content swap applies at (`AW-SRV-012`).
- **Four verbs**, through the explicit pipeline (parse → authorize → validate → apply → emit):
  - `get <item>` (alias `take`) moves an Item from the actor's Room to the actor;
  - `drop <item>` moves an Item the actor carries to the actor's Room;
  - `inventory` (alias `inv`) lists what the actor carries;
  - `examine <item>` (alias `x`) shows an Item's description, whether it's carried or in the Room.
- `look` (`RoomDescribed`) lists the Items in the Room.
- **Carried Items stay with the body.** They stay on a dormant Character and on a linkdead one, and
  they're there again on the next bind.
- `andara-cli play` renders the new Events, and `andara-cli sim` too.

### Out of scope
- Containers, equipment, wear and wield, quantities and stacking, carrying limits, and timed resets.
  Each waits for a decision from Brian. Equipment arrives with combat.
- An Item's Components doing anything beyond its names. Each mechanic brings its own.
- What happens to a deleted Character's carried Items: `AW-SRV-032` decides, and this story's
  question 4 asks architecture to route it there.
- Builder commands that create or destroy Items in place. That's `EPIC-11`.

## Acceptance criteria

1. **Given** a pack version placing a `Lantern` in `town/plaza` **when** it's activated **then**,
   from the tick that applies the swap, `look` in `town/plaza` lists the Lantern's short name. A
   sim-level test decodes the next snapshot round's Zone body and finds the Item Instance in
   `town/plaza`.
2. **Given** a Lantern in the actor's Room **when** the actor sends `get lantern` **then** the actor
   carries it, the Room no longer lists it, the actor reads `You take a brass lantern.`, and every
   other Character in the Room reads `<name> takes a brass lantern.`.
3. **Given** the actor carries the Lantern **when** they send `drop lantern` **then** it's in the Room,
   the actor reads `You drop a brass lantern.`, and others read `<name> drops a brass lantern.`.
4. **Given** the actor carries two Items **when** they send `inventory` **then** only the actor reads
   `You carry:` and the two short names, sorted. **Given** they carry nothing **then** they read
   `You carry nothing.`.
5. **Given** the Lantern carried or in the Room **when** the actor sends `examine lantern` **then** only
   the actor reads its description.
6. **Given** no Item matching the word **when** the actor sends `get`, `drop` or `examine` with it
   **then** the command is rejected with `item_not_here` (`get`, `examine`) or `item_not_carried`
   (`drop`). The rejection names the word the actor typed, lowercased, and state doesn't change. **Given** no
   argument **then** it's the existing `missing_argument`.
7. **Given** two Items whose keywords match the word **when** the actor sends `get <word>` **then**
   the Item with the lowest Item Instance ID is taken, the same result on every replay.
8. **Given** a taken Lantern **when** the same pack version stays active across ticks, a restart and a
   recovery **then** no Lantern reappears in `town/plaza`. **Given** the pack's next activation
   **then** one Lantern is placed again (question 2 decides what happens to the one still lying there
   from the last version).
9. **Given** a Character carrying the Lantern **when** they quit, then bind again later, or go linkdead
   and reconnect **then** `inventory` lists the Lantern.
10. **Given** a recovery from a snapshot round taken while Items were carried and lying in Rooms
    **when** recovery completes **then** the State Hash matches. Every Item is where it was, carried
    or in its Room (`AW-SRV-007`'s check covers the new state).
11. **Given** a snapshot round taken by the previous binary, whose content in effect already has
    placements (`AW-CLI-013` puts one in the `dev` fixture first) **when** this story's binary
    recovers from it **then** there's no `ContentDigestError`, and replaying the logged `ContentSwap`s'
    `world_digest`s matches. Placements must not change the digest of content activated before this
    story (question 5).
12. **Given** the actor carries the Lantern **when** they move or `goto` into another Zone, including
    across a partition handoff **then** `inventory` there lists the Lantern, and the State Hash holds in
    both Zones.

## Interface contract

```protobuf
// CONTRACT SKETCH — not an implementation
// andara.log.v1: additive LoggedCommand oneof members
message Get     { string target = 1; }   // the word the actor typed
message Drop    { string target = 1; }
message Inventory {}
message Examine { string target = 1; }
// andara.game.v1: additive Events, and RoomDescribed gains a field
message ItemTaken    { string zone_id = 1; string room_id = 2; string character_name = 3; string item_name = 4; }
message ItemDropped  { string zone_id = 1; string room_id = 2; string character_name = 3; string item_name = 4; }
message InventoryListed { repeated string item_names = 1; }          // scoped to the actor only
message ItemDescribed   { string item_name = 1; string description = 2; } // scoped to the actor only
// RoomDescribed: repeated string items = 8;   // short names, sorted
```

- **Verb table:** `get` (alias `take`), `drop`, `inventory` (alias `inv`), and `examine` (alias
  `x`). There's no `i` alias, because `i` resolves to the Direction `in` today (a unique prefix), and
  an alias would take it over, since aliases resolve before prefixes (`verbs.go`). Taking `i` for
  inventory, as MUDs often do, is Brian's call.
- **Matching:** the target word matches an Item when it equals one of the Item's keywords,
  case-insensitively. `get` looks in the Room, `drop` among carried Items, and `examine` in carried
  Items first, then the Room. Ties go to the lowest Item Instance ID (AC-7).
- **Numbers and names:** four `LoggedCommand` oneof members (`get`, `drop`, `inventory`, `examine`)
  and four `EventEnvelope.payload` members (`item_taken`, `item_dropped`, `inventory_listed`,
  `item_described`), with `EventType` strings of the same names. Architecture assigns the numbers at
  contract review. `AW-SRV-009` (`ready`) already claims `LoggedCommand` 20–22 and two payload
  Events, so the free numbers on `main` aren't free in the backlog.
- **Parsing:** the target is free text: one new `ArgSpec` kind, `ArgWord` (a single token, lowercased).
  The four verbs aren't `Abbrev`, so they claim only their names and aliases. No existing verb,
  Direction or prefix changes meaning, and a test asserts that `i` and `in` still move `in`.
- **Rejection codes** (`CommandRejected.code`): `item_not_here` and `item_not_carried`, at stage
  `validate`. They're applied in the sim, not at parse, because what's present is world state.
- **Scope:** `ItemTaken` and `ItemDropped` go to the Room. `InventoryListed` and `ItemDescribed` go to
  the actor's Entity only.
- **`andara-cli play` lines:** as AC-2 to AC-5. `look` adds `Items: <short names>` after `Exits:`.

## Data / state impact

- Zone state gains Item Instances, in the representation question 1 decides. **Whether
  `state_version` moves is part of question 1.** ADR-0007 rule 2 bumps it only for a change protobuf
  can't absorb, and `AW-SRV-015` added hashed `EntityState` fields at `StateVersion` 1. If it moves,
  the change adds the `migrations` and `hashers` entries `TestMigrationsCoverEveryVersion` requires
  (`server/store/migrate.go`). Either way, a snapshot round taken before this story still restores.
- **Rollback:**
  - If `state_version` moves, a binary older than it exits `4` (`AW-SRV-007`). If it doesn't, an older
    binary drops the unknown fields, and the round fails its hash check (`ErrHashInvalid`).
  - The log will also hold `Get`, `Drop`, `Inventory` and `Examine`, which an older binary replays as
    `unsupported_command`. So a pre-upgrade round followed by post-upgrade log doesn't help: the
    replay diverges at the first Item command.
  - Rolling back after Items exist therefore needs a world reset (`make world-reset`), or a
    pre-upgrade round with nothing logged after it.

  SRE's runbook line at §8 states this.
- Live Sessions: no effect at rollout. Items appear only when a pack placing them is activated.

## Observability requirements

- **Metrics:**
  - the existing `andara_commands_total{verb}` and `andara_command_rejected_total{stage,code,pre_log}`
    gain the four verbs and two codes. That's bounded, a fixed set;
  - `andara_items` (gauge), labels `location` (`room` or `carried`), the count of Item Instances in the
    World. Two series.
- **Logs:** none new on the command path beyond the pipeline's existing lines (session and trace IDs
  already present). One `info` line at activation, `items placed` with `pack`, `version`, `count` and
  `trace_id`.
- **Traces:** none new. The verbs run inside the existing command spans. Placement runs inside the
  content swap's span, with an `items.placed` attribute.
- **Alerts:** none.

## Test plan

- **Unit:** each verb's apply and its rejection; keyword matching (case, ties by ID); placement at
  activation; a quit and rebind keep carried Items; the snapshot codec's up-migration.
- **Integration:** a determinism test, where replaying a log with get, drop and activation gives the
  same State Hash at every boundary; kill-and-recover with Items carried and lying (AC-10), on top
  of `AW-SRV-007`'s test.
- **Manual/operator:** `make stack-play` extended, or a sibling target: a player finds the fixture's
  Item in `town/plaza`, takes it, sees `You carry:`, drops it, and a bystander reads both lines.

## Definition of done

CLAUDE.md §8, plus the glossary's **Item**, **Entity** and **Container** entries say where an Item
Instance lives (question 1).

## Open questions

For architecture, in `docs/feedback/AW-SRV-047-items.md`. They affect the contract, so the story stays
`draft` until they're answered:
1. **Is an Item Instance an Entity in world state?** Before this story, the glossary called an Item
   Instance "a specific Entity in the World", and its **Entity** entry listed "an Item instance". This
   story's PR marks both as open. `AW-SRV-022` rules "an Item is not an Entity" for Template kinds. Is an
   Item Instance an `EntityState` with an ITEM Template, or a separate `ItemState`? How is a carried
   Item's holder represented? How do carried Items travel with their holder in `Arrive` and across a
   partition handoff (AC-12)? `Arrive` carries only `Entity entity = 3` today. Does `state_version` move?
2. **What a new activation does to the previous version's placed Items.** Brian decided a taken Item
   comes back only at the next activation. PM proposes:
   - untaken Item Instances from the pack's previous placements are removed, and the new version's
     placements are placed;
   - carried Instances stay, keeping their `content_version`, even if the new version drops their
     Template.

   Still open: does a placed Item that was taken and then dropped count as "untaken" (if not, the
   next activation duplicates it)? And what happens to Items lying in a Room the new content removes
   (`EntityRelocated`'s fallback Room, or removed)?
3. **Item Instance IDs:** they must be deterministic, so the same placement in the same activation
   gives the same ID on replay.
4. **A deleted Character's carried Items:** route to `AW-SRV-032`, or decide here.
5. **Placements and `ContentDigest`.** Restore rebuilds the digest from the content in effect and
   compares it with the round's (`server/sim/restore.go`), and replay checks each logged swap's
   `world_digest`. Do placements enter `CanonicalBytes`? If they do, what's the migration that keeps
   rounds and swaps from before this story verifying (AC-11)?
- `[ASSUMPTION]` The player-facing lines in AC-2 to AC-5 are placeholders in the same register as
  `<name> leaves the world.` Brian may reword them, and that's not a contract change, since the
  Events carry names, not sentences.
