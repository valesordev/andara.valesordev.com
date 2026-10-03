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
  sees in a Room or an inventory, the words a player types to name it, or what `examine` shows. `AW-SRV-022` left
  Components to arrive with the mechanic that reads them, and Items are that mechanic.
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
- **The corpus cases the language needs**, listed here so they're specified now. When they land is
  question 1:
  - valid: a placement of the pack's own `Lantern`;
  - valid: an intermediate base with no names that's never placed;
  - invalid: a placement of another pack's Template (unresolved reference);
  - invalid: a placement of an in-pack Template of kind `entity` (wrong kind). Every non-anchor case
    compiles as pack `p` (`corpus/README.md`), so that Template is declared in the case itself;
  - invalid: a placed Item whose resolved names have an empty short name, and one with no keyword.
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

1. **Given** the updated `grammar.ebnf`, `semantics.md`, `errors.md` and `formatting.md` **when**
   `make check` runs **then** it passes with the corpus unchanged. `content-grammar-check` accepts the
   new production against the existing cases.
2. **Given** `content/v1/zone.proto` **when** `make proto-check` runs **then** the change is additive
   (`buf breaking` passes against `main`), and `gen/` is regenerated in the same PR.
3. **Given** the spec's documents **when** read **then**:
   - `semantics.md` states how many Item Instances one placement yields (one), what the names
     Component's fields mean, that keywords match case-insensitively, which references a placement
     may name, and that the names rule applies at a placement;
   - `errors.md` gives each new code a row with its story;
   - `formatting.md` gives the placement's canonical form.
4. **Given** question 1's answer **when** this story moves to `ready` **then** the story says which
   PR lands each corpus case in Scope, and in which directory.

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

- **Unit:** none in this story. The `RoomDefinition` round-trip test is Go, so it's in `AW-CLI-013`.
- **Integration:** `make check`, which runs `content-grammar-check` and `proto-check`. The corpus
  cases' conformance runs in whichever PR question 1 assigns them to.
- **Manual/operator:** none.

## Definition of done

CLAUDE.md §8, plus the glossary's **Item** and **Item Placement** entries match the spec's words.

## Open questions

For architecture, in `docs/feedback/AW-SRV-047-items.md`:
1. **When the corpus cases land, and how the anchors move. This blocks the contract.** Today's Go
   tests run the formatter over every corpus `.aw` file, `pending/` included
   (`TestContentFmtCheckIsCleanOverTheCorpus`, `corpusSources`). So a placement case fails `make check`
   until the parser accepts it. The first core bump also has to change, together:
   - the corpus anchors (`valid/core`, `valid/town`: architecture's);
   - `content/core/templates`, `testdata/` and their tests (implementation's);
   - `scripts/content_grammar_check.py`'s `KEY_ORDER` for the new `RoomDefinition` key (SRE's).

   `perceives` set a precedent: an implementation prerequisite parsed the syntax and dropped it, so
   the pending cases could land. Decide the sequence and the PR split. `semantics.md` §9's wording on
   pending cases ("the story that landed it") may need amending for compiler-gated cases.
2. The names Component: its name, its fields, its defaults on `andara.core.Item`, and whether keywords
   are one string or a list. A list needs a field kind the model doesn't have yet.
3. The placement keyword and its canonical form.
4. Whether a placement may override the Template's fields per Room (a "rusty" lantern here). PM's
   proposal: not in v1, since there's no stacking and no per-Room variation decided.
