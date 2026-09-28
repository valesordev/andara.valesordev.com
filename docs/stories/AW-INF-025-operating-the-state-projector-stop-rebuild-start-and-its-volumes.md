---
id: AW-INF-025
title: Operating the state projector — stop, rebuild, start, and its volumes
epic: EPIC-10
component: infra
type: infra
status: draft
size: M
depends_on: [AW-SRV-019, AW-INF-018]
blocks: []
lane: sre
risk: medium
---

## Context

Issue #80, filed at `AW-SRV-019`'s §8 review (2026-09-24), is a §9 defect. It blocks
`projectors.state.enabled=true` in every environment. So it holds `AW-SRV-019`'s production line and
`AW-INF-008` AC-2 (the state projector on `dev`), both carried over from SPRINT-02.

There are two problems:
1. **No targets for the projector's operations.** `docs/runbooks/state-projector-diverged.md` and
   `projection-stale.md` tell the operator to stop the projector, rebuild it with
   `andara-projector state --rebuild`, and start it again. The chart hard-codes
   `command: [andara-projector, state]`. Running `--rebuild` beside a running Deployment puts two
   writers on one consumer group.
2. **`projector-deployment.yaml` mounts neither the snapshot volume nor `kafkaCreds`.** With
   `snapshot.store=fs`, an in-cluster projector sees no round and replays from zero. That breaks
   `AW-SRV-019` AC-6, and the projector exits 3 once `andara.events.v1` passes its retention. The fs
   PVC is the StatefulSet's (ReadWriteOnce), so it can't simply be shared.

#77, the other half of the blocker, is fixed (`AW-INF-018`).

## User story

As an operator, I want to stop, rebuild and start the state projector with `make` targets, and have
it read the same snapshots the server writes, so that the runbooks work and `dev` can run it.

## Scope

### In scope
- `make projector-stop`, `make projector-rebuild` and `make projector-start`, each with
  `ENV=<local|dev|prod>`, and the compose equivalents for the local stack.
- `projector-rebuild` scales the Deployment to 0, runs `andara-projector state --rebuild` to
  completion as a one-shot Job, then scales back to 1. It refuses while the Deployment has a
  running pod it didn't stop.
- The projector Deployment mounts `kafkaCreds`, and reads snapshot rounds the way the open question
  below decides.
- `projectors.state.enabled=true` on `dev`, once the above holds.
- The two runbooks name the targets instead of hand steps.

### Out of scope
- The projector's code (`AW-SRV-019`).
- The Redis hot projection (`AW-SRV-017`).
- `prod` enablement. `prod` serves no players yet.

## Acceptance criteria

1. **Given** `dev` with the projector running **when** `make projector-stop ENV=dev` runs **then**
   the Deployment has 0 replicas and the consumer group has no members, and it exits 0.
2. **Given** a stopped projector **when** `make projector-rebuild ENV=dev` runs **then** the Job runs
   `--rebuild` to completion, the Deployment is back at 1, and `andara_state_projector_lag_seconds` returns
   to its steady value.
3. **Given** a running projector **when** `make projector-rebuild ENV=dev` runs **then** it stops
   the projector first. There is never more than one writer on the consumer group.
4. **Given** a complete snapshot round on `dev` **when** the projector starts **then** it
   bootstraps from that round, not from zero (`AW-SRV-019` AC-6, in-cluster).
5. **Given** `make k8s-dry ENV=dev` **when** it renders **then** the projector Deployment mounts
   `kafkaCreds` and the snapshot source the open question decides.
6. **Given** the local stack **when** the compose targets run **then** they behave as AC-1 to AC-3.

## Interface contract

| Target | Does |
|---|---|
| `make projector-stop ENV=<env>` | scale to 0, wait for the group to empty |
| `make projector-rebuild ENV=<env>` | stop, one-shot `--rebuild` Job, start |
| `make projector-start ENV=<env>` | scale to 1, wait for Ready |

Exit codes follow the charter's target conventions.

## Data / state impact

`--rebuild` wipes and rebuilds the projector's consumer group and `andara.state.v1` projection.
That's `AW-SRV-019`'s contract; this story only runs it safely.

## Observability requirements

- **Metrics / Logs / Traces / Alerts:** none new. AC-2 and AC-4 read `AW-SRV-019`'s existing
  instruments.

## Test plan

- **Unit:** `helm-test` asserts the Deployment's mounts (AC-5).
- **Integration:** the compose targets against the local stack (AC-6).
- **Manual/operator:** ACs 1–4 on the box.

## Definition of done

CLAUDE.md §8. `AW-INF-008` AC-2 and `AW-SRV-019`'s production line then run on `dev`.

## Open questions

- **For architecture:** how an in-cluster projector reads snapshot rounds. The fs PVC is
  ReadWriteOnce and belongs to the StatefulSet. #80 suggests `snapshot.store=s3` in-cluster (MinIO
  or the box's store), or a read-only share, decided with `AW-INF-007`'s snapshot lifecycle. No ADR
  covers it yet, so it's decided at contract review.
