---
id: AW-SRV-007
title: Recovery from snapshot and log tail, verified in CI
epic: EPIC-04
component: server
type: feature
status: draft
size: M
depends_on: [AW-SRV-006]
blocks: [AW-SRV-014, AW-INF-007]
lane: implementation
risk: high
---

> `status: draft` — unblocked and scoped. Groomed to `ready` alongside `AW-SRV-006`.

## Context

ADR-0002 is explicit that a recovery path not exercised in CI is a recovery path that does not work.
This story makes `kill -9` a tested operation: load the newest snapshot, resume consuming from its
offsets, and assert the resulting State Hash equals what it was before the kill.

The determinism guarantees from `AW-SRV-002` are what make this possible, and this is where they stop
being a design principle and become a production dependency. In particular, ADR-0002 §4's Tick Boundary
Records are what make replay *exact* rather than approximately exact — replay reads the recorded offset
ranges rather than re-deciding them.

## User story

As an operator, I want a killed server to return to a playable World within RTO with a verifiable state
match, so that a crash is an interruption rather than an incident.

## Scope

### In scope
- Recovery: locate the newest valid snapshot, load it, resume consuming from its offsets, replay to the
  log head using recorded Tick Boundary Records.
- State Hash verification at the end of recovery.
- `andara-server recover --verify` — recover and compare without accepting connections, so an operator
  can validate a backup without exposing it to players.
- Re-apply idempotency across the checkpoint window, per `AW-SRV-002` AC-10.
- Refusal on an incomplete history: a gap between snapshot offsets and available log.
- Recovery-time measurement published as a metric and as a CI artifact.

### Out of scope
- Snapshot writing — `AW-SRV-006`.
- Deploy lifecycle — `AW-INF-007`.
- Cross-Partition consistent cuts, which are not needed: snapshots are per-Zone and therefore
  per-Partition by construction.

## Acceptance criteria (known now; completed at grooming)

1. **Given** a running World at tick `T` with a known State Hash **when** the process is killed with
   `SIGKILL` and restarted **then** the recovered World's State Hash at tick `T` is identical.
2. **Given** a snapshot whose offsets precede the earliest available log offset **when** recovery runs
   **then** it refuses to start, exits 1, and names the gap. A World must never be recovered from an
   incomplete history — this is the failure mode that log retention shrinkage would silently create.
3. **Given** recorded Tick Boundary Records **when** replay runs with different fetch batching than the
   original **then** the tick boundaries are taken from the records and the State Hash sequence is
   identical.
4. **Given** a partially written snapshot **when** recovery selects a snapshot **then** it is not
   selectable, and recovery falls back to the previous valid one.
5. **Given** any change to `server/sim` or the persistence adapters **when** CI runs **then** a
   kill-and-recover integration test executes and gates the merge.
6. **Given** a realistic snapshot and log tail **when** recovery runs **then** measured recovery time is
   published and compared against the RTO SLO, broken down by phase — detection, start, snapshot load,
   replay, verify — so the dominant term is visible rather than inferred.
7. **Given** a Command acknowledged to a client with a partition and offset **when** recovery completes
   **then** that offset is present in the log. `andara_acknowledged_commands_lost_total` must remain 0;
   any non-zero value is an incident, not budget burn (`docs/specs/slo/recovery.md`).

## Interface contract

To be written at grooming. Committed now: recovery is a first-class operation rather than a side effect
of boot, exposed as `andara-server recover --verify`.

## Data / state impact

Defines the forward-migration path for snapshots written by older `state_version` values, and the policy
when migration is not possible.

Recovery is the moment every latent non-determinism in the sim core becomes a data-corruption bug, which
is why `AW-SRV-002`'s mechanical determinism guards are a hard dependency rather than a nicety. It is
also why ADR-0005 keeps Python out of the tick: replay replays Commands, never re-runs Behaviors.

## Observability requirements

- **Metrics:** `andara_recovery_duration_seconds` (histogram), `andara_recovery_replayed_ticks` (gauge),
  `andara_recovery_state_hash_match` (gauge, 0 or 1), `andara_recovery_failures_total` (counter, label
  `reason`).
- **Logs:** recovery start and completion at `info` with snapshot tick, offset range, and duration; log
  gap at `error`; hash mismatch at `error`.
- **Traces:** `recovery.run` with `recovery.load_snapshot` and `recovery.replay` children.
- **Alerts:** `RecoveryStateMismatch` on `andara_recovery_state_hash_match == 0`. This is the one alert
  that must page. Runbook ships in this story.

## Test plan

The CI kill-and-recover test; a log-gap fixture asserting refusal; timing-independence with varied fetch
batching; a recovery-time measurement published as a CI artifact so RTO regressions are visible in a
diff.

## Definition of done

CLAUDE.md §8, plus:
- The kill-and-recover test gates merges on every sim-core change.
- `docs/specs/slo/recovery.md` exists with RPO and RTO targets.
- `docs/runbooks/recovery-state-mismatch.md` exists and resolves its alert.

## Open questions

- **RTO is decided**: 120 s p99 at M2, 60 s p99 at Phase 1 exit (`docs/specs/slo/recovery.md`). The
  phase breakdown there shows the dominant terms are Kubernetes failure detection and pod startup, not
  replay — so optimizing this means `AW-INF-003`'s probes and image pre-pull, not a faster simulation.
- **`linkdead_grace > RTO` is a standing invariant** (ADR-0006). At 180 s versus 60 s there is 3× margin;
  neither may move without checking the other, and `AW-SRV-015` asserts it at startup.
- `[NEEDS BRIAN]` What players see during recovery — shared with `AW-INF-007`.
- `[NEEDS BRIAN]` Whether the World refuses to start or rolls back to the last matching tick on a hash
  mismatch. Refusing is safer; rolling back is more playable. A product call.
- Log retention must exceed the age of the oldest snapshot recovery would use, plus margin. Carried from
  `AW-INF-004`'s open retention question; AC-2 is what turns a wrong answer into a loud failure instead
  of a quiet one.
