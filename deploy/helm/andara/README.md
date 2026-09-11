# andara — Helm chart

`andara-server` as a one-replica `StatefulSet` owning all 64 Partitions (ADR-0001), with
readiness that means *simulation* readiness, a snapshot volume that is never deleted by the
chart, and one `Deployment` per projector. Story: `docs/stories/AW-INF-003-kubernetes-workload-topology.md`.

## Files

| Path | What | Edit? |
|------|------|-------|
| `keys.yaml` | server configuration key registry: dotted key → `ANDARA_*` env, type, default, owning story | yes — add a key when the server gains one |
| `schema.base.yaml` | hand-written schema for every non-server value | yes |
| `values.schema.json` | **generated** by `make values-schema` from the two above | no |
| `templates/_env.tpl` | **generated**: ConfigMap lines for every in-code key | no |
| `measurements.yaml` | p99 tick CPU and RSS from `make measure-tick`; requests derive from it | only via `make measure-tick` |
| `files/alerts.yaml` | Prometheus rules, shipped as a ConfigMap and mounted by the compose stack | yes, with a runbook |
| `../values/{local,dev,prod}.yaml` | per-environment values | yes |

## Targets

| Target | Does |
|--------|------|
| `make k8s-dry [ENV=<env>]` | render + `kubeconform -strict` against Kubernetes 1.36.1; every environment when `ENV` is not given; in `make check` |
| `make values-schema` / `values-schema-check` | regenerate / verify the generated files and hold `keys.yaml` against `server/**/*.go`; check is in `make check` |
| `make helm-test` | render-level assertions (PVC retention, no secret material, schema rejections, probes, ordinal→Partitions); in `make check` |
| `make image [TAG=]` | build `andara-server:<tag>` from `deploy/compose/Dockerfile.server` |
| `make kind-load [KIND_CLUSTER=]` | load the image into kind |
| `make helm-install ENV=<env>` | idempotent `helm upgrade --install` into namespace `andara-<env>`; `local` also builds the content ConfigMap from `testdata/content/valid` |
| `make measure-tick [DURATION=300]` | record p99 CPU and RSS into `measurements.yaml`; refuses until the server exposes `andara_tick_duration_seconds` (AW-SRV-002) |

`make image && make kind-load && make helm-install ENV=local` is the whole local path.

## Contract

- **Probes.** startup and readiness `GET /readyz`, liveness `GET /livez`, on `server.http.port`
  (8080). Readiness is 200 only when the World is loaded and ticking (AW-SRV-007 sets the flag);
  liveness never depends on Kafka or a datastore — a broker outage is a read-only World
  (AW-SRV-010), not a restart loop. Startup budget 10 s × 60 = 600 s.
- **Ordinal → Partitions.** Init container `partitions` reads `apps.kubernetes.io/pod-index`,
  writes `ANDARA_SIM_PARTITIONS` (`p mod replicaCount == ordinal`) to a shared `emptyDir`; the
  server container sources it before `exec`.
- **Volumes.** `snapshots` claim at `/var/lib/andara/snapshots`, `helm.sh/resource-policy: keep`
  and `persistentVolumeClaimRetentionPolicy: Retain/Retain`. Losing it costs replay time, not
  data (ADR-0002); the chart still never deletes it.
- **Secrets.** Values carry Secret *names* only (`secrets.tokenKey.secretName` …), mounted at
  `/etc/andara/secrets/<name>/` with mode 0400. No `Secret` is rendered; `make helm-test` plants a
  name and asserts it appears nowhere but as a volume reference.
- **Resources.** `resources.requests` when set; else `measurements.yaml` × `resourcesMultiplier`
  (1.5), rounded up. Limits only when set explicitly.
- **Projectors.** `projectors.{state,redis,postgres}.enabled` render a `Deployment` each,
  `strategy: Recreate` (one consumer-group holder). All `false` until AW-SRV-017/018/019 exist.
- **NetworkPolicy.** Skeleton, `enabled: false`; AW-INF-006 fills the gRPC rules and turns it on.
- **Alerts.** `AndaraServerUnavailable` (scrape down or absent for 2 m) and
  `AndaraServerCrashLooping` (> 3 restarts in 10 m); runbooks in `docs/runbooks/`. The cluster
  Prometheus must scrape the pod under `job="andara-server"` and mount ConfigMaps labelled
  `andara.valesordev.com/prometheus-rules=true` as rules (EPIC-07).

## Environments

`local` is kind on the developer's box (v1.36.1): `pullPolicy: Never`, content from a ConfigMap,
1 Gi claim, OTLP export off. `dev` and `prod` pull from `ghcr.io/valesordev/andara-server`;
`prod`'s tag is always overridden by `make deploy TAG=` (AW-INF-007).
