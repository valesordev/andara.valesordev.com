---
id: AW-INF-022
title: The content repository — where Builders keep their source
epic: EPIC-05
component: infra
type: infra
status: ready
size: S
depends_on: [AW-CLI-002, AW-INF-020, AW-INF-021]
blocks: [AW-INF-023]
lane: sre
risk: low
---

## Context

ADR-0004 puts the live copy of content in the content store and keeps Builders out of this
repository. It doesn't say where a Builder's Content Language source lives between edits. Brian
decided (2026-09-26): **a separate content repository**, the Content Repository. It's private, and
Builders get access to it and not to the code. It gives Builders history, review, and diffs. The
store stays authoritative for what's live, and `content fetch` still recovers what was published.
This repository is public, and a private one keeps lore out of it.

Builders publish from their own machines with `andara-cli` (ADR-0004, `AW-CLI-003`). The repository
holds source and checks it. It doesn't publish. It's the first thing the Builder's Guide
(`AW-INF-023`) tells a new Builder to clone.

## User story

As a builder, I want one repository that holds my pack's source and checks every change I propose,
so that I publish only what compiles and validates, and I keep a history of what I wrote.

## Scope

### In scope
- The repository, `valesordev/andara-world`, private, with `main` protected: a pull
  request, signed commits, and a passing check.
- Layout: `packs/<pack-id>/` holds one Content Pack's `.aw` files with its `pack.aw`. There's a
  `README.md`, and a `CODEOWNERS` naming each pack's Builders.
- A `check` workflow on every pull request and on `main`. For each pack under `packs/`, it runs
  `andara-cli content fmt --check --path packs/<id>` and `andara-cli content validate --path
  packs/<id>`, using the `cli-dev` release (`AW-INF-020`). The core it validates against is the one
  that binary embeds (`AW-CLI-002`). No network, and no credential for `dev`.
- Findings as pull-request annotations at `file:line`, from `validate --output json`.
- A starter pack, `packs/example/`: one Zone, `example`, and two Rooms joined both ways, that compile
  and validate with no findings. The guide's tutorial copies it.

### Out of scope
- Publishing from CI. A merge doesn't publish. Publishing, approving, and activating stay Builder
  actions from `andara-cli`, and the two-person rule is the server's (`AW-SRV-013`).
- The dev fixture pack `town`. Its source is `content/fixtures/town/` in the code repository
  (`AW-SRV-037`), and `AW-INF-021` seeds it.
- Behavior (Python) source. It arrives with `AW-SRV-016`, which will say whether it's in the same
  pack directory.
- The guide itself: `AW-INF-023`. The repository's `README.md` links to it.

## Acceptance criteria

1. **Given** a pull request that adds an Exit to a Room that doesn't exist **when** `check` runs
   **then** it fails, the pull request shows an annotation at that `.aw` file and line with
   `unknown_room`, and `main` can't merge it.
2. **Given** a pull request whose files aren't in `fmt` form **when** `check` runs **then** it fails
   naming each file, and running `andara-cli content fmt --path packs/<id>` locally fixes it.
3. **Given** the starter pack unchanged **when** `check` runs on `main` **then** it passes and
   prints `1 zones, 2 rooms, 0 templates, core andara.core@<n>`, where `<n>` is the core the
   `cli-dev` binary embeds.
4. **Given** a pack with warnings only (`missing_reverse_exit`) **when** `check` runs **then** it
   passes and the warnings show as annotations at `warning` level.
5. **Given** a pull request touching only `packs/example/` **when** `check` runs **then** only that
   pack is validated, and the log names it.
6. **Given** a machine that can reach `dev` (Brian's, on the tailnet) **when** `andara-cli version`
   and `andara-cli server info` run with the `cli-dev` binary **then** the `andara.core@<n>` each
   prints is the same, after `dev` has rolled to the commit `cli-dev` carries. It's run by hand
   and recorded in the verification record. It isn't a CI step, because GitHub's runners can't
   reach `dev`.
7. **Given** someone without access to this code repository **when** they're given access to
   `andara-world` **then** they can clone it, run `check`'s two commands with the `cli-dev`
   binary, and get the same result as CI.
8. **Given** a pack whose `pack.aw` requires an `andara.core` version other than the one the
   `cli-dev` binary embeds **when** `check` runs **then** it fails with `core_version_mismatch`
   naming both numbers. A core bump turns every pack's `check` red until its `requires` line is
   updated. That's intended: it's where the Builder learns the core changed.

## Interface contract

- Repository: `valesordev/andara-world`, private, default branch `main`. Brian creates it, since
  creating a repository in `valesordev` is an org-owner action.
- Layout:
  ```
  README.md                 # what this is, and a link to the Builder's Guide
  CODEOWNERS                # packs/<id>/  @<builder> ...
  packs/<pack-id>/pack.aw   # pack <pack-id> requires andara.core@<n>
  packs/<pack-id>/*.aw
  .github/workflows/check.yaml
  ```
- A directory name under `packs/` equals the `pack` declaration in its `pack.aw`. A mismatch fails
  `check` with `check: packs/<dir> declares pack <id>`.
- `check` runs only `andara-cli` commands, plus the download of the release binary.
- The workflow's one input is `ANDARA_CLI_TAG` (default `cli-dev`). The core comes with the binary.
- A pack's Zone IDs must not collide with the fixture's, which are `town`, `docks`, `wilds` and
  `purgatory`. That's the server's load-time rule, not `check`'s, since `check` validates one pack
  alone. The README says so, and `AW-INF-023` teaches it.

## Data / state impact

None in this repository or in the store. The Content Repository is new and holds only source.

## Observability requirements

- **Metrics / Traces / Alerts:** none; it's a CI workflow in another repository.
- **Logs:** the workflow's output, one validate summary line per pack.

## Test plan

- **Unit:** none.
- **Integration:** the starter pack on `main` (AC-3), plus one throwaway pull request per failure
  (AC-1, AC-2, AC-4), recorded in the verification record with links.
- **Manual/operator:** a fresh clone, as a Builder:
  ```
  andara-cli content fmt --check --path packs/example
  andara-cli content validate --path packs/example   # "1 zones, 2 rooms, 0 templates, core andara.core@<n>"
  ```

## Definition of done

CLAUDE.md §8, with `make check` read as this workflow for the new repository. The verification
record links the throwaway pull requests.

## Open questions

- **Resolved 2026-09-28 (architecture): the name is `andara-world`.** It names a repository, not
  lore. Brian creates it.
- `[ASSUMPTION]` `packs/<id>/` holds one pack per directory, and more than one pack per repository is
  normal (the starter pack plus Brian's, and later other Builders').
- **Resolved 2026-09-28 (architecture):** CI gets `andara.core` from the `andara-cli` binary
  (`docs/feedback/AW-INF-021-dev-content-store.md`, item 4).

## Contract review (architecture, 2026-09-28)

SRE's review is in `docs/feedback/AW-INF-021-dev-content-store.md`: no change to Observability. The
story is `ready`.

1. **Core comes from the binary** (feedback item 4). The old AC-6, "`dev` unreachable fails
   `check`", described a failure mode that no longer exists. It's replaced by SRE's check that the
   binary's core equals `dev`'s, run by hand where `dev` is reachable.
2. **AC-8 is new: a core bump fails `check`.** With one embedded core, `validate` can check exactly
   one `requires` version. The server still loads packs built against an older core
   (`AW-SRV-012`'s skew rule refuses only newer). So a red `check` after a core bump means "update
   `requires`", not "your pack won't load". The guide says so.
3. **The fixture's source moved** to `content/fixtures/town/` (feedback item 3). It isn't in the
   Content Repository, and the starter pack is the only pack there at first.
4. **The starter pack's Zone is `example`,** and the Zone-ID collision with the fixture is stated.
   A Builder copying the starter pack must not name a Zone `town`.
5. **The repository name is decided,** so the Interface contract carries no `[ASSUMPTION]`.
