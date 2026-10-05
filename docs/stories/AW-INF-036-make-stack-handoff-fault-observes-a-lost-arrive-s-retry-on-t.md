---
id: AW-INF-036
title: make stack-handoff-fault observes a lost Arrive's retry on the local stack
epic: EPIC-04
component: infra
type: chore
status: draft
size: S
depends_on: [AW-SRV-051]
blocks: []
lane: sre
risk: low
---

## Context

`AW-SRV-028` is held at `review` until a tracked story carries the live observation of its handoff series and
log lines (CLAUDE.md §8's deferral rule; architecture's §8 review, #434). `AW-SRV-051` builds the mode that
loses or delays an `Arrive` on the running server. This story is the stack half and the carrier: a target
that turns the mode on, walks a Character across a Zone boundary, and reads the server's `/metrics` and logs
back, the way `make stack-recover` reads a recovery. Without a target the observation is a hand-written
sequence, which §9 calls a defect. Architecture suggested that `AW-INF-032` might take the assertion; a
separate target keeps `stack-recover` about the M2 gate and puts this on its own CI line.

## User story

As an operator, I want one command that makes a handoff retry on the local stack and shows me the series and
log lines it produces, so that `AW-SRV-028`'s instrumentation is observed live and not only tested.

## Scope

### In scope
- `make stack-handoff-fault` (the name is SRE's to change): starts the local stack with `AW-SRV-051`'s mode
  on (`drop` and then `delay`), walks one Character across a Zone boundary with `andara-cli play`, and reads
  the server back.
- It asserts the observations below by polling to a deadline (`docs/specs/testing/live-assertions.md`), not
  by one read.
- It is in `.PHONY`, `make help` and `make check-targets`.

### Out of scope
- The mode — `AW-SRV-051`.
- The `error` line from an impossible `Arrive`, unless `AW-SRV-051` adds a mode for it (its Open question 1c).
- A CI workflow; `[ASSUMPTION]` the `stack` workflow takes it after `stack-recover` once it's stable.

## Acceptance criteria

1. **Given** `drop` with `n = 1` **when** the target walks a Character across **then**
   `andara_handoffs_in_transit` is above 0 on a scrape, then returns to 0, `andara_handoff_retries_total`
   reaches 1, and the Character is in the destination Room.
2. **Given** `delay` past `sim.handoff_retry_ticks` **when** the target walks a Character across **then**
   `andara_handoff_stale_arrivals_total` reaches 1 and the Character is in the Room once.
3. **Given** the runs above **when** the server's log is read **then** it holds the retry `debug` line and
   the injected-production `debug` line, and at least one summary `warn` with `retries`, `oldest_attempt`,
   `tick` and `in_transit`.
4. **Given** an assertion that doesn't hold by its deadline **when** the target runs **then** it exits
   non-zero and names the observation.
5. **Given** two runs in a row **when** the second starts **then** it succeeds (idempotent, CLAUDE.md §9).
6. **Given** `make help` and `make check-targets` **when** they run **then** the target appears in both.

## Interface contract

- `make stack-handoff-fault` — no required variables. Exit `0` all observations made; `1` an assertion
  failed (names it); `2` the stack or the mode wasn't available.
- It reads the series names from `AW-SRV-028` and the mode's surface from `AW-SRV-051`'s contract.

## Data / state impact

None beyond the local stack's own volumes; the target leaves the mode off when it ends.

## Observability requirements

No new instrumentation: it reads series `AW-SRV-028` and `AW-SRV-051` define. It prints each observation
as `stack-handoff-fault: <observation>: ok` on stdout.

### Metrics
None.
### Logs
One stdout line per observation.
### Traces
None.
### Alerts
None.

## Test plan
- **Unit:** the script's polling and exit codes under `make scripts-test` (AC-4).
- **Integration:** `make stack-handoff-fault` on a clean checkout after `AW-SRV-051` lands (AC-1 to AC-3, AC-5).
- **Manual/operator:** `make stack-handoff-fault`.

## Definition of done
CLAUDE.md §8, plus:
- **Carries `AW-SRV-028`'s deferred live observation:** `andara_handoff_retries_total` above 0,
  `andara_handoff_stale_arrivals_total` above 0, `andara_handoffs_in_transit` above 0 on a scrape, and the
  `warn` and `debug` lines, observed on the running stack. The `error` line follows `AW-SRV-051`'s ruling on
  Open question 1c.
- `AW-SRV-028`'s §8 record cites this run.

## Open questions
- `[ASSUMPTION]` A separate target rather than an assertion in `stack-recover`; if architecture wants it
  folded in, `AW-INF-032` is `done` and its story would need a follow-up.
