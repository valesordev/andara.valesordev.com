---
id: AW-SRV-043
title: A restore verifies its round
epic: EPIC-04
component: server
type: feature
status: review
size: M
depends_on: [AW-SRV-006, AW-SRV-019]
blocks: [AW-SRV-007]
lane: implementation
risk: medium
---

## Context

#143 was a restore that hashed wrong. With the default `sim.seed`, `sim.RestoreEngine` derived the
seed from the round's World rather than from the empty World the server starts from (#307). Nothing
checks a restored Engine until the round's tick + 1. So the bad restore surfaced one tick later, as a
divergence on an idle tick, and read like an apply bug. It cost a sprint's investigation, and it
held AW-INF-025, AW-INF-008 and AW-SRV-019 on `dev`. A restore that compared itself with the round
tick's recorded Tick Boundary Record would have named the fault where it happened.

The same restore feeds server recovery (`AW-SRV-007`, SPRINT-04) and the state projector
(`AW-SRV-019`). M2's "matching State Hash" gate depends on recovery restoring the World exactly. So
the check belongs in `sim.RestoreEngine`, and both callers report it the same way. The seed is the
second gap. Today the default is derived again on each side, and nothing in the round records it.
Architecture asked for this story after #307 (`docs/feedback/AW-INF-025-projector-operations.md`,
"For architecture: #143"). It isn't on the M3 demo path.

## User story

As an operator, I want a restore from a Snapshot Round to prove that it reproduces the round's
recorded State Hash before anything replays on top of it, so that a bad restore is reported as a
bad restore rather than as a divergence one tick later.

## Scope

### In scope
- **(a) Verify at the round's tick.** After building the Engine, `sim.RestoreEngine` compares the
  restored Engine's `StateHash()` with `state_hash` in the round tick's `TickCompleted`. That's one
  World-wide hash per tick (`log.proto`). A mismatch is `ErrRestoreMismatch`, a distinct refusal,
  with no automatic fallback to an older round or to replay from zero. Each Zone object's own
  `SnapshotEnvelope.state_hash` check, which runs at decode, is unchanged.
- **The projector's exit and log contract,** which gains `restore_mismatch`.
- **(b) The seed in the round.** `SnapshotEnvelope` gains `sim_seed`. A restore refuses a round
  whose seed differs from the configured seed. A round written before the field reads `0`, meaning
  "derived", which is what #307 does.

### Out of scope
- Server recovery's own wiring. `AW-SRV-007` calls `RestoreEngine` and inherits the refusal. Its
  exit table gains the new code by architecture's amendment (Open questions, item 1), not here.
- Falling back to an older round, or to replay from zero, on a mismatch. That's refused by design:
  an operator decides.
- Any change to how rounds are written, beyond recording the seed.

## Acceptance criteria

1. **Given** a round whose restored Zones hash equal to its tick's `TickCompleted` **when**
   `RestoreEngine` runs **then** it returns the Engine, and replay proceeds as today.
2. **Given** a round with one Zone's body altered so that it decodes but hashes differently **when**
   `RestoreEngine` runs **then** it returns `ErrRestoreMismatch` naming the round tick and the
   recorded and restored hashes. No Engine is returned. The fixture re-signs the altered Zone's
   envelope hash, so that the existing per-object check passes and only the new check can catch it.
3. **Given** that round **when** the state projector bootstraps from it **then** it exits with the
   `restore_mismatch` code (`5`) and logs one `error` line, `state projector restore mismatch`,
   with `round_tick`, `reason=hash`, `recorded_hash` and `restored_hash` (hex). It writes no
   checkpoint, and it doesn't fall back to another round or to replay from zero.
   *(Field names aligned with §7 at contract review, 2026-10-02.)*
4. **Given** the same round under `--rebuild` **then** the outcome is the same as AC-3. A rebuild
   doesn't bypass the check.
5. **Given** a round written with `sim_seed = S` (non-zero) and an effective seed `S' ≠ S`
   **when** it's restored **then** `RestoreEngine` returns `ErrSeedMismatch` naming both, before
   any hash comparison, and the projector exits `5` and logs the same line with `reason=seed`,
   `recorded_seed` and `configured_seed`. The effective seed is `sim.seed` when it's set, and the
   derived default when it isn't, so AC-5 holds for a configured `S'` and for a derived one.
   *(Amended at contract review, 2026-10-02: "both non-zero" read as though an unset `sim.seed`
   skipped the check. The seed is in the State Hash, so without this check a seed mismatch would
   still fail AC-2, but as `reason=hash`, which names the wrong fault.)*
6. **Given** a round written before the field (`sim_seed` absent, so `0`) **then** the restore
   derives the seed as #307 does, and AC-1 or AC-2 applies.
7. **Given** a server with the default seed (derived) **when** it writes a round **then** the round
   records the derived value, not `0`. A later restore compares like for like.
8. **Given** #143's original fault, the pre-#307 default-seed derivation, reintroduced in a test
   **then** AC-2's mismatch fires at the round's tick, not a divergence at tick + 1. This is the
   regression test that proves the check would have named #143.

## Interface contract

```go
// CONTRACT SKETCH — not an implementation
var ErrRestoreMismatch = errors.New("restore mismatch")  // wraps *RestoreMismatch
var ErrSeedMismatch    = errors.New("seed mismatch")

type RestoreMismatch struct {
    RoundTick          uint64
    Recorded, Restored []byte // World State Hash: TickCompleted.state_hash vs Engine.StateHash()
}

// RestoreEngine(w, templates, cfg, r) — unchanged signature. RoundState gains
// RecordedHash []byte (TickCompleted.state_hash at r.Tick) and SimSeed uint64.
// The caller supplies RecordedHash from the round tick's Tick Boundary Record.
```

- **Protocol:** `SnapshotEnvelope` gains `uint64 sim_seed = 10;`. `0` means a round written before
  the field, for which the seed is derived. **Pinned** in
  `docs/specs/protocol/andara/state/v1/snapshot.proto` at contract review (2026-10-02), with `gen/`
  regenerated in the same PR. `state_version` doesn't change, because the field is additive.
- **Projector exit codes (`AW-SRV-019`):** add `5` `ExitRestoreMismatch`, which covers both
  `ErrRestoreMismatch` and `ErrSeedMismatch`. Codes `0`–`4` are unchanged. **Confirmed** by
  architecture, 2026-10-02.
- **Server recovery (`AW-SRV-007`):** its exit table gains `6` `ErrRestoreMismatch`, covering both
  errors, distinct from `8` `ErrHashMismatch` (a mismatch after replay). **Confirmed** by
  architecture, 2026-10-02, and amended into `AW-SRV-007` in the same PR. The server's `5` is
  `AW-SRV-026`'s `ExitBoundaryLost`, so `AW-SRV-007`'s incomplete-round exit moves from `5` to `7`.
  Exit `6` sets `andara_recovery_state_hash_match` to `0` and lingers under
  `recovery.mismatch_linger` as the hash mismatch does (SRE's proposal, accepted). The hash mismatch
  itself moved from `2` to `8` in the same review, because Go exits `2` on a panic.
- **Order inside `RestoreEngine`:** state version, content digest, seed, build, hash. The seed check
  comes before the build, so a seed mismatch never reports as `reason=hash`.
- **Where the round tick's boundary is read:** the caller positions its boundary reader at the
  round's tick, not after it, reads that `TickCompleted` for `RecordedHash`, and replays from
  tick + 1. For the projector that's `BoundaryReader.SeekAfter(round − 1)`; for recovery it's
  `AW-SRV-007`'s shared seek.
- **Where the recorded hash comes from:** `TickCompleted.state_hash` (field 3) for the round's
  tick. A round whose tick has no readable `TickCompleted` is `ErrLogGap`, as today.

## Data / state impact

`SnapshotEnvelope` gains one additive field. Older rounds read `0` and keep restoring as they do
after #307. The field doesn't migrate and has no rollback concern: an older binary ignores it. The
next round any server writes carries it.

## Observability requirements

*SRE review, 2026-10-02: amended. The changes are recorded in
`docs/feedback/AW-SRV-043-restore-verifies-round.md`.*

`sim` stays dependency-free (CLAUDE.md §10), so it emits nothing. Each caller of `RestoreEngine`
records the outcome it gets back.

- **Metrics:** `andara_restore_total{caller, outcome}` (counter).
  - `caller` is one of `projector`, `recovery`, or `verify`. `verify` is `AW-SRV-007`'s
    `Admin.VerifySnapshotRound`, which restores a scratch Engine in the server process.
  - `outcome` is one of `ok`, `hash_mismatch`, or `seed_mismatch`.
  - A process pre-seeds at 0 only the callers it hosts: 3 series in `andara-projector`, 6 in
    `andara-server`, and 9 in total. Zone ID and tick are never labels.
  - This story wires and pre-seeds the projector's 3. The server's 6 are wired by `AW-SRV-007`,
    which owns both server callers. `andara-server recover --verify`, the one-shot, also counts as
    `verify`. This story's §8 check observes only the projector's series.
  - The projector exits `5` on a mismatch, so its `hash_mismatch` series restarts at 0 before any
    scrape reaches it. That's acceptable, because no alert reads it. Exit `5` is the projector's
    signal on a real backend. The live observation is `outcome="ok"` after each bootstrap. The
    mismatch series is verified on the metric object in the integration test (CLAUDE.md §8). That
    means the projector's in-process registry, read with `testutil` after `Run` returns, not a
    scrape of the stack after the process exits. *(The Test plan's Integration line was reworded
    to match at contract review, 2026-10-02.)*
- **Logs:**
  - `error` `state projector restore mismatch`, with `round_tick`, `reason` (`hash` or `seed`),
    and `trace_id`.
    - For `hash`: `recorded_hash` and `restored_hash`, in hex. That follows the
      `<role>_hash` convention of the existing `state projector diverged` line, whose pair is
      `recorded_hash` and `replayed_hash`.
    - For `seed`: `recorded_seed` and `configured_seed`.
  - `info` `state projector restore verified`, with `round_tick`, `zones`, `restored_hash`, and
    `trace_id`, once per bootstrap.
  - Recovery's pair is `AW-SRV-007`'s, `recovery restore mismatch` and `recovery restore verified`,
    with the same fields and that story's required fields.
- **Traces:**
  - The projector has no bootstrap span today. Its bootstrap only opens `state.replay` and
    `state.verify`. This story adds `state.bootstrap`, a root around round selection, restore,
    and the dump, with `round_tick`, `rebuild`, and `outcome` as attributes.
  - Under it sits `restore.verify`, which wraps the `RestoreEngine` call: build, then compare. It
    carries `round_tick`, `zones`, and `outcome`, with span status `Error` on a mismatch. In
    recovery it's a child of `recovery.run`, after `recovery.load_snapshot`. In `verify` it's a
    child of the `Admin.VerifySnapshotRound` server span.
  - Every one of these roots is always sampled, since only `Game/Submit` is ratio-sampled
    (`server/telemetry/sampling.go`).
- **Alerts:** none new. Each failure reaches an existing symptom alert:
  - The projector's exit `5` restarts it in a loop. That's `StateProjectorDown` (ticket, 10 min).
    `docs/runbooks/state-projector-down.md`'s "Respond, by the last exit code" table gains a `5`
    row: capture the round tick and both hashes, and don't `--rebuild`, because a rebuild hits
    the same check (AC-4). SRE writes the row at this story's §8 instrumentation check.
  - *(Decided at contract review, 2026-10-02: exit `6` sets the gauge to `0` and lingers, and
    `restore` is a `reason`. Recovery's hash mismatch, "exit `2`" below, is now `8`.)*
  - Recovery's exit `6`, under `AW-SRV-007`, leaves no ready server. That's
    `AndaraServerUnavailable` (page). SRE proposes that it also sets
    `andara_recovery_state_hash_match` to `0`, and lingers under `recovery.mismatch_linger`
    as exit `2` does, so that `RecoveryStateMismatch` names the cause
    wherever that alert can see it. See `AW-SRV-007`'s SRE amendment. The row in
    `server-unavailable.md`'s symptom table ships with `AW-SRV-007`'s
    `recovery-state-mismatch.md`.
  - `andara_recovery_failures_total{reason}` (`AW-SRV-007`) gains `restore`, for exit `6`. That's
    architecture's amendment, together with the exit code (Open questions, item 1).
- **Dashboard:** no panel. One outcome per bootstrap isn't a time series anyone watches.

## Test plan

- **Unit (`sim`):** ACs 1, 2 and 5–8. The altered-body round, the seed mismatch, the pre-field
  round, a recorded default seed, and #143's fault reintroduced.
- **Unit (`projector`):** ACs 3 and 4, covering the exit code, the log line, no checkpoint and no
  fallback.
- **Integration:** against the local stack's Redpanda and a filesystem snapshot store, a projector
  bootstrapped from a deliberately corrupted round: `projector.Run` returns the mismatch, mapped to
  exit `5`, and in the same test the projector's in-process registry, read with `testutil` after
  `Run` returns, has `andara_restore_total{caller="projector", outcome="hash_mismatch"}` at `1` and
  `outcome="ok"` at `0`. No scrape: the process that counted it would have exited (SRE item 7,
  reworded at contract review 2026-10-02). A second case does the same for a seed mismatch.
- **Manual/operator:** `make projector-rebuild ENV=dev` on a healthy `dev` logs `restore verified`.

## Definition of done

CLAUDE.md §8.

## Open questions

1. **Resolved 2026-10-02 (architecture): the exit codes.** Projector `5` and recovery `6`, as
   proposed. `AW-SRV-007`'s incomplete-round exit moves from `5` to `7`, because `AW-SRV-026` holds
   the server's `5`. Recorded in `AW-SRV-007`'s body.
2. **Resolved 2026-10-02 (architecture): the `AW-SRV-007` edge.** Added in the commit that moves
   this story to `ready`.
3. `[ASSUMPTION]` Planned for SPRINT-04, ahead of `AW-SRV-007` (architecture, 2026-10-01).

## Verification record — 2026-10-03 (implementation; `review` until the §8 checklist passes)

Branch `impl/aw-srv-043-restore-verifies-round`.

| AC | Test | What it asserts |
|----|------|-----------------|
| 1 | `TestRestore_VerifiesItsRound` (sim, on the derived and a configured seed); `TestRun_ASoundRoundVerifies` (Redpanda) | The round restores, and the tick after it hashes as the live World's. The projector counts `ok`, logs `state projector restore verified` with the round tick and the recorded hash, and catches up |
| 2 | `TestRestore_AlteredBodyIsARestoreMismatch` (sim); `TestRun_ACorruptedRoundExitsFive` (Redpanda, the body altered and its envelope re-signed so the per-object check passes) | `*RestoreMismatch` naming the round tick and both hashes, and no Engine |
| 3 | `TestRun_ACorruptedRoundExitsFive/rebuild=false` | Exit `5`. One `error` line with `round_tick`, `reason=hash`, `recorded_hash`, `restored_hash` and `trace_id`. `andara_restore_total{caller="projector",outcome="hash_mismatch"}` reads 1 and `ok` reads 0 on the in-process registry. No checkpoint, and nothing on the state topic |
| 4 | `TestRun_ACorruptedRoundExitsFive/rebuild=true` | The same under `--rebuild` |
| 5 | `TestRestore_SeedMismatch` (sim: configured against configured, derived against configured, configured against derived, with the recorded hash also spoiled); `TestRun_ARoundFromAnotherSeedExitsFive` (Redpanda) | `*SeedMismatch` naming both seeds, before any hash comparison. Exit `5`, `reason=seed` with `recorded_seed` and `configured_seed`, and `seed_mismatch` counted |
| 6 | `TestRestore_PreFieldRound` | A round with `sim_seed` 0 restores on the derived seed. One written on another seed is then a hash mismatch, not a seed mismatch |
| 7 | `TestRestore_RoundRecordsTheDerivedSeed`; `TestRestore_DefaultSeedIsTheOneTheWorldStartedWith`; `TestRestoredEngineContinuesTheWorld` (store) | A default-seed server records the derived value, never 0, and a configured one records its own. The store decodes it, and every Zone in a round must agree |
| 8 | `TestRestore_Issue143IsNamedAtTheRoundTick` | The pre-#307 derivation, from the round's World, is a `*RestoreMismatch` at the round's tick |

**§7, tested:** `state.bootstrap`, with `round_tick`, `rebuild` and `outcome`, has `restore.verify`
as its child, with `round_tick`, `zones`, `outcome` and `Error` status on a mismatch.
`TestRun_ACorruptedRoundExitsFive` asserts both.

**Mutations, each caught:**
- dropping the hash comparison;
- dropping the seed check;
- writing `sim_seed` as 0;
- not counting restores;
- renaming the mismatch line.

**Changed beyond the story's surface:**
- **The recorded hash is mandatory.** A `RoundState` with no `RecordedHash` is refused, so a
  caller that never read the round tick's boundary can't skip the check. `AW-SRV-007`'s callers
  must supply it.
- **`sim.RestoreOutcome`** maps `RestoreEngine`'s error to the outcome label, so `AW-SRV-007`'s
  callers count the same way.
- **`sim.EffectiveSeed`** is the configured-or-derived seed.

**Outstanding before `done`:**
- ~~SRE's §8 instrumentation check, observing `outcome="ok"` after a bootstrap, and the exit `5`
  row in `docs/runbooks/state-projector-down.md`.~~ Done: see the §8 instrumentation check below.
- ~~The operator step, `make projector-rebuild ENV=dev` logging `restore verified`.~~ Done on
  2026-10-04: see the §8 operator step below.

## §8 instrumentation check — 2026-10-03 (SRE, `sre/aw-srv-043-verify`)

**The projector's §7 instrumentation passes on the local stack.** The local compose has no
projector service, so the check ran the built `andara-projector state --rebuild` on the host against
the stack's Redpanda, the server's real snapshot rounds (`.local/data/snapshots`, the `fs` store)
and its OTLP collector. The compose has no projector writer, so the host run is the only one on the
consumer group, which is what makes `--rebuild` safe here (`projection-stale.md` forbids it beside a
live projector). It is `make stack-projector-check` (`scripts/stack_projector_check.sh`), needing
`make up` and `make build`. It fails unless the metric, the log line and the Tempo spans below are
all observed, and the `stack` workflow runs it after `stack-linkdead`. At `main` 37e3a1b:

- **Metric, scraped from the projector's own `/metrics`:**
  ```
  andara_restore_total{caller="projector",outcome="hash_mismatch"} 0
  andara_restore_total{caller="projector",outcome="ok"} 1
  andara_restore_total{caller="projector",outcome="seed_mismatch"} 0
  ```
  All 3 series are pre-seeded and `ok` counted the bootstrap.
- **Log:** `info` `state projector restore verified`, with `round_tick` (1733786), `zones` (4),
  `restored_hash` and `trace_id`.
- **Traces, read back from Tempo by that `trace_id`:** `state.bootstrap` (root; `rebuild=true`,
  `round_tick`, `outcome=ok`) with `restore.verify` as its child (`round_tick`, `zones=4`,
  `outcome=ok`).
- **Mismatch paths:** `TestRun_ACorruptedRoundExitsFive` (both `--rebuild` settings) and
  `TestRun_ARoundFromAnotherSeedExitsFive` pass against the stack's Redpanda
  (`ANDARA_KAFKA_BROKERS=localhost:9092`). The mismatch series is read on the in-process registry,
  as the Observability section specifies, not scraped.

**Not yet emitted by a running process:**
- `andara_restore_total` for `caller="recovery"` and `caller="verify"` (6 series in
  `andara-server`). `AW-SRV-007` wires them. Its Definition-of-done line carries the live
  observation for `recovery` only; `verify` (`Admin.VerifySnapshotRound`, `recover --verify`) has no
  DoD line, which goes to PM and architecture in `docs/feedback/AW-SRV-043-restore-verifies-round.md`.
- The mismatch series and the `error` line from a running projector: it exits `5` before a scrape.
  The integration tests cover them.
- The operator step, `make projector-rebuild ENV=dev`, needs this build on `dev`, which is
  tailnet-only.

**Runbook:** `docs/runbooks/state-projector-down.md`'s exit `5` row no longer says it ships with
this story.

## §8 operator step — 2026-10-04 (SRE, `sre/aw-srv-043-verify-dev`)

**`make projector-rebuild ENV=dev` ran on `dev` and the restore verified.** `dev` was Synced and
Healthy at `main@b335936` (the Application's current build, which contains this story), and its
projector runs `ghcr.io/valesordev/andara-server:dev@sha256:8ac96a51…`, built from `b335936`. The
run, at 2026-10-04T15:02Z:

```
projector-stop: stopped (group andara-projector-state-dev empty) in 2s
projector-rebuild: Job andara-projector-state-rebuild created; waiting for `state projector caught up`
projector-rebuild: caught up at tick 1584989; stopping the Job
projector-start: ready in 7s
projector-rebuild: rebuilt to tick 1584989 in 17s
```

The projector `projector-start` brought up straight after (`andara-projector-state-7d6899b757-2hfjl`)
bootstrapped from the newest snapshot round on `dev`, and logged:

```
state projector restore verified   round_tick=1584757 zones=4
  restored_hash=8a04504a4089d7ec4af61f049016476fe99c69a827e465655f4ae3bb5d13984a
state projector started            round_tick=1584757 rebuild=false from_zero=false
state projector caught up          tick=1585066
```

Its own `/metrics`, read from the pod, has `andara_restore_total{caller="projector",outcome="ok"} 1`
and `hash_mismatch` and `seed_mismatch` at `0`.

**What this does and doesn't show.** The rebuild Job's own log wasn't kept: `projector-rebuild`
deletes the Job once it has caught up, so its `--rebuild` bootstrap line isn't in this record, and
which round the Job restored isn't recorded either. The evidence is the Deployment pod's bootstrap
(`rebuild=false`): it goes through the same `bootstrapEngine` and `RestoreEngine` check as a
`--rebuild` one, on the newest complete round, and logged `restore verified` for round 1584757.

The Job finishing shows less than that. `projector-rebuild` waits for `state projector caught up`,
and a Job that hit a mismatch would have exited 5 and failed instead, but a rebuild that finds no
complete round bootstraps from offset zero, runs no restore, and also reaches `caught up`
(`server/projector/run.go`). This run had rounds on the store, as the pod's line shows, so it isn't
that case, but the Job's success doesn't prove it. Two follow-ups, neither this story's, filed as issue #396:
- `projector-rebuild` should require `restore verified` in the Job's log before it deletes the Job.
- Its `Failed` message lists exits 2, 3 and 4 and omits 5 (`scripts/projector.py`); it should name
  the restore mismatch.
