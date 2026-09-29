---
id: AW-INF-020
title: Builders download andara-cli without a Go toolchain
epic: EPIC-06
component: infra
type: infra
status: review
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
- Bundling `andara.core`. The binary embeds it at build time (`AW-CLI-002`, from
  `content/core/VERSION`), so the archive carries no core file and needs no install step.
  Checking the embedded core against `dev`'s is `AW-INF-022`'s.

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
   **then** it exits 0. The binary is statically linked (`CGO_ENABLED=0`). `fmt` needs no core
   pack, so it runs with no server either.

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

- **Resolved 2026-09-28 (architecture): the binaries attach to this repository's releases.**
  `valesordev/andara.valesordev.com` is public, so a Builder downloads them with no access to it.
- **Resolved 2026-09-29 (Brian): five platforms.** `linux` and `darwin` on `amd64` and `arm64`,
  and `windows/amd64`, as built.

## Contract review (architecture, 2026-09-28)

SRE's observability review is in `docs/feedback/AW-INF-020-cli-release.md` and the story. The story
is `ready`.

1. **Core ships inside the binary, not beside it** (`docs/feedback/AW-INF-021-dev-content-store.md`,
   item 4). SRE's version of (b) had the archive carry the compiled core, and `andara-cli version`
   print it. Embedding and the version line are Go source under `cmd/`, which is
   implementation's, so both are in `AW-CLI-002`. This story builds whatever `cmd/andara-cli` is
   at the commit, and gains no dependency on `AW-CLI-002`. Once `AW-CLI-002` has merged, AC-1's
   `andara-cli version` also prints `andara.core@<N>`. That line is `AW-CLI-002`'s AC, and this
   story's AC-3 matches only `andara-cli <version> (<commit>)`, so it holds before and after.
2. **AC-7 keeps `fmt`.** It's the one content command that needs neither a server nor a core. That's
   what makes it a clean "no Go toolchain" check before `AW-CLI-002` lands. `AW-INF-022`'s `check`
   runs `validate` with the downloaded binary, which covers the embedded core end to end.
3. **The public-repository assumption is resolved.** It's public (`gh repo view`, 2026-09-28).
   The five-platform assumption doesn't touch the contract, and it stays.

## Verification record (SRE, 2026-09-29)

On `sre/aw-inf-020-builders-download-andara-cli-without-a-go-toolchain`.

| AC | How | Result |
|----|-----|--------|
| 1 | `publish.yaml`'s `cli` job, after `publish`: `make cli-release`, `make cli-release-publish TAG=cli-dev`, then an anonymous download whose `andara-cli version` must name the commit the `cli-dev` tag points at | **owed on the first merge** |
| 2 | `cli_release_publish.sh` moves the `cli-dev` tag only when `HEAD` is `origin/main`'s head, as `image_publish.sh` does for `:dev`; otherwise it exits 0 and says which commit owns it | **owed on the first merge**; checked the way AW-INF-013 checked it |
| 3 | `Check.test_a_good_release_prints_version_and_commit`: a release laid out on disk, fetched by `file://`, prints `cli-release-check: andara-cli <version> (<commit>) ok`, exit 0. Against GitHub, the workflow's anonymous step runs it under `env -u GH_TOKEN` | pass locally; **GitHub half owed on the first merge** |
| 4 | `Check.test_a_changed_archive_is_a_checksum_mismatch`: exit 1, `cli-release-check: checksum mismatch for <file>` | pass |
| 5 | `release.yaml` on `v*` tags: `make cli-release-publish TAG=<tag>` creates a normal release (never `--prerelease`) and doesn't touch `cli-dev` | **owed**: needs a `v*` tag, which is Brian's to cut |
| 6 | `ci.yaml`'s `cli-release` job runs `make cli-release` on every pull request. The workflow's token is read-only | this PR's run |
| 7 | Locally: the `linux/amd64` binary is `ELF … statically linked`, and `content fmt --check --path …/corpus/valid/town` exits 0. In CI, the same command runs in `busybox:stable`, which has no Go toolchain and no libc | pass locally; this PR's run |

- **`VERSION` now ignores non-release tags.** `git describe --tags` would have found the `cli-dev`
  tag on a recent `main` commit and stamped every later build `cli-dev-N-g…`. `VERSION` matches
  `v*` only (`Targets.test_version_ignores_the_cli_dev_tag`, which fails without the change).
- **`TAG` stays `dev` for the image targets.** The CLI targets take `TAG` only from the command line
  and default to `cli-dev` (`Targets.test_check_defaults_to_cli_dev`).
- **One target past the contract:** `make cli-release-publish`, so that CI's publish step is a
  make target (§9), as `image-publish` is.
- **`.gitignore`** gains `/dist/`. It isn't on SRE's writable list.
- **`make cli-release`:** five archives in 77 s on the box. Each holds the binary, `LICENSE` and
  `NOTICE`.

## Verification after merge (SRE, 2026-09-29)

Three `publish` runs on `main` since #158 merged: `65889c8` (run 36635779296), `d3d855e`
(36637878128) and `f3648da` (36639509017). All three are green, and each `cli` job logged
`cli-release-publish: cli-dev -> <commit>` and then its anonymous `cli-release-check`.

| AC | Result |
|----|--------|
| 1 | **Pass.** `cli-dev` carries `andara-cli_f3648da_{darwin,linux}_{amd64,arm64}.tar.gz`, `…_windows_amd64.zip` and `SHA256SUMS`, and the tag names `f3648daa0372`. Each run's download reported its own commit: `andara-cli 65889c8 (65889c8) ok`, then `d3d855e`, then `f3648da` |
| 2 | **Pass by construction, as AW-INF-013's AC-6 was.** `cli-dev` moved `65889c8 → d3d855e → f3648da`, each run finishing before the next merge, and the previous version's archives were deleted each time: the release holds only `f3648da`'s. The runs never overlapped, so the branch that leaves the tag to a newer run (`cli_release_publish.sh`, the `origin/main` comparison) hasn't executed. That branch is read, not run: the only way to exercise it is to race two merges on `main` |
| 3 | **Pass.** From the box with no `GH_TOKEN`: `cli-release-check: andara-cli f3648da (f3648da) ok`, exit 0. CI's step runs under `env -u GH_TOKEN` |
| 5 | **Owed.** Needs a `v*` tag, which is Brian's to cut |

**§8 instrumentation (SRE):** the Observability requirements are the job's log line and its
summary. Each `cli` job ends with the `cli-release-check` line, and its `publish cli-dev` step,
which writes the summary table (commit, archives, checksums), exits 0. No metrics, traces or
alerts are specified. The item holds.

**The story stays at `review` on AC-5 alone** (a `v*` tag). Codex on #164 caught that the
five-platform `[ASSUMPTION]` also held it. Brian resolved that on 2026-09-29, keeping all five.
