---
id: AW-INF-008
title: Cluster observability wiring — the chart's metrics, logs, and traces reach Grafana Cloud
epic: EPIC-07
component: infra
type: infra
status: ready
size: S
depends_on: [AW-INF-003, AW-INF-006]
blocks: [AW-INF-009]
lane: architecture
risk: low
---

## Context

`AW-INF-003` and `AW-INF-006` each end with the same sentence: "the cluster Prometheus must scrape the
pod under `job="andara-server"` … (EPIC-07)". Nobody checked that there is a cluster Prometheus. There
is not. The box runs Grafana's `k8s-monitoring` 4.5.0: four Alloy collectors in `observability` that
ship metrics to Grafana Cloud Mimir (`prometheus-us-central1`), pod logs to Loki, and OTLP traces to
Tempo, under `cluster="solo7-local"`. Scraping is by pod annotation (`prometheus.io/scrape`, `/port`,
`/path`; `k8s.grafana.com/job` names the job). Traefik is already annotated and collected; cert-manager
is on as an integration. The Andara pod is not annotated, so nothing the two chart stories instrumented
leaves the pod, and on `dev`/`prod` the server's OTLP exporter points at the chart default
`localhost:4317` — a sidecar that does not exist.

This story is the wiring, and only the wiring: three annotations, one endpoint, and the rules made
correct for a backend where `dev` and `prod` are two namespaces in one tenant. It states what the
platform provides (Brian's `k8s-monitoring` release, not touched) against what the chart provides.
Evaluating the rules there is `AW-INF-009`, because that needs a token only Brian can issue.

## User story

As an operator, I want the server's metrics, logs, and traces to arrive where the dashboards are, so
that "is the World healthy" is answerable without `kubectl exec`.

## Scope

### In scope
- Pod-template annotations on the StatefulSet and each projector Deployment so Alloy's annotation
  autodiscovery scrapes `:8080/metrics` under the job names the alerts already use.
- `server.telemetry.otlp_endpoint` for `dev` and `prod`: the Alloy receiver Service.
- `files/alerts.yaml`: every rule grouped `by (namespace)` and every `absent()` scoped to a namespace,
  so a `dev` outage pages as `dev` and does not need `prod` to be down too. The compose stack has no
  `namespace` label, so the rules carry `or absent(...)` forms that still work there — proven both ways.
- `make observe-check ENV=<env>`: with a Grafana Cloud read token in the environment, queries the
  Prometheus HTTP API for the pod's series and the trace/log presence, and exits non-zero on absence.
  Without a token it says so and exits `3`, never a vacuous pass.
- The chart README's "the cluster Prometheus must …" paragraph replaced by what is actually true.

### Out of scope
- Rule evaluation and alert routing in Grafana Cloud — `AW-INF-009`.
- Dashboards in Grafana Cloud. The compose stack's `tick-health.json` is the dashboard of record; its
  cloud twin lands with the first SLO that has data (`AW-SRV-002`).
- Changing the `k8s-monitoring` release. It is Brian's, and it already collects what this story needs.

## Acceptance criteria

1. **Given** the chart installed in `andara-<env>` **when** the pod is `Ready` for two scrape intervals
   **then** `up{job="andara-server", namespace="andara-<env>", cluster="solo7-local"}` is `1` in the
   Grafana Cloud Prometheus API, and `andara_sessions_active` carries the same three labels.
2. **Given** `projectors.state.enabled=true` **when** its pod is `Ready` **then**
   `up{job="andara-projector-state", namespace="andara-<env>"}` is `1`; the three projector jobs are
   distinct.
3. **Given** the server on `dev` or `prod` **when** it boots **then** no `otlp export failed` line is
   logged in the first five minutes, and an `andara.game.v1.Game/OpenSession` span (the RPC span `AW-SRV-005` emits) from `make stream-soak` is retrievable
   from Tempo by trace ID.
4. **Given** the server's JSON log **when** Loki is queried for `{namespace="andara-<env>",
   container="server"} | json | session_id="<id>"` **then** the `session opened` line for that Session
   is returned.
5. **Given** `files/alerts.yaml` **when** `promtool check rules` and the compose Prometheus load it
   **then** both pass, and `AndaraServerUnavailable` still goes `pending` on `docker stop` of the compose
   server (the `AW-INF-003` verification, repeated).
6. **Given** two namespaces with the chart installed **when** one pod is deleted **then** the rule
   expressions, evaluated against the Grafana Cloud series with `promtool query instant`-equivalent
   calls, return a result carrying that namespace only. (Evaluation *as an alert* is `AW-INF-009`.)
7. **Given** `make observe-check ENV=local` with no token **when** it runs **then** it exits `3` with
   `observe-check: no GRAFANA_CLOUD_READ_TOKEN; cannot verify` — not `0`.
8. **Given** `make helm-test` **when** it runs **then** the four annotations are on every pod template
   the chart renders and the job names match the `job=` selectors in `files/alerts.yaml` exactly.

## Interface contract

### Pod annotations (StatefulSet and projector Deployments)

| Annotation | Value | Why |
|------------|-------|-----|
| `prometheus.io/scrape` | `"true"` | Alloy's `annotation_autodiscovery_pods` selector |
| `prometheus.io/port` | `server.http.port` (`8080`) | |
| `prometheus.io/path` | `/metrics` | |
| `k8s.grafana.com/job` | `andara-server` / `andara-projector-<name>` | sets `job` exactly; the fallback would be `app.kubernetes.io/name` = `andara` and break every selector |

`k8s.grafana.com/instance` is not set: the default (`<namespace>/<pod>`) is bounded and useful.

### Values

| Value | `local` | `dev` / `prod` |
|-------|---------|----------------|
| `server.telemetry.otlp_endpoint` | `""` (unchanged; no collector in the CI cluster) | `k8s-monitoring-alloy-receiver.observability.svc:4317` |
| `observability.annotations` | `true` | `true` — `false` renders none, for a cluster without autodiscovery |
| `observability.collectorNamespace` | `observability` | `observability` — what `networkPolicy.observabilityNamespace` already is; this story renames nothing, only documents that the scrape source is the `alloy-metrics` DaemonSet in that namespace |

### Alert rules — the shape every rule takes from here

```promql
# CONTRACT SKETCH — AndaraServerUnavailable, after
max by (namespace) (up{job="andara-server"}) == 0
  or absent(up{job="andara-server", namespace="andara-prod"})
  or absent(up{job="andara-server", namespace="andara-dev"})
```

Compose has no `namespace` label; `max by (namespace)` over an unlabelled series yields one result
with no `namespace`, which is the existing behavior. The `absent()` list is the environments that
exist — three lines, not a template.

### Make targets

| Target | Does | Exit |
|--------|------|-----:|
| `make observe-check ENV=<env>` | queries `$GRAFANA_CLOUD_PROM_URL` with `$GRAFANA_CLOUD_READ_TOKEN` for AC-1/2/4 series and a recent trace; prints each series with its labels | `0` all present · `1` a series absent · `3` no token |

### Platform, stated not assumed

What the `k8s-monitoring` release provides and this story relies on, verified 2026-09-17:
annotation autodiscovery on pods (collector `alloy-metrics`, clustered DaemonSet); `podLogsViaLoki`
for every container's stdout; an OTLP receiver at `k8s-monitoring-alloy-receiver:4317`/`4318`;
`cluster="solo7-local"` on every series; the cert-manager integration; Traefik's own
`prometheus.io/*` annotations. A cluster without these needs `make kind-platform` extended — the CI
cluster has none of them, which is why AC-1–4 run against the box and AC-5, 7, 8 run in CI.

## Data / state impact

None.

## Observability requirements

This story *is* the observability requirement. Cardinality it introduces: `job` (4 values),
`namespace` (3), `instance` (one per pod). No unbounded label.

## Test plan

- **Unit (`make helm-test`):** AC-8 — annotations present on every pod template, job names equal the
  set of `job=` selectors parsed out of `files/alerts.yaml`; `observability.annotations=false` renders
  none; `promtool check rules` over the file.
- **Integration (compose, `stack` workflow):** AC-5 — rules load and the existing `docker stop` check
  still goes `pending`.
- **Integration (box, manual, recorded in the verification record):** AC-1–4, 6 with a read token;
  AC-7 without one.
- **Manual/operator:**
  ```
  make helm-install ENV=dev
  GRAFANA_CLOUD_PROM_URL=... GRAFANA_CLOUD_READ_TOKEN=... make observe-check ENV=dev
  # expect: up{job="andara-server",namespace="andara-dev",cluster="solo7-local"} 1, four andara_* series listed
  ```

## Definition of done

CLAUDE.md §8, plus: the README paragraph is corrected; `AW-INF-003` and `AW-INF-006` verification
records gain a line pointing here for "verified against a real backend" on the cluster.

## Open questions

- `[ASSUMPTION]` Alloy's autodiscovery scrape interval is the release default (60 s). AC-1's "two
  intervals" is 2 minutes; nothing here depends on it being faster.
- `[ASSUMPTION]` `dev` and `prod` share the `solo7-local` tenant and are told apart by `namespace`. When
  prod moves to its own cluster, `cluster` does the job and the `absent()` lines change with it.
- **Amends EPIC-07:** "This epic owns no stories of its own, by design" was true while the observed
  things were being built. The wiring to a backend nobody's story owned is exactly the gap that
  sentence said the epic would name; `AW-INF-008` and `AW-INF-009` are its first stories.
