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
| `../tests/alerts_test.yaml` | `promtool test rules` over `files/alerts.yaml`: the compose shape and the per-namespace cluster shape | yes, with every rule change |
| `../values/{local,dev,prod}.yaml` | per-environment values | yes |
| `../../k8s/cert-manager/andara-ca.yaml` | the private CA chain (cluster-scoped; applied by `make helm-install`, not by the chart) | yes |
| `../../k8s/traefik/values.yaml` | Traefik as the box has it, for `make kind-platform` on a fresh cluster | yes |
| `../../kind/config.yaml` | a kind cluster shaped like the box: `:80`/`:443` mapped, `ingress-ready` node | yes |

## Targets

| Target | Does |
|--------|------|
| `make k8s-dry [ENV=<env>]` | render + `kubeconform -strict` against Kubernetes 1.36.1; every environment when `ENV` is not given; in `make check` |
| `make values-schema` / `values-schema-check` | regenerate / verify the generated files and hold `keys.yaml` against `server/**/*.go`; check is in `make check` |
| `make helm-test` | render-level assertions (PVC retention, no secret material, schema rejections, probes, ordinal→Partitions); in `make check` |
| `make image [TAG=]` | build `andara-server:<tag>` from `deploy/compose/Dockerfile.server` |
| `make image-publish` | build for linux/amd64 and push `ghcr.io/valesordev/andara-server:sha-<12 hex>` then `:dev`; what `.github/workflows/publish.yaml` runs on every merge to `main` (AW-INF-013) |
| `make image-check ENV=<env> [TAG=dev] [REGISTRY_ONLY=1]` | prove a published tag pulls anonymously (and a `sha-` tag carries its commit), then from the cluster with a throwaway `/bin/true` pod in `andara-<env>` |
| `make kind-load [KIND_CLUSTER=]` | load the image into kind |
| `make helm-install ENV=<env> [TAG=]` | idempotent `helm upgrade --install` into namespace `andara-<env>`; `local` installs the kind-loaded `IMAGE:TAG`, any other environment its values file's image, with `TAG=` pinning one (`scripts/helm_image_args.sh`), and a moving tag like `:dev` pinned to the digest it names now, so a rerun rolls the pod exactly when `publish` has moved it; applies and waits on the private CA first; `local` also builds the content ConfigMap from `testdata/content/valid`; writes the CA bundle to `.local/tls/cluster/<env>/ca.pem` and prints the `andara-cli` line and the `/etc/hosts` hint |
| `make kind-platform [KIND_CLUSTER=]` | install Traefik and cert-manager into a fresh kind cluster (skips releases that already exist — the box's are Brian's) |
| `make stream-soak ENV=<env> [SOAK=5m]` | hold a Subscribe through the edge for `SOAK`, renew the edge certificate mid-stream, assert Traefik counted gRPC; nightly at 60 m in CI |
| `make measure-tick [DURATION=300]` | record p99 CPU and RSS into `measurements.yaml`; refuses until the server exposes `andara_tick_duration_seconds` (AW-SRV-002) |

`make image && make kind-load && make helm-install ENV=local` is the whole local path on the box;
a fresh cluster needs `kind create cluster --config deploy/kind/config.yaml && make kind-platform` first.

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
- **Edge (AW-INF-006).** Two `Ingress` on `host` (class `traefik`, entrypoint `websecure`): `/` for
  Game, `/andara.admin.v1.Admin/` behind a `Middleware` `ipAllowList` of `admin.allowedCIDRs`
  (403 at the edge; the server never sees it). TLS ends at Traefik with `andara-edge-tls` and is
  re-originated to the pod as HTTPS/HTTP-2 through a `ServersTransport` that verifies the pod
  against the private CA (`rootCAs` reads `ca.crt` from the server certificate's own Secret;
  `serverName` is `andara.<ns>.svc`). No host port is bound by the chart — the cluster's Traefik
  owns `:443`. Traefik has no per-Ingress stream timeout; `make stream-soak` is what proves an
  idle stream survives an hour.
- **Certificates.** `andara-edge` (SAN `host`, from `tls.issuer`) and `andara-server` (SANs
  `andara-0.andara.<ns>.svc`, `andara.<ns>.svc`, from `tls.privateIssuer`, into
  `secrets.tlsCert.secretName`), 90 d, `renewBefore: 240h`. Traefik reloads the edge Secret live;
  the pod loads its certificate once (AW-SRV-005) and needs a restart inside `renewBefore` — see
  `docs/runbooks/certificate-expiring.md`. The `andara-ca` ClusterIssuer is cluster-scoped and
  comes from `deploy/k8s/cert-manager/andara-ca.yaml`, not from the chart.
- **NetworkPolicy.** On by default: the grpc port admits `networkPolicy.ingressNamespace`
  (`traefik`) and `agentNamespace` only; the http port admits `observabilityNamespace`. Enforced by
  kind's kindnet, verified by CI from a pod in another namespace.
- **Alerts.** `AndaraServerUnavailable` (scrape down or absent for 2 m) and
  `AndaraServerCrashLooping` (> 3 restarts in 10 m); `CertificateExpiringSoon` (< 7 d) and
  `IngressErrorRateHigh` (5xx > 1% on the Andara routers for 10 m — Connect and gRPC-Web only;
  Traefik 3.7 labels every gRPC response `code="2"`); runbooks in `docs/runbooks/`. Every alert
  carries the `namespace` it is about, so `dev` and `prod` page separately from one tenant;
  `deploy/helm/tests/alerts_test.yaml` proves that and the compose shape under `promtool test
  rules` in `make helm-test`. Where the rules are *evaluated* on the cluster is `AW-INF-009`;
  the ConfigMap is the file, shipped, not a rule anything loads yet.
- **Observability (AW-INF-008).** There is no cluster Prometheus. The cluster runs Grafana's
  `k8s-monitoring` (Brian's release, not this chart's): Alloy scrapes pods by annotation and
  remote-writes to Grafana Cloud with `cluster="solo7-local"`, ships every container's stdout to
  Loki, and receives OTLP at `k8s-monitoring-alloy-receiver.observability.svc:4317`. The chart's
  side is `observability.annotations` — `prometheus.io/scrape`, `/port` (the http port), `/path`
  and `k8s.grafana.com/job` on every pod template: `andara-server` for the StatefulSet,
  `andara-projector-<name>` for each projector — and, on `dev`/`prod`,
  `server.telemetry.otlp_endpoint` pointing at that receiver. The scraper runs in
  `networkPolicy.observabilityNamespace`, which the NetworkPolicy admits to the http port.
  Traefik and cert-manager are scraped by the platform already. `make observe-check ENV=<env>`
  asks Grafana Cloud whether it all arrived (credentials from `GRAFANA_CLOUD_*`; it exits 3
  without them rather than pass).

## Environments

`local` is kind on the developer's box (v1.36.1): `pullPolicy: Never`, content from a ConfigMap,
1 Gi claim, OTLP export off (the CI kind cluster has no collector), `host: andara.local` (an `/etc/hosts` line; `make helm-install` prints
it), and `172.16.0.0/12` in the Admin allowlist because a connection from the box reaches Traefik
from the docker bridge, not from 127.0.0.1. `dev` and `prod` are `andara-dev.solo7.valesordev.com` and `andara.solo7.valesordev.com` on the
cluster's `letsencrypt` ClusterIssuer (clients need no CA file), and pull from `ghcr.io/valesordev/andara-server`,
which CI publishes on every merge to `main` (AW-INF-013): `dev` follows the moving `:dev` tag (`pullPolicy: Always`),
and `make helm-install ENV=dev TAG=sha-<12 hex>` pins a build. `prod`'s tag is always overridden by `make deploy TAG=`
(AW-INF-007). **`dev` runs broker-free** until AW-INF-014 puts a Kafka broker in its namespace (decided 2026-09-24):
content from the same ConfigMaps as `local`, Accounts and Commands in memory, and `make helm-install ENV=dev` refuses
to run without `ANDARA_BOOTSTRAP_OPERATOR=<user>:<password>`, because `local`'s default credential is public and
this edge is not.
