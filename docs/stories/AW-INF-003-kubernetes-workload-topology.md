---
id: AW-INF-003
title: Kubernetes workload topology, volumes, and probes for andara-server
epic: EPIC-01
component: infra
type: infra
status: draft
size: M
depends_on: [AW-INF-001, AW-INF-002]
blocks: [AW-INF-006, AW-INF-007]
lane: architecture
risk: high
---

> `status: draft` — scoped, sequenced, and unblocked, but not yet groomed to `ready`. Its interface
> contract will be written when M2 approaches, against a server that exists. Grooming it now would
> mean specifying probes for a process whose readiness semantics are still being built.

## Context

ADR-0001 decided the shape: a `StatefulSet` with one replica at launch, owning all 64 Partitions, with
stable identity and storage because sharding later is a consumer-group rebalance rather than a
migration. This story renders that decision as a chart.

The unusual constraint is readiness. A pod that is running is not a pod that can serve players — it may
still be replaying its log tail. Readiness must reflect *simulation* readiness, or Kubernetes will send
traffic to a server that has no World yet.

## User story

As an operator, I want `andara-server` deployed as a chart with honest health signals, so that
Kubernetes' idea of healthy matches a player's.

## Scope

### In scope
- Helm chart: `StatefulSet`, `ServiceAccount`, `ConfigMap`, `PodDisruptionBudget`, volume claims.
- Partition assignment derived from pod ordinal, so scaling out is the sharding mechanism.
- Liveness (process alive), readiness (World loaded, ticking, consumer lag under threshold), and
  startup probes with a budget covering worst-case log replay.
- Volume lifecycle for snapshots, including what happens on chart uninstall.
- Resource requests and limits informed by measured tick cost, not guessed.
- Values schema covering every server configuration key from `AW-INF-002`, `AW-SRV-002`, and
  `AW-SRV-005`.

### Out of scope
- Service, ingress, TLS, and gRPC routing — `AW-INF-006`.
- Deploy lifecycle hooks — `AW-INF-007`.
- Kafka cluster deployment. Topics are `AW-INF-004`; the cluster itself is a platform dependency.
- Multi-replica partition assignment. The chart must not forbid it, but activating it is a later,
  measurement-driven change.

## Acceptance criteria (known now; completed at grooming)

1. **Given** any environment values file **when** `make k8s-dry ENV=<env>` runs **then** manifests render
   and validate against the target API version, exiting non-zero on any schema violation.
2. **Given** a pod that is running but still replaying its log **when** its readiness probe is polled
   **then** it reports not-ready and receives no traffic.
3. **Given** a chart uninstall **when** it runs **then** the snapshot volume is **retained**, not
   deleted. An uninstall that silently destroys the World's only local copy is the failure mode this
   story exists to prevent.
4. **Given** any rendered manifest **when** it is inspected **then** no secret value appears in a
   ConfigMap, an environment literal, or a chart default.
5. **Given** the values schema **when** a value outside its declared type or range is supplied **then**
   `helm template` fails rather than rendering an invalid ConfigMap.

## Interface contract

To be written at grooming. It will cover: the values schema in full, the ordinal-to-partition mapping,
probe endpoints and their semantics, and volume retention policy.

Committed now: every server configuration key is settable through chart values and appears in the values
schema, per CLAUDE.md §8.

## Data / state impact

Snapshot volume lifecycle across pod replacement, scale-to-zero, and uninstall. Because ADR-0002 makes
Kafka the authority and snapshots an RTO optimization, losing a snapshot volume is survivable — it costs
replay time, not data. That is worth stating in the runbook, because it changes how urgently an operator
should treat a volume problem.

## Observability requirements

- **Metrics:** `kube_*` workload metrics scraped; `andara_build_info` used to confirm the running
  version and content version match intent.
- **Logs:** pod lifecycle correlated with the server's own `trace_id`.
- **Alerts:** `AndaraServerUnavailable` and `AndaraServerCrashLooping`, both symptom-based, both
  requiring their SLO documents first, both shipping runbooks in this story.

## Test plan

`make k8s-dry` for every environment in CI. A kind-based test asserting a pod stays not-ready until its
World is loaded.

## Definition of done

CLAUDE.md §8, plus: `make k8s-dry` validates every environment in CI, and every alert has a runbook.

## Open questions

- **Resolved 2026-09-10 (Brian): the kind cluster on Brian's box, Kubernetes v1.36.1.** Greenfield —
  no platform to conform to. `make k8s-dry` validates against v1.36.1; the chart targets that first.

  **What this costs, recorded here because nobody will remember it later.** The kind "cluster" is four
  containers on one machine (`solo7-control-plane` plus three workers). RF=3 with
  `min.insync.replicas=2` is three replicas on one disk, so the settings AW-INF-004 declares
  non-negotiable are correct for a real cluster and buy nothing against the failure they exist for —
  a lost disk. `docs/specs/slo/recovery.md`'s zero-RPO target is, until this moves, a claim about one
  machine. ADR-0002 carries the same note under `Revisit when`. The chart is **not** softened to match:
  it stays correct for a real cluster, because the box is where this runs first, not where it stays.

  Two facts about this box that `AW-INF-006` needs: the kind control plane already publishes `:80` and
  `:443`, and other projects hold `:3000`, `:8081`, and `:9000`. Ingress must not assume a free port.
- `[ASSUMPTION]` Environments are `local`, `dev`, and `prod`. Adding staging later is cheap; assuming it
  exists when it does not is not.
- Resource requests cannot be set honestly until `AW-SRV-002` has produced real tick measurements. This
  is a reason to groom this story late, not a reason to guess.
