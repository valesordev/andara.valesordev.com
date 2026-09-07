---
id: AW-INF-007
title: Deploy lifecycle — pre-stop snapshot, post-start recovery, and rollback
epic: EPIC-01
component: infra
type: infra
status: draft
size: M
depends_on: [AW-INF-003, AW-SRV-007]
blocks: []
assignee: claude-code
risk: high
---

> `status: draft` — scoped and unblocked. Groomed once `AW-SRV-007` has a measured recovery time, since
> that number is what determines whether a deploy is a blip or an outage.

## Context

ADR-0001 accepts that a deploy interrupts the World until sharding is active: a rolling update of a
one-replica `StatefulSet` is a scheduled restart. What makes it acceptable rather than alarming is that
recovery is exact — the new pod resumes from a snapshot and the log tail with a matching State Hash.

That has to be expressed as lifecycle, not as a runbook step someone remembers. A pre-stop hook takes a
snapshot so the replay is short; the new pod recovers before it reports ready; a rollback is the same
mechanism in reverse.

## User story

As an operator, I want deploying to be a routine interruption measured in seconds, so that shipping is
not an event.

## Scope

### In scope
- Pre-stop hook: snapshot at a tick boundary, checkpoint offsets, then terminate — within the
  termination grace period, with the grace period derived from measured snapshot duration.
- Post-start: recover from the newest snapshot plus log tail, verify State Hash, then report ready.
- Rollback path, including the case where the previous binary cannot read the newer snapshot's
  `state_version`.
- Player-facing behavior during the interruption. `[NEEDS BRIAN]` — this is a design decision, not an
  ops one.
- Deploy tracing: pre-stop snapshot and post-start recovery both appear as spans.

### Out of scope
- The snapshot and recovery mechanisms — `AW-SRV-006` and `AW-SRV-007`.
- Image build and publish.
- Zero-downtime deploys, which require sharding and partition draining. Explicitly not Phase 1.

## Acceptance criteria (known now; completed at grooming)

1. **Given** a running deployment **when** a rolling update is triggered **then** the pre-stop hook
   completes a snapshot before the container is terminated, and the hook's duration is inside the
   termination grace period.
2. **Given** the new pod starting **when** it recovers **then** its State Hash matches the pre-deploy
   hash, and only then does it report ready.
3. **Given** a recovery whose State Hash does not match **when** it completes **then** the pod does not
   report ready, the `RecoveryStateMismatch` alert fires, and the deploy halts. A mismatched World must
   never take traffic.
4. **Given** a rollback to a binary that cannot read the current snapshot's `state_version` **when** it
   starts **then** it refuses with both versions named, and the documented recovery is to replay from an
   older snapshot rather than to start with an unreadable one.
5. **Given** a deploy **when** it completes **then** measured player-visible interruption is recorded as
   a metric and compared against the RTO SLO.

## Interface contract

To be written at grooming.

## Data / state impact

The `state_version` forward/backward compatibility question is this story's hardest part and it only
appears on rollback. A binary that can write a snapshot format its predecessor cannot read makes rollback
impossible without replaying from an older snapshot — which is survivable precisely because Kafka holds
the history, but only if retention reaches back far enough. This connects directly to the retention
question left open in `AW-INF-004`.

## Observability requirements

- **Metrics:** `andara_deploy_interruption_seconds` (histogram), `andara_prestop_snapshot_duration_seconds`,
  `andara_recovery_duration_seconds` (from `AW-SRV-007`).
- **Traces:** `deploy.prestop_snapshot` and `deploy.recovery` as spans, so a deploy is traceable end to
  end.
- **Alerts:** none new. `RecoveryStateMismatch` from `AW-SRV-007` is the one that must page, and it
  already exists.

## Test plan

A kind-based test performing a rolling update and asserting a matching State Hash, plus a rollback test
across a deliberate `state_version` bump.

## Definition of done

CLAUDE.md §8, plus: the rolling-update test runs in CI, and the runbook states the expected interruption
duration as a number derived from measurement.

## Open questions

- `[NEEDS BRIAN]` What players see during a deploy interruption. A countdown, a graceful "the world is
  briefly closing" message, or silence-and-reconnect. This is player experience, not operations.
- `[NEEDS BRIAN]` Acceptable deploy cadence, which together with interruption duration determines
  whether sharding becomes urgent for reasons unrelated to load.
