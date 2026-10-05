---
id: AW-SRV-050
title: The snapshot sizing fixture carries a population of Placed marks
epic: EPIC-08
component: server          # server | cli | infra | client
type: chore                # feature | infra | spike | chore | bug
status: draft              # draft | ready | in-progress | review | done | blocked
size: S                    # S | M | L  — L means "split it"
depends_on: [AW-SRV-028]
blocks: [AW-SRV-047]
lane: implementation       # architecture (contracts) | sre (infra, ops) | implementation (source)
risk: low                  # low | medium | high
---

## Context

`AW-SRV-028` keeps a `Placed` mark in each Zone for every Entity that ever arrived by handoff, for good,
and nothing prunes them. They are hashed Zone state, copied by every in-tick snapshot, so they cost
snapshot copy time and State Hash work. `docs/runbooks/simulation-lagging.md` ("The handoff marks") sets
two thresholds on `andara_handoff_placed_entries`, 25,000 and 100,000, and says plainly that neither is
measured against marks. The sizing fixture's in-tick copy already takes about 12 of its 15 ms
`snapshot.max_stall_ms` without marks, so marks draw on 2 to 3 ms of headroom.

SRE asked for the measurement, and architecture agreed (`docs/feedback/AW-SRV-028-handoff-contract.md`,
"For PM: a follow-up for the sizing fixture with marks"). It belongs before `AW-SRV-047`, whose Item
Instances take a new Entity ID each time they're placed, and are what make marks grow.

## User story

As an operator, I want the measured cost of a `Placed` mark population in the snapshot copy and State
Hash, so that the runbook's thresholds are numbers I can trust instead of guesses.

## Scope

### In scope
- Give the sizing fixture (`server/simtest/sizing.go`: 16 Zones, 25,000 Entities, 15 ms
  `snapshot.max_stall_ms`) a `Placed` mark population, at 25,000 and at 100,000.
- Record the in-tick copy time and the State Hash cost with each population, with and without `-race`.
- Replace the runbook's two thresholds with measured ones (the number changes go in the same PR as a
  request to SRE: the runbook is `docs/runbooks/`, SRE's).

### Out of scope
- Pruning the marks (a `HandoffClosed` record) — `AW-SRV-028`'s Out of scope names it. Whether it is
  needed is what this measurement decides.
- An alert on the marks: no SLO covers them (CLAUDE.md §7).
- Changing `snapshot.max_stall_ms`, or the stall guards `#172` ruled.

## Acceptance criteria

1. **Given** the fixture with 25,000 marks **when** the snapshot copy test runs **then** the in-tick
   copy time and State Hash cost are written to the test's output file (`recovery-timing.json`'s
   sibling, name `[ASSUMPTION]` below), by build (`-race` and not).
2. **Given** the fixture with 100,000 marks **when** it runs **then** the same two numbers are recorded.
3. **Given** the fixture with no marks **when** it runs **then** its result matches today's within the
   test's own noise, so the baseline is unchanged.
4. **Given** the recorded numbers **when** the PR merges **then** the runbook request names a threshold
   for each of the 25,000 and 100,000 rows, each computed from the recorded cost and the stall budget
   as stated in the request (not guessed).
5. **Given** the fixture's marks **when** they are built **then** every mark's Entity ID is unique and
   the sequence is non-zero, so the cost measured is the cost of a real population.
6. **Given** `go test` with and without `-race` **when** the new cases run **then** each completes and
   is gated by a stall limit of its own, so CI doesn't go red on a mark population before the limit exists
   for it. `[ASSUMPTION]` the limit is the existing CPU-time guard's factor (`#172`).

## Interface contract

```go
// CONTRACT SKETCH — not an implementation
package simtest

// SizingFixture gains a mark population. Zero marks is today's fixture.
func SizingFixture(opts ...SizingOption) *sim.Engine
func WithPlacedMarks(n int) SizingOption
```

- **Test names:** `TestSnapshotCopyStaysInsideTheStallBudget` keeps its name and gains subtests
  `marks=0`, `marks=25000`, `marks=100000`.
- **Output:** an artifact per run, JSON, with `marks`, `race`, `copy_cpu_ms`, `hash_cpu_ms`, and the
  budget. `[ASSUMPTION]` name `placed-marks-timing.json`.

## Data / state impact

None. Test-only. No schema or config change.

## Observability requirements
No new instrumentation (a test-only story). `andara_handoff_placed_entries` and
`andara_snapshot_tick_stall_seconds` already exist (`AW-SRV-028`, `AW-SRV-006`).

### Metrics
None.
### Logs
None.
### Traces
None.
### Alerts
None.

## Test plan
- **Unit:** the three subtests above; a fixture whose mark count is checked (AC-5).
- **Integration:** none.
- **Manual/operator:** `go test ./server/simtest -run TestSnapshotCopyStaysInsideTheStallBudget` with and
  without `-race`, reading the artifact. `[ASSUMPTION]` a `make` target exists or is added by SRE
  (CLAUDE.md §9); if not, the command above is a §9 defect for an SRE story.

## Definition of done
CLAUDE.md §8, plus:
- The runbook request is in `docs/feedback/` addressed to SRE with the measured numbers.

## Open questions
- `[ASSUMPTION]` The artifact's name and shape, and the limit in AC-6. Neither changes another story's
  contract.
- `[ASSUMPTION]` Size S: the fixture is one constant, per `AW-SRV-006`.
