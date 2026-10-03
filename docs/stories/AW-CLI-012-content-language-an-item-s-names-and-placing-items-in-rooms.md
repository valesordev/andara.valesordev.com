---
id: AW-CLI-012
title: Content Language: an Item's names and placing Items in Rooms
epic: EPIC-05
component: cli
type: feature
status: draft
size: M
depends_on: [AW-CLI-005]
blocks: [AW-CLI-013]
lane: architecture
risk: medium
---

## Context

Builders are about to author Items (Brian, 2026-10-03: "content needs a server story for items as
this will drive some of the content definition coming up"). The Content Language can declare an
Item Definition today, `template Lantern extends andara.core.Item {}`, but nothing more. Two gaps
stop a Builder from authoring a usable Item:
- **Names.** `andara.core.Item` is an empty root. No core Component gives an Item what a player
  sees in a Room or an inventory, the words a player types to name it, or what `examine` shows. Per
  `AW-SRV-022`, "systems that read Components arrive with their mechanic", and Items are that
  mechanic.
- **Placement.** A `room` holds `desc`, `exit` and `component` only (`grammar.ebnf`). There's no way
  to say "this Room starts with a lantern in it".

This story is the specification half, architecture's, as `AW-CLI-005` was for the language.
`AW-CLI-013` builds the compiler half and ships `andara.core@2`, and `AW-SRV-047` puts the Items
in the World. Brian's decisions for the first Items (2026-10-03):
- the verbs are get, drop, inventory and examine;
- a placed Item comes back only when its pack version is next activated;
- there are no carrying limits and no stacking: one Item Instance per placed Item.

## User story

As a builder, I want to give an Item a name, the words that refer to it, and a description, and to
place Items in my Rooms, so that the Items I design show up in the World where I put them.

## Scope

### In scope
- **A core Component for an Item's names** in `andara.core`, carried by `andara.core.Item`:
  - a short name, shown in a Room listing and an inventory (`a brass lantern`);
  - one or more keywords a player types to name it (`lantern`, `brass`);
  - a description, which `examine` shows.

  Its name, field names, field kinds and defaults are this story's to decide, within ADR-0010's
  Component model (Components are data, fields are `string`, `int` or `bool`). Deciding them bumps
  `andara.core` to version 2.
- **A placement declaration inside `room`**: it names an Item Definition by Template reference,
  in-pack or `<pack>.<Name>`, and places one Item Instance. Placing two means writing two
  declarations, since there's no stacking (Brian, 2026-10-03).
- **The compiled form**: the field placements take in `RoomDefinition` (`content/v1/zone.proto`).
  It must be additive, so a v1 Room without placements decodes unchanged.
- **Errors in `errors.md`**:
  - a placement naming an unknown Template;
  - a placement naming a Template whose kind isn't `item`;
  - an Item Definition missing the names Component, or with an empty short name or no keyword.
- **Corpus cases** in `docs/specs/content-language/v1/corpus/`, valid and invalid, for each rule.
  `town`'s corpus pack gains a placed `Lantern`.
- `semantics.md` and `formatting.md` updated, with the canonical form of a placement.

### Out of scope
- The compiler and the `andara.core@2` seed: `AW-CLI-013`.
- Instances, verbs and state in the server: `AW-SRV-047`.
- Containers (an Item inside an Item), equipment slots, quantities, and timed resets. Each waits for a
  decision from Brian.
- Placing NPCs. Same shape, different kind. It arrives with the Behavior Agent stories, and this
  story's syntax shouldn't make it awkward.

## Acceptance criteria

1. **Given** the updated spec **when** `make check` runs **then** `content-conformance` passes against
   the corpus, including at least one valid placement case and one invalid case per new error code.
2. **Given** a placement naming `town.Lantern` from a Room in another pack **when** the corpus case
   compiles **then** the expected output names the placed Template by its full name. Given a
   placement of `town.Guard`, whose kind is `entity`, **then** the expected finding is the
   wrong-kind error at the placement's line.
3. **Given** `content/v1/zone.proto` **when** `make proto-check` runs **then** the change is additive
   (`buf breaking` passes against `main`), and a v1 `RoomDefinition` with no placements is
   byte-identical after a round trip.
4. **Given** an Item Definition with no names Component, or with an empty short name or no keyword
   **when** the corpus case compiles **then** the expected finding names the Template and the
   missing field.
5. **Given** the spec's documents **when** read **then** `semantics.md` states how many Item
   Instances one placement yields (one), what the names Component's fields mean, and that keywords
   match case-insensitively. `formatting.md` gives the placement's canonical form.

## Interface contract

This is the spec. Its decisions are listed under Open questions, and each is architecture's. PM's
requirements are the Scope above. The sketch below shows the shape PM expects, for review only:

```
// CONTRACT SKETCH — not an implementation
template Lantern extends andara.core.Item {
  component Names { short: "a brass lantern"  keywords: "lantern brass"
                    desc: "A brass lantern, its glass sooted." }
}
zone town "Town" {
  room plaza "Market Plaza" {
    desc "A dusty square of packed earth."
    item Lantern          // one Item Instance of town.Lantern, here
  }
}
```

## Data / state impact

`zone.proto` and `template.proto` change additively. `andara.core` goes to version 2, and
`content/core/VERSIONS` gains a line (`AW-INF-029`'s append-only rule). Packs built against
`andara.core@1` keep working: they place no Items, and `AW-SRV-012`'s skew rule refuses only packs
built against a newer core.

## Observability requirements

- **Metrics / Logs / Traces / Alerts:** none. This is a specification. `AW-CLI-013` and `AW-SRV-047`
  carry the instruments that apply.

## Test plan

- **Unit:** none; the corpus is the test.
- **Integration:** `content-conformance` in `make check`, against the new corpus cases.
- **Manual/operator:** none.

## Definition of done

CLAUDE.md §8, plus the glossary's **Item** and **Item Placement** entries match the spec's words.

## Open questions

For architecture, in `docs/feedback/AW-SRV-047-items.md`:
- The names Component: its name, its fields, and whether keywords are one string or a list. A list
  needs a field kind the model doesn't have yet.
- The placement keyword and its canonical form.
- Whether a placement may override the Template's fields per Room (a "rusty" lantern here). PM's
  proposal: not in v1, since there's no stacking and no per-Room variation decided.
