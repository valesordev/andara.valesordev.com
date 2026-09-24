---
id: AW-INF-014
title: A Kafka broker on the box for dev and prod
epic: EPIC-10
component: infra
type: infra
status: draft
size: M
depends_on: [AW-INF-004]
blocks: [AW-INF-007]
lane: architecture
risk: medium
---

## Context

ADR-0002 makes Kafka the ordering authority, and the server's defaults assume one: `content.source`,
`sim.source` and `auth.store` are all `kafka`. `values/local.yaml` opts out of all three (`dir`,
`memory`, `memory`), because the local cluster has no broker. `values/dev.yaml` and
`values/prod.yaml` don't opt out, and `kafka.brokers` is empty everywhere. Nothing installs a broker
on the box. Compose runs Redpanda, and `docs/runbooks/world-read-only.md` already tells an operator
to `kubectl -n andara-<env> get pods -l app.kubernetes.io/name=redpanda`, a pod no story creates.

The `AW-INF-008` review found this on 2026-09-24, while scoping `AW-INF-013`. Publishing the image
lets `dev` pull and start, but the server can't reach Ready without a broker. So `AW-INF-008`'s five
backend checks need this story as well. `AW-INF-005`'s cluster-side `broker-assert` and `AW-INF-007`'s
deploy both assume a broker too, and neither names who installs it.

## User story

As an operator, I want `dev` and `prod` on the box to have the broker the server is built around,
so that they run the same ordering, durability, and read-only behavior the compose stack proves.

## Scope

### In scope *(provisional until the Open questions are answered)*
- A broker on the box, installed by a make target and not by hand, pinned to the Redpanda version
  compose runs (`v25.1.10`).
- Topics and schemas applied to it by `AW-INF-004`'s `make topics-apply` / `make schemas-apply`,
  per environment.
- `kafka.brokers` (and the schema registry URL) set in `values/dev.yaml` and `values/prod.yaml`, with
  the NetworkPolicy admitting the server to it.
- A `make` target that proves `dev` reaches Ready against it. That target is what `AW-INF-008` runs
  first.

### Out of scope
- The operational contract, SLO, and rehearsal — `AW-INF-005`.
- Tiered storage and multi-node durability for `prod`. They are decided here only if Brian picks a
  shared broker (Open questions).

## Acceptance criteria

To be written once the Open questions are answered. It must at least show:
- `make helm-install ENV=dev` reaching Ready on the box;
- `make stack-smoke`'s equivalent (a Session opened, a Command logged) against `andara-dev`;
- a broker restart observed as `andara_ingress_degraded` 1 → 0, as `AW-SRV-010` proves in compose.

## Interface contract

Pending the Open questions.

## Data / state impact

The first durable Command log outside a developer's laptop. Its retention settings are
`AW-INF-004`'s declaration. Deleting the broker's volume loses the World's history, so the install
target must never do that (the same rule the chart applies to its snapshot volume).

## Observability requirements

Broker metrics scraped by the platform's Alloy through pod annotations, as `AW-INF-008` does for the
server. Alerts are `AW-INF-005`'s.

## Test plan

Pending the Open questions.

## Definition of done

CLAUDE.md §8, plus: `AW-INF-008`'s record names the first `dev` install that reached Ready.

## Open questions

- **[NEEDS BRIAN] One broker per environment, or one shared by both?** Options:
  - A single-node Redpanda in each `andara-<env>` namespace keeps the environments isolated and
    matches the runbook's `kubectl -n andara-<env>`.
  - One broker shared by both halves the footprint, and separates `dev` from `prod` by topic prefix.
    That puts a prefix into `AW-INF-004`'s declaration and every client.
  - Recommendation: per namespace, installed through the Redpanda Helm chart with
    `helm.sh/resource-policy: keep` on its volume. `prod` moving to its own cluster (`AW-INF-008`'s
    note) then changes nothing.
- **[NEEDS BRIAN] Should `dev` run like `local` until this lands?** `dev` could set `content.source:
  dir`, `sim.source: memory`, `auth.store: memory`. It would reach Ready as soon as `AW-INF-013`
  publishes, and `AW-INF-008`'s checks could run this week. The cost: `dev` would stop exercising
  Kafka, which is most of what makes it different from `local`, and it would need `local`'s content
  ConfigMap. Recommendation: yes, as a dated values change revoked by this story, because the
  observability checks don't depend on the broker.
