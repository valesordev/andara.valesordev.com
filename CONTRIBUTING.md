# Contributing to Andara's World

Thanks for looking. This repository is the engine, toolchain, and design record for
Andara's World, a server-authoritative MUD. It is built in the open, in two lanes, with an
unusual amount of the work done by Claude Code against a written charter — read
[`CLAUDE.md`](CLAUDE.md) first; it is the operating manual for this repo, human or not.

## What to contribute here, and what not to

- **Engine, tooling, infrastructure, and the design record** — yes. Bugs, tests, stories,
  ADRs, chart and CI improvements, runbooks.
- **World content** — not here. Zones, items, NPCs, and dialogue are authored outside this
  repository and published through `andara-cli` (ADR-0004). Fixtures under `testdata/` are
  for the loader, not the world.
- **Game design, lore, and mechanics** — Brian's call, not a pull request. Propose in an
  issue; a story that invents a mechanic or a name will be sent back at grooming.

## Ground rules `make check` enforces

`make bootstrap && make up && make check` is the whole onboarding path; if it fails on a
clean machine that is a bug here, not in your setup. `make check` is the gate CI runs —
format, vet, lint, tests, protobuf freshness, story validation, manifest validation, chart
tests, and the license check — and a PR that fails it will not be reviewed until it passes.

Two constraints worth knowing before you write code:

- `server/sim` is dependency-free and deterministic: no network, datastore, filesystem,
  clock, or global randomness. `depguard` enforces the import list.
- The interface contract in a story is written *before* the code (`CLAUDE.md` §2). A change
  that alters a contract updates the story in the same PR.

## Branches, commits, pull requests

- Branch from `main`. Work that belongs to a story is named for it:
  `aw-srv-014-room-graph-loader`.
- Commits that belong to a story end with a `Story: AW-SRV-014` trailer.
- Keep PRs to one story or one concern. CI must be green.
- Architectural decisions get an ADR in `docs/adr/`; ADRs are never deleted, only
  superseded.

## Licensing

See [`LICENSING.md`](LICENSING.md) for the map. What it means for a contribution:

- Code you contribute is licensed under **Apache-2.0**; documentation under `docs/` under
  **CC BY-SA 4.0**. By opening a pull request you agree your contribution is licensed under
  the same terms as the file it changes — inbound equals outbound, no CLA.
- New Go, Python, shell, and protobuf files carry the two-line SPDX header shown in
  `LICENSING.md`. `make license-check` fails without it.
- You warrant that you have the right to contribute what you submit. Do not paste in code
  or prose under a license that is not compatible with the one it lands under.
