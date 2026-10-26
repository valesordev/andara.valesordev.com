---
id: ADR-0014
title: "Collection split: k8s-monitoring collects Kubernetes, Fleet collects the shared services"
status: accepted          # draft | proposed | accepted | rejected | superseded by ADR-XXXX
date: 2026-10-21
deciders: [brian]
gates: [AW-INF-065, AW-INF-051]   # their contracts are written against this decision
---

## Context

`ADR-0012` decision 15 (Brian, 2026-10-10) put **the whole collection pipeline in Grafana Fleet Management**. The
cluster was to run bare Alloy collectors that do nothing until Fleet gives them a pipeline. Every scrape, the
`environment` stamp, the keep-list and the export would be Terraform-managed Fleet pipelines (`scrape` → `stamp` →
`keep` → `export`, plus `logs` and `traces`). `AW-INF-051` was to write those pipelines and `AW-INF-065` to install
the bare collectors. Nothing of it has been built: the rebuilt cluster `andara` (`ADR-0013`) has no collectors yet,
and `solo7dev` has no pipeline.

**Brian's direction of 2026-10-21 changes the split.** The pinned `k8s-monitoring` chart does the standard Kubernetes
collection itself. Fleet Management carries only what falls outside the chart's scope, such as Kafka and the
databases in `andara-shared`. This ADR turns that direction into decisions that `AW-INF-065` and `AW-INF-051` can be
built to (`AW-INF-079`).

Facts this rests on, read from the chart `grafana/k8s-monitoring` **4.5.3** (the current release; the deleted
`solo7` cluster ran 4.5.0):

- Collectors are a `collectors:` map of named Alloy instances, each with its own `remoteConfig` block (`url`, `auth`,
  `extraAttributes`, `pollFrequency`). Fleet registration is **per collector**, and a collector that has chart
  features can also pull Fleet pipelines.
- Grafana documents a collector's local and remote configurations as **isolated**. A Fleet pipeline cannot reach
  into the chart's local graph, and the chart's graph cannot reach into a Fleet pipeline. This was decision 15's
  reason for refusing its option (b) (Codex, PR #520), and it still holds.
- Every destination applies rules to everything sent to it: `metricProcessingRules` (Alloy relabel rules) on a
  `prometheus` destination, `logProcessingStages` on a `loki` destination, and `processors.transform` on an `otlp`
  destination. Each feature also has its own `metricsTuning` allow-list, and kube-state-metrics' default list
  drops two series the rules need. That is `AW-INF-009`'s finding against 4.5.0
  (`kube_pod_container_status_last_terminated_exitcode`, `kube_pod_container_status_ready`).
- Features that receive data over the network: `prometheusMetricsReceiver` (remote-write in),
  `lokiLogsReceiver`, and `applicationObservability` (OTLP in). Received data flows to the destinations like
  any feature's.
- Annotation autodiscovery and pod logs take `namespaces` and `excludeNamespaces`. Integrations exist for
  `cert-manager` and `postgresql`, but not for Kafka.
- Annotation autodiscovery reads `k8s.grafana.com/scrape`, `k8s.grafana.com/metrics.portNumber` and
  `k8s.grafana.com/metrics.path` by default, and takes `job` from `k8s.grafana.com/job`. The names are values
  (`annotationAutodiscovery.annotations.*`). It has no `prometheus.io/*` handling of its own.
- The server's chart annotates its pods `prometheus.io/scrape`, `prometheus.io/port`, `prometheus.io/path` and
  `k8s.grafana.com/job` (`deploy/helm/andara/templates/_helpers.tpl`, `AW-INF-008`). So the chart's defaults would
  **not** scrape the servers, and the deleted cluster's values (another repo) must have remapped the names. The
  Kafka brokers carry the same four annotations, with `job` `andara-kafka`, and serve the Strimzi Metrics Reporter
  on `:9404` (`deploy/k8s/kafka/kafka.yaml`).
- node-exporter is scraped by the `hostMetrics` feature (`linuxHosts`), not by `clusterMetrics` (`UPGRADING.md`,
  "Cluster Metrics feature split").
- The server exports its log records over OTLP as well as writing them to stderr (`AW-SRV-024`,
  `server/telemetry/logexport.go`). `deploy/helm/values/dev.yaml` points that export, and the traces, at
  `k8s-monitoring-alloy-receiver.observability.svc:4317`.

## Options considered

### Option A — the chart collects everything; Fleet is unused

Annotation autodiscovery would scrape the brokers too, and the `postgresql` integration covers Postgres.

**Good:** one tool, one configuration, one place for the stamp and the keep-list. It is the smallest build.
**Bad:** it isn't Brian's direction. A Kafka scrape change becomes a chart upgrade, and nothing about collection is
visible in Fleet.
**Costs us:** the Fleet plumbing already decided for decision 15 (provider, tokens, `tf.py`) is left unused.

### Option B — the split, with each Fleet pipeline exporting to Grafana Cloud itself

The chart collects Kubernetes and exports. Each Fleet pipeline scrapes its service, stamps it, filters it and
remote-writes to the stack.

**Good:** the Fleet pipelines are self-contained, and a broken chart collector doesn't stop them.
**Bad:** the stamp, the keep-list and the export each exist twice: once in chart values, once in Alloy files in
Terraform. Decision 9's table and the `unknown` guard are written in two languages with two test suites. The
pipelines carry endpoints (`__PLACEHOLDER__` substitution from tfvars) and read the telemetry token through
`sys.env`.
**Costs us:** two places to keep in step, and a Fleet pipeline that holds a write credential path.

### Option C — the split, with Fleet pipelines forwarding into the chart (chosen)

The chart collects Kubernetes. One chart-defined collector, `fleet`, has no chart features and pulls the Fleet
pipelines. Those pipelines discover and scrape their service, then remote-write to the chart's in-cluster
receiver. They never send to Grafana Cloud. The receiver delivers to the same destinations as every chart
feature, so the destinations' rules (stamp, guard, keep) apply to both. Isolation doesn't matter, because the
hand-off crosses the network, not a component reference.

**Good:** decision 9's table, the guard, the metric keep-list and the export each exist **once**, in chart values,
for both owners. A Fleet pipeline carries no endpoint, no credential and no `__PLACEHOLDER__` token. A bad Fleet
pipeline can blind only the shared-service series, not `andara-dev`.
**Bad:** one more hop. The receiver collector becomes a dependency of Fleet's series, and of the server's traces,
which already depend on it. The hand-off is the item this ADR verifies first (`[verify 065]` below).
**Costs us:** a Fleet pipeline still needs a review: it runs on a collector that holds the Fleet read credential
and can read that pod's environment.

### Option D — keep decision 15 (everything in Fleet)

Refused by Brian (2026-10-21).

## Decision

**Option C.** Each part below is the contract `AW-INF-065` and `AW-INF-051` build to. Where `ADR-0012` decisions 13 and
15 and `ADR-0013` decision 2 say otherwise, this ADR wins (see *Supersedes*).

### 1. The split: one owner per source

The rule is **by namespace and by kind of data**. Kubernetes-level data about *any* pod (its state, its logs, its
node) and every workload outside `andara-shared` belong to the chart. A shared service's *own* metrics, from
inside `andara-shared`, belong to Fleet.

| Source | Owner | Chart feature or Fleet pipeline | Why this owner |
|---|---|---|---|
| kube-state-metrics, kubelet and cAdvisor | chart | `clusterMetrics`, with `telemetryServices.kube-state-metrics.deploy: true` | standard Kubernetes collection |
| node-exporter | not deployed | `telemetryServices.node-exporter.deploy: false`, no `hostMetrics` | no rule or dashboard panel reads a `node_*` series (`ADR-0012` decision 13: unread series are not shipped). The first reader enables `hostMetrics.linuxHosts`. This amends `ADR-0013` decision 2's "collectors, kube-state-metrics, node-exporter". |
| Andara servers and projectors (`andara-local`, `andara-dev`, `andara-ci`) | chart | `annotationAutodiscovery`, with `annotations.scrape: prometheus.io/scrape`, `annotations.metricsPortNumber: prometheus.io/port` and `annotations.metricsPath: prometheus.io/path` (`job` stays `k8s.grafana.com/job`), and an include-list, `namespaces`: `[andara-local, andara-dev, traefik]` on the box and `[andara-local, andara-ci, traefik]` in CI. CI's lifecycle job runs the server in `andara-local` on cluster `ci` (`.github/workflows/kind.yaml`, `NS=andara-local`), and `drills` uses `andara-ci` (AW-INF-061). The render assertion of §7 checks the CI list against the namespaces that workflow names. So `andara-shared` and CI's throwaway Kafka namespace, which has no fixed name, are never discovered. | the pods' existing annotations, remapped in values rather than changed in two charts; `job` must come out `andara-server` and `andara-projector-state` (`ADR-0012` decision 13), and a `make check` render asserts the remap |
| the server's traces (OTLP) | chart | `applicationObservability` on the `receiver` collector | the chart's OTLP receiver is the standard path; no Fleet value |
| pod logs, every namespace listed below | chart | `podLogsViaLoki`, `namespaces`: `[andara-local, andara-dev, andara-shared]` on the box, `[andara-local, andara-ci]` in CI | logs of a pod are Kubernetes-level data, including a broker's. **The server's logs are owned here, from stderr**, because stderr also holds what is written before the OTLP exporter starts (a boot failure). |
| the server's OTLP log records (`AW-SRV-024`) | dropped at the `receiver` | `applicationObservability.logs` with a filter that drops every record | The server sends logs and traces to one `otlp_endpoint`, and nothing switches only the logs off. Shipping both would double the server's log volume with duplicates. The receiver accepts the records, so the exporter reports no failure, and drops them. A server switch for the log export is an implementation story PM may write. Nothing waits on it. |
| cert-manager | chart | `integrations.cert-manager` | the chart has the integration |
| Traefik | chart | `annotationAutodiscovery` on Traefik's pod annotations | platform edge, standard scrape; `[verify 065]` that the pinned Traefik chart annotates its pods, else an `extraConfig` scrape on the same collector. *Amended 2026-10-21 (A2):* its pods carry `k8s.grafana.com/job: traefik`, so `job="traefik"`. |
| Kafka brokers (`andara-log`, the Strimzi Metrics Reporter on `:9404`) | **Fleet** | `shared_kafka` (A5) | Brian's direction: outside the chart's standard scope, changed without a chart upgrade |
| Postgres | **Fleet** | `shared_postgres` (later) | Brian's direction ("database observability"), even though the chart has a `postgresql` integration. Nothing is deployed until `AW-SRV-018` has a binary (`ADR-0013` decision 3); the story that deploys Postgres writes the pipeline. |
| Schema registry (Karapace), versitygw, Redis | **Fleet**, when shipped | none yet | nothing reads their series today (`ADR-0012` decision 13: unread series are not shipped), and Redis has no binary. The first rule or dashboard panel that reads one adds its pipeline in its own story. |
| the collectors' own metrics | chart | not shipped | nothing reads them; Fleet's inventory shows collector health |

The chart's `selfReporting` feature is **off**: it ships a series nothing reads.

**What is lost from decision 15:** "every scrape, relabel and export rule is visible in Grafana Cloud and managed in
this repo" now holds only for Fleet's sources. The chart's rules are still managed in this repo, but as Helm values
applied by `make platform-up`. They don't appear in Grafana Cloud's Fleet pages, and no daily job detects a hand
`helm upgrade`. The Terraform `drift` job never covered a Helm release, and still doesn't.

### 2. The chart: pin, values, installer

- **Chart:** `grafana/k8s-monitoring`, pinned at **`4.5.3`** in `deploy/platform/versions.yaml` (`ADR-0013` amendment
  18: the one source of a platform version). The chart's `alloy-operator` subchart installs the Alloy CRD.
  `platform-up`'s order must put it before the collectors (`[verify 065]`).
- **Release:** `k8s-monitoring` in namespace `observability`. That namespace is deliberately not `andara-*`, so the
  collectors' own pods never fall under the `unknown` guard's `namespace=~"andara-.*"`.
- **Values:** in `deploy/platform/collectors/`:
  - `values.yaml`: shared by every cluster;
  - `values-box.yaml`: the `fleet` collector, and the namespaces of the box's environments;
  - `values-ci.yaml`: no `fleet` collector, and CI's namespaces (`andara-local`, `andara-ci`).
- **Two sources** are committed beside the values, and the stamp and keep rules are rendered from them (§4, §5):
  - `environments.yaml`: decision 9's table;
  - `keep-list.yaml`: the chart's metric allow-list.
- **Installer:** `make platform-up` (`PROFILE=box` for the box, `PROFILE=ci` for CI), as `ADR-0013` decision 2 already
  says for the collectors. The chart's values are `make`'s, never Terraform's (`ADR-0012` decision 13, option (c)
  refused).
- **Collectors** (names fixed, because the Fleet matcher, the receiver's Service and the tests use them):

| Collector | Workload | Carries |
|---|---|---|
| `metrics` | Deployment, 1 replica | `clusterMetrics` (with kube-state-metrics), `annotationAutodiscovery`, `integrations` |
| `logs` | DaemonSet | `podLogsViaLoki` |
| `receiver` | Deployment, 1 replica, with its `extraService` | `applicationObservability` (OTLP in) and `prometheusMetricsReceiver` (remote-write in, for Fleet) |
| `fleet` (box only) | Deployment, 1 replica | no chart feature; `remoteConfig.enabled: true` |

- **The receiver's address is fixed:** `extraService.fullname: k8s-monitoring-alloy-receiver`. That is the name the
  committed server values already dial (`deploy/helm/values/dev.yaml`, `prod.yaml`, the chart README). The chart's
  default would render `k8s-monitoring-alloy`. It exposes:
  - OTLP gRPC on `4317`, the server's `otlp_endpoint`, unchanged;
  - remote-write on `9090`, `prometheusMetricsReceiver`'s default, the Fleet pipelines' one URL:
    `http://k8s-monitoring-alloy-receiver.observability.svc:9090/api/v1/metrics/write`.

  `make check` asserts both against the render (`[verify 065]` item 1: the path, and that `extraService` carries
  both ports).

### 3. Fleet on its own collector

- **Only `fleet` registers with Fleet Management.** Its attributes are `cluster` (`andara`) and `stack`
  (`solo7dev`), set in `remoteConfig.extraAttributes`. The chart also sends its `presets` attribute, and no matcher
  reads it. `environment` is still not an attribute (`ADR-0012` decision 15). The chart features don't run on
  `fleet`, so a bad pipeline can crash or starve only `fleet`.
- **Fleet pipelines** stay `grafana_fleet_management_pipeline` resources in the `solo7dev` root, matched on `stack`
  alone (decision 15 unchanged on this point). Their sources are `deploy/terraform/grafana/pipelines/<name>.alloy`,
  read with one literal `file()` each. There is no `replace()` and no `__PLACEHOLDER__` token: a pipeline names no
  endpoint. Its only output is `prometheus.remote_write` to the receiver URL in §2, written literally in the
  `.alloy` file. `make check` fails on any other `remote_write` URL, any
  `otelcol.exporter.*`, any `loki.write`, and any `sys.env` call outside the allow-list in the pipeline-safety bullet.
- **A Fleet pipeline sets `namespace` and `job`** from discovery (`__meta_kubernetes_namespace`, the pod's
  `k8s.grafana.com/job` annotation), because the chart's stamp keys on `namespace` (§4).
- **The pipelines now:** `shared_kafka`, which discovers pods in `andara-shared` that carry `k8s.grafana.com/job:
  andara-kafka`, scrapes `:9404`, keeps its allow-list (§5) and forwards. `shared_postgres` comes with Postgres.
- **Credentials.** The telemetry token's scopes are unchanged (`ADR-0012` decision 4: `metrics:write logs:write
  traces:write fleet-management:read`). There is one token, in the Secret **`grafana-telemetry`** in `observability`.
  `AW-INF-065` creates it from `.local/box.env`. The chart's destinations read it, and so does `fleet`'s
  `remoteConfig` (`secret.create: false`, `name: grafana-telemetry`). `fleet` needs only the
  `fleet-management:read` scope, and the token holds more. Splitting it would add a fifteenth secret for a
  collector that runs our own reviewed pipelines, so it isn't split. The Fleet endpoints come from `solo7dev`'s
  `terraform.tfvars` (`fleet_url`, `fleet_user`), as before.
- **Pipeline safety** (decision 15's Consequence, narrowed): the Fleet apply token is still configuration execution
  on `fleet`. `fleet`'s environment no longer holds the token (A6: its `GCLOUD_RW_API_KEY` is set empty), but
  `make check` still refuses
  `sys.env` in a pipeline unless an entry in `deploy/terraform/grafana/pipelines/env-allow.txt` names that
  variable (empty today). Postgres's monitoring password will be the first entry, in its own story.

### 4. The `environment` stamp: one table, rendered once per signal, at the destinations

- **The one place:** the chart's destinations. Every series, log line and span, from a chart feature or forwarded
  by Fleet, reaches Grafana Cloud through one of three destinations, and nothing else exports:
  - `solo7dev-metrics` (`prometheus`), with the table in `metricProcessingRules`;
  - `solo7dev-logs` (`loki`), with the table in `logProcessingStages`;
  - `solo7dev-traces` (`otlp`, traces only), with the table in `processors.transform` on `k8s.namespace.name`.
- **One source:** `deploy/platform/collectors/environments.yaml` holds decision 9's table, unchanged:
  - `andara-local` and `andara-ci` map to `local`;
  - `andara-dev` maps to `dev`;
  - `andara-staging` maps to `staging`;
  - `andara-prod` maps to `prod`.

  The three rule blocks are rendered from it. Whether they are rendered at install or committed with a staleness
  check is `AW-INF-065`'s choice. `make check` asserts that all three rendered blocks encode the same table.
  Decision 9's "the only mapping" now lives in this file instead of the `stamp` pipeline.
- **The guard's new home:** each block ends by setting `environment="unknown"` where none was set. That is decision
  15's guard, moved from the `stamp` pipeline to the destinations.
- **Values, per source namespace:**

| Series from | `environment` |
|---|---|
| `andara-local`, `andara-ci` | `local` |
| `andara-dev` | `dev` |
| `andara-shared` (Kafka through Fleet; the brokers' pod logs through the chart. *Amended 2026-10-21 (A1):* its kube-state-metrics rows aren't kept) | `unknown`. A shared service serves both environments, so a label can't name one. `ADR-0013` amendment 15 stands: the first alert on a shared service reopens decision 9, and a rule derives the environment from the topic prefix, the consumer group or the bucket. `observe-check KEEP_LIST=1` still excludes `namespace="andara-shared"` from the guard. |
| any other namespace (`kube-system`, `observability`, `cert-manager`, Traefik's) | `unknown`; `certmanager_*` and `traefik_*` remain `PLATFORM_SERIES`, whose environment the rules derive (`ADR-0012` decision 1) |

- **`cluster`:** the destinations' `clusterLabels: [cluster]` with `cluster.name: andara`. The chart's default also
  sets `k8s.cluster.name`, which is dropped, because no rule reads it.
- **CI** (`values-ci.yaml`) sends to the same destinations, with the same rendered blocks, when it holds the token
  (§7), with `cluster="ci"` (A3). `andara-ci` maps to
  `local` through the table, so CI needs no override: decision 15's "`environment="local"` set by an override"
  lapses.

### 5. The keep-list: enforced at each owner's last step, read from two named files

- **Chart:** `deploy/platform/collectors/keep-list.yaml` is the allow-list, as metric names, each with the rule,
  dashboard panel or drill that reads it. It is rendered into a final `keep` rule on `solo7dev-metrics`, after the
  stamp. Because the receiver delivers there too, the rule applies to forwarded Fleet series as well.
  *Amended 2026-10-21 (A1):* a `kube_` name is kept only from the namespaces `environments.yaml` maps.
  - Each feature's own `metricsTuning.useDefaultAllowList` is **`false`**, so the destination is the **one** filter.
    With the defaults on, kube-state-metrics' list silently drops two keep-list series, which is how `AW-INF-009`'s
    keep-list check failed on 4.5.0.
- **Fleet:** each pipeline also keeps only its own names before forwarding (`shared_kafka` keeps `kafka_*` names
  and `up`), so an unfiltered broker scrape never crosses the hop. Its names must also be in `keep-list.yaml`,
  marked `owner: fleet`. The destination keeps what it is told to keep, so a name kept by a pipeline but missing
  from `keep-list.yaml` is dropped at the destination and fails the test below.
- **Ownership of a name follows its prefix:** `kafka_` and `pg_` belong to Fleet; every other name belongs to the
  chart. **`up` is shared**: every scrape produces it, so it is in `keep-list.yaml` once, kept for all jobs, and
  each Fleet pipeline also keeps its own job's `up`. `make check` asserts all of these:
  - every metric name in `alerts.yaml` and the dashboard is in `keep-list.yaml`;
  - each `owner: fleet` name is kept by the pipeline that owns its prefix;
  - the chart's discovery reaches no Fleet-owned source. A static check can't see what an annotated pod will
    expose, so this is asserted on the render: `annotationAutodiscovery.namespaces` is the include-list of §1,
    which has no `andara-shared`.
- **What reads the files:**
  - `scripts/observe_keep_list.py`'s offline half (the fault-only names) reads `keep-list.yaml`;
  - its `KEEP_PIPELINE` constant changes from `pipelines/keep.alloy` to that file, in `AW-INF-065` (`AW-INF-075` is
    merged and is not reopened for it);
  - `KEEP_LIST=1`'s live half queries the stack, as before;
  - the keep-list tests in `make check` (every name in `alerts.yaml` and the dashboard is kept) are `AW-INF-065`'s,
    moved from `AW-INF-051`, and they read `keep-list.yaml`. `AW-INF-047`'s parity test (the rendered rules against
    `alerts.yaml`) and `AW-INF-049`'s dashboard don't read a keep-list and don't change.
- **A rule change is now also a collector change.** A rule that names a new series needs that name in
  `keep-list.yaml` (the test enforces it), and then `make platform-up`. Until the platform is reconciled, the rule
  sees no data, and `no_data_state = OK` keeps it silent. The rule's PR says so, and `KEEP_LIST=1` after
  `platform-up` is the check.

### 6. The cost of shipping the shared services

Kafka series are shipped from now on. Today nothing of the brokers is shipped (`AW-INF-051`'s Observability section).
`AW-INF-039` (in progress) consumes under-replicated partitions and the ISR shrink rate, so those series now have a
reader. The `shared_kafka` allow-list starts as:

| Series (Strimzi Metrics Reporter names, as served on `:9404`; A5) | Per broker | Brokers | Series |
|---|---|---|---|
| `kafka_server_replicamanager_underreplicatedpartitions` | 1 | 3 | 3 |
| `kafka_server_replicamanager_underminisrpartitioncount`, `…_atminisrpartitioncount` | 2 | 3 | 6 |
| `kafka_server_replicamanager_isrshrinkspersec_total`, `…_isrexpandspersec_total` | 2 | 3 | 6 |
| `kafka_controller_kafkacontroller_offlinepartitionscount`, `…_activecontrollercount` (combined KRaft nodes) | 2 | 3 | 6 |
| `up{job="andara-kafka"}` | 1 | 3 | 3 |
| **Kafka total** | | | **24** (bound: 50) |
| Postgres, when deployed (`pg_up`, `pg_stat_database_numbackends` and `pg_settings_max_connections` for the two databases) | | | about 10 (bound: 30) |

The bounds allow for label variants (`instance`). Per-topic series
(`kafka_server_brokertopicmetrics_*{topic}`) are **not** kept: across both environments' topics on 3 brokers they
come to about 1,000 series, and no reader exists. The reporter's own `allowList` in `kafka.yaml` is unchanged, since
it bounds the scrape, not the shipment.

**Against the limit:** `solo7dev` is on Grafana Cloud Pro, which includes **20,000 active metric series** and 50 GB
of logs a month (A6; this said the free tier's 10,000 until `AW-INF-065` read the stack's plan). The record moves
from `AW-INF-051` to `AW-INF-065`, its one owner. The shared
services add at most 80. The chart side is the same set decision 15 planned, so the split moves no series.
`AW-INF-065` records the total per `environment` beside the limit (with CI's share, `cluster="ci"`, stated apart; amendment A3), the record that `ADR-0012`'s revisit trigger
("`solo7dev` exceeds its plan's active-series allowance twice in a month", A6) measures. Logs: shipping the brokers' pod logs is new volume against the plan's log allowance, and
`AW-INF-065` records the first week's ingest.

### 7. CI's `andara-ci`

- **What runs:**
  - CI's kind job installs the same chart at the same pin with `values.yaml` and `values-ci.yaml` (`make platform-up
    PROFILE=ci`), when the job holds `solo7dev`'s telemetry token. Its series carry `cluster="ci"`, which
    `AndaraServerUnavailable`'s guard excludes (A3).
  - Without the token (a fork), it installs anyway with the `grafana-telemetry` Secret empty, so the collectors
    ship nothing. Only the Grafana Cloud assertions are skipped, with a notice (`ADR-0012` decision 14, `ADR-0013`'s
    Agreement). That run is where `[verify 065]` item 6 is observed.
  - There is no `fleet` collector and no Fleet registration (decision 15's reason stands: ephemeral collectors would
    pollute the inventory).
  - The `drills` job's throwaway Kafka is not scraped.
- **Parity is proven offline.** `make check` runs `helm template` with each profile and asserts:
  - the rendered stamp, guard and keep blocks of all three destinations are byte-identical between `box` and `ci`;
  - the profiles differ only in the keys `values-ci.yaml` and `values-box.yaml` are allowed to set (the collector
    list, the namespace lists, `cluster.name`).

  This replaces `AW-INF-051` AC-10's `replace()`-versus-script byte test, which has nothing left to compare.

### 8. Terraform's scope

- **What remains Terraform's** (per stack root, `solo7dev` now and `solo7` in `AW-INF-078`):
  - the Fleet pipelines (`shared_kafka`, later `shared_postgres` and any service pipeline);
  - the Fleet provider, its tokens and `tf.py`'s composition of them, from `AW-INF-051` scope items 1 and 2;
  - decision 15's `tf_policy.py` rules for pipeline sources. *Corrected 2026-10-21 (A4):* the original said
    "minus the `replace()`/`__PLACEHOLDER__` allowance". `tf_policy.py` never had one, and it gains none;
  - everything else in `ADR-0012` (rules, contact points, policy trees, dashboards, IRM).
- **Not Terraform's:** the chart, its values, the stamp, the guard, the chart's keep-list, the collectors and the
  telemetry Secret. Those belong to `make platform-up`.

### 9. The 12 rules in `alerts.yaml`

Every series the 12 rules read had decision 15's planned `scrape` pipeline as its owner, and now has the chart. Nothing
had shipped from either, so no live series changes. **No series name changes.** The labels the rules select on keep
their meaning:
- `job` comes from `k8s.grafana.com/job`;
- `namespace` is the pod's;
- `exported_namespace` and `name` come from cert-manager (`[verify 065]` item 3);
- `router` comes from Traefik;
- `environment` comes from §4's stamp.

The only label added is `cluster`.

| Rule | Reads | Owner and feature |
|---|---|---|
| `AndaraServerUnavailable` | `up{job="andara-server"}`; guard `count(up{namespace!=""}) > 0` | chart, `annotationAutodiscovery`. **The guard changes meaning; see below.** |
| `AndaraServerCrashLooping` | `kube_pod_container_status_restarts_total` | chart, `clusterMetrics` |
| `RecoveryStateMismatch` | `kube_pod_container_status_last_terminated_exitcode` ‡, `kube_pod_container_status_ready`, `andara_recovery_state_hash_match` ‡ | chart: `clusterMetrics` (default allow-list off, §5) and `annotationAutodiscovery` |
| `SimulationLagging`, `SnapshotStale`, `StateProjectorDiverged`, `ProjectionStale`, `ContentLoadFailing`, `SessionsDroppingAtRate` | `andara_*` | chart, `annotationAutodiscovery` |
| `StateProjectorDown` | `up{job="andara-projector-state"}`, `kube_deployment_spec_replicas` | chart, `annotationAutodiscovery` and `clusterMetrics` |
| `CertificateExpiringSoon` | `certmanager_certificate_expiration_timestamp_seconds` | chart, `integrations.cert-manager` |
| `IngressErrorRateHigh` | `traefik_router_requests_total` | chart, `annotationAutodiscovery` in namespace `traefik` (`[verify 065]` item 4) |

**`AndaraServerUnavailable`'s guard.** The guard reads "telemetry is arriving at all" as `count(up{namespace!=""}) >
0`. Fleet now ships `up{job="andara-kafka", namespace="andara-shared"}` through `receiver`, a path that doesn't pass
through `metrics`. So a dead `metrics` collector, with `receiver` and `fleet` alive, would satisfy the guard. The
`dev` absence line would then page "server unavailable" for what is a collector fault. The guard becomes
`count(up{namespace!="", namespace!="andara-shared"}) > 0`, so it reads the chart's scrape alone, as it did when
the guard was written. *Amended 2026-10-21 (A3):* the guard is `count(up{cluster!="ci", namespace!="",
namespace!="andara-shared"}) > 0`, so it also ignores CI's series. That is an amendment to `AW-INF-047`'s rendered rule and to `alerts.yaml`. Until a
telemetry-freshness rule exists, a dead `metrics` collector is silent rather than misattributed (Consequences).

### 10. Supersedes, and the ripple

**In `ADR-0012`**, superseded by this ADR, with a dated annotation at each place:

- **Decision 1** (added 2026-10-21 with A3): the guard sentence "stays exactly as it is". §9 and A3 change its
  expression and its meaning.

- **Decision 15:**
  - "the whole collection pipeline lives in Fleet Management";
  - the bare-collector item and its `[verify]`;
  - the six pipelines;
  - `stamp` as the guard's home;
  - CI's static-config parity.
- **Decision 13:**
  - "the collection pipelines are files in this repo (decision 15), rendered with the stack endpoints";
  - "the allow-list is read from the in-repo pipeline source";
  - order step 1's "`AW-INF-051`'s pipelines are applied" (now: `shared_kafka` is applied; the chart is installed by
    the rebuild).
- **Decision 9:**
  - "the Fleet `stamp` pipeline derives `environment`" now reads "the chart's destinations derive `environment`
    (`ADR-0014` §4)";
  - the table itself is unchanged.

Kept unchanged: decision 15's IRM half, its credentials, its `stack`-only matcher and its `tf_policy.py` fixtures
for `file()` and `templatefile`.

**In `ADR-0013`**, decision 2's table row "Collectors … the pipelines are Fleet's" now reads "the chart's features
collect, and Fleet pipelines collect `andara-shared`'s services (`ADR-0014`)". "One `k8s-monitoring` release for both
environments" holds: it is the `k8s-monitoring` release in `observability`. node-exporter, in the same row, is not
deployed (§1).

**The glossary's** *Alloy*, *Fleet Management* and *Keep-list* entries are rewritten to match.

**Stories**, amended after acceptance (`AW-INF-079` AC-8):

| Story | What changes | New story? |
|---|---|---|
| `AW-INF-065` (draft) | **Gains:** the chart's features and the four collectors (§2), the autodiscovery remap and include-lists (§1), the receiver's fixed Service and ports, the OTLP-log drop filter, `environments.yaml` and `keep-list.yaml` with their rendered rules, the keep-list and parity tests moved from `AW-INF-051`, `KEEP_PIPELINE`, the log stream-label allow-list (also from `AW-INF-051`: `environment`, `namespace`, `cluster`, `job`, `container`), the active-series and log records (from `AW-INF-051`), AC-2 (`environment` on a series) and AC-4 (`unknown` outside the table) moved from `AW-INF-051`, and `AW-INF-009`'s inherited line (the two kube-state-metrics series kept, now by the chart). **Changes:** AC-3 ("each collector registers") becomes "only `fleet` registers"; AC-7's "reachable by the exporters" becomes the include-lists; the Secret `grafana-telemetry` holds the token under one key and the endpoint usernames, which aren't secret, under one key each (`prom`, `loki`, `tempo`, `fleet`; the chart's `usernameKey` defaults to `username` and must be set per destination and on `fleet`'s `remoteConfig` (`usernameKey: fleet`); every reader takes the token as `passwordKey: token`, except `solo7dev-traces`, which reads it through `passwordFrom` with a placeholder when it is empty, A6); node-exporter is not installed; the bare-collector `[verify]` is dropped. It **stays blocked by `AW-INF-051`**, because `[verify 065]` item 1 and §5's `owner: fleet` test need `shared_kafka`. Its `[verify]` items are the ones below. Likely `L`. | PM's call: a split such as **065a** (install, Secret, `fleet` registration, the receiver) and **065b** (stamp, guard, keep-list and their tests) |
| `AW-INF-051` (ready) | Shrinks to the Fleet plumbing (provider, tokens, `tf.py`, the `pipelines/` safety checks without `replace()`) plus `shared_kafka`, and its series spelling `[verify 051]` (resolved, A5). Loses `scrape`, `stamp`, `keep`, `logs`, `traces` and `export`, its keep-list and `replace()` tests, ACs 2, 4 and 10, the active-series record, and the inherited `AW-INF-009` line, all of which move to `AW-INF-065`. Its Observability line "broker … series are **not shipped**" reverses: `shared_kafka` ships §6's set. Probably `S`. | no |
| `AW-INF-047` (ready) | The rules are unchanged except `AndaraServerUnavailable`'s guard (§9: `namespace!="andara-shared"`; *amended 2026-10-21 (A3):* also `cluster!="ci"`). "keeps the `environment` it arrived with: `unknown` once `AW-INF-051`'s `stamp` runs" now reads "once `AW-INF-065`'s destinations stamp", and its blocked-by link moves from `AW-INF-051` to `AW-INF-065`. | no |
| `AW-INF-049` (ready) | No contract change. Its live data already waits on `AW-INF-065`. | no |
| `AW-INF-061` (ready) | Its out-of-scope line "the same pipeline files as static config with `environment="local"` (`AW-INF-065`)" becomes "the chart with `values-ci.yaml` (`platform-up PROFILE=ci`), no override". The throwaway Kafka's namespace is outside the CI include-list, so it isn't scraped. | no |
| `AW-INF-075` (review, merged) | none to its contract; its constant moves in `AW-INF-065` | no |
| `AW-INF-070` (ready) | Step 4(i-b) applies `shared_kafka` only. Step 4(iii-b) becomes "the chart is installed and `fleet` has registered and pulled `shared_kafka`". | no |
| `AW-INF-078` (parked) | `solo7`'s collection is the same chart with a prod values profile on prod's cluster, plus `solo7`'s copy of the pipelines | no |
| Postgres's deployment story (`ADR-0013` "later") | writes `shared_postgres` and its `env-allow.txt` entry | written when `AW-SRV-018` has a binary |

**`[verify 065]`**, observed in `AW-INF-065` against the pinned 4.5.3:

1. The receiver's Service (§2) carries ports `4317` and `9090`, and the remote-write path is
   `/api/v1/metrics/write`. A `prometheus.remote_write` from a `fleet` pipeline to that URL arrives, and `solo7dev-metrics`' `metricProcessingRules` apply to it. A forwarded series carries
   `namespace="andara-shared"` and `environment="unknown"`, and an unkept `kafka_` name is dropped. **If it
   fails,** fall back to Option B for Fleet's sources only, with the table and the guard written a second time in
   the pipeline and a test asserting the two encode the same table. That fallback is an amendment here, not a
   silent change.
2. The `alloy-operator` CRD is installed before the collectors, in `platform-up`'s order.
3. `integrations.cert-manager` keeps `exported_namespace` and `name` on `certmanager_certificate_expiration_timestamp_seconds`
   (the rule's and the keep-list check's selector).
4. Traefik's pods in namespace `traefik` are discovered by annotation (§1's Traefik row), and
   `traefik_router_requests_total` arrives with `router` intact.
5. The `otlp` destination's transform sees `k8s.namespace.name` on the server's spans.
6. With the telemetry Secret empty, the chart installs, the collectors are `Ready` and ship nothing (`ADR-0012`
   decision 14), observed in CI's run without the token (§7).
7. The annotation remap of §1 yields `up{job="andara-server"}` and `up{job="andara-projector-state"}` with
   `namespace` set, from `andara-local` and `andara-dev`.
8. The receiver accepts the server's OTLP log records and drops them: the server's log-export
   counter `andara_log_export_dropped_total` stays at 0 and `andara_log_export_queue_size` does not grow, both
   read from the server pod's `/metrics`, because neither is on the keep-list (`server/telemetry/logexport.go`).
   No OTLP-origin log line reaches `solo7dev`.

## Consequences

- **Two configuration homes for collection**, not one: Helm values for Kubernetes (applied by `make platform-up`) and
  Fleet pipelines for the shared services (applied by Terraform). An operator looking for "why is this series
  missing" has two places to look. The split rule (§1: by namespace and kind) is the map.
- **Chart collection is invisible in Grafana Cloud and has no drift job.** A hand `helm upgrade` stays until the
  next `make platform-up`. Decision 15 bought visibility in Fleet's pages; for the chart's sources that is given up.
- **A rule that needs a new series needs a platform reconcile** (§5). Before it, the rule is silent, not failing.
- **The `receiver` collector is a single point** for the server's traces and every Fleet series. If it dies, both
  stop, and nothing alerts on it (`AW-INF-065`'s recorded blind spot, "a pipeline that ships nothing looks like a
  healthy `dev`", is narrowed but not closed).
  - Narrowed: a bad Fleet pipeline no longer blinds `andara-dev`, because the chart's metrics and logs don't pass
    through `fleet`.
  - Not closed: a dead `metrics` collector or an empty token still looks healthy. With §9's guard change, a dead
    `metrics` collector reads as no data, which is silent, rather than as a misattributed "server unavailable" page.
  - The telemetry-freshness rule `AW-INF-047`'s set is owed stays the fix.
- **Broker series reach `solo7dev`**, about 24 now. Shared-service series stay `environment="unknown"`, so a rule on
  them can't route by environment until decision 9 is reopened (`ADR-0013` amendment 15).
- **Upgrading the chart is a platform change with a wide reach.** 4.x renamed collectors once already
  (`UPGRADING.md`: named instances became a map). The pin and the offline render tests (§7) catch a values break
  at `make check`, not at the rebuild.
- **The `__PLACEHOLDER__` and `replace()` machinery decision 15 designed is not built.** `tf_policy.py` never
  had an allowance for it and gains none (corrected 2026-10-21, A4).
- **Foreclosed:** a Fleet pipeline that exports to Grafana Cloud directly, unless the fallback in `[verify 065]` item 1
  is taken by amendment; and per-feature `metricsTuning` as a second filter.

## Revisit when

- `[verify 065]` item 1 fails: amend to the fallback (Option B for Fleet's sources), or move Kafka to the chart (Option A).
- Fleet carries more than three service pipelines, or one needs a feature the chart has as an integration: re-weigh
  Option A for that service.
- The chart's collection drifts from the repo twice (a hand `helm upgrade` found after the fact): add a scheduled
  `helm diff` against the values, or move the chart's collection into Fleet after all.
- `solo7dev` exceeds its plan's active-series allowance (`ADR-0012`'s trigger, reworded by A6), or the brokers' logs
  exceed the plan's log allowance in a month: cut the shared services' logs to `warn` and above first.
- The first alert on a shared-service series: reopen `ADR-0012` decision 9 for its environment value (`ADR-0013`
  amendment 15).
- CI's `local` series mask or trip a box `local` alert in practice (A3): give CI an environment of its own, by an
  amendment to `ADR-0012` decision 9, or a stack of its own.

## Amendments

**2026-10-21, SRE's observability review (`AW-INF-079`).** SRE reviewed the ADR as merged in #600, and three of
its findings are decided here. A4 corrects a false statement.

- **A1. `kube_*` series are kept only from the environments' namespaces.** Without this, §5's keep rule filters by
  name alone. So `kube_pod_container_status_restarts_total`, `…_ready`, `…_last_terminated_exitcode` and
  `kube_deployment_spec_replicas` would ship for every pod in `kube-system`, `observability`, `cert-manager`,
  `traefik`, `argocd`, `strimzi` and `andara-shared`. Every rule that reads them selects an Andara workload
  (`container="server", pod=~"andara-[0-9]+"`, or `deployment="andara-projector-state"`), and decision 13 says
  unread series aren't shipped.
  - **The rule:** the `keep` rule on `solo7dev-metrics` keeps a name with the prefix `kube_` only where
    `namespace` is a key of `environments.yaml` (today `andara-local`, `andara-ci`, `andara-dev`, `andara-staging`,
    `andara-prod`). The regex is rendered from that file, so the table still lives in one place.
  - **The test:** §7's render test asserts the regex equals `environments.yaml`'s keys.
  - **Effect:** `andara-shared`'s brokers get no `kube_*` rows. Their pod logs are still shipped. The first rule on
    a broker pod's state widens the prefix rule in its own change.
- **A2. Traefik's `job` is `traefik`.** Traefik's pods carry no `k8s.grafana.com/job`, so autodiscovery would give
  them the chart's default job. §5's `owner` test and `KEEP_LIST=1` key some checks by job. The pod annotation
  `k8s.grafana.com/job: traefik` is set in `deploy/k8s/traefik/values.yaml`'s `deployment.podAnnotations`
  (`AW-INF-065`). If `[verify 065]` item 4 takes the `extraConfig` fallback, that scrape sets `job="traefik"` too.
  `IngressErrorRateHigh` doesn't select on `job`, so no rule changes.
- **A3. CI keeps shipping, and the guard ignores it.** With the token, CI ships `up` from `andara-local` and
  `andara-ci` on cluster `ci` into `solo7dev`. §9's guard doesn't select on `cluster`, so during any CI run a dead
  box `metrics` collector would still satisfy it, and `andara-dev`'s absence line would post "server unavailable",
  the misattribution §9 removes for Fleet.
  - **The guard becomes** `count(up{cluster!="ci", namespace!="", namespace!="andara-shared"}) > 0`. It is the same
    in both stacks: `solo7` has no CI, and its prod cluster's name isn't `ci`. This amends `AW-INF-047`'s guard
    again, beside §9's change. `values-ci.yaml` sets `cluster.name: ci`, and §7's render test asserts it.
  - **CI's series still reach `solo7dev`, as decided:** `ADR-0013` decision 8 keeps the `drills` rule-state reads
    and `stack-recover-mismatch` "where the `solo7dev` secrets exist" (`AW-INF-061`, AW-INF-064), and `ADR-0012`
    decision 10 counts CI as a real backend. "CI ships nothing" was weighed and refused, because it would void those.
  - **The known overlap, accepted:** CI's series carry `environment="local"`, the same as the box's
    `andara-local` (`ADR-0013` made CI a `local`). The rules aggregate by `namespace`, and by `(environment,
    namespace)` after `AW-INF-047`, so during a CI run CI's `andara-local` and the box's can mask each other in a
    `local` alert, in either direction. `local` is a developer install that pages nobody. Revisit-when names the
    trigger. The drills are unaffected today: CI's drills run in `andara-ci`, and CI's lifecycle job (which uses
    `andara-local`) reads nothing by selector. But `ADR-0012` decision 1's "`local` has two sources (`andara-local`,
    `andara-ci`)" holds only per cluster. A box `ENV=local` drill that reads `namespace="andara-local"` adds
    `cluster="andara"` (AW-INF-064's selectors).
  - **Churn:** each CI run brings new `pod` and `instance` values, about 100 series a run by the kube row counts.
    AW-INF-065's active-series record states CI's share (`cluster="ci"`) apart from the box's.
- **A4. Correction to §8 and the Consequences.** They said `tf_policy.py`'s `replace()`/`__PLACEHOLDER__` allowance
  is removed. `scripts/tf_policy.py` has no such allowance (its only `replace` is a path separator), so nothing is
  removed. It gains none.

**Not decided here:** SRE's note 4. The `receiver` is a single point, and no rule watches it (Consequences). The
telemetry-freshness rule is the fix. It belongs with `AW-INF-047`'s rule set and needs its own SLO line, and SRE
raises it with PM.

**Ripple:**
- `AW-INF-065`: A1's rule and test; A2's annotation; A3's `cluster.name: ci` render assertion and the CI share in
  the active-series record.
- `AW-INF-047`: A3's guard, with a promtool case in which only `cluster="ci"` series exist.
- `ADR-0012` decision 1: the guard sentence, annotated.
- AW-INF-064: its box `ENV=local` selectors add `cluster="andara"`.
- `AW-INF-061`: no change.

**2026-10-23, `AW-INF-051`'s `[verify 051]` and its first apply.** Two names in this ADR were wrong against
the live systems. Both are corrected in place above, so the ADR names what is applied.

- **A5. Pipeline names are Alloy identifiers, and the ISR rates carry `persec`.**
  - **The pipeline name.** Fleet refuses `shared-kafka`: `invalid_argument: pipeline name must be valid alloy
    identifier` (terraform run 39953398767, which created nothing). The pipeline is `shared_kafka` (created by run
    39958791748), and Postgres's will be `shared_postgres`. **The rule:** a Fleet pipeline's name equals its
    source's file name and matches `[a-z_][a-z0-9_]*`, which `make pipelines-check` enforces. The mock provider and
    `alloy validate` don't check names, so the check is the only guard before the API.
  - **The ISR rates.** The Strimzi Metrics Reporter serves `kafka_server_replicamanager_isrshrinkspersec_total`
    and `…_isrexpandspersec_total` (counters), not `…_isrshrinks_total` and `…_isrexpands_total` (SRE read
    `:9404` from `quay.io/strimzi/kafka:1.2.0-kafka-4.3.1` with `kafka.yaml`'s allow-list, 2026-10-22). The other
    five names in §6 match. The count is unchanged at 24. The live read from a broker in `andara-shared` is
    `AW-INF-051` AC-12's.

**Ripple (A5):** `AW-INF-051`'s body; `AW-INF-065` (AC-7, AC-9, its scope and out-of-scope lines, and the
keep-list names); `AW-INF-070` (step 4's pipeline); `AW-INF-039`, whose rule reads the ISR shrink rate by the
`persec_total` name when it is written; `ADR-0012` decision 1's annotation. `AW-INF-078` names neither.

**2026-10-26, `AW-INF-065`'s build and §8.** SRE found five facts on the live systems while building the
collectors. They are decided here, and §3, §6, §10 and Revisit-when are corrected in place.

- **A6. What the first install found.**
  - **Grafana's default Fleet pipelines bypass the destinations.** `solo7dev` holds pipelines that Grafana made,
    which Terraform doesn't manage. Two of them match `fleet`: `self-monitoring-metrics`
    (`collector.os=~".*"`) and `self-monitoring-logs-kubernetes` (`platform="kubernetes"`). Both write straight to
    the stack with `sys.env("GCLOUD_RW_API_KEY")`, and the chart wires that variable to the Secret's `token` on any
    remote-config collector. For about 8 minutes on 2026-10-25, 44 `alloy_*` names reached `solo7dev` with no stamp
    or keep rule. **The rule:** `fleet`'s `GCLOUD_RW_API_KEY` is set to `""` (`values-box.yaml`), so no pipeline
    can authenticate from the environment. This retires §3's "`fleet`'s environment holds the token". **And:**
    Grafana's default pipelines are disabled as code in each stack's root, so `fleet` stops sending a 401 every
    interval. That needs Terraform to adopt resources Grafana created, so it is its own `lane: sre` story (`solo7`'s
    half is `AW-INF-078`'s). Until then the 401s are harmless: they carry no token, and `{__name__=~"alloy_.+"}`
    in `solo7dev` returns nothing (`AW-INF-065` §8).
  - **Traces read the token through `passwordFrom`.** Alloy's `otelcol.auth.basic` refuses an empty password. So
    with the token unset (decision 14's run without shipping), a fresh `receiver` never loads and is never Ready.
    `solo7dev-traces` reads the Secret's `token` through `passwordFrom`, as
    `coalesce(convert.nonsensitive(…data["token"]), "unset")`. With no token, traces send the placeholder and get a
    401, like the other destinations' empty password. The token still comes from the one Secret key, and Alloy's
    API shows it as `(secret)`. `prometheus` and `loki` basic auth accept an empty password and keep
    `passwordKey: token`.
  - **Traces use gRPC to `<tempo host>:443`.** The chart refuses HTTP for Grafana Cloud Traces, so the host is
    taken from `solo7dev`'s `tempo_url`.
  - **Loki adds `service_name` at ingest.** Streams in `solo7dev` carry a sixth label, which Grafana Cloud's
    service-name discovery adds after the collector's `label_keep`. It is bounded by container names. The
    stream-label allow-list (§4) is the collector's, and it holds there. Runbook selectors still may not name
    `service_name`, because it isn't ours and its value isn't contracted.
  - **The plan is Pro, not the free tier.** `solo7dev` is on Grafana Cloud Pro, on the owner's employee account,
    so overages aren't billed today. It includes 20,000 active series and 50 GB each of logs and traces, shared
    with the account's other products (the owner, 2026-10-26; `docs/runbooks/grafana-credentials.md`). The owner's
    rule is not to go far past the allowance. §6 and Revisit-when now read "the plan's allowance", and so do
    `ADR-0012`'s retention note and its revisit trigger (annotated). The first measured count was 41 active series,
    0.2%.

**Ripple (A6):** `AW-INF-065`'s Definition of done (Pro's allowance, not the free tier's 10,000) and AC-3 (a
replaced `fleet` pod's leftover registration isn't counted); `ADR-0012`'s retention note and its revisit trigger;
PM, for the default-pipelines story; `AW-INF-078`, for `solo7`'s default pipelines and `fleet` setting.
