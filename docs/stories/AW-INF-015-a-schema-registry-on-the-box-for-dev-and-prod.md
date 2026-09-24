---
id: AW-INF-015
title: A schema registry on the box for dev and prod
epic: EPIC-10
component: infra
type: infra
status: draft
size: S
depends_on: [AW-INF-014]
blocks: []
lane: architecture
risk: low
---

## Context

`AW-INF-004` registers every protobuf subject with a schema registry, in every environment, through
`make schemas-apply` / `schemas-diff`. Locally that registry comes free with Redpanda. `AW-INF-014`
puts Apache Kafka on the box (Strimzi, following ADR-0002 §7, decided 2026-09-24), and Kafka has no
registry. The server doesn't read one at runtime: no key in `keys.yaml` names it. So `dev` and
`prod` run without it, and only the governance check is missing: whether the registry holds what
`deploy/kafka/schemas.yaml` declares.

## User story

As an operator, I want `make schemas-diff ANDARA_ENV=dev` to answer against the box, so that schema
drift is caught where production runs and not only on a laptop.

## Scope

### In scope *(to pin at grooming)*
- A Confluent-API-compatible registry per `andara-<env>` namespace, pinned by version. Candidates are
  Karapace and Apicurio (`ccompat/v7`), both Apache-2.0. Confluent's own Schema Registry is under the
  Confluent Community License, which this repo's REUSE posture would have to accept first.
- `schemas.py`'s `registry_url(env)` for `dev`/`prod`, reached in-cluster the same way `AW-INF-014`
  reaches the brokers.
- Re-measuring `AW-INF-004`'s registry findings (e.g. Redpanda rejecting `version: -1` references)
  against the chosen registry.

### Out of scope
- Any runtime use of the registry by the server. None exists.

## Acceptance criteria

To be written at grooming. It must show `make schemas-apply` then `make schemas-diff` clean against
`andara-dev`.

## Interface contract

Pending grooming.

## Data / state impact

The registry's own storage: a `_schemas` topic on `AW-INF-014`'s brokers, if the choice is Karapace
or Confluent-compatible.

## Observability requirements

Pending grooming.

## Test plan

Pending grooming.

## Definition of done

CLAUDE.md §8.

## Open questions

- Karapace or Apicurio. Recommendation to come at grooming. Karapace is closer to a drop-in for the
  Confluent API that `schemas.py` speaks.
