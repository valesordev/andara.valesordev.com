---
id: AW-INF-001
title: Repo scaffold, Makefile automation surface, story tooling, and CI skeleton
epic: EPIC-01
component: infra
type: infra
status: review
size: M
depends_on: []
blocks: [AW-INF-002, AW-INF-003, AW-INF-004, AW-SRV-001, AW-SRV-005, AW-SRV-020, AW-CLI-001]
assignee: claude-code
risk: low
---

## Context

Nothing in this repo can be built, validated, or shipped until there is a single automation surface
and a module layout to build into. CLAUDE.md §9 makes the make target the unit of automation: a
documented sequence of shell commands that is not a target is a defect. This story establishes that
surface and the conventions every later story inherits.

It also establishes the planning tooling — story scaffolding, frontmatter validation, backlog
generation, dependency graphing — because a backlog that is hand-maintained drifts within a week.
Per CLAUDE.md §2, all of this runs *around* the game, so it is Claude Code's to write, not Cursor's.

## User story

As a developer, I want `make bootstrap && make check` to work on a freshly cloned repo, so that I
can start on any story without reading a wiki or asking anyone how the build works.

## Scope

### In scope
- Go module at the repo root, package layout for `server/`, `admin/`, and `cmd/`.
- `Makefile` implementing the CLAUDE.md §9 baseline target list, with `make help` as the default goal.
- `scripts/` implementations for `backlog`, `validate-stories`, `graph`, `story`, `adr`.
- Linter, formatter, and editor configuration.
- Protobuf toolchain and `make proto` / `make proto-check`, since ADR-0007 makes protobuf the schema
  authority for the wire, the log, snapshots, and content — and generated code is committed.
- `.gitignore`, `.editorconfig`, `.golangci.yml`.
- GitHub Actions workflow running the identical `make check` a developer runs.
- Component `README.md` stubs for `server/`, `admin/`, `client/`.

### Out of scope
- Local runtime stack (server + datastores + observability) — `AW-INF-002`.
- Kubernetes manifests, Helm charts, `make k8s-dry` producing real output — `AW-INF-003`.
- Container image build and publish. No story owns this yet; `deploy/compose/Dockerfile.server`
  builds a local-stack image from source and is explicitly not a production image.
- Any Go application source. `make check` passes on an empty module and stays passing as source
  arrives.

## Acceptance criteria

1. **Given** a clean clone on a machine with Go ≥ 1.24 and Python ≥ 3.9 **when** a developer runs
   `make bootstrap` **then** it exits 0, installs or verifies every tool `make check` needs, and a
   second immediate run also exits 0 with no side effects.
2. **Given** any working tree **when** a developer runs `make` with no arguments **then** the target
   list prints with one line of description per target and exit code 0.
3. **Given** a clean clone **when** a developer runs `make check` **then** fmt, vet, lint, test,
   story validation, and protobuf-codegen freshness all run, and the command exits 0.
4. **Given** a story file whose `depends_on` names `AW-SRV-999`, which does not exist **when**
   `make validate-stories` runs **then** it exits 1 and prints one line naming the offending file,
   the field, and the unresolved ID.
5. **Given** two story files whose `depends_on` form a cycle **when** `make validate-stories` runs
   **then** it exits 1 and prints the cycle as an ID chain.
6. **Given** a story file missing a required frontmatter key, or carrying a value outside that key's
   enumeration **when** `make validate-stories` runs **then** it exits 1 and names the file, the key,
   and the permitted values.
7. **Given** the current `docs/stories/` contents **when** `make backlog` runs **then** `BACKLOG.md`
   is regenerated deterministically — running it twice produces no diff on the second run.
8. **Given** `make backlog` has been run and a story is then edited **when** `make check` runs
   **then** it exits 1 with a message stating that `BACKLOG.md` is stale and naming `make backlog`
   as the fix.
9. **Given** any working tree **when** a developer runs `make story COMP=SRV TITLE="Load zone files"`
   **then** a new file is created at `docs/stories/AW-SRV-<next>-load-zone-files.md` from the
   template with the ID and title filled in, `<next>` is one greater than the highest existing SRV
   number, and the path is printed to stdout.
10. **Given** an existing story ID **when** `make story` is run twice in a row **then** the second
    run allocates a distinct, higher ID and never overwrites an existing file.
11. **Given** any working tree **when** a developer runs `make adr TITLE="Sharding model"` **then**
    a new `docs/adr/ADR-<next>-sharding-model.md` is created from the ADR template with a
    zero-padded four-digit ID one greater than the highest existing.
12. **Given** the current backlog **when** `make graph` runs **then** it emits a Mermaid `graph LR`
    to stdout in which every story is a node and every `depends_on` is an edge, and node styling
    distinguishes `blocked` from every other status.
13. **Given** a push to any branch **when** CI runs **then** it executes `make check` and no other
    build logic, so that a green CI and a green local run cannot disagree.
14. **Given** no Kubernetes manifests exist yet **when** `make k8s-dry` runs **then** it exits 0 and
    prints that there are no manifests to validate. An empty manifest set is not a failure.
15. **Given** a `.proto` file edited without regenerating **when** `make check` runs **then** it exits
    1 naming the stale package and `make proto` as the fix.

## Interface contract

### Make targets

| Target | Arguments | Exit 0 | Exit non-zero |
|--------|-----------|--------|---------------|
| `help` | — | prints target list (default goal) | never |
| `bootstrap` | — | toolchain present and verified | a required tool is missing and cannot be installed |
| `up` / `down` | — | delegated to `AW-INF-002` | stack failed to start |
| `check` | — | fmt, vet, lint, test, validate-stories, backlog-freshness, k8s-dry all pass | any sub-step fails |
| `fmt` | — | sources formatted | — |
| `vet` | — | no vet findings | vet findings |
| `lint` | — | no lint findings | lint findings |
| `test` | `PKG=./...` | tests pass | test failure |
| `backlog` | — | `BACKLOG.md` regenerated | generation error |
| `story` | `COMP=` `TITLE=` | file created, path on stdout | `COMP` not in {SRV,CLI,INF,CLT}, `TITLE` empty |
| `adr` | `TITLE=` | file created, path on stdout | `TITLE` empty |
| `validate-stories` | — | all frontmatter valid, IDs resolve, no cycles | any violation |
| `graph` | — | Mermaid on stdout | generation error |
| `proto` | — | regenerates Go, Python, and TypeScript from `docs/specs/protocol/` | codegen failure |
| `proto-check` | — | generated code matches the `.proto` sources | generated code is stale |
| `k8s-dry` | `ENV=dev` | manifests render and validate, or none exist | render or schema failure |
| `clean` | — | build artifacts removed | — |

All targets are idempotent and safe to re-run. Every failure prints a single actionable line before
exiting non-zero; no target requires reading documentation to interpret its output.

### Story frontmatter schema

Enforced by `make validate-stories`. Required keys, in this order:

| Key | Type | Permitted values |
|-----|------|------------------|
| `id` | string | `AW-(SRV|CLI|INF|CLT)-\d{3}`, must match the filename prefix |
| `title` | string | non-empty |
| `epic` | string | `EPIC-\d{2}`, must resolve to a file in `docs/epics/` |
| `component` | enum | `server`, `cli`, `infra`, `client` |
| `type` | enum | `feature`, `infra`, `spike`, `chore`, `bug` |
| `status` | enum | `draft`, `ready`, `in-progress`, `review`, `done`, `blocked` |
| `size` | enum | `S`, `M`, `L` |
| `depends_on` | list | story IDs that resolve to existing files |
| `blocks` | list | story IDs that resolve to existing files |
| `assignee` | enum | `cursor`, `claude-code` |
| `risk` | enum | `low`, `medium`, `high` |

Additional validation rules:

- `component` must agree with the `id` prefix (`SRV`↔`server`, `CLI`↔`cli`, `INF`↔`infra`,
  `CLT`↔`client`).
- `depends_on` and `blocks` must be symmetric: if A lists B in `depends_on`, B must list A in
  `blocks`. Asymmetry is an error, not a warning.
- `depends_on` must be acyclic.
- A story with `status: ready` may not depend on a story with `status: draft` or `status: blocked`.
- `size: L` with `status: ready` is an error — CLAUDE.md §6 requires L stories to be split first.

### Repository layout

```
cmd/andara-server/      # server main
cmd/andara-cli/         # CLI main
server/                 # server packages; server/sim is the dependency-free simulation core
admin/                  # CLI packages
agents/                 # Python Behavior Agent SDK and runtime (ADR-0005), outside the Go module
client/                 # Phase 2, TypeScript, outside the Go module
gen/                    # generated protobuf code, committed (ADR-0007)
deploy/compose/         # AW-INF-002
deploy/helm/            # AW-INF-003
docs/specs/protocol/    # .proto sources — the schema authority
scripts/                # make target implementations
```

Go module path: `github.com/valesordev/andara`.

### Enforced import boundary

`make lint` fails if any package under `server/sim/...` imports: `net`, `net/*`, `database/sql`,
`time`, `math/rand`, `os`, or any Kafka, Redis, Postgres, gRPC, or Connect package. This is
ADR-0001's seam invariant and ADR-0002's determinism requirement made mechanical. The denied list
lives in `.golangci.yml` under `depguard` so that widening it requires an explicit, reviewable diff.

**Amendment (implementation):** this section originally read `time` (except `time.Duration`).
`depguard` denies packages, not symbols, so that exception is not expressible. `time` is denied
outright, which is the stricter and more defensible reading anyway: ADR-0008 measures mechanics in
Ticks, so a duration reaches the core as a Tick count and the core never needs the package. Verified
mechanically — a fixture importing `net` and `time` from `server/sim` fails `make lint` with both
denials named.

## Data / state impact

None. No runtime state, no schema, no migrations. `BACKLOG.md` is a generated artifact and is
committed so that the repo is readable on GitHub without running anything; it is never hand-edited.

## Observability requirements

This story produces no runtime component, so it carries no metrics, traces, or alerts. It carries
build-time observability instead:

- **Logs** — every make target prints, on failure, exactly one line of the form
  `make: <target>: <what failed> (<how to fix>)` before exiting non-zero. Silent failures and
  stack-trace-only failures are defects.
- **CI** — the workflow surfaces which sub-step of `make check` failed as a distinct, named step,
  so a red build is diagnosable from the summary view without opening logs.
- **Metrics** — none. Explicitly none; do not add build-timing metrics until there is a build slow
  enough to justify them.
- **Alerts** — none.

## Test plan

- **Unit:** `scripts/` frontmatter parser against fixture stories covering: valid, missing key,
  bad enum value, unresolved `depends_on`, asymmetric `depends_on`/`blocks`, two-node cycle,
  three-node cycle, `ready` depending on `draft`, `size: L` with `status: ready`.
- **Integration:** `make check` on a fixture repo containing one valid story; `make backlog` run
  twice asserting an empty diff on the second run; `make story` run twice asserting distinct
  allocated IDs; `make bootstrap` run twice asserting identical end state.
- **Manual/operator:**
  ```
  git clone <repo> && cd andara.valesordev.com
  make bootstrap && make check && make graph
  make story COMP=SRV TITLE="Throwaway story"   # inspect, then delete the file
  ```
  Expected: three exit-0 runs, a Mermaid graph on stdout, and a scaffolded file at the printed path.

## Verification record — 2026-09-07

Every acceptance criterion was executed against the working tree, not reasoned about.

| AC | Result |
|----|--------|
| 1 | `make bootstrap` twice, exit 0 both times, identical output. Tooling is pinned and installed into `./bin`. |
| 2 | `make` with no arguments prints the target list, exit 0. |
| 3 | `make check` runs fmt, vet, lint, test, proto-check, validate-stories, backlog-check, k8s-dry; exit 0. |
| 4 | Fixture with `depends_on: [AW-SRV-999]` — one line naming file, field, and unresolved ID. |
| 5 | Two-node cycle fixture — `dependency cycle: AW-INF-901 -> AW-INF-902 -> AW-INF-901`. |
| 6 | Fixture missing `risk` and carrying `status: almost-ready`, `size: XL` — three lines, each naming the file, key, and permitted values. |
| 7 | `make backlog` twice, empty diff on the second run. |
| 8 | Story title edited, `make backlog-check` fails naming `make backlog` as the fix. |
| 9, 10 | `make story COMP=SRV` twice — `AW-SRV-020`, then `AW-SRV-021`. No overwrite. |
| 11 | `make adr TITLE="Fixture decision"` — a file scaffolded at the next zero-padded four-digit ID, one greater than the highest existing. Inspected, then deleted. |
| 12 | `make graph` emits `graph LR` with per-status `classDef` styling. |
| 13 | CI runs `make check` sub-steps and nothing else. |
| 14 | `make k8s-dry` with no chart — exit 0, states there is nothing to validate. |
| 15 | `make proto-check` regenerates into a temp tree and diffs against `gen/`; no-op with no `.proto` sources. |

Two defects were found and fixed in the process:

- **Every make recipe failed.** `SHELL := /usr/bin/env bash` — make does not word-split `SHELL`, so
  it looked for a program literally named `/usr/bin/env bash`. Nothing in this repo had ever run,
  including in CI. Now `SHELL := bash`.
- **`.golangci.yml` was v1 schema.** An unpinned `go install ...@latest` gets golangci-lint v2, which
  rejects a v1 config outright. The config is migrated to v2 and the binary is pinned.

Two notes on exactness rather than defects:

- ACs 4–6 say the validator "exits 1". `scripts/validate_stories.py` does exit 1; `make
  validate-stories` surfaces make's own 2. Exit non-zero with an actionable line is the intent, and
  overriding make's convention to hit a literal 1 would be worse.
- `make proto`/`proto-check` are wired and skip cleanly with no `.proto` sources. They are exercised
  end to end for the first time by `AW-SRV-005`.

## Definition of done

CLAUDE.md §8, plus:
- `make bootstrap && make check` passes on a machine that has never seen this repo.
- CI runs `make check` and nothing else.
- Every target in the CLAUDE.md §9 baseline list exists and either works or fails with a message
  naming the story that will implement it.

## Open questions

- `[ASSUMPTION]` Go module path is `github.com/valesordev/andara`. Changing it later is a
  mechanical rename but touches every file, so confirm before `AW-SRV-001` starts.
- `[ASSUMPTION]` The existing top-level `admin/` directory is `andara-cli`'s package root. If
  `admin/` was intended as something else (a web admin surface), say so and it gets its own
  component ID.
- `[ASSUMPTION]` Single Go module for `server` and `admin` rather than two modules, so that
  protocol types are shared by import rather than by duplication or a third module.
- `[ASSUMPTION]` Python 3.9+ is an acceptable dependency for the `scripts/` tooling. The alternative
  is writing them in Go, which is more code and makes the tooling depend on the build it validates.
  Note that ADR-0005 makes Python a runtime dependency of the project anyway, via Behavior Agents.
- `[ASSUMPTION]` `buf` for protobuf codegen and lint rather than raw `protoc`, because ADR-0007's
  additive-only and reserved-number rules are exactly what `buf breaking` enforces automatically.
