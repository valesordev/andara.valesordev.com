# 7. Reference

[← The Builder's Guide](README.md)

**This is the interim page.** `make builder-reference` (`AW-INF-028`) will generate these tables
from `andara-cli` itself, into `reference.md`, which replaces this page. Until then, here are the
tables as they stand, and where each comes from.

## Directions

Twelve, and only these, in lowercase. Each has a reverse, used for the warning about Exits with no
way back. The glossary's [World content](../glossary.md#world-content) section has the table.

| Direction | Reverse |
|-----------|---------|
| `north` | `south` |
| `northeast` | `southwest` |
| `east` | `west` |
| `southeast` | `northwest` |
| `up` | `down` |
| `in` | `out` |

Each pair works both ways: `south`'s reverse is `north`. The abbreviations players type (`n`, `ne`,
`u`) aren't valid in content.

## Component types

Every type the server defines today. Any of them may be on a Room, a Zone, or a Template.

| Type | Fields | Meaning |
|------|--------|---------|
| `andara.core.Behavior` | `name` (string) | names the Behavior an NPC runs. Recorded, not yet run |
| `andara.core.Dark` | none | the Room is unlit |
| `andara.core.Indoors` | none | enclosed, with no weather or sky |
| `andara.core.Memory` | none | the Entity remembers |
| `andara.core.NoMagic` | none | magic doesn't work here |
| `andara.core.NoRecall` | none | recall and self-teleport don't leave from here |

## `andara.core` Templates

Version 1, the `core:` line of `andara-cli version`.

| Template | Kind | Chain |
|----------|------|-------|
| `andara.core.Entity` | entity | Entity |
| `andara.core.Character` | entity | Entity › Character |
| `andara.core.Npc` | entity | Entity › Npc, adding `andara.core.Memory` |
| `andara.core.Item` | item | Item |

## Diagnostic codes

Every code, its severity, and what triggers it, is in the specification's
[error contract, §3](../specs/content-language/v1/errors.md#3-the-codes):
- [3.1, raised only by the compiler](../specs/content-language/v1/errors.md#31-raised-only-by-the-compiler);
- [3.2, raised by the compiler and the server alike](../specs/content-language/v1/errors.md#32-raised-by-the-compiler-defined-by-the-loader);
- [3.3, the two warnings](../specs/content-language/v1/errors.md#33-warnings);
- [3.5, raised only by the server](../specs/content-language/v1/errors.md#35-loader-only--never-raised-by-the-compiler),
  such as `zone_removed` at activation.

The usual fixes for the codes you'll meet are in [section 9](09-when-something-fails.md#the-errors-youll-meet).
