---
id: AW-INF-029
title: content/core/VERSIONS is append-only, enforced by make check
epic: EPIC-05
component: infra
type: infra
status: draft
size: S
depends_on: [AW-SRV-013]
blocks: []
lane: sre
risk: low
---

## Context

`content/core/VERSIONS` maps each `andara.core` version to the digest of its blobs. The server and
`andara-cli` each hold their embedded core to the line for `content/core/VERSION` (`AW-SRV-013`
AC-19, `AW-CLI-002` AC-10). So `andara.core@N` means the same bytes everywhere only while no line,
once written, ever changes. Nothing enforces that today. A rebase or a hand edit could rewrite line
`N`, and every environment already holding `andara.core@N` would then disagree with the next build.
Source: `docs/feedback/AW-SRV-013-publish-path.md`, "For PM", item 2. It isn't on the demo path.

## User story

As a developer, I want `make check` to refuse a change to any existing line of
`content/core/VERSIONS`, so that a core version number can never come to mean different bytes.

## Scope

### In scope
- `make core-versions-check`, in `CHECK_TARGETS`. It compares `content/core/VERSIONS` with the merge
  base and allows only new lines appended at the end.
- Supplying the merge base in CI (`ci.yaml`): enough fetch depth or an explicit base ref.

### Out of scope
- The digest check itself, which is `AW-SRV-013` and `AW-CLI-002`.
- Bumping the core, which is implementation's, in `content/`.

## Acceptance criteria

1. **Given** a change that appends one line `N+1 <digest>` **when** `make core-versions-check` runs
   **then** it exits `0`.
2. **Given** a change that edits, deletes or reorders an existing line **then** it exits `1` with
   `core-versions-check: line <n> changed; VERSIONS is append-only`.
3. **Given** an appended line whose version isn't the previous last version plus one **then** it
   exits `1`, naming the line.
4. **Given** no change to the file **then** it exits `0`.
5. **Given** CI on a pull request **then** the merge base with `main` is available, and the check
   runs against it. On `main` itself, it compares with the first parent.
6. **Given** a local run with no `origin/main` **then** it exits `2` with
   `core-versions-check: no merge base; fetch origin`.

## Interface contract

- `## core-versions-check: fail if content/core/VERSIONS changes other than by appending the next version`
- The base is `BASE_REF`, defaulting to `origin/main`, and the merge base is computed against `HEAD`.
- Exits: `0` ok; `1` a violation, named; `2` no base.

## Data / state impact

None.

## Observability requirements

None. It's a `make` target in CI, with no service, metric or trace. It reports through its exit code
and its `core-versions-check:` lines in the CI log.

## Test plan

- **Unit** (`scripts/tests/`): fixture repositories for an append, an edit, a deletion, a reorder, a
  skipped number, and no change (ACs 1–4); and the missing base (AC-6).
- **Integration:** CI's `check` job runs it with the base supplied (AC-5).
- **Manual/operator:** `make core-versions-check` on a clean tree exits `0`.

## Definition of done

CLAUDE.md §8.

## Open questions

- `[ASSUMPTION]` The next version is the previous one plus one. `VERSION` is monotonic (ADR-0004,
  amended 2026-09-28), so no gap is legitimate.
