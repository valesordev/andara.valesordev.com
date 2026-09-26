---
id: AW-INF-016
title: make up builds the server image from the tree it runs in
epic: EPIC-01
component: infra
type: bug
status: ready
size: S
depends_on: [AW-INF-002]
blocks: []
lane: architecture
risk: low
---

## Context

`scripts/stack.sh up` runs `docker compose up -d --wait` with no build step. Compose builds
`andara-server` only when no image exists, so once one does, `make up` starts it whatever the tree
holds (#73). The SPRINT-01 demo hit this on a fresh clone of `origin/main` `343cdbe`. `make up`
started an `andara-andara-server` image built at 2026-09-25T20:47Z, before #93–#95 landed AW-SRV-012.
Reaching the tree's server took a hand-typed `docker compose … build andara-server`, which the demo
marks `§9 defect → AW-INF-016`. Every §8 record that says "image rebuilt" relies on the same
unwritten step.

CLAUDE.md §9 makes `make bootstrap && make up && make check` the whole onboarding path, and it only
holds if `make up` runs the tree. CI is unaffected, because `stack.yaml` starts on a runner with no
image.

## User story

As an operator, I want `make up` to run the server built from the tree I'm standing in, so that a
demo or a §8 record observes the code under review and not an older build.

## Scope

### In scope
- `scripts/stack.sh up` builds `andara-server` from `deploy/compose/Dockerfile.server` before
  starting it, every time, relying on the layer cache for the no-change case.
- The build passes the same three build args `make image` passes: `VERSION`, `COMMIT` and
  `REVISION`. `REVISION` becomes the `org.opencontainers.image.revision` label, and `COMMIT` is
  stamped into both binaries as `main.commit`, which `andara_build_info{commit}` reports. Leaving
  `COMMIT` out would stamp the Dockerfile's default, `unknown`.
- A tree with uncommitted changes gives `COMMIT` and `REVISION` the same `-dirty` suffix, so the
  metric and the label never disagree about what ran.
- `make up` prints the running server's revision label on its summary, next to `andara-server`.
- `make up` on an already-running stack whose server image changed recreates only `andara-server`.

### Out of scope
- The projector's image and targets: #80, groomed with `AW-INF-007`'s snapshot-store decision.
- The compose snapshot path: #74, groomed with `AW-SRV-007`.
- The published ghcr image: `AW-INF-013`.

## Acceptance criteria

1. **Given** an `andara-andara-server` image built from an older commit **when** `make up` runs on
   a clean tree at `HEAD` **then** the running container's `org.opencontainers.image.revision`
   equals `git rev-parse HEAD`, and the summary prints `andara-server revision <sha>`.
2. **Given** a tree with an uncommitted change under `server/` **when** `make up` runs **then** the
   label and the summary line read `<sha>-dirty`.
3. **Given** a stack already up from `HEAD` **when** `make up` runs again with no change **then**
   no container is recreated (every container's `Created` time is unchanged), the build reports
   every layer cached, and the command exits 0.
4. **Given** a stack already up **when** a commit changes `server/` and `make up` runs **then**
   only `andara-server` is recreated, and it passes `--wait` before `make up` returns.
5. **Given** `make up` on a clean tree and on a dirty one **when** the server's `/metrics` is read
   **then** `andara_build_info{commit}` is never `unknown`. Its value, less any `-dirty` suffix, is
   a prefix of the container's revision label less the same suffix, and both carry `-dirty`
   exactly when the tree is dirty.
6. **Given** a build that fails (a compile error) **when** `make up` runs **then** it exits
   non-zero with `up: andara-server image build failed; see the output above`, and the previously
   running server, if any, is left running.

## Interface contract

- `make up`: unchanged invocation. It gains a build of `andara-server` before
  `up … andara-server` whenever the server profile is active (`server_profile_args`).
- Build args: the Makefile's existing `VERSION` (`git describe --tags --always --dirty`),
  `COMMIT` (`git rev-parse --short HEAD`) and `REVISION` (`git rev-parse HEAD`), each passed as
  `make image` passes them, with `-dirty` appended to `COMMIT` and `REVISION` on a dirty tree.
  `stack.sh` receives them from the Makefile rather than recomputing them, so `make up` and
  `make image` can't stamp a build differently.
- **The suffix is added in the Makefile's `COMMIT` and `REVISION` themselves**, not in `stack.sh`,
  so `make image` and `make build` stamp `-dirty` too and the three can't disagree. Dirty means
  what `git describe --dirty` (which `VERSION` already uses) means: a tracked file differs from
  `HEAD`. An untracked file doesn't count, even if it lands in the build context.
  *(Amended at architecture's contract review, 2026-09-26: the draft named both the Makefile
  as the single source and a suffix `make up` alone added, and those can't both hold.)*
- A variable given on the command line (`make up COMMIT=…`) still wins, as `?=` already makes it.
  CI's `publish.yaml` builds from a clean checkout, so the published image never carries `-dirty`.
- Summary line, added after `andara-server localhost:8443 (gRPC, TLS)`:
  `  andara-server revision   <sha>[-dirty]`, read back from the running container's label, not
  from the build args.
- Exit codes: 0 up; 1 build failed or a service unhealthy (existing).
- No new environment variables.

## Data / state impact

None. Volumes and `.local/` are untouched. A recreated server replays the log as any restart does.

## Observability requirements

- **Metrics:** none new. `andara_build_info{commit}` from the server already carries the commit.
  AC-5 compares it against the label.
- **Logs:** `stack.sh` prints the build's result line and the revision; no structured logging
  (a developer script).
- **Traces / Alerts:** none.

## Test plan

- **Unit:** `scripts/tests/`: the revision string with a clean and a dirty tree, and the summary
  line reading the label.
- **Integration:** `stack.yaml` asserts that the summary's revision equals `github.sha`, and that
  `andara_build_info{commit}` scraped from the server is a prefix of it (AC-5), polled to a
  deadline per `live-assertions.md`.
- **Manual/operator:**
  ```
  make up                       # prints "andara-server revision <HEAD>"
  make up                       # again: nothing recreated
  curl -s 127.0.0.1:8080/metrics | grep andara_build_info   # commit matches
  ```

## Definition of done

CLAUDE.md §8, plus: #73 is closed by the merging PR.

## Open questions

- `[ASSUMPTION]` Building every time is acceptable, because the layer cache keeps a no-change
  build short. Architecture records the measured no-change `make up` time in the verification
  record. If it's over 10 s, the record names what busts the cache.
- The build context is the repo root minus `.dockerignore`. Until #78 removes it, that context
  includes the 35 MB `andara-projector` at the root. It doesn't bust the cache, but it's sent to the
  daemon on every `make up`. That's noted here, not fixed here.
