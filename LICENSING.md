# Licensing

This repository holds three kinds of work under three licenses. The split is declared
mechanically in [`REUSE.toml`](REUSE.toml), verified by `make license-check`, and explained
here.

| What | Where | License | Copyright |
|------|-------|---------|-----------|
| Code, build, deploy, configuration, generated code, test fixtures | Everything not listed below — including the protocol under `docs/specs/protocol/` | [Apache-2.0](LICENSE) | Valesor Development |
| Documentation | `docs/` (prose: ADRs, epics, stories, specs, SLOs, runbooks, glossary, roadmap) and the generated `BACKLOG.md` | [CC BY-SA 4.0](LICENSES/CC-BY-SA-4.0.txt) | Valesor Development |
| Creative work — *Andara's World* itself | The name; the world and its setting; every work of canon | Open Canon (below) | solo7.media |

## Code — Apache License 2.0

Everything that builds, runs, ships, or tests the game is under the
[Apache License, Version 2.0](LICENSE): Go and Python sources, the Makefile and scripts, the
Helm chart, compose stack, and Kubernetes manifests, CI workflows, and the `.proto` files
under `docs/specs/protocol/`. The protocol lives among the documentation but it is source —
it compiles into `gen/`, and generated code inherits the license of what it was generated
from. The same goes for any migration or schema file under `docs/specs/schema/`: `.sql` and
`.json` there are Apache-2.0 by the same `REUSE.toml` override, and a new machine-readable
format under `docs/` needs adding to it.

The fixtures under `testdata/` are code, not content. The zones there ("Town", "Docks",
"Wilds") exist to exercise the loader and are not part of Andara's World.

## Documentation — CC BY-SA 4.0

The design record under `docs/` — architecture decision records, epics, stories, protocol
and schema prose, SLOs, runbooks, the glossary, and the roadmap — is licensed under
[Creative Commons Attribution-ShareAlike 4.0 International](LICENSES/CC-BY-SA-4.0.txt), the
Valesor Development standard for written work. `BACKLOG.md` is generated from the stories
and carries their license.

Attribution: *"Andara's World documentation, © Valesor Development, CC BY-SA 4.0,
https://github.com/valesordev/andara.valesordev.com"*.

## Creative work — Open Canon

Andara's World is a creative work of [solo7.media](https://solo7.media). The software in this
repository is a general MUD engine and toolchain that happens to be built to run it; the
world is licensed separately from the engine, under solo7.media's **Open Canon** split:

| Tier | Covers | License |
|------|--------|---------|
| **Tier 1 — World and Setting** | The world and its setting: the place, its peoples, history, and vocabulary — the stage others may build on | [CC BY-SA 4.0](https://creativecommons.org/licenses/by-sa/4.0/) |
| **Canon works** | The authored works that constitute the official canon of Andara's World | [CC BY-NC-ND 4.0](https://creativecommons.org/licenses/by-nc-nd/4.0/) |

In plain terms: you may create your own works set in Andara's World and publish them, as
long as you credit solo7.media and release them under the same share-alike terms. You may
copy and share the canon works as they are, for non-commercial purposes, with credit. You
may not alter them or sell them.

### Where the creative work lives

Almost none of it is here. Content packs — zones, items, NPCs, dialogue — are authored
outside this repository and published through `andara-cli` (ADR-0004), so the license of a
given pack is declared where the pack is. What this repository does contain of the creative
work is the name and the setting vocabulary in `docs/glossary.md`. That material is Tier 1,
licensed CC BY-SA 4.0 — the same license as the documentation around it — and attributable
to solo7.media.

No canon work is in this repository, so `LICENSES/` does not carry the CC BY-NC-ND 4.0 text:
`reuse lint` fails on a license text nothing references, and a license file no path uses
would be a claim about the tree that is not true. The day a canon artifact does land here,
the same commit adds `LICENSES/CC-BY-NC-ND-4.0.txt` and a `REUSE.toml` annotation for it.

Attribution for Tier 1 material: *"Andara's World, © solo7.media, CC BY-SA 4.0"*.

## Names and marks

*Andara's World*, *Valesor Development*, and *solo7.media* are names and marks of their
owners. Apache-2.0 §6 and Creative Commons §2(b)(2) each say so, and this says it plainly:
none of the licenses in this repository grants any right to use them. A derivative world or
a fork of the engine needs its own name.

## How the split is enforced

- [`REUSE.toml`](REUSE.toml) maps every path to a license and a copyright holder. Later
  entries win for a path that matches more than one, which is how `docs/specs/protocol/`
  stays Apache-2.0 inside a CC BY-SA 4.0 tree.
- `LICENSES/` holds the full text of every license the tree references, under its SPDX
  identifier. The root `LICENSE` is the Apache-2.0 text again, because that is the one file
  GitHub reads to label the repository.
- Hand-written Go, Python, shell, and protobuf sources carry a two-line SPDX header, so a
  file copied out of the tree keeps its license with it. Generated code under `gen/`
  inherits the header from its `.proto`.
- `make license-check` runs [`reuse lint`](https://reuse.software) and the header check; it
  is part of `make check` and therefore of CI.

<!-- REUSE-IgnoreStart -->
The header, for a new source file:

```go
// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development
```

For Python and shell it is the same two lines after `#`, below the shebang.
<!-- REUSE-IgnoreEnd -->

A new *kind* of file outside `docs/` — a template, a golden — needs no header; the `**`
annotation in `REUSE.toml` already covers it as Apache-2.0. Inside `docs/` the default is
CC BY-SA 4.0, so a new machine-readable format there needs the Apache-2.0 override extended.
A new *tree* of documentation or creative material needs a `REUSE.toml` entry. In neither
case will `make license-check` tell you so; check the map above when adding one.

## Open questions

Held by Brian; none affects the software license.

1. The official content packs contain both setting (a place, its history) and authored
   work (room prose, dialogue, quest text). Which of their parts are Tier 1 and which are
   canon is a creative decision this document does not make. When it is made, the Content
   Version manifest (ADR-0004) is the place to declare a pack's license, so the boundary is
   stated in data and travels with the pack.
2. When the Open Canon framework is published at solo7.media, link it from the table above
   in place of the inline description.
