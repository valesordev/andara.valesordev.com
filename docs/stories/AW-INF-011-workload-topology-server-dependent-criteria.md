---
id: AW-INF-011
title: Workload topology — probes and sizing against the real tick loop and recovery
epic: EPIC-01
component: infra
type: chore
status: ready
size: S
depends_on: [AW-INF-003, AW-SRV-002, AW-SRV-007]
blocks: []
lane: architecture
risk: medium
---

## Context

`AW-INF-003` shipped the chart on 2026-09-11 with four criteria it could only half-execute: readiness
during log replay and the 3× startup budget need `AW-SRV-007`'s recovery; the wedged-tick liveness
condition and the resource measurement need `AW-SRV-002`'s tick loop. They were recorded as owed, not
skipped, and stayed open through two §8 passes. On 2026-09-17 Brian decided to split them out; this
story carries them.

The risk rating is the chart's, not the chore's: these are the criteria that decide whether
Kubernetes' idea of healthy matches a player's. Until they are executed, the probe budgets and the
`prod` resource requests are reasoned numbers with `measured: false` beside them.

## User story

As an operator, I want the chart's probes and requests proven against the running simulation, so
that a pod Kubernetes calls ready is a pod a player can play on, and one it calls wedged is.

## Scope

### In scope
The criteria below, executed on the kind cluster with the server stories they name; `make
measure-tick` run for real and `measurements.yaml` committed from it; the `andara_probe_state` gauge
`AW-INF-003` named as owed to the implementation lane, verified once whichever story adds it lands.

### Out of scope
- Changing probe semantics. `/readyz` and `/livez` contracts are `AW-INF-003`'s and `AW-SRV-007`'s;
  a change there is a change to those stories.

## Acceptance criteria

Numbered as in `AW-INF-003`.

2. **Given** a pod whose `/readyz` returns `503 {"phase":"replay"}` (`AW-SRV-007`) **when** the
   readiness probe polls **then** the pod is absent from the Service's endpoints; **given** `200
   {"phase":"serving"}` **then** it is present — observed during a real recovery, not a stub.
7. **Given** `AW-SRV-007`'s measured worst-case recovery on the sizing fixture **when** compared with
   the startup budget (`periodSeconds × failureThreshold`, 600 s) **then** the budget is at least 3×
   the measurement, or the values change and the story says by how much.
8. **Given** a tick loop that has not completed a tick in `sim.tick_budget_ms × 100` (`AW-SRV-002`,
   induced with the fault the story provides) **when** `/livez` is polled **then** it returns `503
   {"reason":"tick_wedged"}` and liveness restarts the pod within `periodSeconds × failureThreshold`.
10. **Given** `make measure-tick ENV=local DURATION=300` against the sizing fixture **when** it runs
    **then** `measurements.yaml` is written with `measured: true`, p99 tick CPU and RSS, and the
    `prod` requests derived from it are within 2×; `helm-test` stops warning.

## Interface contract

None new; `AW-INF-003`'s tables stand.

## Data / state impact

`measurements.yaml` changes from placeholder to measured. Requests in `prod` move with it — a values
change, rolled by the next deploy.

## Observability requirements

`andara_probe_state{probe="startup"|"readiness"|"liveness"}` (gauge, 3 series) as `AW-INF-003`
specified, verified emitting on the cluster once it exists.

## Test plan

- **Integration (kind, `kind` workflow):** AC-2 and AC-8 as steps — the workflow's own comment names
  them as "what it cannot prove yet"; this story replaces that sentence with the steps.
- **Manual/operator:** AC-7 and AC-10, recorded in the verification table with the numbers.

## Definition of done

CLAUDE.md §8, plus: `measurements.yaml` is `measured: true`; `AW-INF-003`'s record points here.

## Open questions

None; every open item is a dependency, named per criterion.
