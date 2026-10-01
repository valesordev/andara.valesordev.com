# 6. The language, by example

[← The Builder's Guide](README.md)

This is a tour, not the rules. Each part links to the section of the
[specification](../specs/content-language/v1/README.md) that defines it. Where the two disagree,
the specification is right, and the guide has a bug.

The example is `dev`'s own town, in this repository's `content/fixtures/town/`.

## The pack line

```
pack town requires andara.core@1
```

Every pack has exactly one `pack` line, in any file. It names the pack and the version of
`andara.core` it was written against. Files and declarations can come in any order.
[Packs, files, and order](../specs/content-language/v1/semantics.md#2-packs-files-and-order).

## Zones, Rooms, and Exits

```
zone town "Town" {
  fallback plaza

  room plaza "Market Plaza" {
    desc "A dusty square of packed earth."
    exit north -> hall
    exit east -> wilds.trail
    exit south -> docks.pier
  }

  room hall "Town Hall" {
    desc "Stone walls and faded banners."
    exit south -> plaza
  }
}
```

- `zone town "Town"`: an ID, then the name players see. IDs are lowercase.
  [Lexical structure and identifiers](../specs/content-language/v1/semantics.md#1-lexical-structure-and-identifiers).
- `fallback plaza`: where anyone standing in a Room that a later version removes is moved.
  [`fallback`](../specs/content-language/v1/semantics.md#fallback).
- `room plaza "Market Plaza"` and `desc`: a Room's ID, title and description.
  [Zones, Rooms, and Exits](../specs/content-language/v1/semantics.md#3-zones-rooms-and-exits) and
  [Prose](../specs/content-language/v1/semantics.md#prose).
- `exit north -> hall`: one way, from this Room. A way back is a second Exit, in the other Room. A
  bare target is a Room in the same Zone, and `wilds.trail` is Room `trail` of Zone `wilds`, which
  must be in the same pack. [Exit targets](../specs/content-language/v1/semantics.md#exit-targets)
  and [Directions](../specs/content-language/v1/semantics.md#directions).

Two warnings are worth knowing. Neither stops a pack:
- An Exit with no Exit back is legal, and it's usually a forgotten return, so `validate` warns
  `missing_reverse_exit`. Purgatory's `out` into the plaza is one-way on purpose.
- A Room nothing in its Zone leads into warns `orphan_room`.

Both are in [errors.md §3.3](../specs/content-language/v1/errors.md#33-warnings).

## Components on Rooms and Zones

```
zone cellars "The Cellars" {
  component andara.core.Indoors {}
  …
  room vault "The Vault" {
    component andara.core.Dark {}
    component andara.core.NoMagic {}
  }
}
```

A Component is a named piece of data on a Room, a Zone, or a Template. Most of today's are markers,
with no fields and an empty `{}`. Only the server defines Component types, so a type it doesn't know
is an error. [Section 7](07-reference.md#component-types) lists them.
[Components on Rooms and Zones](../specs/content-language/v1/semantics.md#components-on-rooms-and-zones).

## Templates

```
template Merchant extends andara.core.Npc {
  component andara.core.Behavior { name: "town.merchant" }
}

template Lantern extends andara.core.Item {}
```

A Template is a type of thing: an NPC, an Item. Template names are capitalized. A Template
`extends` one parent, either a Template of your pack (`Merchant`) or one of `andara.core`'s
(`andara.core.Npc`), and gets every Component its parent has. It can add Components, and set their
fields, but it can never remove one.
[Templates](../specs/content-language/v1/semantics.md#4-templates),
[`extends` resolution](../specs/content-language/v1/semantics.md#extends-resolution), and
[Components on Templates, and merge](../specs/content-language/v1/semantics.md#5-components-on-templates-and-merge).

`content inspect template town.Merchant --path <pack>` shows where each field came from.

Templates compile and load today, but nothing places an NPC or Item in a Room until milestone M4
([section 1](01-what-a-builder-does.md#what-content-can-express-today)).

## Comments and layout

`//` starts a comment. Comments are kept in your sources, which are published with each version, and
`fetch` brings them back. `andara-cli content fmt` owns the layout, so don't fight it.
[Comments](../specs/content-language/v1/semantics.md#comments) and
[Canonical formatting](../specs/content-language/v1/formatting.md).

## Where to read more

- [The semantics](../specs/content-language/v1/semantics.md): what every construct means.
- [The error contract](../specs/content-language/v1/errors.md): every diagnostic code, what triggers
  it, and where its position lands.
- [The grammar](../specs/content-language/v1/grammar.ebnf).
- [The corpus](../specs/content-language/v1/corpus/README.md): small, checked examples of every rule,
  valid and invalid.
