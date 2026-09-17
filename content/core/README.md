# `andara.core` — the base content pack

Base Templates ship as a Content Pack, not as Go structs (ADR-0010 decision 8): the compiler
needs `andara.core.Npc` to resolve `extends andara.core.Npc` on a laptop with no server, and a
Builder pack pins the core version it compiled against so skew is a legible error.

`templates/` holds the compiled form — one `TemplateDefinition` per declaration, flattened,
`resolved: true` — which is what the server loads (AW-SRV-022) and what `make deploy` publishes
and activates as `andara.core@<tag>` before a pod reports ready (AW-INF-007). The `.aw` sources
these compile from arrive with the Content Language (AW-CLI-005, AW-CLI-006); until then the
compiled form is authored by hand and `testdata/templates/templates/` carries a byte-identical
copy that `TestCoreSeedMatchesFixture` holds against this directory.

| Template | Kind | Chain | Components |
|----------|------|-------|------------|
| `andara.core.Entity` | entity | root | — |
| `andara.core.Character` | entity | Entity › Character | — (AW-SRV-014 adds what a Character needs) |
| `andara.core.Npc` | entity | Entity › Npc | `andara.core.Memory` |
| `andara.core.Item` | item | root | — |

`Item` is its own root: a chain has one kind, and an Item is not an Entity.
