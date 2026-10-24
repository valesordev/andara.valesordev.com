# Snapshot object store

The server writes Zone snapshot rounds to an S3 API and the state projector reads them
(AW-INF-025). On the box's cluster one gateway serves both environments (AW-INF-058,
`ADR-0013` decision 4). prod keeps its own store in its own cluster (decision 10).

| File | Where | Applied by |
|---|---|---|
| `shared.yaml` | `andara-shared`, for `local` and `dev` | `make objectstore-up` (and `make shared-up`, which runs it) |
| `objectstore.yaml` | `andara-prod`, prod's own | `make objectstore-install ENV=prod` |

## The shared gateway

versitygw `v1.8.0` with its internal IAM (`--iam-dir` on the PVC), a 10Gi PVC, Service
`andara-objectstore.andara-shared.svc:9000`.

| User | Role | Bucket | Its pair lives in |
|---|---|---|---|
| root | admin | all | `andara-shared/andara-objectstore-root`, operator only |
| `andara-local` | `user` | owns `andara-snapshots-local` | `andara-local/andara-snapshot-s3` (the copy the server and projector mount), source `andara-shared/andara-objectstore-local` |
| `andara-dev` | `user` | owns `andara-snapshots-dev` | `andara-dev/andara-snapshot-s3`, source `andara-shared/andara-objectstore-dev` |
| `andara-it` | `userplus` | creates `andara-test-*` | `andara-shared/andara-objectstore-it`, operator only; `make test-integration` exports it (AW-INF-060) |

Each user's access key is its name, so the gateway's access log names the user on every line.
A pair is generated once in `andara-shared`, and `make objectstore-install ENV=<env>` copies the
environment's pair into its namespace. Re-running the install re-copies it, so the copy can be
recovered by running the install again. No environment's ServiceAccount has RBAC on Secrets in
`andara-shared`.

**What the gateway enforces** (verified on `v1.8.0`, 2026-10-24, and re-proved by
`make objectstore-check`):
- A `user` lists, reads, writes and deletes in its own bucket.
- A `user` is refused all four on any other bucket, and can't create a bucket.
- `andara-it` creates, writes and deletes its own `andara-test-*` buckets.
- `andara-it` is refused every verb on an environment bucket, deleting it included, and is refused
  an existing name (409 `BucketAlreadyExists`).

**What it does not prevent** (`ADR-0013`):
- One environment filling the shared PVC: `make shared-check` reports its use and fails above 80%.
- The root pair deleting either bucket: it is the operator's, and only `objectstore.py` uses it.
- `andara-it` naming a bucket outside `andara-test-*`: the prefix is the suite's convention, and
  versitygw has no name-prefix ACLs.

**Network:** port 9000 admits the `andara` and `andara-objectstore-tools` pods in namespaces
labelled `andara.valesor/env` in `{local, dev}`, and the tools pods in `andara-shared`.
`make shared-netcheck` proves it for both shared listeners.

## Observability

- **Metrics:** none. versitygw `v1.8.0` offers StatsD and DogStatsD sinks
  (`--metrics-statsd-servers`, `--metrics-dogstatsd-servers`) and no Prometheus endpoint, and none
  is wired. Snapshot health is observed from the server side: `SnapshotStale` and the server's
  snapshot metrics, which carry `environment`.
- **Logs:**
  - The access log goes to stdout (`--access-log=stdout`). Each line carries the time, the
    requester (the access key, which is the user's name), the bucket and its owner, the
    operation, the status and the error code, and never a secret. A refusal reads `AccessDenied`
    with the refused user and bucket.
  - Read it with `kubectl -n andara-shared logs deployment/andara-objectstore`.
  - Every `make objectstore-*` step prints `objectstore-<cmd>: <step>`.

## Targets

| Target | What it does |
|---|---|
| `make objectstore-up` | The gateway, its root Secret (generated once), and `andara-it`. Idempotent. The gateway's pod template carries the root Secret's uid, so a recreated root Secret restarts the gateway onto it. |
| `make objectstore-install ENV=<local\|dev>` | That environment's namespace label, user, owned bucket and Secret copy. Idempotent. A pair the gateway refuses (a rotation interrupted before the gateway learned it) is pushed to the gateway from `andara-shared/andara-objectstore-<env>`; an unreachable gateway changes nothing and fails. |
| `make objectstore-install ENV=prod` | prod's own per-namespace gateway (AW-INF-025, unchanged). |
| `make objectstore-check` | The isolation test: each pair against both buckets, and `andara-it` against both buckets and a fresh `andara-test-*` one. The gateway's access log confirms the refusals. |
| `make shared-down CONFIRM=andara-shared` | Removes the gateway, its PVC (every environment's snapshots) and the operator-only Secrets. |

Exit codes: `0` ok, `1` a failed step or assertion, `2` usage (an unknown `ENV`), `3` the cluster
context, `andara-shared`, or the root Secret is missing.
