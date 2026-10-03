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

  Component *types* are defined on the server, and Builders compose them (ADR-0010 decision 7). So
  this story specifies the type: its name, field names, field kinds and defaults, within ADR-0010's
  Component model (fields are `string`, `int` or `bool`). `AW-CLI-013` registers it in the server's
  component registry and ships it in `andara.core@2`.
- **A placement declaration inside `room`**: it names an Item Definition and places one Item
  Instance. Placing two means writing two declarations, since there's no stacking (Brian,
  2026-10-03). The reference follows `semantics.md` §4: a Template in the same pack, or in
  `andara.core`, and no other pack. A pack is the unit of publication and activation (§10).
- **The compiled form**: the field placements take in `RoomDefinition` (`content/v1/zone.proto`).
  It must be additive, so a v1 Room without placements decodes unchanged. This story's PR runs
  `make proto` and commits `gen/`, so the server parses the new field from then on.
- **Errors in `errors.md`**:
  - a placement whose Template doesn't resolve, including another pack's Template;
  - a placement naming a Template whose kind isn't `item`;
  - a placed Item Definition whose resolved names have an empty short name or no keyword.

  The names rule applies at the placement, to the Template it places. `andara.core.Item` and an
  intermediate base that's never placed aren't checked, since every Item inherits the Component by
  merge (§5).
- **Corpus cases** under `corpus/pending/`, each with `PENDING: AW-CLI-013` (`corpus/README.md`,
  "Pending cases"), valid and invalid, for each rule, including an unplaced intermediate base with
  no names. They can't be in `valid/` or `invalid/` yet: no compiler accepts them until
  `AW-CLI-013`, which moves them, and updates the corpus anchors, `valid/core` and `valid/town`.
- `semantics.md` and `formatting.md` updated, with the canonical form of a placement.

### Out of scope
- The compiler, the server's component registry, and the `andara.core@2` seed: `AW-CLI-013`.
- Item Instances, verbs and state in the server: `AW-SRV-047`.
- Placing another pack's Items. That follows the cross-pack rule, if it's ever relaxed.
- Containers (an Item inside an Item), equipment slots, quantities, and timed resets. Each waits for a
  decision from Brian.
- Placing NPCs. Same shape, different kind. It arrives with the Behavior Agent stories, and this
  story's syntax shouldn't make it awkward.

## Acceptance criteria

1. **Given** the updated spec and its pending cases **when** `make check` runs **then**
   `content-grammar-check` passes, and `content-conformance` passes, reporting each new case as
   pending on `AW-CLI-013` and changing no existing valid or invalid case.
2. **Given** pending cases for a placement of the pack's own `Lantern`, a placement of
   `andara.core.Item`, a placement of another pack's Template, and a placement of `town.Guard` (kind
   `entity`) **when** read **then** the first two are valid. The third expects the unresolved
   reference error, and the fourth the wrong-kind error, each at the placement's line.
3. **Given** `content/v1/zone.proto` **when** `make proto-check` runs **then** the change is additive
   (`buf breaking` passes against `main`), and a v1 `RoomDefinition` with no placements is
   byte-identical after a round trip.
4. **Given** a placed Item whose resolved names have an empty short name or no keyword **when** its
   pending case is read **then** the expected finding names the Template, the missing field, and the
   placement's line. **Given** an intermediate base with no names that's never placed **then** its
   case is valid.
5. **Given** the spec's documents **when** read **then** `semantics.md` states how many Item
   Instances one placement yields (one), what the names Component's fields mean, that keywords match
   case-insensitively, and which references a placement may name. `formatting.md` gives the
   placement's canonical form.

## Interface contract

This is the spec. Its decisions are listed under Open questions, and each is architecture's. PM's
requirements are the Scope above. The sketch below shows the shape PM expects, for review only:

```
// CONTRACT SKETCH — not an implementation
template Lantern extends andara.core.Item {
  component andara.core.Names { short: "a brass lantern"  keywords: "lantern brass"
                                desc: "A brass lantern, its glass sooted." }
}
zone town "Town" {
  room plaza "Market Plaza" {
    desc "A dusty square of packed earth."
    item Lantern          // one Item Instance of this pack's Lantern, here
  }
}
```

## Data / state impact

`zone.proto` changes additively. The names Component is a `ComponentValue`, so `template.proto`
doesn't change. `andara.core` goes to version 2, and
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
