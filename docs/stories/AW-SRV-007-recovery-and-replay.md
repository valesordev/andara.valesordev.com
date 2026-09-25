---
id: AW-SRV-007
title: Recovery from snapshot and log tail, verified in CI
epic: EPIC-04
component: server
type: feature
status: ready
size: M
depends_on: [AW-SRV-006, AW-SRV-026, AW-SRV-028]
blocks: [AW-INF-007, AW-SRV-032, AW-INF-011]
lane: implementation
risk: high
---

## Context

ADR-0002 is explicit that a recovery path not exercised in CI is a recovery path that does not work.
This story makes `kill -9` a tested operation: load the newest complete snapshot round, resume consuming
from its offsets, and assert the resulting State Hash equals what it was before the kill.

The determinism guarantees from `AW-SRV-002` are what make this possible, and this is where they stop
being a design principle and become a production dependency. ADR-0002 §4's Tick Boundary Records are
what make replay *exact* rather than approximately exact — replay reads the recorded offset ranges
rather than re-deciding them.

**Decided 2026-09-11 (Brian): on a State Hash mismatch the server refuses to start.** It does not
search backwards for a round that verifies. A server that picks its own history is a server that can
silently serve the wrong World; a server that stops is an incident with a human in it. The operator's
tools for that incident are in this story's contract.

## User story

As an operator, I want a killed server to return to a playable World within RTO with a verifiable state
match, so that a crash is an interruption rather than an incident.

## Scope

### In scope
- Recovery at boot: select the newest complete snapshot round, load every Zone, seek each Partition to
  the round's offsets, replay to the log head using recorded `TickCompleted` boundaries, verify the
  State Hash against the last boundary, then and only then report ready.
- Refusal, exit code, and log line on: hash mismatch, log gap, unreadable `state_version`, incomplete
  round with no older complete one.
- `andara-server recover --verify [--round <tick>]` — recover and compare without accepting connections.
- `andara-cli snapshot list` and `andara-cli snapshot verify` over `Admin`, so an operator can choose a
  round without a shell on the pod.
- Re-apply idempotency across the checkpoint window, per `AW-SRV-002` AC-10.
- Recovery-time measurement by phase, published as a metric and as a CI artifact.
- A cold start with no snapshot at all replays from offset zero — the M1 behavior, kept.

### Out of scope
- Snapshot writing — `AW-SRV-006`.
- Deploy lifecycle, pre-stop snapshot, and the rollback runbook — `AW-INF-007`.
- Probes. This story exposes readiness; `AW-INF-003` wires it.
- What players see. Recovery is silent to a connected client beyond a dropped stream; `AW-SRV-015`
  rebinds them and `AW-INF-007` owns any notice.

## Acceptance criteria

1. **Given** a running World at tick `T` with a known State Hash **when** the process is killed with
   `SIGKILL` and restarted **then** the recovered World's State Hash at tick `T` is identical, and the
   server reports ready only after that comparison.
2. **Given** a round whose offsets precede the earliest available log offset on any owned Partition
   **when** recovery runs **then** it exits `3` with an `error` line naming the Partition, the round's
   offset, and the log's earliest offset. A World is never recovered from an incomplete history.
3. **Given** recorded `TickCompleted` boundaries **when** replay runs with different fetch batching
   than the original **then** the boundaries are taken from the records and the State Hash sequence is
   identical at every boundary.
4. **Given** a round with a missing or hash-invalid Zone object **when** rounds are listed **then** it
   is `incomplete` and recovery selects the newest `complete` one instead.
5. **Given** the recovered hash differs from the `TickCompleted` hash at the same tick **when**
   recovery finishes replay **then** the server exits `2`, `andara_recovery_state_hash_match` is `0`,
   and the `error` line names the tick, both hashes, and the round used. It never accepts a connection.
6. **Given** any change to `server/sim` or `server/store` **when** CI runs **then** the
   kill-and-recover integration test executes and gates the merge.
7. **Given** the sizing fixture and a 60 s tail **when** recovery runs in CI **then**
   `recovery-timing.json` is published with per-phase durations, and the `replay` phase is under 5 s.
8. **Given** a Command acknowledged to a client with a partition and offset **when** recovery completes
   **then** that offset is at or below the replayed head. `andara_acknowledged_commands_lost_total`
   stays `0`.
9. **Given** no snapshot round exists **when** recovery runs **then** it replays from offset zero on
   every Partition and reaches ready with a hash matching the last `TickCompleted`.
10. **Given** `recover --verify --round 4200` **when** it runs **then** it loads that round, replays to
    head, prints `match` or `mismatch` with both hashes, exits `0` or `2`, and never binds `grpc.listen`.
11. **Given** a round whose Zone objects carry disagreeing `prng_state` or `next_event_id` **when** the
    round is loaded **then** it is refused, naming the Zones and both values; the round is not
    selectable and recovery falls back to the newest round that agrees.
    **Added 2026-09-22, from `AW-SRV-006`'s implementation (feedback §4).** `sim.WorldState` holds one
    PRNG and one `NextEventID` for the World, not one per Zone, but `ZoneState` carries both — so every
    object in a round repeats the same two values. A round is one cut at one tick, so they agree by
    construction, and the repetition is what lets a Zone be restored alone with the PRNG state replay
    needs. Which means a round where they *disagree* is a round assembled from two different cuts, and
    restoring it would seed replay with a generator that never produced the recorded history. This
    criterion also fixes which copy wins when they agree: any of them, because they are equal — the
    check is the contract, not a tie-break.

## Interface contract

```go
// CONTRACT SKETCH — not an implementation
package store   // outside sim; uses sim.WorldStore and sim.Engine

type Round struct {
    Tick         sim.Tick
    StateVersion uint32
    Zones        []ZoneSnapshotRef       // one per owned Zone; sorted
    Complete     bool                    // every owned Zone present and hash-valid
}

// ListRounds groups WorldStore keys by tick and marks completeness against the
// process's owned Zones. Newest first.
//
// The grouping is a prefix scan: the key is
// {zone_id}/{tick}/{state_version}/{offset} (AW-SRV-006, amended 2026-09-22),
// so one Zone's part of a round is the prefix {zone_id}/{tick}/ and a round
// needs no Get per candidate to discover it. Tick precedes state_version so
// that lexical order is tick order across a rollback, which writes newer
// ticks at an older version. Completeness is still a per-object hash check, so
// verifying a round does read every object it names — discovery is what the
// key format makes cheap, not verification.
func ListRounds(ctx context.Context, ws sim.WorldStore, owned []sim.ZoneID) ([]Round, error)

// Recover is the whole boot-time path. It returns a ready Engine or a typed error.
func Recover(ctx context.Context, opts RecoverOptions) (*sim.Engine, Report, error)

type RecoverOptions struct {
    Round   *sim.Tick   // nil: newest complete
    Verify  bool        // true: compare and return; never serve
}
type Report struct {
    Phases   map[string]time.Duration   // load, seek, replay, verify
    Round    Round
    Replayed uint64                     // ticks
    Match    bool
}
```

### Sequence

```
select round ─▶ load Zones ─▶ migrate state_version ─▶ seek Partitions ─▶ replay boundaries ─▶ verify ─▶ ready
     │              │                │                       │                  │               │
  none: offset 0  ErrRound       ErrStateVersion         ErrLogGap        ErrOffsetGap    ErrHashMismatch
```

Ready means: hash verified, consumer lag under `sim.tick_budget_ms × 10`, and the first live tick
completed. `/readyz` (`AW-INF-003`) reads this flag; nothing else sets it.

### Exit codes

| Code | Condition |
|-----:|-----------|
| `0` | recovered and serving, or `--verify` matched |
| `1` | configuration or store error before recovery began |
| `2` | `ErrHashMismatch` — the alerting condition |
| `3` | `ErrLogGap` — retention shorter than the snapshot age |
| `4` | `ErrStateVersion` — binary older than the snapshot |
| `5` | `ErrRoundIncomplete` with no complete round and `recovery.require_snapshot=true` |

### CLI

| Command | RPC | Output |
|---------|-----|--------|
| `andara-server recover --verify [--round T]` | — | `match`/`mismatch`, both hashes, phase timings; exit per table |
| `andara-cli snapshot list [--zone Z] [--local]` | `Admin.ListSnapshotRounds`, or none with `--local` | table: tick, state_version, zones, complete, age |
| `andara-cli snapshot verify --round T` | `Admin.VerifySnapshotRound` | `match`/`mismatch`; runs server-side against a scratch Engine, never the live one |

**`--local` reads the configured store directly and issues no RPC. Added 2026-09-22, from
`AW-SRV-006`'s implementation (feedback §9).** That story's test plan called `andara-cli snapshot
list` before this story's `Admin` RPC existed, and it was implemented against the store rather than by
defining this story's contract from the implementation lane — which was the right call. The two
readings are not redundant and both are worth keeping: the `Admin` path answers *what does the running
server see*, `--local` answers *what is actually in the bucket*. A runbook wants the second precisely
when the first disagrees with it, or when no server is running — which is the state a recovery starts
from. One command with a flag rather than two command names, so an operator learns one.

```protobuf
// CONTRACT SKETCH — added to andara/admin/v1/admin.proto
rpc ListSnapshotRounds(ListSnapshotRoundsRequest) returns (ListSnapshotRoundsResponse);
rpc VerifySnapshotRound(VerifySnapshotRoundRequest) returns (VerifySnapshotRoundResponse);
message ListSnapshotRoundsRequest { string zone_id = 1; uint32 limit = 2; }
message SnapshotRound { uint64 tick = 1; uint32 state_version = 2; repeated string zone_ids = 3; bool complete = 4; int64 taken_at_unix_nano = 5; }
message ListSnapshotRoundsResponse { repeated SnapshotRound rounds = 1; }
message VerifySnapshotRoundRequest { uint64 tick = 1; }
message VerifySnapshotRoundResponse { bool match = 1; bytes expected_hash = 2; bytes actual_hash = 3; }
```

### Configuration

| Key | Env | Default | Notes |
|-----|-----|---------|-------|
| `recovery.require_snapshot` | `ANDARA_RECOVERY_REQUIRE_SNAPSHOT` | `false` | `true` in prod once M2 lands: a cold replay is a retention bug, not a boot |
| `recovery.replay_batch` | `ANDARA_RECOVERY_REPLAY_BATCH` | `4096` | fetch size during replay; AC-3 proves it does not matter |
| `recovery.verify_timeout` | `ANDARA_RECOVERY_VERIFY_TIMEOUT` | `600s` | bounds `--verify` |

### Error taxonomy

`ErrHashMismatch{Tick, Expected, Actual, Round}`, `ErrLogGap{Partition, Need, Have}`,
`ErrStateVersion` (from `AW-SRV-006`), `ErrRoundIncomplete{Tick, Missing}`, `ErrOffsetGap` (from
`AW-SRV-002`, re-raised during replay).

## Data / state impact

No new persisted state. Consumer group offsets are **not** trusted at boot: recovery seeks explicitly
to the round's offsets and re-applies the window, which `AW-SRV-002` AC-10 makes idempotent. The
consumer group's committed offset is only a hint for lag metrics until the first live tick.

Log retention on `andara.commands.v1` and `andara.events.v1` must exceed the age of the oldest round an
operator might select, plus margin. `AW-INF-005` owns the number; AC-2 turns a wrong one into exit `3`
rather than a quietly wrong World.

## Observability requirements

### Metrics
- `andara_recovery_duration_seconds` — histogram, label `phase` (`load`, `seek`, `replay`, `verify`,
  `total`). Bounded enum.
- `andara_recovery_replayed_ticks` — gauge.
- `andara_recovery_state_hash_match` — gauge, 0 or 1. Set once per recovery.
- `andara_recovery_failures_total` — counter, label `reason` (`hash`, `gap`, `version`, `round`,
  `store`).
- `andara_acknowledged_commands_lost_total` — counter; the RPO SLI, must stay 0.
- `andara_recovery_round_tick` — gauge, the round used.

### Logs
- `info` at start with round tick and offsets; `info` at ready with phase durations.
- `error` on every refusal, one line, with the fields the exit-code table names.
- Required fields: `ts`, `level`, `msg`, `service`, `env`, `tick`, `partition`, `trace_id`.

### Traces
- `recovery.run` root; children `recovery.load_snapshot` (per Zone, `zone_id`, `bytes`),
  `recovery.seek`, `recovery.replay` (`ticks`, `records`), `recovery.verify`.

### Alerts
- `RecoveryStateMismatch` on `andara_recovery_state_hash_match == 0`. Pages. Runbook
  `docs/runbooks/recovery-state-mismatch.md` ships here and its mitigation is
  `andara-cli snapshot list` → `andara-server recover --verify --round` → deploy pinned to that round
  via `AW-INF-007`.

## Test plan

- **Unit:** `ListRounds` grouping and completeness against fixtures with a missing Zone and a
  hash-invalid Zone; exit-code mapping per error; `Report` phase accounting.
- **Integration (CI, gates merges):** kill-and-recover against a throwaway Redpanda and the filesystem
  store — run the sizing fixture 90 s, `SIGKILL`, restart, assert AC-1; vary `recovery.replay_batch`
  across three values asserting AC-3; truncate the log below the round's offset asserting exit `3`;
  corrupt one Zone object asserting AC-4; flip one byte in `prng_state` asserting exit `2`; no-snapshot
  cold start asserting AC-9. Publish `recovery-timing.json` (AC-7).
- **Manual/operator:**
  ```
  make up && andara-server &
  kill -9 %1 && andara-server            # expect: "recovery complete match=true" then ready
  andara-cli snapshot list               # expect: rounds newest first, complete=true
  andara-server recover --verify --round <tick>   # expect: match, exit 0
  ```

## Definition of done

CLAUDE.md §8, plus:
- The kill-and-recover test gates merges on every `server/sim` and `server/store` change.
- `docs/runbooks/recovery-state-mismatch.md` exists and resolves its alert with `andara-cli` commands
  only.
- `recovery-timing.json` is a CI artifact and its `replay` phase is compared against the previous
  run in the job summary.
- **Inherited from `AW-SRV-006`'s §8 pass (2026-09-24):** this story is the first to drive a real
  snapshot failure through the server. Its §8 shows, from the running server's own registry,
  `andara_snapshot_failures_total{reason="encode"|"timeout"|"stall"}` moving, and
  `{reason="boundary"}` once `AW-SRV-006` AC-8 lands. `store` and `rounds_total{incomplete}` were
  observed at 006's pass.

## Open questions

- **`ListRounds` and the load-Zones phase land in `AW-SRV-019` (2026-09-24, Brian's call).** The state
  projector bootstraps from the newest complete round and cannot wait for this story's dependencies. It
  implements `store.ListRounds` and `store.Round` exactly as this contract states them, plus loading a
  round into a `sim.Engine`. This story reuses both unchanged and keeps the rest of its sequence. If
  implementing either turns up a defect in the sketch above, the fix is an amendment here, not a
  divergence there.

- **Inherited from the review of PR #32 (2026-09-19), measured by implementation:** `tickloop.Recover`
  reads every `TickCompleted` on the topic into memory before replaying — on a local log of ~180k
  ticks, RSS sat at ~4 GB for the first 40 s after boot before settling at 270 MB. Recovery memory is
  linear in log length until a snapshot bounds the replay window. This story's contract must have
  `Recover` stream boundaries from the round's tick forward rather than load the topic, and its
  RTO measurement must state peak RSS alongside seconds.

- **Inherited from `AW-SRV-002` (2026-09-18):** `tickloop.Recover` already replays recorded
  boundaries at boot and refuses a missing one as `ErrBoundaryGap` — a tick applied but never
  published. Once one boundary is lost the process publishes no more and keeps ticking; the next
  restart recovers to the last delivered boundary. **Decided 2026-09-18 (Brian): exit into exact
  recovery** — `AW-SRV-026` wires it (exit `5`), and this story inherits a process that never runs
  past a lost boundary; `ErrBoundaryGap` joins `ErrLogGap` and `ErrOffsetGap` here as the backstop.
  Zone faults quarantine the Zone, not the Partition (`AW-SRV-027`); whether a panic should instead
  crash-and-recover, now that recovery is exact, is still this story's to weigh.

- **Resolved 2026-09-11 (Brian): refuse on hash mismatch.** No automatic search for an older round.
- **RTO is decided**: 120 s p99 at M2, 60 s p99 at Phase 1 exit (`docs/specs/slo/recovery.md`). The
  dominant terms are Kubernetes detection and pod startup; optimizing this means `AW-INF-003`'s probes
  and image pre-pull, not a faster simulation.
- **`linkdead_grace > RTO` is a standing invariant** (ADR-0006), asserted at startup by `AW-SRV-015`.
- `[NEEDS BRIAN]` What players see during recovery — carried by `AW-INF-007`. Does not affect this
  contract: recovery is silent on the wire.
- `[ASSUMPTION]` `recovery.require_snapshot` defaults `false` so M1 keeps booting; `AW-INF-003`'s prod
  values set it `true`.
