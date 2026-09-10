---
id: AW-SRV-021
title: Components on Rooms and Zones, and the closed Direction set
epic: EPIC-02
component: server
type: feature
status: ready
size: M
depends_on: [AW-SRV-001]
blocks: [AW-CLI-003]
lane: implementation
risk: medium
---

## Context

ADR-0010 was accepted on 2026-09-10. Decision 8 says the component model covers Rooms and Zones, not
only Entities and Items — a Room carrying `andara.core.Dark{}` is the motivating case. `AW-SRV-001`
shipped `Room` and `Zone` as plain structs and is in `review`; it is not reopened. This story is the
addition, and it is genuinely additive because `AW-SRV-001` was told not to close the seam and did
not: `zone.proto` leaves `RoomDefinition` field 5 open and unreserved, with a comment saying what
attaches there.

The same day closed the canonical `Direction` set — the twelve in `docs/glossary.md`, each with a
reverse. `AW-SRV-001` shipped with Direction as an opaque string and said so in its own open
questions: *"it cannot yet reject `norht` as a typo."* Both changes land in the loader, on the same
model, so they are one story rather than two that would conflict in the same files.

## User story

As a builder, I want to attach Components to Rooms and Zones and have the loader reject a misspelled
Direction, so that a Room can be dark or magic-dead without a code change, and a typo fails the build
instead of producing a Room nobody can leave.

## Scope

### In scope
- `repeated ComponentValue components = 5` on `RoomDefinition`, and the equivalent on
  `ZoneDefinition`, in `andara/content/v1/zone.proto`.
- `Components` on the `Room` and `Zone` Go types, keyed by component type (ADR-0010 decision 3: at
  most one of each type).
- A **server-defined component registry**: the closed vocabulary of component types the server
  understands. ADR-0010 decision 7 — Builders compose components, they do not create them — so an
  unrecognised component type is a load error naming the type, the file, and the Room.
- Seed vocabulary for Rooms and Zones only: `andara.core.Dark`, `andara.core.NoMagic`,
  `andara.core.Indoors`, `andara.core.NoRecall`. Enough to prove the mechanism; see Open questions.
- Closed `Direction` validation in the loader, against `docs/glossary.md`'s twelve.
- A `warn` for an Exit whose reverse is absent — legal, and usually a Builder forgetting the way back.

### Out of scope
- Components on Entities and Items — those arrive with the Entities themselves, not here.
- Template inheritance and field-level override merge (ADR-0010 decisions 2, 4, 5). This story
  attaches components to *instances* authored in a Zone Definition. Templates are `AW-SRV-012`'s.
- Any component that changes simulation behaviour. `Dark{}` is data here; the system that reads it
  and suppresses a Room description belongs to the story that adds looking in the dark.
- Direction *abbreviation* expansion — `n`, `ne`, `u`. That is the command parser, `AW-SRV-003`.
- Growing the Direction set. It is expected to grow; doing so is a glossary edit plus a content
  revalidation, and it needs no story.

## Acceptance criteria

1. **Given** a Zone file whose Room declares `andara.core.Dark` **when** the World is loaded **then**
   the Room resolves and its component set contains exactly that component.
2. **Given** a Room declaring a component type that is not in the registry **when** the World is
   loaded **then** load fails with exit code 1 naming the unknown type, the file, and the Room ID —
   and the message states that component types are defined on the server (ADR-0010 decision 7), so a
   Builder reading it knows to file an issue rather than to check their spelling forever.
3. **Given** a Room declaring the same component type twice **when** the World is loaded **then**
   load fails with exit code 1 naming the type and the Room. ADR-0010 decision 3 keys components by
   type; two of a thing has no override semantics.
4. **Given** a Zone declaring a Zone-level component **when** the World is loaded **then** it is
   resolvable on the `Zone` and does not appear on its Rooms. Zone-level and Room-level components do
   not merge in this story.
5. **Given** two loads of the same content **when** the resulting topologies are serialized **then**
   they are byte-identical, with components sorted by component type. This extends `AW-SRV-001` AC-10;
   a component set that serialized in map order would break the State Hash (ADR-0007 rule 3).
6. **Given** an Exit with direction `norht` **when** the World is loaded **then** load fails with exit
   code 1 naming the file, the line, the Room ID, and the offending label, and listing the twelve
   permitted Directions.
7. **Given** an Exit with direction `north` whose target Room has no `south` Exit back **when** the
   World is loaded **then** load succeeds and emits a `warn` naming both Rooms and the missing
   reverse. One-way Exits are legal; silent ones are not.
8. **Given** a Zone file authored before this story, with no `components` field **when** the World is
   loaded **then** it loads unchanged with an empty component set. The field is additive
   (ADR-0007 rule 1) and existing content must not need editing.

## Interface contract

```protobuf
// CONTRACT SKETCH — not an implementation
message ComponentValue {
  // Namespaced type, e.g. "andara.core.Dark" (ADR-0010 decision 6). Rejected at
  // load if the server has no such type registered.
  string type = 1;

  // Field values for the component. Deliberately not google.protobuf.Any and not
  // a map: ADR-0007 rule 3 forbids both anywhere near the State Hash. Sorted by
  // name at compile time, like every other repeated field the sim hashes.
  repeated ComponentField fields = 2;
}
```

`RoomDefinition.components` is field **5**, the number `zone.proto` has been holding open for it.
`ZoneDefinition.components` takes the next free number on that message.

| Behaviour | Exit 0 | Exit 1 |
|-----------|--------|--------|
| unknown component type | — | names type, file, Room, and that types are server-defined |
| duplicate component type on one Room | — | names type and Room |
| Direction outside the closed set | — | names file, line, Room, label, and the permitted twelve |
| Exit with no reverse | yes, with a `warn` | — |
| content with no `components` field | yes, empty set | — |

The component registry is a server-side table, not configuration. Adding a type is a code change and
a release — that is ADR-0010 decision 7, and it is the whole reason the error message in AC-2 has to
say so.

## Data / state impact

`RoomDefinition` and `ZoneDefinition` gain a field. Additive under ADR-0007 rule 1, so `buf breaking`
passes and existing content loads unchanged (AC-8). Both subjects that carry the content topics
(`andara.content.blobs.v1-value`, `andara.content.versions.v1-value`) accept the new field under
`BACKWARD` compatibility; `make schemas-diff` will report the registry as behind until
`make schemas-apply` runs, which is the intended signal.

A Room's component set feeds the State Hash, so it obeys the determinism rules in
`andara/log/v1/log.proto`: sorted `repeated`, no maps, no floats.

## Observability requirements

- **Metrics:** `andara_content_components_total{component_type}` at load — cardinality is bounded by
  the registry, which is closed by construction, so this is safe as a label. `andara_content_load_warnings_total{kind}`
  with `kind` in `{orphan_room, missing_reverse_exit}` — bounded, and the two together tell an operator
  whether a content pack is sloppy before players find out.
- **Logs:** every rejection at `error` with `file`, `line`, `zone_id`, `room_id`, and the offending
  value. Every `warn` with the same fields. No `room_id` as a metric label — that is the unbounded
  cardinality CLAUDE.md §7 rejects on sight.
- **Traces:** within `AW-SRV-001`'s existing content-load span; component and Direction validation are
  attributes on it, not spans of their own. Per-Room spans would be one span per Room.
- **Alerts:** none. This is boot-time validation — it fails the boot, and `AndaraServerCrashLooping`
  from `AW-INF-003` already covers a server that will not start.

## Test plan

- **Unit:** the registry rejects an unknown type; duplicate detection; Direction validation across all
  twelve plus `norht`, `North`, and the empty string; reverse-exit warning on a genuine one-way Exit.
- **Integration:** load a fixture Zone with components on Rooms and on the Zone; assert AC-1, AC-4.
  Load `AW-SRV-001`'s existing fixtures unchanged and assert AC-8 — those fixtures predate this field,
  which is what makes them the right regression test.
- **Determinism:** extend `AW-SRV-001`'s existing byte-identical serialization test to content
  carrying components, with the components authored out of type order so a sort that does not happen
  is visible.
- **Manual/operator:** `make up && make check`. Author a Room with `andara.core.Drak`, boot, and read
  the error — it should name the typo and say component types are server-defined.

## Definition of done

CLAUDE.md §8, plus: `make schemas-apply` has been run so the registry carries the new schema, and
`make schemas-diff` is clean.

## Open questions

- `[NEEDS BRIAN]` **The core component vocabulary beyond the four seeded here.** `Dark`, `NoMagic`,
  `Indoors`, `NoRecall` are enough to prove the mechanism and are the ones that recur across every MUD;
  which Room and Zone properties Andara actually wants is game design, and game design is yours
  (CLAUDE.md §11). This does not block the story: the mechanism is the story, and adding a type to the
  registry afterwards is a small change. ADR-0010 decision 7 makes this list the thing Builders will
  push on, so expect it to grow from real content rather than from guessing now.
- `[ASSUMPTION]` Zone-level and Room-level components do not merge or inherit — a Zone's `Dark{}` does
  not make its Rooms dark. Merging is ADR-0010 decision 4's semantics applied across a containment
  boundary rather than an inheritance one, which is a different rule and wants its own decision. AC-4
  pins the non-merging behaviour so that a later change to it is a visible test change.
