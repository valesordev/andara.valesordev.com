---
id: AW-INF-035
title: make marks-sizing runs the Placed-mark sizing measurement
epic: EPIC-04
component: infra
type: chore
status: draft
size: S
depends_on: []
blocks: [AW-SRV-050]
lane: sre
risk: low
---

## Context

`AW-SRV-050` measures what a population of `Placed` marks costs the snapshot copy and the State Hash, with
and without `-race`, so `docs/runbooks/simulation-lagging.md`'s two thresholds on
`andara_handoff_placed_entries` can be measured instead of guessed. Its operator step is running
`TestSnapshotCopyStaysInsideTheStallBudget`'s mark subtests in both builds and keeping each build's artifact.
The Makefile has no target for it (`make measure-tick` runs the server against the sizing fixture and records
p99 tick CPU and RSS; it doesn't run this test), and CLAUDE.md §9 treats a documented `go test` sequence that
isn't a target as a defect. That is `AW-SRV-050`'s §9 defect, routed here as §2 prescribes. Raised by Codex's
review of #420.

## User story

As an operator, I want one command that measures the cost of a `Placed` mark population in both builds, so
that the runbook's thresholds come from a repeatable run rather than a hand-typed `go test` sequence.

## Scope

### In scope
- A target `make marks-sizing` (the name is SRE's to change) that runs `AW-SRV-050`'s `marks=0`,
  `marks=25000` and `marks=100000` subtests without `-race`, then with it.
- It keeps each build's artifact at a named path and prints `copy_cpu_ms` and `hash_cpu_ms` per population.
- It is in `.PHONY`, in `make help`, and in `make check-targets`' list.

### Out of scope
- The measurement itself, its fixture and its artifact's fields — `AW-SRV-050`.
- Choosing the runbook's thresholds — SRE's, from the numbers (`AW-SRV-050` Open question 1).
- Running it in CI. `[ASSUMPTION]` the first version is operator-run; a CI job is a later story if the numbers
  need tracking.

## Acceptance criteria

1. **Given** `AW-SRV-050`'s subtests exist **when** `make marks-sizing` runs **then** it runs them once
   without and once with `-race`, and exits 0.
2. **Given** a run **when** it finishes **then** one artifact per build exists at a named path, each with
   `marks`, `race`, `copy_cpu_ms`, `hash_cpu_ms` for 0, 25,000 and 100,000, and the target prints those
   numbers to stdout.
3. **Given** a subtest fails in either build **when** the target runs **then** it exits non-zero and names
   the build that failed.
4. **Given** it is run twice in a row **when** the second run starts **then** it succeeds and overwrites the
   artifacts (idempotent, CLAUDE.md §9). It needs no broker and no cluster.
5. **Given** `make help` and `make check-targets` **when** they run **then** `marks-sizing` appears in both.

## Interface contract

- `make marks-sizing` — no required variables. `[ASSUMPTION]` optional `OUT=<dir>` for the artifacts,
  default a path under the build directory.
- Exit codes: `0` both builds passed; `1` a subtest failed; `2` the fixture or artifact was missing.
- It reads the artifact name and fields from `AW-SRV-050`'s Interface contract (`placed-marks-timing.json`,
  an `[ASSUMPTION]` there). If that changes, this target follows it.

## Data / state impact

None. Build output only.

## Observability requirements

No instrumentation: a developer target, no server, CLI command or deployed workload. It prints its results.

### Metrics
None.
### Logs
`marks-sizing: <build>: copy_cpu_ms=… hash_cpu_ms=…` per build and population on stdout.
### Traces
None.
### Alerts
None.

## Test plan
- **Unit:** the target's script, if it has one, under `make scripts-test`: a failing subtest exits non-zero
  and names its build (AC-3).
- **Integration:** `make marks-sizing` on a clean checkout after `AW-SRV-050` lands (AC-1, AC-2, AC-4).
- **Manual/operator:** `make marks-sizing`; read the printed numbers.

## Definition of done
CLAUDE.md §8, plus:
- `AW-SRV-050`'s operator step names this target.

## Open questions
- `[ASSUMPTION]` Operator-run only for now (see Out of scope).
