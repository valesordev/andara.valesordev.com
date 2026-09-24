---
id: AW-INF-008
title: Cluster observability wiring — the chart's metrics, logs, and traces reach Grafana Cloud
epic: EPIC-07
component: infra
type: infra
status: review
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

This story is the wiring, and only the wiring: four annotations, one endpoint, and the rules made
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
3. **Given** the server on `dev` or `prod` **when** it boots **then** no `otlp log export failed` line is
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
7. **Given** `make observe-check ENV=local` with no token **when** it runs **then**
   `scripts/observe_check.py` exits `3` with `observe-check: no GRAFANA_CLOUD_READ_TOKEN; cannot verify`
   — not `0` — and `make` reports `Error 3` (its own status is `2`, as for any failed recipe).
8. **Given** `make helm-test` **when** it runs **then** the four annotations are on every pod template
   the chart renders, the StatefulSet's job is exactly `andara-server` and each projector's is its own
   `andara-projector-<name>`, and every `job=` selector in `files/alerts.yaml` names a job some pod
   template is scraped as. (Not the converse: the projectors have no rules yet.)

## Interface contract

### Pod annotations (StatefulSet and projector Deployments)

| Annotation | Value | Why |
|------------|-------|-----|
| `prometheus.io/scrape` | `"true"` | Alloy's `annotation_autodiscovery_pods` selector |
| `prometheus.io/port` | `server.http.port` (`8080`) | |
| `prometheus.io/path` | `/metrics` | |
| `k8s.grafana.com/job` | `andara-server` / `andara-projector-<name>` | sets `job` exactly; the fallback would be `app.kubernetes.io/name` = `andara` and break every selector |

`k8s.grafana.com/instance` is not set, so `instance` is Prometheus's default, the target address
(`<pod IP>:8080`): one value per pod *incarnation*, bounded by restarts. `pod` is the stable identity
and what a query should group by. A static annotation cannot do better — at `replicaCount: 2` both
pods would carry it.

A pod with two declared ports (`grpc`, `http`) yields two discovered targets; `prometheus.io/port`
rewrites both to `:8080` and the scrape pool keeps one, because their labels are identical after
relabelling. Observed 2026-09-23: Traefik, which the platform already collects, declares four ports
and no port-name annotation, and the live `annotation_autodiscovery_http` component lists it as one
target (`10.244.0.2:9100/metrics`).

### Values

| Value | `local` | `dev` / `prod` |
|-------|---------|----------------|
| `server.telemetry.otlp_endpoint` | `""` (unchanged; no collector in the CI cluster) | `k8s-monitoring-alloy-receiver.observability.svc:4317` |
| `observability.annotations` | `true` | `true` — `false` renders none, for a cluster without autodiscovery |

The scrape source is the `alloy-metrics` DaemonSet in `observability`, which
`networkPolicy.observabilityNamespace` already admits to the http port. That value is the one
statement of it; a second key saying the same thing would render nothing.

### Alert rules — the shape every rule takes from here

```promql
# CONTRACT SKETCH — AndaraServerUnavailable, after
max by (namespace) (up{job="andara-server"}) == 0
  or (
       absent(up{job="andara-server", namespace="andara-dev"})
    or absent(up{job="andara-server", namespace="andara-prod"})
  ) and on() count(up{namespace!=""}) > 0
```

On the cluster, down is usually *absence*: the platform's annotation scrape keeps only `Running`,
`Ready` pods, so a pod failing readiness drops out of the target list exactly as a deleted
StatefulSet does, and its `up` goes stale. `== 0` there is a Ready pod whose `/metrics` cannot be
reached. The `absent()` lines are the environments that must exist — `andara-local` is deliberately
not one — and they are guarded: in compose no series has a namespace, so the guard is empty and
`absent(...andara-prod...)` — true there forever — cannot fire. Compose is the reverse of the
cluster: its scrape target is static, so a stopped server is `up == 0`, never no `up`, and `max by
(namespace)` over its unlabelled series is one result with no `namespace` — the existing behavior.

The other rules, by what their series carry:

| Rule | Per environment because |
|------|-------------------------|
| `AndaraServerCrashLooping` | kube-state-metrics labels each series with the pod's namespace |
| `SimulationLagging`, `SnapshotStale` | per-series comparisons; the scrape's `namespace` rides through |
| `SessionsDroppingAtRate` | both sides `sum by (namespace)` |
| `CertificateExpiringSoon` | the platform scrapes cert-manager without `honor_labels`, so the Certificate's namespace arrives as `exported_namespace`; `label_replace` puts it back in `namespace` |
| `IngressErrorRateHigh` | Traefik's series carry `namespace="traefik"`; the environment is the router name's prefix (`<namespace>-<ingress>-…`, the naming `make stream-soak` asserts on every kind run), lifted out by `label_replace` before the per-namespace ratio |

`deploy/helm/tests/alerts_test.yaml` holds each row under `promtool test rules`.

### Make targets

| Target | Does | Exit |
|--------|------|-----:|
| `make observe-check ENV=<env>` | with `$GRAFANA_CLOUD_READ_TOKEN`, asks Prometheus for AC-1/2's series, Loki for AC-4's `session opened` line (`SESSION_ID=` pins one), and Tempo for AC-3's `OpenSession` span by that line's `trace_id` (`TRACE_ID=` overrides); prints each with its labels; then evaluates every rule in `files/alerts.yaml` as an instant query and prints what each would fire for (AC-6's instrument, outside the exit status) | `0` all present · `1` a signal absent · `2` a backend refused the query · `3` a credential or URL unset |

Grafana Cloud's three backends are three hosts with three user IDs, so the environment is
`GRAFANA_CLOUD_{PROM,LOKI,TEMPO}_URL` and `_USER`, plus the one token (an access policy with
`metrics:read`, `logs:read`, `traces:read`). The URLs and IDs are on the stack's details page and are
not secrets; the token is, and the script neither takes it as an argument nor prints it. AC-2's
expected projector jobs come from `PROJECTORS` (comma-separated, `none` for none) or else from the
namespace's Deployments via `kubectl`; when neither can answer, the script exits `3` — an unknown
projector set is not an empty one.

`make bootstrap` pins `promtool` 3.1.0 — the compose Prometheus's version — from the upstream release
tarball against a checksum in `scripts/bootstrap.sh`. Not `go install`: the Prometheus module carries
`replace` directives, which `go install pkg@version` refuses.

### Platform, stated not assumed

What the `k8s-monitoring` release provides and this story relies on, verified 2026-09-17 and again
on 2026-09-23 against the rendered `alloy-metrics` and `alloy-receiver` configuration (read-only):
annotation autodiscovery on pods (collector `alloy-metrics`, clustered DaemonSet); `podLogsViaLoki`
for every container's stdout; an OTLP receiver at `k8s-monitoring-alloy-receiver:4317`/`4318`;
`cluster="solo7-local"` on every series; the cert-manager integration; Traefik's own
`prometheus.io/*` annotations. A cluster without these needs `make kind-platform` extended — the CI
cluster has none of them, which is why AC-1–4 run against the box and AC-5, 7, 8 run in CI.

## Data / state impact

None.

## Observability requirements

This story *is* the observability requirement. Cardinality it introduces: `job` (4 values),
`namespace` (3), `instance` (one per pod incarnation; see the contract). No unbounded label.

## Test plan

- **Unit (`make helm-test`):** AC-8 — annotations present on every pod template with all three
  projectors enabled, jobs as in the AC, every `job=` selector parsed out of `files/alerts.yaml`
  scraped; `observability.annotations=false` renders none; `promtool check rules` over the file and
  `promtool test rules` over `deploy/helm/tests/alerts_test.yaml` — the compose shape (server down
  pages once, without a namespace; a healthy server never pages on absence) and the cluster shape
  (prod down, prod deleted, dev's Sessions dropping, dev's edge 5xx, a Certificate in each
  environment: each pages for its own namespace only).
- **Integration (compose, `stack` workflow):** AC-5 — rules load and the existing `docker stop` check
  still goes `pending`.
- **Integration (box, manual, recorded in the verification record):** AC-1–4, 6 with a read token;
  AC-7 without one.
- **Manual/operator:**
  ```
  make helm-install ENV=dev
  export GRAFANA_CLOUD_READ_TOKEN=... GRAFANA_CLOUD_{PROM,LOKI,TEMPO}_{URL,USER}=...
  make stream-soak ENV=dev SOAK=1m        # opens a Session: the log line and the span AC-3/4 read
  make observe-check ENV=dev
  # expect: ok up{…namespace="andara-dev"…} 1, ok andara_sessions_active, ok loki … trace_id=<id>,
  #         ok tempo trace <id> carries andara.game.v1.Game/OpenSession; exit 0
  kubectl -n andara-dev delete pod andara-0 && make observe-check ENV=dev   # AC-6, within the restart
  # expect: AndaraServerUnavailable  {namespace="andara-dev"} — and no andara-prod
  ```

## Definition of done

CLAUDE.md §8, plus: the README paragraph is corrected; `AW-INF-003` and `AW-INF-006` verification
records gain a line pointing here for "verified against a real backend" on the cluster.

## Verification record — 2026-09-23

Groomed 2026-09-17, implemented 2026-09-23 in a separate session. What ran here: `make check`,
`promtool` 3.1.0 against the rule file and its tests, `helm template` for all three environments, and
read-only `kubectl` against the box's `observability` namespace. What did not: nothing is installed
in `andara-dev` or `andara-prod` on the box today, and this session holds no Grafana Cloud read token.
The box ACs are therefore owed, with the exact command — not claimed.

| AC | Result | How |
|----|--------|-----|
| 1 | **owed (box)** | `make helm-install ENV=dev`, then `make observe-check ENV=dev` with a read token |
| 2 | **owed (box)** | as AC-1, once a projector binary exists (`AW-SRV-019` first); until then `helm-test` renders all three with distinct jobs |
| 3 | **owed (box)** | as AC-1, after `make stream-soak ENV=dev`: observe-check fetches the `OpenSession` span by the logged `trace_id`; the five-minute log check is `kubectl -n andara-dev logs andara-0 --since=5m \| grep -c 'otlp log export failed'` → `0` |
| 4 | **owed (box)** | as AC-3 — the same `session opened` line is what observe-check reads |
| 5 | pass (unit) · CI (compose) | `promtool check rules`: 7 rules; `promtool test rules`: 7 groups pass, in `make helm-test`. The tests bite: the story's own sketch (no guard) fails both compose groups, and `main`'s rules fail all five cluster groups while passing both compose ones. The compose half — every rule `health: ok`, `AndaraServerUnavailable` `inactive` → `pending` on `stop andara-server` → `inactive` on `start` — is a new `stack` workflow step, polled to deadlines, run on this PR |
| 6 | half | the expressions return only the affected namespace against synthetic series in both label shapes (`promtool test rules`); against Grafana Cloud's series it is the last two lines of the operator test plan, owed with AC-1 |
| 7 | pass | `observe_check.py` exits `3` with the exact message, naming whichever of token, URL, or user is unset first; `make` reports `Error 3`. Against a fake backend: `0` all present, `1` on an absent `up`, `2` on a 401 |
| 8 | pass | `helm-test` for `local`, `dev`, `prod`; the StatefulSet's job mutated to `andara` fails naming both the job map and the unscraped `andara-server` selector |

Contract amendments made while implementing, dated so a reviewer can see where the implementation
pushed back on the spec — all in place above:
- the `absent()` lines are guarded by the presence of namespaced series — as sketched they fire
  forever in compose, which AC-5 would have caught;
- `CertificateExpiringSoon` and `IngressErrorRateHigh` take their namespace from `exported_namespace`
  and the router name, because the platform's scrapes give those series cert-manager's and Traefik's
  — on the cluster the certificate summary would have read `cert-manager/andara-edge`;
- AC-8 asserts the direction that can hold (alert selectors ⊆ scraped jobs), AC-7 says what `make`
  does with an exit code, AC-3 names the line the server actually logs;
- `observability.collectorNamespace` dropped — it would have rendered nothing;
- `instance` is the pod address, not `<namespace>/<pod>`;
- `observe-check` takes three URLs and three user IDs, not one, and exits `2` when a backend refuses;
- `promtool` from the release tarball, checksum-pinned, not `go install`.

Found, and owed elsewhere:
- **Trace export failures log no JSON line.** `server/telemetry` builds the trace exporter but sets
  no `otel.SetErrorHandler`, so a failed span export goes to OpenTelemetry's default handler — the
  standard library logger, plain text on stderr — and nothing counts it. AC-3 can only assert the log
  exporter's line. An `AW-SRV-024` follow-up for the implementation lane.
- **Every server log line would reach Loki twice on `dev`/`prod`.** `telemetry.otlp_endpoint` turns on
  both exporters (`server/README.md`), and the platform already ships the container stream: one copy
  from the node agent (`container="server"`, what AC-4 queries), one through the receiver
  (`job`/`pod`, no `container`). **Decided 2026-09-23 (Brian): one log path per environment, and on
  Kubernetes it is stdout via the node agent — the server does not send logs to the receiver there.**
  `AW-SRV-033` (implementation lane, `ready`) adds `telemetry.otlp_logs` and sets it `false` in the
  chart's defaults; the key is registered in `keys.yaml` now, pending. Until it lands, a `dev`/`prod`
  install double-writes — neither is installed today, so nothing is paying for it yet.
- **For `AW-INF-009`:** a rule group loaded before an environment is installed pages for it —
  `absent(...andara-prod...)` is true until prod exists. `andara-local` on the box is the other way
  round: not in the `absent()` list, so a local pod that is not Ready is silent; it pages only when a
  Ready local pod's scrape fails. Both are routing decisions, which is that story's.
- The runbook and SLO expressions that quoted the old aggregate shapes
  (`server-unavailable.md`, `ingress-error-rate.md`, `slo/edge-availability.md`,
  `slo/session-availability.md`) now say what the rules evaluate.

## Review — 2026-09-24 (§8, against `main` at `63727dd`): stays `review`

PR #56 merged. Three of eight ACs pass, and five still need the box. The box can't produce them
today, and not only because this session has no token.

| AC | Result |
|----|--------|
| 5 | **pass, compose half observed.** The `stack` job on #56 (run 35931558406) loaded every rule `health: ok` and saw `AndaraServerUnavailable` go `inactive` (23:11:41Z) → `pending` on `stop andara-server` (23:11:45Z) → `inactive` on `start` (23:11:49Z). The unit half is `promtool` in `make helm-test`, green on `main` |
| 7, 8 | pass — `make check` on `main` |
| 1–4, 6 | **owed**, and blocked as below |

`make check` is clean on `main`. `main`'s `stack` job has been red since 2026-09-24:
`quay.io/minio/minio` now refuses anonymous pulls, so `make up` fails before any rule loads. That
is unrelated to this story, it doesn't touch AC-5's evidence above, and it is filed separately.

**Why the box ACs can't run today.** Found at review, and a defect in this story's own test plan:
`make helm-install ENV=dev` cannot bring up a pod on the box.
- No workflow publishes `ghcr.io/valesordev/andara-server`, and an anonymous pull of it is denied.
  EPIC-01 names "image publishing and environment promotion" as in scope, and no story carries it.
- `scripts/helm_install.sh` always sets `image.repository` to its `IMAGE` argument, which defaults to
  the locally built `andara-server`. `values/dev.yaml` sets `pullPolicy: Always`, so the kubelet
  would try Docker Hub for a kind-loaded image.
- Nothing is installed in `andara-dev` or `andara-prod`, and the session holds no
  `GRAFANA_CLOUD_*`.

The §8 rule for instruments with no in-cluster caller does not cover this. That rule is for a
caller that lands in a later story. The server here is ready, and what is missing is an image, a
broker, and a credential.

**Decided 2026-09-24 (Brian): publish the image.** `AW-INF-013` (`ready`) makes CI push
`:dev` and `:sha-<12-hex>` on every merge and fixes `helm_install.sh`. It gets `dev` as far as
a started container. Scoping it found a third blocker: `dev` inherits `content.source`,
`sim.source` and `auth.store` of `kafka`, with no broker on the box and no story installing one. So
`dev` can't reach Ready, and these ACs can't run, until `AW-INF-014` (`draft`, two questions for
Brian) also lands. One of those questions is whether `dev` should run broker-free like `local` in
the meantime, which would unblock this story as soon as the image publishes. The read token is
still Brian's to provide either way.

The DoD's pointer lines are in place: `AW-INF-003`'s verification record has one, and `AW-INF-006`
has no verification record, so its pointer is the first bullet of its Open questions.

## Open questions

- **Resolved 2026-09-23 (read from the platform):** the autodiscovery scrape interval is 60 s — the
  rendered `alloy-metrics` relabelling defaults `__scrape_interval__` to `60s` absent a
  `k8s.grafana.com/metrics.scrapeInterval` annotation, which the chart does not set. AC-1's "two
  intervals" is 2 minutes.
- **Resolved 2026-09-23 (read from the platform):** `dev` and `prod` share one tenant and are told apart
  by `namespace` — the box has one `remote_write` with `external_labels` `cluster="solo7-local"`, so
  every namespace on it lands in the same series space. When prod moves to its own cluster, `cluster`
  does the job and the `absent()` lines change with it; that is a new story, not a drift of this one.
- **Amends EPIC-07:** "This epic owns no stories of its own, by design" was true while the observed
  things were being built. The wiring to a backend nobody's story owned is exactly the gap that
  sentence said the epic would name; `AW-INF-008` and `AW-INF-009` are its first stories.
