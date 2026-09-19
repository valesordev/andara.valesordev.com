---
id: AW-SRV-029
title: Perception through Exits — Builder-declared senses on a room connection
epic: EPIC-02
component: server
type: feature
status: ready
size: M
depends_on: [AW-SRV-004, AW-SRV-021]
blocks: []
lane: implementation
risk: medium
---

## Context

`AW-SRV-004` scoped perception to the Room and left one question for Brian: does a Character perceive
anything in an adjacent Room — a fight next door, a shout down the corridor? Brian decided on
2026-09-19: **it is an attribute on the room connection, set by the Builder.** Not a global rule
and not a property of the Room; a stone door and an archway lead to the same Room and let different
things through, and only the Builder knows which is which.

So the model is: an Exit declares the **senses** that pass through it into the Room that owns it.
The sim, when it emits a Room-scoped Event that carries a sense, also emits a *perceived-through*
form to every neighbouring Room whose Exit into the source lets that sense through, naming the
Direction the observer perceives it from. Scope remains the sim's decision (perception is computed
inside the simulation, `AW-SRV-004`); the Hub keeps matching Rooms and never edits the sim's
envelope — it chooses among the forms the sim prepared and withholds the actor's `client_ref` from
every other Session (`AW-SRV-004` as merged), which is delivery, not redaction.
Senses are a server-defined closed vocabulary, like Component types (ADR-0010 decision 7): Builders
compose them, they do not invent them.

## User story

As a builder, I want to say on each connection what can be seen or heard through it, so that a
tavern's noise spills into the street but not through the cellar door.

## Scope

### In scope
- `ExitDefinition.perceives` (`zone.proto`, field 4): the senses that pass **through this Exit into
  its source Room** — what a Character standing in the Room that owns the Exit perceives of the Exit's
  target Room. Directional by construction; a two-way passage that carries sound both ways declares
  it on both Exits.
- The sense registry: `sight`, `sound` to start. Server-defined and closed; an unknown sense is a
  load error naming the file, line, Room, Direction, and the permitted senses (the `AW-SRV-021`
  Direction pattern).
- A sense on each Event type that has one, declared in `server/sim` beside the type: `CharacterArrived`
  and `CharacterLeft` are `sight`; `RoomDescribed`, `CommandRejected`, `ZoneFaulted`,
  `SimulationStopped` carry none and never cross an Exit. Types added later declare theirs or are
  Room-bound. **No shipped Event type carries `sound` yet** — the first is the communication verb
  (`say`), which has no story; `sound` is in the registry so a Builder can declare it today and the
  content stays valid when that Event arrives. Until then a `sound`-only Exit passes nothing, which
  AC-1 pins.
- The perceived-through form: for a Room-scoped emit with sense `S`, one additional envelope per
  neighbouring Room `N` whose Exit `N → source` lists `S`, scoped to `N`, with
  `EventEnvelope.perceived_from` = the Direction of that Exit (what the observer in `N` would walk
  to reach the source). One hop only; a perceived-through form never propagates again.
- `World.InboundExits(room)` built at load — the index the emit walks — and kept when Exits cross a
  Zone boundary: a neighbour in another Zone is a legal recipient, and its Scope names that Zone.
- `andara-cli sim repl --tap-events` shows the perceived-through form with its Direction.

### Out of scope
- What a perceived-through Event *says* to the player ("you hear footsteps to the north") — the
  client renders from `perceived_from` and the type; wording is `AW-CLI-004`'s and Brian's.
  `AW-CLI-004` carries the rule as an inherited item: a `CharacterArrived`/`CharacterLeft` with
  `perceived_from` set is rendered by that Direction, never by the payload's movement direction,
  which is the mover's and not the observer's.
- Senses with range beyond one hop (shouting across a Zone, scrying) — a later Scope shape, and
  Brian's design call when a mechanic needs it.
- Exit conditions (doors that close, locks) changing what passes — `[NEEDS BRIAN]` on the Exit
  glossary entry; when a door state exists, it masks `perceives` and that story says how.
- The Content Language keyword for `perceives` — `AW-CLI-005`, which this story informs.

## Acceptance criteria

1. **Given** Rooms A and B with an Exit `A → B` declaring `perceives: [sound]` and a `sight` Event
   emitted in B **when** the tick completes **then** an observer in A receives nothing from it, and
   an observer in B receives the whole form.
2. **Given** the same Exit declaring `perceives: [sight]` and `CharacterArrived` emitted in B
   **when** the tick completes **then** the observer in A receives a `CharacterArrived` whose
   `perceived_from` is the Exit's Direction, and its `room_id` names B — not A.
3. **Given** an Exit `B → A` that declares nothing **when** a `sight` Event is emitted in A **then**
   the observer in B receives nothing: perception is per Exit and per direction.
4. **Given** A → B (`sight`) and B → C (`sight`) and an Event in C **when** the tick completes
   **then** A receives nothing — one hop.
5. **Given** an Exit crossing a Zone boundary with `perceives: [sight]` **when** `CharacterArrived`
   is emitted in the target **then** the observer in the other Zone receives the perceived-through
   form, and the Event record on `andara.events.v1` names both Rooms in its Scope.
6. **Given** a Zone file whose Exit declares `perceives: [smell]` **when** the World is loaded
   **then** load fails with exit code 1 naming the file, line, Room, Direction, `smell`, and the
   permitted senses, and stating that senses are server-defined.
7. **Given** two loads of the same content **when** serialized **then** byte-identical, with
   `perceives` sorted; and the golden hash sequence is unchanged, because Scope is not state.
8. **Given** `AW-SRV-001`'s fixtures, which declare no `perceives` **when** loaded **then** they
   load unchanged and no Event ever crosses an Exit (the `AW-SRV-021` AC-8 pattern).
9. **Given** an observer whose Entity is the actor of the Event (the one who arrived) **when** the
   perceived-through form is delivered to neighbours **then** the actor receives only the whole form,
   never its own perceived-through echo.
10. **Given** a Zone file whose Exit declares `perceives: [sound]` **when** loaded **then** it is
    valid content, `andara_content_exit_senses_total{sense="sound"}` counts it, and a test asserts
    that `SenseOf` maps no shipped type to `sound` — so the day one does, the test names it and
    the sound path gets its own AC rather than being switched on silently.

## Interface contract

```protobuf
// CONTRACT SKETCH — not an implementation
// andara/content/v1/zone.proto
message ExitDefinition { /* direction, to_zone, to_room */ repeated string perceives = 4; }  // sorted; registry-validated
// andara/game/v1/event.proto
message EventEnvelope { /* … */ string perceived_from = 9; }  // Direction; empty on the whole form
```

```go
// CONTRACT SKETCH — server/sim
type Sense string                      // "sight", "sound"; Senses() is the closed registry
func SenseOf(EventType) (Sense, bool)  // table beside the types; false = Room-bound
func (w *World) InboundExits(RoomRef) []InboundExit   // {From RoomRef, Direction, Perceives []Sense}, sorted
// ApplyContext.Emit(scope, env): for a Room-scoped emit whose type has a sense, also emits the
// perceived-through forms; each is its own Event with its own ID, addressed to one neighbour Room.
```

### Error taxonomy

`unknown_sense` (load; extends `AW-SRV-001`'s `ValidationError`), `duplicate_sense` (load; `[sound,
sound]`).

### Configuration

None.

## Data / state impact

`ExitDefinition` gains a field — additive under ADR-0007 rule 1, `buf breaking` clean, existing
content unchanged (AC-8). `EventEnvelope` gains a field, additive. Perceived-through forms consume
Event IDs, so a World with `perceives` declared produces a different Event sequence from one without;
the golden fixture declares none and its hashes stand (AC-7).

## Observability requirements

- **Metrics:** `andara_events_perceived_through_total{sense}` — counter, cardinality the registry.
  `andara_content_exit_senses_total{sense}` at load, same bound.
- **Logs:** load rejections at `error` with `file`, `line`, `zone_id`, `room_id`, `direction`,
  `sense`. No per-Event lines.
- **Traces:** none new; the emit stays inside `command.apply`.
- **Alerts:** none.

## Test plan

- **Unit:** AC-1 to AC-4, AC-9 on `simtest` fixtures with three Rooms; the registry rejecting
  `smell` and `[sound, sound]`; `InboundExits` sorted and stable; `SenseOf` covers every
  `EventType` the sim declares (a test that fails when a new type is added without a decision).
- **Integration:** AC-5 across a Zone boundary through the Hub; AC-7 on the golden sequence; AC-8 on
  `AW-SRV-001`'s fixtures.
- **Manual/operator:** `andara-cli sim repl --content ./testdata/content/perceives --start town/street
  --tap-events town/tavern` — a `north` from the tavern shows `CharacterLeft` in the tavern tap and
  a `perceived_from: south` form in the street.

## Definition of done

CLAUDE.md §8, plus: the glossary's Sense entry names the registry; `AW-CLI-005` carries the
`perceives` keyword as an inherited item; `AW-SRV-004`'s open question records this story as the
answer.

## Open questions

- **Resolved 2026-09-19 (Brian):** adjacent-Room perception is an attribute of the Exit, set by the
  Builder per connection.
- `[NEEDS BRIAN]` The sense vocabulary beyond `sight` and `sound`. The mechanism is the story; adding
  a sense is a registry entry, and which senses Andara has is game design.
- `[ASSUMPTION]` One hop. A sense that carries further is a different shape (range, attenuation) and
  arrives with the mechanic that needs it.
- `[ASSUMPTION]` `perceives` is declared on the perceiving Room's Exit (what *I* can sense through
  *my* doorway), not the source Room's. It keeps a Room's perception legible from its own definition
  and matches how a Builder walks a map; the reverse convention is one index flip if it reads wrong
  in the language.
