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

*(SRE observability review, 2026-09-28. The first draft said "none new". Enabling the projector on
`dev` is what `projection-freshness.md` deferred its absence rule to, so this story carries it.)*

- **Metrics:** none new in code. The projector Deployment is scraped by annotation under
  `job="andara-projector-state"` (the chart's `andara.scrapeAnnotations`, already rendered).
  AC-2 reads `andara_state_projector_lag_seconds` against `andara_state_projector_lag_budget_seconds`,
  polled per `live-assertions.md`. The one-shot `--rebuild` Job carries **no** scrape annotation:
  it's short-lived, and a second target under the same job would read as a second writer. So
  `andara_state_rebuild_duration_seconds` from the Job is never scraped, and the rebuild's duration
  comes from its log line (below), not the histogram.
- **Logs:** each target prints `projector-<stop|rebuild|start>: <step>` progress lines to stderr and
  a final line naming the outcome and elapsed time:
  - `projector-stop: stopped (group <group> empty) in <n>s`
  - `projector-rebuild: rebuilt to tick <t> in <n>s`
  - `projector-start: ready in <n>s`
  On failure, the final line names the step that failed, and the target exits non-zero. The Job's
  and the Deployment's own lines ship to Loki through the cluster's OTLP receiver as the server's
  do: `consumer group wiped for a rebuild`, `state projector started` (with `round_tick`), and
  `state projector caught up`. AC-4 is evidenced by `state projector started` carrying a non-zero
  `round_tick` equal to the newest complete round's tick, not by the lag gauge.
- **Traces:** none new. The rebuild Job emits `AW-SRV-019`'s `state.replay` → `state.verify` spans.
  The Job must flush its exporter before it exits 0. If the §8 check finds the Job's spans missing
  from Tempo, that's an `AW-SRV-019` defect, filed as an issue, not a reason to hold this story.
- **Alerts:** one new alert, `StateProjectorDown`, with its runbook:
  - **Expression:** the projector's Deployment wants a replica and no Ready projector pod has been
    scraped, per namespace:
    ```
    max by (namespace) (kube_deployment_spec_replicas{deployment="andara-projector-state"}) > 0
      unless on(namespace) max by (namespace) (up{job="andara-projector-state"} == 1)
    ```
    for 10 m. `projector-stop` and `projector-rebuild` scale the Deployment to 0, so a planned stop
    never fires. A crash loop or an unready pod drops out of the annotation scrape and does fire.
    The expression never uses `absent()` over every namespace (the `AndaraServerUnavailable`
    lesson). It's keyed on desired replicas, so an environment with the projector disabled has no
    Deployment and no series.
  - **Severity:** `ticket`, `slo: projection-freshness`. No player reads a projection.
  - **Runbook:** `docs/runbooks/state-projector-down.md`, in this story. Its first steps are the
    pod's restart count and last exit code (`2` divergence, `3` log gap, `4` state_version). Each
    exit code routes to `state-projector-diverged.md`, to `make projector-rebuild`, or to an
    escalation.
  - `projection-freshness.md`'s *Known gaps* bullet "A projector that is not running exports no
    lag" is replaced by a pointer to this alert. `docs/runbooks/README.md` lists the runbook.
  - `deploy/helm/tests/alerts_test.yaml` covers three cases: fires on replicas 1 with no `up`;
    silent on replicas 0; silent in compose, which has no `kube_*` series.
- **Rebuild and `ProjectionStale`.** After a rebuild, lag stays over budget until the projector
  catches up. A rebuild longer than the rule's `for` (5 m) tickets `ProjectionStale`. That's correct:
  the SLO's exhaustion policy counts rebuilds against the budget. `projection-stale.md` gains one
  line: check `projector-rebuild`'s final line before diagnosing. No inhibition is added.
- **Delivery.** The rules are evaluated in Grafana Cloud only once `AW-INF-009` delivers
  `files/alerts.yaml`. Until then, §8 verifies the rule with `promtool` (`make helm-test`) and the
  series it reads on the real backend. The §8 record says which of the two was observed.

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
