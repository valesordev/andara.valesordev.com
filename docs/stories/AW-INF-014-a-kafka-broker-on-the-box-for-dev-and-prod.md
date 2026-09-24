---
id: AW-INF-014
title: A Kafka broker on the box for dev and prod
epic: EPIC-10
component: infra
type: infra
status: in-progress
size: M
depends_on: [AW-INF-004, AW-INF-013]
blocks: [AW-INF-007, AW-INF-015]
lane: architecture
risk: medium
---

## Context

ADR-0002 makes Kafka the ordering authority, and §7 is explicit about which Kafka: "**Kafka** in
production. **Redpanda** locally." The server's defaults assume a broker: `sim.source` and
`auth.store` are `kafka`, and so is `content.source`. `dev` and `prod` on the box have no broker.
`kafka.brokers` is empty, nothing installs one, and `docs/runbooks/world-read-only.md` already sends
an operator to broker pods no story creates. `AW-INF-008`'s review found this on 2026-09-24. The
interim decided the same day runs `dev` broker-free like `local`. That was applied in `AW-INF-013`'s
PR, and this story removes it.

**Decided 2026-09-24 (Brian):**
- **One broker per namespace.**
- **Apache Kafka via Strimzi, as ADR-0002 §7 says.** No superseding ADR is needed. The first draft
  of this story recommended the Redpanda chart, which would have contradicted the accepted ADR. `dev`
  and `prod` on the box are the production path, so what runs there is the production broker.

Two consequences follow:
- `AW-INF-004`'s `broker.assert` block (`unclean.leader.election.enable`), which exists because
  Redpanda has no such property, finally has a broker to assert against.
- `AW-INF-005`'s "test against real Kafka before production" becomes something `dev` can do.

`deploy/kafka/topics.yaml` already declares replication factor 3 and `min.insync.replicas` 2 for
`dev` and `prod`, so each namespace runs three brokers. This story inherits that declaration and
doesn't revisit it. The box has room: four kind nodes (three workers) on one 24-core, 62 GiB host.

## User story

As an operator, I want `dev` and `prod` to run on the broker ADR-0002 chose for production, so that
ordering, durability, and read-only behavior on the box are the ones production will have. Compose's
Redpanda is only an approximation of them.

## Scope

### In scope
- **The operator, once per cluster.** `make kafka-operator` runs `helm upgrade --install strimzi`
  with chart `strimzi/strimzi-kafka-operator` **1.2.0** (repo `https://strimzi.io/charts/`) into
  namespace `strimzi`, with `watchNamespaces: [andara-dev, andara-prod]`. It is idempotent. The chart
  defaults already meet the Restricted Pod Security Standard.
- **The brokers, per namespace.** `deploy/k8s/kafka/kafka.yaml` holds a `Kafka` named `andara-log`
  and a `KafkaNodePool` named `broker`, applied unchanged to `andara-dev` and `andara-prod` (details
  in the contract). No templating: the manifest names no namespace, and nothing in it differs per
  environment.
- **A toolbox for `rpk`.** `deploy/k8s/kafka/tools.yaml` is a one-replica Deployment
  `andara-kafka-tools` running the Redpanda image compose pins (`v25.1.10`) as `sleep infinity`, so
  `scripts/topics.py` can keep speaking `rpk` to a Kafka broker. `rpk` is a Kafka-API client, and
  `AW-INF-004`'s tooling is built on its JSON output.
- **`scripts/topics.py` picks its broker by environment.**
  - `--env local` keeps today's runner: compose's container, else a host `rpk`.
  - `--env dev|prod` runs `kubectl -n andara-<env> exec deploy/andara-kafka-tools -- rpk -X
    brokers=andara-log-kafka-bootstrap:9092 …`. It never falls back to compose. Today's runner
    ignores `--env`, so `make topics-apply ANDARA_ENV=dev` with `make up` running would apply `dev`'s
    declaration to the local Redpanda.
  - The Redpanda-only `broker.local` cluster config (`enable_consumer_group_metrics`) stays `local`'s.
- **`make kafka-install ENV=<dev|prod>`.** It is idempotent and refuses `local`, which has no cluster
  broker by design. In order, it:
  1. ensures the operator;
  2. creates the namespace;
  3. applies both manifests;
  4. waits for `kafka/andara-log` `Ready` and the toolbox `Available`;
  5. runs `topics.py apply --env <env>`.
- **`make kafka-broker-bounce ENV=<env>`.** It deletes one broker pod, **polls only for recovery**,
  and then asserts once that nothing degraded during the whole window (AC-5). "The server never went
  degraded" is an absence, so `docs/specs/testing/live-assertions.md` §3 applies: the window is
  closed by a recovery checkpoint and checked against records that don't miss transitions. A
  sampled gauge would miss them.
- **Values.**
  - `values/dev.yaml` and `values/prod.yaml` set `server.kafka.brokers:
    andara-log-kafka-bootstrap:9092`.
  - `dev` drops the interim's `sim.source: memory` and `auth.store: memory`, so both return to their
    `kafka` defaults.
  - `dev` **keeps** `content.source: dir`, the content ConfigMaps, the token-key Secret, and the
    required `ANDARA_BOOTSTRAP_OPERATOR`. Compose does the same: content reaches Kafka only when
    `AW-SRV-012` and `AW-CLI-003` publish it.
- **`scripts/helm_install.sh`.**
  - `BROKER_FREE` becomes `local` alone.
  - The ConfigMap and Secret provisioning keys on a new `CONTENT_FROM_CONFIGMAP="local dev"` (until
    `AW-SRV-012`).
  - `ENV=dev|prod` refuses unless `kafka/andara-log` is `Ready`, naming `make kafka-install`.
- **`make k8s-dry`.** It validates `deploy/k8s/kafka/*.yaml` against Strimzi 1.2.0's CRD schemas,
  pinned in the repo beside the cert-manager and Traefik schemas it already uses.
- **Docs.** The chart README (Environments, Targets), `docs/runbooks/world-read-only.md` (the pods
  are `andara-log-broker-*`, and `rpk` runs via the toolbox), and `server/README.md` is untouched.

### Out of scope
- A schema registry on the box — `AW-INF-015`. The server reads none at runtime; `make
  schemas-apply/diff` stay compose-only until then.
- Content on Kafka, and therefore `prod` reaching Ready. `prod` has no content ConfigMap, and its
  deploy path is `AW-INF-007`. This story installs `prod`'s brokers and points its values at them.
- TLS and SASL on the listener. Traffic stays in-namespace, admitted by the listener's
  NetworkPolicy (AC-6). The chart's `secrets.kafkaCreds` stays unused.
- Alerts, SLOs, and the degradation rehearsal — `AW-INF-005`, which this story makes runnable.
- Strimzi's Topic and User Operators. `topics.yaml` is the single source of topics (`AW-INF-004`),
  and a `KafkaTopic` beside it would be a second one.
- Snapshot storage. `snapshot.store` stays `fs` on the PVC. `s3` on the box needs a bucket, and
  `AW-INF-007` will hit that first.

## Acceptance criteria

1. **Given** the box **when** `make kafka-operator` runs twice **then** release `strimzi` is chart
   `1.2.0` in namespace `strimzi`, watching `andara-dev` and `andara-prod`, and the second run
   changes nothing.
2. **Given** `make kafka-install ENV=dev` **when** it returns `0` **then** `kafka/andara-log` is
   `Ready` on Kafka `4.3.1` in KRaft mode, with three `broker` pods on three distinct nodes. Their
   PVCs carry `deleteClaim: false`, so they survive deleting the `Kafka`. A rerun changes nothing.
3. **Given** AC-2 **when** `make topics-diff ANDARA_ENV=dev` runs **then** it exits `0`:
   `andara.commands.v1` has 64 partitions, and every topic has replication factor 3 and
   `min.insync.replicas` 2. The broker reports `unclean.leader.election.enable=false` and
   `auto.create.topics.enable=false`. **Given** `make up` also running **then** `ANDARA_ENV=dev`
   still reaches the cluster, never compose. A unit test covers the runner choice.
4. **Given** AC-3 **when** `ANDARA_BOOTSTRAP_OPERATOR=… make helm-install ENV=dev` runs **then** the
   pod is `Ready` with `sim.source` and `auth.store` of `kafka`. `make stream-soak ENV=dev SOAK=1m`
   passes, and `andara_ingress_degraded` is `0`. The Account store is on the broker, not in memory:
   `andara.accounts.v1` holds the bootstrap operator's record, read through the toolbox (`rpk topic
   consume andara.accounts.v1 -n 1`). After `kubectl delete pod andara-0`, the soak's login succeeds
   again against the same record.
5. **Given** AC-4 **when** `make kafka-broker-bounce ENV=dev` runs **then** one broker pod is deleted
   at `T0`, and the target polls until **the checkpoint**: the pod is back `Ready`, and `rpk cluster
   health` through the toolbox reports no under-replicated partitions. Only then does it assert, once,
   over `[T0, checkpoint]`, that the server never left service. That is what two in-sync replicas of
   three buy. Each record below is complete, not sampled, so a flip between two scrapes can't hide:
   - `andara-0`'s `Ready` condition has a `lastTransitionTime` before `T0`, and its `restartCount`
     is unchanged. The kubelet records every transition.
   - `kubectl logs andara-0 --since-time=T0` has no `command log unreachable` line. The server logs
     one on every entry into degraded mode, and its probe pings the broker every second whether or
     not anyone is playing (`server/README.md`).
   - `andara_ingress_submits_total{outcome="unavailable"}` read from the pod after the checkpoint
     equals its value before `T0`. It is a counter, so a transient refusal would have left it
     higher.

   The target never waits on `andara_ingress_degraded` itself. A gauge read at intervals can't show
   that something *didn't* happen between reads. *(Rewritten at PR #61's review: the first draft
   polled the gauge "throughout".)*
6. **Given** AC-2 **when** a pod without the `andara` labels in `andara-dev` connects to
   `andara-log-kafka-bootstrap:9092` **then** the connection is refused by the listener's
   NetworkPolicy, while `andara-0` and the toolbox connect. kind's kindnet enforces NetworkPolicy
   (`AW-INF-003`).
7. **Given** `kafka/andara-log` absent or not `Ready` in `andara-dev` **when** `make helm-install
   ENV=dev` runs **then** it exits `1` naming `make kafka-install ENV=dev`, before touching the
   release.
8. **Given** `make check` **when** it runs **then** `make k8s-dry` validates both Kafka manifests
   against the pinned Strimzi 1.2.0 schemas, and `make helm-test` renders `dev` and `prod` with
   `ANDARA_KAFKA_BROKERS=andara-log-kafka-bootstrap:9092` and `dev` without the memory switches.
9. **Given** AC-2 **when** Alloy's annotation discovery runs **then** it lists three
   `job="andara-kafka"` targets in `andara-dev`, scraping the JMX exporter on `:9404`. What arrives in
   Grafana Cloud is checked with `AW-INF-008`'s token, in the same sitting.

## Interface contract

### Names

| Thing | Name |
|-------|------|
| Operator release / namespace | `strimzi` / `strimzi` |
| `Kafka` | `andara-log` (ADR-0002: the log) |
| `KafkaNodePool` | `broker`, roles `[controller, broker]`, `replicas: 3` → pods `andara-log-broker-{0,1,2}` |
| Bootstrap (`kafka.brokers`) | `andara-log-kafka-bootstrap:9092` |
| Toolbox | Deployment `andara-kafka-tools`, label `app.kubernetes.io/name: andara-kafka-tools` |
| Metrics job | `andara-kafka` (pod annotation `k8s.grafana.com/job`) |

### The manifest

```yaml
# CONTRACT SKETCH — not an implementation; deploy/k8s/kafka/kafka.yaml
apiVersion: kafka.strimzi.io/v1
kind: KafkaNodePool            # broker: roles [controller, broker], replicas 3
# storage: persistent-claim 20Gi, class standard, deleteClaim: false
# resources: requests cpu 500m, memory 2Gi; limits memory 2Gi; jvmOptions -Xms1g -Xmx1g
# template.pod: required podAntiAffinity on kubernetes.io/hostname;
#   annotations prometheus.io/scrape "true", /port "9404", /path /metrics, k8s.grafana.com/job andara-kafka
---
kind: Kafka                    # andara-log, version 4.3.1, KRaft (node pools only)
# listeners: plain 9092, type internal, tls false,
#   networkPolicyPeers: podSelector app.kubernetes.io/name In [andara, andara-kafka-tools]
# config: default.replication.factor 3, min.insync.replicas 2,
#   offsets.topic.replication.factor 3, transaction.state.log.replication.factor 3,
#   transaction.state.log.min.isr 2, unclean.leader.election.enable false,
#   auto.create.topics.enable false
# metricsConfig: jmxPrometheusExporter from ConfigMap andara-log-metrics (Strimzi's kafka example)
# no entityOperator (topics come from topics.yaml, AW-INF-004)
```

Per namespace that is three brokers of about 2 GiB each, 6 GiB for `dev` plus `prod`. The limits
are there so a broker's page cache cannot starve the server on the same node.

### Make targets

| Target | Does | Exit |
|--------|------|------|
| `make kafka-operator` | Strimzi 1.2.0 into `strimzi`, watching `andara-dev`, `andara-prod` | `0` · `1` helm failed |
| `make kafka-install ENV=<dev\|prod>` | operator, namespace, manifests, wait `Ready`, `topics.py apply --env <env>` | `0` · `1` a step failed · `2` `ENV=local` or unknown |
| `make kafka-broker-bounce ENV=<env>` | record `T0` and the `unavailable` counter; delete one broker; poll to the checkpoint (broker `Ready`, no under-replicated partitions; 10 min cap); then assert once over `[T0, checkpoint]` from the `Ready` condition, the restart count, the server log, and the counter (AC-5) | `0` recovered and never degraded · `1` degraded in the window, or no checkpoint by the cap |
| `make topics-apply` / `topics-diff ANDARA_ENV=<env>` | unchanged surface; the runner is chosen by env (Scope) | unchanged |

Consumer groups keep their `-<env>` suffix (`topics.yaml`). With a broker per namespace the suffix is
redundant, but it is harmless, and keeping it means the names don't change if the environments ever
share a broker.

## Data / state impact

The first durable Command log and Account store outside a laptop. The broker volumes are retained
(`deleteClaim: false`), and no target in this story deletes a PVC; losing one loses that
environment's history, which the snapshot rounds (ADR-0002) do not replace. Moving `dev` from memory
to Kafka discards nothing, because the interim kept nothing across restarts.

## Observability requirements

- **Metrics:** the brokers' JMX exporter, scraped by the platform's Alloy through the annotations
  above (`AW-INF-008`'s mechanism), job `andara-kafka`. The server's existing broker series
  (`andara_ingress_degraded`, the produce path) start to mean something on `dev`.
- **Logs:** the broker pods' stdout, shipped by the node agent like every container.
- **Alerts:** none here; `WorldReadOnly` and `SimulationConsumerLagging` are `AW-INF-005`'s.

## Test plan

- **Unit (CI, `make check`):**
  - `topics.py`'s runner choice for `local` and `dev`, with compose "running", as a table test
    (AC-3's second half);
  - `k8s-dry` over the manifests (AC-8);
  - `helm-test` for the values (AC-8).
- **Integration (box, recorded; CI's kind cluster runs `local`, which stays broker-free):** AC-1 to
  AC-7 and AC-9, in order:
  ```
  make kafka-install ENV=dev && make topics-diff ANDARA_ENV=dev
  ANDARA_BOOTSTRAP_OPERATOR=… make helm-install ENV=dev
  ANDARA_BOOTSTRAP_OPERATOR=… make stream-soak ENV=dev SOAK=1m
  make kafka-broker-bounce ENV=dev
  ```
  Plus AC-6's probe from a throwaway unlabelled pod, and AC-7 against a namespace with no Kafka.
- `AW-INF-008`'s box verification runs in the same sitting, since both need `dev` up.

## Definition of done

CLAUDE.md §8, plus:
- `AW-INF-007` returns from `blocked` to `ready`;
- `values/dev.yaml`'s interim comment is gone;
- `AW-INF-008`'s record names the first `dev` install that reached Ready on Kafka.

## Implementation record — 2026-09-24

Implemented in the session that groomed it, at Brian's request. CLAUDE.md §2 prefers separate
sessions; the contract was merged first (PR #61). Architecture lane only: manifests, scripts, the
Makefile, CI, values, and docs, with nothing under an implementation directory.

**Amendments to the contract, each forced by what the implementation found:**
- **Metrics:** `metricsConfig.type: strimziMetricsReporter` with a `values.allowList`, not the JMX
  exporter's ConfigMap. Strimzi 1.2's `v1` API offers it, and it serves Prometheus text on `:9404`
  directly. The allow-list keeps per-partition series out of Grafana Cloud: replica manager,
  controller, broker-topic and request counters only.
- **Recovery checkpoint:** `kafka-broker-bounce` counts under-replicated partitions with Kafka's own
  `kafka-topics.sh --describe --under-replicated-partitions`, run in a surviving broker. The contract
  said `rpk cluster health`, which calls Redpanda's admin API and doesn't exist on Kafka. A count
  that can't be taken reads `?`, never `0`, and so do the unreadable `/metrics` and log reads in the
  assertion step.
- **Schema validation:** `make k8s-dry` validates the manifests against the CRDs-catalog's
  `kafka.strimzi.io/v1` schemas, fetched the same way cert-manager's and Traefik's already are. They
  are not "pinned in the repo". That was a misdescription of how `k8s-dry` works.
- **CI:** the `topics.py` runner test lives in `scripts/tests/`, run by a new `make scripts-test`,
  added to `make check` and to CI's per-target steps (the parity guard holds).

## Open questions

- **Resolved 2026-09-24 (Brian): one broker per namespace.**
- **Resolved 2026-09-24 (Brian): `dev` runs broker-free until this lands.** Applied in
  `AW-INF-013`'s PR; this story removes it (Scope).
- **Resolved 2026-09-24 (Brian): Apache Kafka via Strimzi, per ADR-0002 §7.** The alternative was
  Redpanda per namespace with a superseding ADR. It is cheaper and has a built-in registry, but it
  would have left production on an approximation of the broker ADR-0002 chose, and made `AW-INF-004`'s
  `unclean.leader.election.enable` assertion a permanent no-op.
