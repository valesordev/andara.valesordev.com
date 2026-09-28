---
id: AW-INF-020
title: Builders download andara-cli without a Go toolchain
epic: EPIC-06
component: infra
type: infra
status: draft
size: S
depends_on: [AW-INF-013]
blocks: [AW-INF-022, AW-INF-023]
lane: sre
risk: low
---

## Context

`andara-cli` is the Builder's whole toolchain (ADR-0004, `AW-CLI-003`), but the only way to get it
today is `make build` in a clone of this repository. A Builder is not a Developer (glossary), and
the content repository's CI (`AW-INF-022`) needs the binary without building it. `AW-INF-013`
publishes the server image on every merge to `main`, and nothing publishes the CLI.

`dev` runs `main` (`AW-INF-019`), so the CLI a Builder uses against `dev` should come from the same
commit stream. Protocol version negotiation (`AW-SRV-005`) already refuses a client too far from the
server, so an old download fails with a message rather than behaving differently.

## User story

As a builder, I want to download a working `andara-cli` for my machine from a stable URL, so that I
can compile, validate, and publish content without cloning or building the game.

## Scope

### In scope
- On every merge to `main`, after `publish`'s image job: build `andara-cli` for `linux/amd64`,
  `linux/arm64`, `darwin/amd64`, `darwin/arm64` and `windows/amd64`, and attach them to a rolling
  GitHub pre-release tagged `cli-dev` with a `SHA256SUMS` file.
- On a `v*` tag: the same binaries attached to that tag's release, as a normal release.
- Binaries stamped as `make build` stamps them: `andara-cli version` prints the version and commit.
- `make cli-release` builds the five archives and `SHA256SUMS` into `dist/`. CI runs that target, so
  the build is the same one a person can run locally.
- `make cli-release-check`: downloads `cli-dev` for the runner's platform, anonymously, verifies its
  checksum, and runs `andara-cli version`.

### Out of scope
- Package managers (Homebrew, apt, winget). Later, if Builders ask.
- Code signing and notarization for macOS. Gatekeeper's quarantine prompt is documented in the guide
  (`AW-INF-023`) instead.
- `andara-server` and `andara-projector` binaries. They ship as images.

## Acceptance criteria

1. **Given** a merge to `main` **when** the workflow completes **then** the `cli-dev` release has
   five archives and `SHA256SUMS`, and each archive's `andara-cli version` reports that merge's
   commit.
2. **Given** two merges in quick succession **when** both runs finish **then** `cli-dev` carries the
   later commit, never the earlier, using the same head-of-main guard `image_publish.sh` uses for
   `:dev`.
3. **Given** no GitHub credentials **when** `make cli-release-check` runs **then** it downloads,
   verifies the checksum, prints `cli-release-check: andara-cli <version> (<commit>) ok`, and exits
   0.
4. **Given** an archive whose checksum doesn't match **when** `make cli-release-check` runs **then**
   it exits 1 with `cli-release-check: checksum mismatch for <file>`.
5. **Given** a `v*` tag pushed **when** the workflow completes **then** a non-pre-release named for
   the tag exists with the same five archives, and `cli-dev` is unchanged.
6. **Given** a pull request **when** CI runs **then** `make cli-release` builds all five archives,
   and nothing is published.
7. **Given** the `linux/amd64` binary on a machine with no Go toolchain **when**
   `andara-cli content fmt --check --path docs/specs/content-language/v1/corpus/valid/town` runs
   **then** it exits 0. The binary is statically linked (`CGO_ENABLED=0`). `fmt` needs no cached
   core pack, so it runs with no server either.

## Interface contract

- `make cli-release`: `## cli-release: build andara-cli for every Builder platform into dist/, with
  SHA256SUMS`.
- `make cli-release-check [TAG=cli-dev]`: `## cli-release-check: download a published andara-cli
  anonymously, verify it, and run it`.
- Archive names: `andara-cli_<version>_<os>_<arch>.tar.gz` (`.zip` for Windows), each holding
  `andara-cli` (or `andara-cli.exe`), `LICENSE` and `NOTICE`.
- Stable download URL: `https://github.com/valesordev/andara.valesordev.com/releases/download/cli-dev/<archive>`.
  Because the version is in the file name, the guide names `SHA256SUMS` as the index.
- Exit codes: 0 ok; 1 build, download, or checksum failure; 2 usage.
- No new environment variables.

## Data / state impact

None. Releases are GitHub objects. The `cli-dev` release is replaced on every merge, and `v*`
releases are permanent.

## Observability requirements

- **Metrics / Traces:** none; it's a CI job.
- **Logs:** the workflow's step output, ending with the `cli-release-check` line. The job summary
  names the commit `cli-dev` now carries and lists the five archives with their checksums, so
  "which CLI is current" is answered from the run, not by downloading. *(SRE observability review,
  2026-09-28.)*
- **Alerts:** none. A failed publish is a red run on `main`, the same signal a failed `:dev` image
  publish gives (`AW-INF-013`). A `cli-dev` older than `dev`'s image is the AC-2 guard's to
  prevent. It isn't alerted on.

## Test plan

- **Unit:** none.
- **Integration:** the workflow runs `make cli-release-check` against what it just published (AC-3).
  AC-2 is checked the way `AW-INF-013` checked it.
- **Manual/operator:**
  ```
  make cli-release           # dist/ holds five archives and SHA256SUMS
  make cli-release-check     # "andara-cli <version> (<commit>) ok"
  ```

## Definition of done

CLAUDE.md §8.

## Open questions

- `[ASSUMPTION]` Attaching the binaries to this repository's releases is acceptable. This
  repository is public, so a Builder downloads them with no access to it. A Builder never needs to
  read or write the source.
- `[ASSUMPTION]` Five platforms. Brian works on Linux. Dropping Windows is a one-line change.
