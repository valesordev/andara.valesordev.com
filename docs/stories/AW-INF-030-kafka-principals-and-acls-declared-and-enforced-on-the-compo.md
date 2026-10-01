---
id: AW-INF-030
title: Kafka principals and ACLs, declared and enforced on the compose stack
epic: EPIC-01
component: infra
type: infra
status: draft
size: M
depends_on: [AW-SRV-044]
blocks: [AW-INF-031]
lane: sre
risk: medium
---

## Context

ADR-0011 (accepted 2026-09-29) puts SASL/SCRAM-SHA-512 on every broker listener, denies by default,
and gives each workload its own principal, all declared in `deploy/kafka/topics.yaml`. This story is
the declaration and its first enforcement. The `principals:` section and its apply path go on
both brokers. The chart gets a Secret reference per workload. The compose Redpanda runs SASL and
ACLs from its first boot. And `make` wrappers carry the operator's credentials to `rpk`. CI on the
compose stack then enforces what `dev` will.

It carries `AW-SRV-019` AC-9 as an inherited Definition-of-done line: only
`andara-projector-state` can produce to `andara.state.v1`. That's the line that lets `AW-SRV-019`
close. `dev`'s migration, which is Strimzi's authorizer, its User Operator and the four-step order,
is `AW-INF-031`. It's split out because it's an ordered, rollback-sensitive operation on a live
cluster. This story's apply path has to exist before that migration can use it. It depends on
`AW-SRV-044`, since a SASL listener refuses clients that can't speak it.

## User story

As an operator, I want each workload's Kafka rights declared beside the topics they govern and
enforced by the broker, so that a compromised or buggy client can't write what it shouldn't, and CI
proves it.

## Scope

### In scope
- **`principals:` in `deploy/kafka/topics.yaml`,** with ADR-0011 decision 3's table:
  `andara-server`, `andara-projector-state` and `andara-operator` (superuser). Each has least
  privilege by topic and group prefix.
- **The apply path:**
  - `make topics-apply` creates and aligns principals and ACLs. On Redpanda it does this with
    `rpk security`. On Strimzi it renders `KafkaUser` resources, applied once `AW-INF-031` enables
    the User Operator;
  - `make topics-diff` reports ACL drift the same way it reports topic drift.
- **The compose Redpanda:** SASL/SCRAM on the client listener, ACLs enforced, the principals created
  at startup, and fixed local-only passwords in the compose file.
- **The chart (ADR-0011 decision 6):**
  - `secrets.kafkaCreds.secretName` for the server, and `projectors.<name>.kafkaCreds.secretName`
    for each projector, with no fallback between them;
  - each Secret mounted at `/etc/andara/secrets/kafka`;
  - `ANDARA_KAFKA_SASL_USERNAME` set per workload.
- **`make` wrappers** for `rpk` that pass the `andara-operator` credentials, so
  `topics-apply`, `world-reset` and the others keep working against a SASL broker.

### Out of scope
- `dev`'s cluster: the Strimzi listener, authorization, the User Operator and the migration order.
  Those are `AW-INF-031`.
- The client code, which is `AW-SRV-044`.
- Principals for `AW-SRV-017` and `AW-SRV-018`. Each declares its own when it lands.
- TLS on the listener.

## Acceptance criteria

1. **Given** `make up` **then** the compose Redpanda refuses an unauthenticated client at its first
   request, and the server and the projector run Ready, each authenticated as its own principal.
2. **Given** the compose stack **when** each principal declared in `topics.yaml` other than
   `andara-projector-state` and the superuser produces to `andara.state.v1` **then** the broker
   rejects it with an authorization error, and `andara-projector-state` succeeds. The test reads the
   principals from `topics.yaml`, so a principal declared later is covered without editing the
   test. *(This is `AW-SRV-019` AC-9 as #315 amends it, asserted in CI.)*
3. **Given** `andara-projector-state`'s credentials **when** they produce to the commands or events
   topic **then** the broker rejects it.
4. **Given** `make topics-apply` on the compose stack **then** every principal and ACL in
   `topics.yaml` exists, and a second run changes nothing.
5. **Given** an ACL added by hand that `topics.yaml` doesn't declare, or a declared one removed
   **when** `make topics-diff` runs **then** it exits `1`, naming the principal and the operation.
6. **Given** the chart rendered for each environment **then** each workload mounts only its own
   Secret reference. A projector with no reference renders no mount. `make helm-test` asserts both.
7. **Given** `make world-reset`, `make topics-apply` or any other target that runs `rpk` **then** it
   authenticates as `andara-operator` without the developer passing credentials.
8. **Given** CI's `stack` job **then** ACs 1–3 run there on every change.
9. **Given** `topics.yaml` **then** exactly one principal is a superuser, `andara-operator`, and no
   workload's chart values reference its Secret. A test asserts both, so the exemption in AC-2 can't
   grow silently.

## Interface contract

```yaml
# CONTRACT SKETCH — not an implementation
principals:
  andara-projector-state:
    acls:
      - {resource: topic, name: andara.commands.v1, ops: [read, describe]}
      - {resource: topic, name: andara.state.v1,    ops: [read, write, describe]}
      - {resource: group, prefix: andara-projector-state-, ops: [read]}
  andara-operator:
    superuser: true
```

- The exact operations per principal are derived from the clients and asserted by tests (ADR-0011
  decision 3). The table in ADR-0011 is the outline.
- **Chart values:**
  - `secrets.kafkaCreds.secretName` (server) and `projectors.<name>.kafkaCreds.secretName`;
  - username env `ANDARA_KAFKA_SASL_USERNAME`, and the password file at
    `/etc/andara/secrets/kafka/password`.
- **`make` targets:** `topics-apply` and `topics-diff` cover principals and ACLs. Existing targets
  that wrap `rpk` read the operator's credentials from the compose file locally, and from the
  `andara-operator` Secret in a cluster.

## Data / state impact

The compose Redpanda starts empty, with SASL on from its first boot, so there's nothing to migrate.
No topic, offset or record changes. `dev` is untouched until `AW-INF-031`.

## Observability requirements

- **Metrics:** none new on the broker side. `andara_kafka_auth_failures_total{workload}`
  (`AW-SRV-044`) is the client-side signal. `topics-diff` drift is reported through its exit code and
  lines, as today.
- **Logs:** `topics-apply` names each principal and ACL it creates or changes. `topics-diff` names each
  drifted one.
- **Traces:** none.
- **Alerts:** none here. Whether authentication failures on `dev` alert, tied to the availability
  SLO, is `AW-INF-031`'s call. *(PM's proposal, for SRE's review.)*

## Test plan

- **Unit** (`scripts/tests/`): rendering principals to `rpk security` calls and to `KafkaUser`
  manifests; the drift comparison (AC-5); the chart's Secret references (AC-6).
- **Integration:** CI's `stack` job, covering ACs 1–3 and 7.
- **Manual/operator:**
  ```
  make up && make topics-diff     # expect: no drift, exit 0
  make stack-play                 # expect: passes with every client authenticated
  ```

## Definition of done

CLAUDE.md §8, plus:
- **Inherited from `AW-SRV-019` AC-9:** every declared principal other than `andara-projector-state`
  and the operator superuser is refused when it produces to `andara.state.v1`, asserted in CI on the
  compose stack (AC-2, AC-8, AC-9). This is AC-9 as #315 amends it, exempting the operator
  superuser.

## Open questions

1. **Ruled 2026-10-01 (architecture); resolved when #315 merges: AC-9 and the superuser.** Option
   (a). #315 amends `AW-SRV-019` AC-9 to "any Kafka principal declared in `deploy/kafka/topics.yaml` other than
   `andara-projector-state` and the operator superuser `andara-operator` … is rejected".
   ADR-0011 decision 3 accepts the superuser's audit gap, and bounds it: one superuser, used by the
   `rpk` toolbox only, with no workload mounting its Secret. AC-2 asserts the amended criterion, and
   AC-9 asserts the bound. Scoped admin ACLs (option (b)) weren't taken, since they would reopen an
   accepted ADR for a gap it already names. **Until #315 is on `main`,** `AW-SRV-019` AC-9 still
   reads "any principal other than `andara-projector-state`", and this story's AC-2 is narrower than
   it. This story can't reach `ready` until then.
2. `[ASSUMPTION]` `KafkaUser` manifests are rendered and validated here (`k8s-dry`), but first
  applied by `AW-INF-031`, because the User Operator isn't enabled before then.
