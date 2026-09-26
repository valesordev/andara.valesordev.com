---
id: AW-INF-022
title: The content repository — where Builders keep their source
epic: EPIC-05
component: infra
type: infra
status: draft
size: S
depends_on: [AW-CLI-002, AW-INF-020, AW-INF-021]
blocks: [AW-INF-023]
lane: architecture
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
- The repository, `valesordev/andara-world` (`[ASSUMPTION]`), private, with `main` protected: a pull
  request, signed commits, and a passing check.
- Layout: `packs/<pack-id>/` holds one Content Pack's `.aw` files with its `pack.aw`. There's a
  `README.md`, and a `CODEOWNERS` naming each pack's Builders.
- A `check` workflow on every pull request and on `main`. For each pack under `packs/`, it runs
  `andara-cli content fmt --check --path packs/<id>` and `andara-cli content validate --path
  packs/<id>`, using the `cli-dev` release (`AW-INF-020`) and the `andara.core` that `dev` has active
  (`AW-INF-021`).
- Findings as pull-request annotations at `file:line`, from `validate --output json`.
- A starter pack, `packs/example/`: one Zone and two Rooms that compile and validate, which the guide's
  tutorial copies.

### Out of scope
- Publishing from CI. A merge doesn't publish. Publishing, approving, and activating stay Builder
  actions from `andara-cli`, and the two-person rule is the server's (`AW-SRV-013`).
- The dev fixture pack `town`. Its source stays with this repository's spec corpus (`AW-INF-021`).
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
   prints `1 zones, 2 rooms, 0 templates, core andara.core@<n>`, where `<n>` is `dev`'s active
   core.
4. **Given** a pack with warnings only (`missing_reverse_exit`) **when** `check` runs **then** it
   passes and the warnings show as annotations at `warning` level.
5. **Given** a pull request touching only `packs/example/` **when** `check` runs **then** only that
   pack is validated, and the log names it.
6. **Given** `dev` unreachable **when** `check` runs **then** it fails with
   `check: can't get andara.core from dev; <reason>`. It never validates against a guessed core.
   The core source is item 4 in `docs/feedback/AW-INF-021-dev-content-store.md`.
7. **Given** someone without access to this code repository **when** they're given access to
   `andara-world` **then** they can clone it, run `check`'s two commands with the `cli-dev`
   binary, and get the same result as CI.

## Interface contract

- Repository: `valesordev/andara-world` (`[ASSUMPTION]`), private, default branch `main`.
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
- The workflow's inputs are `ANDARA_CLI_TAG` (default `cli-dev`) and whatever feedback item 4
  settles for the core.

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

- `[ASSUMPTION]` The name `andara-world`. Brian creates the repository, or grants architecture the
  right to create it in `valesordev`.
- `[ASSUMPTION]` `packs/<id>/` holds one pack per directory, and more than one pack per repository is
  normal (the fixture plus Brian's, and later other Builders').
- Item 4 in `docs/feedback/AW-INF-021-dev-content-store.md`: where CI gets `andara.core`.
