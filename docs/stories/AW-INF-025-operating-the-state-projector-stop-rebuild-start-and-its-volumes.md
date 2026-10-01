---
id: AW-INF-025
title: Operating the state projector — stop, rebuild, start, and its volumes
epic: EPIC-10
component: infra
type: infra
status: review
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
- The projector Deployment mounts its own `projectors.state.kafkaCreds`, never the server's
  `secrets.kafkaCreds` (ADR-0011 decision 6).
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
   not 0 (`AW-SRV-019` AC-6, in-cluster). **And**, polled to `PROJECTOR_REBUILD_TIMEOUT` per
   `live-assertions.md`, `andara_state_projector_lag_seconds` falls to or under
   `andara_state_projector_lag_budget_seconds` while `andara_state_digest_mismatches_total` stays 0
   and no `state projector diverged` line is logged. *(Amended 2026-09-29, §8 review of
   `AW-INF-008`: without this, AC-4 passes on a projector that halts one tick later, which is #143.)*
5. **Given** `make k8s-dry ENV=dev` **when** it renders **then** the projector Deployment mounts
   the Secret named by `projectors.state.kafkaCreds.secretName` at `/etc/andara/secrets/kafka`,
   renders no Kafka credential mount when that value is empty (it doesn't fall back to
   `secrets.kafkaCreds`), and both it and the server StatefulSet take `andara-snapshot-s3` by
   `envFrom` and carry `ANDARA_SNAPSHOT_STORE=s3`. *(Amended 2026-09-29, ADR-0011: the projector
   has its own Kafka principal, so it can't share the server's Secret.)*
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

## Contract amendment (architecture, 2026-09-29): AC-4 must see the projector verify past the round

#143: a projector bootstrapped from a round diverges at round tick + 1, and a from-zero replay of
the same log verifies. AC-4 as reviewed on 2026-09-28 read only `round_tick` on the `started` line,
so it would have passed #143. It now also requires the projector to catch up with no digest
mismatch.
- **#143 is a prerequisite for AC-2 and AC-4 on `dev`, not a reason to weaken them.** Neither
  criterion may be met by starting the projector from zero. Everything else in this story, ACs 1,
  3 and 5 to 8, can be built and verified now.
- **If #143 is still open when the rest is done,** the story goes to `review` owing AC-2 and AC-4,
  as `AW-INF-019` did with its AC-4. Enabling the projector on `dev` is still in scope. If it
  diverges there, that's #143's evidence, and it doesn't fail this story's other ACs.
- **`AW-INF-008` AC-2 doesn't wait on #143.** It's observed on the projector's first Ready
  (`AW-INF-008`, §8 review 2026-09-29).
- No Interface contract change. The metrics and log line are `AW-SRV-019`'s and the SLO's.

## Contract amendment (architecture, 2026-09-29): the projector's own Kafka credentials

ADR-0011 (accepted 2026-09-29) gives each workload its own Kafka principal. The projector is
`andara-projector-state`, the only writer of `andara.state.v1`. So the projector can't mount the
server's `secrets.kafkaCreds`: whichever principal both used would carry both sets of rights, and
`AW-SRV-019` AC-9 would be unmeetable. The scope bullet and AC-5 now name
`projectors.state.kafkaCreds.secretName`, with no fallback to the server's value.

The broker still authenticates nobody until ADR-0011's SRE story lands. So on `dev` this story
renders the mount only if the value is set, and it's fine for it to be empty until then. What
matters is that the chart can't hand the projector the server's Secret.

## Verification record (SRE, 2026-09-29), before merge

On `sre/aw-inf-025-operating-the-state-projector`.

| AC | How | Result |
|----|-----|--------|
| 1 | `make projector-stop ENV=dev` on the box | **owed after merge.** The Deployment exists only once `values/dev.yaml` reaches `main` |
| 2 | `make projector-rebuild ENV=dev`, then the lag gauge against its budget | **owed after merge, and on #143** (architecture, 2026-09-29) |
| 3 | The same run: `projector-stop`'s final line before the Job is created | **owed after merge.** By construction, `rebuild()` runs `stop()` to completion before `create job` |
| 4 | `state projector started` with `round_tick` equal to the newest round in `andara-snapshots-dev`, then lag within budget with no divergence | **owed after merge, and on #143** |
| 5 | `helm_test.test_snapshot_s3_and_projector_creds`: on `dev`, both workloads take `andara-snapshot-s3` by `envFrom`, and `ANDARA_SNAPSHOT_STORE=s3`. The projector mounts `projectors.state.kafkaCreds` at `/etc/andara/secrets/kafka` when it's set. With only `secrets.kafkaCreds` set, it has no `kafka-creds` volume. `local` is unchanged (`fs`, no Secret, no projector). Mutation-checked: removing `snapshotS3` from `dev.yaml` fails it twice | pass |
| 6 | `make objectstore-install ENV=dev`, run twice. The first run created the Secret. The second said `Secret andara-snapshot-s3 exists; unchanged`, and `resourceVersion` stayed `5245701`. Both exited 0. A write, list and delete through rclone worked on the local-path PVC (versitygw's xattr metadata). "The server's next round lands within 3 × `snapshot.interval`" | first half pass; **the round is owed after merge** |
| 7 | `Usage.*` in `scripts/tests/test_projector.py`: no `ENV`, or `ENV=local`, exits 2 with no `kubectl` on `PATH`. `Preconditions.test_a_disabled_projector_is_refused_naming_the_environment`: exit 1, no `scale`, `create` or `delete` | pass |
| 8 | `Preconditions.test_a_running_rebuild_job_is_refused_before_scaling`: names the Job, with no change made | pass; the box half is owed after merge |

**Before the switch to `s3` (Data section):** all 64 `andara.events.v1` partitions have
log-start-offset 0 (after `world-reset`, 23:52Z). So the first `s3` boot's replay from zero has
its whole log.

**Beyond the contract:**
- **The Application ignores the projector's replica count.** It has `ignoreDifferences` on
  `/spec/replicas` for `andara-projector-state`, with `RespectIgnoreDifferences=true`. Without
  that, selfHeal would undo `projector-stop` within seconds. This is in `deploy/argocd/andara-dev.yaml`,
  applied with `make argocd-install ENV=dev` (Synced/Healthy afterwards).
- **The rebuild ends at `caught up`, not at `Complete`,** because `--rebuild` never exits. The
  question is in `docs/feedback/AW-INF-025-projector-operations.md`, for architecture.
- **`world-reset` on `s3` empties the bucket** through `objectstore.py` (verified against the
  bucket: two objects in, none out, bucket kept). It stops the projector through `projector-stop`'s
  code, as the contract's note to AW-INF-021 asks. Its `s3` refusal from #165 is gone.
- **`k8s-dry` validates `deploy/k8s/objectstore/`** (4 resources).
- **Bucket tool:** `rclone/rclone:1.75.1` by digest, 130 MB, rather than `amazon/aws-cli`, which is
  594 MB, for two operations.

**Alert:** `StateProjectorDown` in `files/alerts.yaml`, with three promtool cases: it fires for
`andara-dev` alone at 13 m, is silent on replicas 0, and is silent in compose. Mutation-checked.
Its runbook is `docs/runbooks/state-projector-down.md`, listed in the runbook README.
`projection-freshness.md`'s known gap now points to it. Delivery to Grafana Cloud is AW-INF-009's.
**Also owed at §8:** that Grafana Cloud's kube-state-metrics keeps `kube_deployment_spec_replicas`
for `andara-projector-state`.

**Review of #171 (Codex, 2026-09-29):**
- **Fixed, P1: the projector mounts the server's content.** `dev` runs `content.source=dir` at
  `/content`, and `andara-projector` loads content before it starts. The Deployment mounted
  neither ConfigMap, so it would have exited at boot instead of becoming Ready, and so would the
  rebuild Job copied from it. It now mounts `andara-content` at `/content` and
  `andara-content-templates` at `/content/templates`, as the StatefulSet does. It also carries the
  server's `checksum/content`, so a fixture change rolls both and the replica never runs other
  content than the server. `helm_test.test_snapshot_s3_and_projector_creds` asserts both.
  Mutation-checked: without the template change it fails twice.

## §8 review (architecture, 2026-09-30): stays `review`

Against `main` at `79fd622`. Merged in #171 (`7967a38`). `make check` is green on `main`, and
`helm_test` and `scripts/tests/test_projector.py` (79 cases) re-ran green in this review.

| AC | Result |
|----|--------|
| 1 | **owed.** Not yet run on `main`, though it can be now |
| 2 | **owed, on #143.** Read as amended below |
| 3 | **owed.** Runnable now: the stop-before-Job ordering shows even though the rebuild then diverges |
| 4 | **fails on `dev`**, which is the amendment working. See below |
| 5 | pass (SRE's record; re-run) |
| 6 | first half pass. The second half, a round in `andara-snapshots-dev` within 3 × `snapshot.interval`, is **owed**. `andara-0` logs `snapshot round complete` about every 60 s on `s3`, but a bucket listing hasn't been recorded |
| 7, 8 | pass (unit). The box half of 8 is **owed** |

**#143 has reproduced on `dev`.** Observed read-only at 17:57Z: `andara-projector-state` is in
CrashLoopBackOff, with 136 restarts in 13 h, each exiting 2 on `state projector diverged at tick
132838`. That's the first start after the merge, bootstrapped from the server's first `s3` round and
diverging right after it. The Application is Synced/Degraded. `StateProjectorDown` would be firing,
but it isn't delivered until `AW-INF-009`. Leaving the projector crash-looping or stopping it is
SRE's call (`docs/feedback/AW-INF-025-projector-operations.md`). A `projector-rebuild` won't clear
it, because it bootstraps from the newest round again.

**Ruling on `--rebuild` never completing** (SRE's question in the feedback file): **SRE's reading is
accepted, and it's recorded here as a contract amendment.** A rebuild is done when the Job's pod
logs `state projector caught up`. The target reads the tick from that line and deletes the Job,
which is a SIGTERM and a clean stop, committing its checkpoint. `projector-start` then resumes the
Deployment from that checkpoint, with no second `--rebuild`. AC-2's "the Job completes" reads as
"the Job's pod logs `caught up`, and the target deletes it". The Interface contract's "to completion"
and "waits for the Job to complete" read the same way. Making `--rebuild` exit on caught-up isn't
routed to implementation: it would change `AW-SRV-019`'s binary so a Job condition could be read
literally, and it would buy nothing the log line doesn't. One condition binds the reading: **AC-2's
observation must show the Deployment reusing the Job's checkpoint.** Every start bootstraps its World
from a snapshot round (`server/projector/run.go`), because the checkpoint holds a tick and offsets,
not state. So what shows reuse is the Deployment's first `state projector started` line after the
rebuild. It has `committed=true` and a `committed_tick` at or after the `<t>` in
`projector-rebuild: rebuilt to tick <t>`. A `committed=false` there means the checkpoint was lost and
the Job's work thrown away. *(Corrected 2026-09-30 on review of #176, which first said "not
bootstrapping from a round". No start can meet that.)*

**Deviations accepted as they stand:** `objectstore-install` exits 3 when `kubectl` is missing, the
same usage class as `projector-*`'s 2. The enabled check reads the Deployment's existence. `make`
reports any failed recipe as 2, so exit codes are asserted on the script, as everywhere else. Two
nits for SRE's next touch: `objectstore.py` hard-codes `andara-objectstore` where the chart has
`objectstore.service`, and `deploy/k8s/objectstore/objectstore.yaml:12` still names `objectstore.sh`.

**What closes it:**
1. SRE runs ACs 1, 3, 6 (the round) and 8 (the box half) on `main`, and records them here.
2. #143's fix, then ACs 2 and 4 observed on `dev`, with AC-2 read as above.
3. SRE's §8 instrumentation record: `up{job="andara-projector-state"}` with the lag and budget
   gauges; the Job's `state.replay` → `state.verify` spans in Tempo; kube-state-metrics keeping
   `kube_deployment_spec_replicas` for the projector in Grafana Cloud; and the production digest
   line inherited from `AW-SRV-019`.

## §8 instrumentation check and after-merge ACs (2026-09-30, SRE): AC-2 and AC-4 owed on #143

On `sre/sprint-03-review-verify`, on the box, with `andara-dev` Synced at `main@79fd622` and Degraded.
It was Degraded because the projector had been in CrashLoopBackOff for 13 h (135 restarts, exit `2`).
Each start logged `state projector diverged earlier and it is unresolved` for tick 132,838. That's
the divergence marker from its first start after this story merged.

| AC | Run | Result |
|----|-----|--------|
| 6 (owed half) | The bucket, listed through `objectstore.py`'s rclone pod | pass. A round per 600 ticks, each four Zone objects, the newest `…/00000000000000656692/…` at 18:08:09Z. The server logs `snapshot round complete` each minute. 3,480 objects, growing by four a minute, until `AW-INF-007`'s retention |
| 1 | `make projector-stop ENV=dev` | pass. `projector-stop: stopped (group andara-projector-state-dev empty) in 2s`, exit 0. `spec.replicas` 0; `rpk group describe`: `Empty`, 0 members |
| — | `make projector-start ENV=dev PROJECTOR_START_TIMEOUT=45s` | exit 1, `failed in andara-dev: start: andara-projector-state not Ready in 45s`. Correct for a projector holding a divergence |
| 3 | `make projector-rebuild ENV=dev`, from the unready running projector | pass. `projector-stop` completed first in the output. The last projector pod's `Killing` came about 18:10:38Z, and the Job and its pod started at 18:10:39Z |
| 8 (box half) | A second `make projector-rebuild ENV=dev` while the Job's pod ran | pass. `rebuild: Job andara-projector-state-rebuild is still running in andara-dev; nothing was changed`, exit 1 (make: `Error 1`). Replicas stayed 0 |
| 4 | The Job's log | **first half passes; the second fails on #143.** `state projector started` with `round_tick=657893`, `from_zero=false`, `rebuild=true`, after `bootstrap from snapshot round` and `boundary reader positioned after the round`. Then `state projector diverged` at tick 657,894, round + 1 |
| 2 | Same run | **owed on #143.** The Job failed (`backoffLimit: 0`, exit 2). The target said `rebuild: Job andara-projector-state-rebuild failed; its pod's exit code is in kubectl … describe job …` and exited 1 without running `projector-start` |

**#143 on `dev`.** This is its in-cluster reproduction, through the rebuild path, from a round the
server wrote to `s3`. So the round-capture fault isn't an `fs` artifact. It's added to #143.

**Instrumentation.**
- **Logs:** `consumer group wiped for a rebuild`, `state projector started` (with `round_tick`) and
  `state projector diverged` are on the pods' stdout, with `service`, `env` and `tick`. Each target's
  final line matches the Observability section's form.
- **Not verified on Grafana Cloud.** No `GRAFANA_CLOUD_*` credentials were in this session. These are
  owed at §8, and they're the list in architecture's "What closes it" item 3:
  1. `up{job="andara-projector-state"}`, with the lag and lag-budget gauges;
  2. the rebuild Job's `state.replay` → `state.verify` spans in Tempo. This Job diverged, so a
     `state.verify` carrying the mismatch is the one to look for;
  3. `kube_deployment_spec_replicas{deployment="andara-projector-state"}` kept by Grafana Cloud's
     kube-state-metrics;
  4. the production digest line inherited from `AW-SRV-019`;
  5. the lines above in Loki.
  `make observe-check ENV=dev` covers 1 and 5 once the projector is Ready, which waits on #143.
  `StateProjectorDown`'s promtool cases pass (`make helm-test`). Its delivery is `AW-INF-009`'s.
- **What the 13 h crash loop says about the alert.** It's the case `StateProjectorDown` exists for:
  desired 1, never scraped Ready. It paged nobody because no rule is evaluated yet (`AW-INF-009`).

**`dev` as left, which is SRE's call as the §8 review above asks:** the projector is at 0 replicas, which the Application ignores. The failed Job
stays until its TTL, 18:11Z + 1 h. At 0 replicas `StateProjectorDown` is silent by design, and no
lag is exported. Restarting it before #143 is fixed only reproduces the crash loop. After the
fix, `make projector-rebuild ENV=dev` is AC-2 and AC-4.

The story stays `review`, owing AC-2 and AC-4 (#143) and the Grafana Cloud observations above.

## §8 instrumentation check, Grafana Cloud half (2026-10-01, SRE)

Read from Grafana Cloud with the read token from `.local/box.env` (Brian, this session), against the
2026-09-30 run recorded above. `make observe-check ENV=dev` reports the server's `up` and
`andara_sessions_active` present.

| Owed item (architecture's "What closes it", item 3) | Observed |
|---|---|
| `kube_deployment_spec_replicas{deployment="andara-projector-state"}` kept by kube-state-metrics | **yes.** `andara-dev` `0` (scaled down since the run), so `StateProjectorDown`'s left operand exists |
| The rebuild Job's lines in Loki | **yes**, from pod `andara-projector-state-rebuild-6bgbw`: `consumer group wiped for a rebuild` (`group=andara-projector-state-dev`), `bootstrap from snapshot round` (`tick=657893`), `state projector started` (`round_tick=657893`, `from_zero=false`, `rebuild=true`), `state projector diverged` (`tick=657894`), `state projector stopped` |
| The Job's `state.replay` → `state.verify` spans in Tempo | **`state.replay` yes** (trace `73603b44…`). **`state.verify`, none.** `server/projector/run.go` opens and closes `state.verify` only inside the callback for a tick that verified, so the diverging tick gets no span, and none has a duration. Filed as **#290** for `AW-SRV-019`, as this story's Observability section routes missing Job spans. It doesn't hold this story |
| `up{job="andara-projector-state"}` with the lag and budget gauges | **owed.** The projector is at 0 replicas until #143's fix (`observe-check`: absent) |
| `AW-SRV-019`'s inherited production digest line | **owed**, with the above, on the first Ready projector after #143 |

The instrumentation item now owes only what a Ready projector can show. AC-2 and AC-4 are still
owed on #143.

## AC-2, AC-4 and the rest of §8 on `dev` (SRE, 2026-10-01): #143 fixed

#307 fixed #143: with the default `sim.seed` 0, a restore derived the seed from the round's World.
`dev` ran an image with the fix from `0cb713c`, on both the server and the projector, after the
publish runs for `26f6e97` and `8d816ab` were superseded in the queue. A rebuild at 18:12Z on the
old image (`b7162dd`) diverged again, as expected, and its Job was deleted. The projector stayed at
0 replicas.

`make projector-rebuild ENV=dev`, 18:18:24Z to 18:18:39Z, exit 0:
```
projector-stop: stopped (group andara-projector-state-dev empty) in 1s
projector-rebuild: Job andara-projector-state-rebuild created; waiting for `state projector caught up`
projector-rebuild: caught up at tick 25198; stopping the Job
projector-start: ready in 7s
projector-rebuild: rebuilt to tick 25198 in 15s
```

| AC | Observed | Result |
|----|----------|--------|
| 2 | Read as architecture amended it (§8 review): the Job's pod logged `caught up` at 25198, the target deleted it, and the Deployment's first `state projector started` has `committed=true`, `committed_tick=25232`, at or after 25198. So the checkpoint was reused. Then `andara_state_projector_lag_seconds` 0.0143–0.0147 against `_lag_budget_seconds` 5, over 60 s from `/metrics` and in Grafana Cloud. The 10-minute max is 0.0147 | pass |
| 4 | The Job: `state projector started` `round_tick=24643`, the newest complete round then. The Deployment: `round_tick=25243`, the newest round at its start (the server's rounds: 24643, 25243, 25844), `from_zero=false`. Then caught up at 25262, ticking on (25465 → 26265), `andara_state_digest_mismatches_total` 0, no `diverged` line, 0 restarts | pass |

**The §8 instrumentation items still owed (architecture's "What closes it", item 3) are now all
observed in Grafana Cloud:**
- `up{job="andara-projector-state", namespace="andara-dev"} == 1`. `make observe-check ENV=dev`
  reports every signal present, with `StateProjectorDown`, `StateProjectorDiverged` and
  `ProjectionStale` silent;
- the lag and budget gauges, as above;
- `kube_deployment_spec_replicas{deployment="andara-projector-state"}` `1` (it was `0` at the
  earlier check);
- the rebuild Job's `state.replay` traces, with 10 `state.verify` children each. #290, no span for
  a *diverging* tick, is a separate case and still open;
- **`AW-SRV-019`'s production digest line:** the digest assertion runs continuously on `dev`. The
  projector verifies every tick, with `digest_mismatches_total` 0, live.

With ACs 1–8 observed, the story's instrumentation item and every owed AC are met. Architecture's §8
can move it.
