---
id: AW-INF-031
title: dev's Kafka moves to SASL and deny-by-default with no client denied
epic: EPIC-01
component: infra
type: infra
status: draft
size: M
depends_on: [AW-INF-030]
blocks: []
lane: sre
risk: high
---

## Context

ADR-0011 (accepted 2026-09-29) fixes the order in which `dev`'s Strimzi Kafka moves to SASL/SCRAM
and deny-by-default, so that no client is ever denied. It has to be this order, because Kafka
refuses to create an ACL while no authorizer is configured (`SecurityDisabledException`):
1. turn the authorizer on, with `andara-operator` and `ANONYMOUS` as super users;
2. add the SCRAM listener beside `plain`, and turn on the User Operator, which creates the
   `KafkaUser`s and their ACLs; verify with `make topics-diff`;
3. move the clients, each onto its own Secret;
4. remove `plain`, and `ANONYMOUS` from the super users.

`AW-INF-030` built the declaration, the apply path and the chart. This story is the live
migration, split out because it's an ordered, rollback-sensitive operation on a running cluster.
Each step is a separate apply, and each can be undone by restoring the previous one. No topic,
offset or record changes at any step. It's on neither the M3 nor the M2 path. It's the step that
makes `dev` enforce what CI already does.

## User story

As an operator, I want `dev`'s broker to authenticate every client and hold it to its ACLs, so that
`dev` enforces what CI does and the audit topic is writable only by the server.

## Scope

### In scope
- **Strimzi:**
  - `authorization: simple`, with super users `andara-operator` (and `ANONYMOUS` until step 4);
  - the SCRAM-SHA-512 listener, and later the removal of `plain`;
  - the User Operator enabled for users only (ADR-0011 decision 5), with the Topic Operator still
    excluded.
- **The four steps as `make` targets.** Each is idempotent, and each has a matching undo.
- **The runbook:** the order, each step's check, and each step's rollback.
- **`dev`'s chart values:** each workload's Secret reference pointed at the User Operator's
  generated Secret.

### Out of scope
- `prod`, which runs nothing yet. When it does, it starts at step 4's end state, with no migration.
- The client code (`AW-SRV-044`) and the declaration and apply path (`AW-INF-030`).

## Acceptance criteria

1. **Given** step 1 applied **then** brokers roll, every client still on `plain` keeps working as
   `ANONYMOUS`, and `andara_sessions_active` shows no drop beyond the brokers' rolling restart.
2. **Given** step 2 applied **then** `make topics-diff ENV=dev` exits `0`, with every principal and
   ACL in `topics.yaml` present, and the User Operator has generated one Secret per principal.
3. **Given** step 3 **then** the server and the projector are Ready on the SCRAM listener, each as
   its own principal. `andara_kafka_auth_failures_total` stays at 0 throughout.
4. **Given** step 4 applied **then** an unauthenticated client is refused at its first request, and
   `andara-server`'s credentials are refused a produce to `andara.state.v1` on `dev`.
5. **Given** any step's undo target **when** it's run after that step **then** the cluster returns
   to the previous step's state, and every client keeps working.
6. **Given** each step **then** no topic's configuration, offsets or records change. `make
   topics-diff` reports topic config identical before and after.
7. **Given** the runbook **then** every step and every undo is a `make` target with its expected
   observable result (CLAUDE.md §9).

## Interface contract

- **Targets:** `make kafka-auth-step STEP=<1|2|3|4> ENV=<env>` and
  `make kafka-auth-undo STEP=<1|2|3|4> ENV=<env>`. Each exits `0` once its check passes, and `1`
  naming the check that failed. *(PM's proposal, for SRE to name as it sees fit.)*
- **Strimzi `Kafka` resource:** `authorization.type: simple`, `superUsers`, and a `scram-sha-512`
  listener on the in-namespace port. `entityOperator.userOperator` is enabled, and `topicOperator`
  stays absent.
- **Runbook:** `docs/runbooks/kafka-auth-migration.md`.

## Data / state impact

No topic, offset or record changes (ADR-0011). Brokers roll at steps 1, 2 and 4, and clients
redeploy at step 3. Live Sessions see whatever a broker rolling restart already causes on `dev`, and
nothing more. From step 4, rollback is restoring `plain` with `ANONYMOUS` as a super user, which is
`kafka-auth-undo STEP=4`.

## Observability requirements

- **Metrics:** `andara_kafka_auth_failures_total{workload}` (`AW-SRV-044`) is watched through every
  step, and stays at 0. No new metric.
- **Logs:** each target prints its step, what it applied, and its check's result.
- **Traces:** none.
- **Alerts:** SRE decides whether `dev` alerts on sustained authentication failures, tied to the
  server's availability SLO. If it does, the runbook entry lands here (CLAUDE.md §7).

## Test plan

- **Unit:** each step's and undo's rendered `Kafka` resource diff, against `k8s-dry`.
- **Integration:** the full sequence and every undo on the box's kind cluster (`andara-dev`) before
  `dev` itself.
- **Manual/operator:** the runbook on `dev`, with each step's result recorded in the §8 record.

## Definition of done

CLAUDE.md §8, plus: ACs 1–4 observed on `dev`, recorded step by step.

## Open questions

- `[ASSUMPTION]` `dev`'s chart values switch at step 3, in the same commit as the client redeploy.
