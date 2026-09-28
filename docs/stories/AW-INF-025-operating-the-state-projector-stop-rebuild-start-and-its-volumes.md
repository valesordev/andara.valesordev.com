---
id: AW-INF-025
title: Operating the state projector — stop, rebuild, start, and its volumes
epic: EPIC-10
component: infra
type: infra
status: ready
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
  `ENV=<dev|prod>`. Each exits 1 naming the environment if `projectors.state.enabled` is false
  there.
- `projector-rebuild` runs `projector-stop` first, then `andara-projector state --rebuild` to
  completion as a one-shot Job, then `projector-start`. It refuses, exit 1, if a rebuild Job for
  the environment is still running.
- **The snapshot source (decided at contract review): the in-cluster server and projector both
  use `snapshot.store=s3`**, against an S3 endpoint in the same namespace:
  - `make objectstore-install ENV=<dev|prod>` installs versitygw (the compose stack's pin,
    `versity/versitygw:v1.8.0` by digest) into `andara-<env>`, with its own PVC. It creates the
    bucket `andara-snapshots-<env>` and the Secret `andara-snapshot-s3`
    (`AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, generated once). It's idempotent, and follows
    `kafka-install`'s pattern.
  - `values/dev.yaml`: `server.snapshot.store: s3`, `s3_bucket: andara-snapshots-dev`,
    `s3_endpoint: http://andara-objectstore:9000`.
  - The server StatefulSet and the projector Deployment both take `andara-snapshot-s3` by
    `envFrom`. The server's S3 store reads the default credential chain (`server/store/s3.go`).
- The projector Deployment mounts `kafkaCreds`.
- `projectors.state.enabled=true` on `dev`, once the above holds.
- The two runbooks name the targets instead of hand steps.

### Out of scope
- The projector's code (`AW-SRV-019`).
- The Redis hot projection (`AW-SRV-017`).
- `prod` enablement. `prod` serves no players yet. The targets accept `ENV=prod` and refuse while
  it's disabled there.
- A projector in the compose stack. Compose runs none, and adding one isn't needed to operate
  `dev`. *(The draft's AC-6 asked for compose equivalents of targets that have no compose
  service to act on.)*
- Removing the StatefulSet's snapshot `volumeClaimTemplates`. They're immutable on a live
  StatefulSet, and changing them means deleting it. The PVC stays, unused on `dev`, until
  `AW-INF-007`'s snapshot lifecycle retires it.
- Round retention in the bucket (`snapshot.keep_rounds`, `AW-INF-007`). Until then `dev`'s bucket
  grows by one round per `snapshot.interval`, which is small at `dev`'s size.

## Acceptance criteria

1. **Given** `dev` with the projector running **when** `make projector-stop ENV=dev` runs **then**
   the Deployment has 0 replicas, the consumer group `andara-projector-state-dev` has no members,
   and it exits 0 with `projector-stop: stopped (group andara-projector-state-dev empty) in <n>s`.
2. **Given** a stopped projector **when** `make projector-rebuild ENV=dev` runs **then** the Job
   completes, the Deployment is back at 1 Ready replica, and, polled to
   `PROJECTOR_REBUILD_TIMEOUT` per `live-assertions.md`, `andara_state_projector_lag_seconds` is at
   or under `andara_state_projector_lag_budget_seconds`.
3. **Given** a running projector **when** `make projector-rebuild ENV=dev` runs **then** the
   Deployment reaches 0 replicas before the Job's pod starts. The Job's start time is after the
   last projector pod's termination, and the target's output shows `projector-stop` completing
   first.
4. **Given** a complete snapshot round in `andara-snapshots-dev` **when** the projector starts
   **then** its `state projector started` line carries `round_tick` equal to that round's tick,
   not 0 (`AW-SRV-019` AC-6, in-cluster).
5. **Given** `make k8s-dry ENV=dev` **when** it renders **then** the projector Deployment mounts
   `kafkaCreds`, and both it and the server StatefulSet take `andara-snapshot-s3` by `envFrom` and
   carry `ANDARA_SNAPSHOT_STORE=s3`.
6. **Given** `make objectstore-install ENV=dev` run twice **when** the second run finishes **then**
   it exits 0, the bucket and Secret are unchanged (the Secret's `resourceVersion` doesn't move),
   and the server's next round lands in the bucket within `3 × snapshot.interval`.
7. **Given** `ENV` unset, or an environment where `projectors.state.enabled` is false **when** any
   projector target runs **then** it exits 2 for a missing `ENV` and 1 for a disabled one, naming
   the environment, and changes nothing.
8. **Given** a rebuild Job still running in `andara-<env>` **when** `make projector-rebuild` runs
   **then** it exits 1 naming the Job, before scaling anything.

## Interface contract

| Target | Does | Waits for, and its bound |
|---|---|---|
| `make projector-stop ENV=<env>` | scale the Deployment to 0 | the group to have no members, `PROJECTOR_STOP_TIMEOUT` (default `120s`) |
| `make projector-rebuild ENV=<env>` | `projector-stop`, one-shot `--rebuild` Job, `projector-start` | the Job to complete, `PROJECTOR_REBUILD_TIMEOUT` (default `30m`, the Job's `activeDeadlineSeconds`) |
| `make projector-start ENV=<env>` | scale to 1 | Ready, `PROJECTOR_START_TIMEOUT` (default `300s`) |
| `make objectstore-install ENV=<env>` | versitygw, bucket, Secret, as in Scope | versitygw Ready, `120s` |

- **Exit codes:** `0` done; `1` a precondition failed (AC-7, AC-8), a step failed, or a bound
  expired, with the final line naming the step; `2` usage (`ENV` missing or not `dev`/`prod`).
- **Job:** `andara-projector-state-rebuild`, from the Deployment's image, env and `envFrom`, with
  `command: [andara-projector, state, --rebuild]`, `backoffLimit: 0`, and
  `ttlSecondsAfterFinished: 3600`. `projector-rebuild` deletes a *finished* Job before it creates
  its own. AC-8 refuses only one that's still running.
- **Chart values:** `objectstore.service` (default `andara-objectstore`), and the existing
  `server.snapshot.*` keys. No new server configuration.
- **For `make world-reset` (`AW-INF-021`, feedback item 2):** with this story, the snapshot store
  on `dev` is the bucket `andara-snapshots-dev`, not the PVC. The reset must empty the bucket,
  scale the projector to 0 (`projector-stop`), and delete the group `andara-projector-state-dev`
  along with the recreated `andara.state.v1`. A snapshot or a group offset that outlives its log
  is a wrong World or a wrong projection, not a slow one.

## Data / state impact

- `--rebuild` wipes and rebuilds the projector's consumer group and `andara.state.v1` projection.
  That's `AW-SRV-019`'s contract; this story only runs it safely.
- **`dev`'s server moves from `fs` to `s3`.** The rounds on the PVC aren't copied. The first
  boot on `s3` finds no round, and recovers by replaying `dev`'s log from its start. That's
  correct and slower, and it's safe only while `andara.events.v1` still holds `dev`'s first
  record (30-day retention; `dev`'s log began in SPRINT-02). Before the switch merges, SRE checks
  that the earliest offset on every `andara.events.v1` partition is 0, and records it. If it
  isn't, the switch waits for `AW-INF-021`'s `world-reset`.
- **Rollback:** set `server.snapshot.store` back to `fs` in `values/dev.yaml`, and disable the
  projector in the same revert. The PVC still holds its old rounds, and recovery replays the log
  forward from the newest of them.

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

- **Unit:** `helm-test` asserts AC-5 on the rendered `dev` manifests, and that `local`'s render
  is unchanged.
- **Integration:** none in CI. No CI environment has a broker-backed cluster.
- **Manual/operator**, on the box, recorded in the verification record: AC-6, then AC-1 to AC-4,
  AC-7 and AC-8, with each target's final line quoted.

## Definition of done

CLAUDE.md §8. `AW-INF-008` AC-2 and `AW-SRV-019`'s production line then run on `dev`.

## Open questions

- **Resolved 2026-09-28 (architecture, contract review): in-cluster snapshots are `s3`,** served
  by versitygw in the namespace. See the contract review below.

## Contract review (architecture, 2026-09-28)

SRE's observability review is in `docs/feedback/AW-INF-025-projector-operations.md`, and it
doesn't depend on the snapshot source. The story is `ready`.

1. **The snapshot source is `s3`, served in-cluster by versitygw.** ADR-0002 already puts
   snapshots in object storage; `fs` was the single-machine stand-in. The options:
   - A read-only share of the RWO PVC works on the box's single-node kind cluster, because RWO
     binds a node and not a pod. It breaks on the first multi-node cluster, and it ties the
     projector's scheduling to pod-0's.
   - A ReadWriteMany volume needs an NFS provisioner kind doesn't have: a second storage system
     to share one directory.
   - A projector sidecar in the server's pod can't be stopped without the server, which defeats
     `projector-stop`.
   - `s3` gives two readers and one writer with no volume coupling. The server already
     implements and tests it (`server/store/s3_test.go`, against versitygw in compose), and it's
     what `prod` uses anyway.

   The cost is one more stateful component per namespace. versitygw is the gateway the compose
   stack already pins and passes the s3 suite on (`AW-SRV-006`), so no new technology arrives.
2. **The draft's AC-6 is dropped.** Compose runs no projector, so compose equivalents would have
   nothing to act on.
3. **The draft contradicted itself on a running projector.** Scope said `rebuild` refuses; AC-3
   said it stops the projector first. It now stops it first (AC-3), and refuses only a
   concurrent rebuild (AC-8).
4. **Every wait has a named bound, and the exit codes are pinned.** "Follow the charter's
   conventions" wasn't a contract.
5. **AC-2's "steady value" is now the lag budget,** polled to a deadline.
6. **The `volumeClaimTemplates` stay.** Removing them means deleting the StatefulSet, which is
   `AW-INF-007`'s snapshot lifecycle, not this story's.
7. **`make world-reset` clears the bucket, not the PVC, once this lands.** That's in the
   Interface contract for `AW-INF-021`, which owns the target.
8. **Size stays M.** The object store is one Deployment, one PVC, one Secret and one bucket,
   installed as `kafka-install` installs the broker. If SRE finds it bigger, the split is
   `objectstore-install` alone, ahead of this story.
