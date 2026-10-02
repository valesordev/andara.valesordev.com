---
id: AW-SRV-043
title: A restore verifies its round
epic: EPIC-04
component: server
type: feature
status: draft
size: M
depends_on: [AW-SRV-006, AW-SRV-019]
blocks: []
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
  exit table gains the new code by architecture's amendment (Open questions, item 2), not here.
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
   `restore_mismatch` code and logs one `error` line, `state projector restore mismatch`, with
   `round_tick`, `recorded` and `restored`. It writes no checkpoint, and it doesn't fall
   back to another round or to replay from zero.
4. **Given** the same round under `--rebuild` **then** the outcome is the same as AC-3. A rebuild
   doesn't bypass the check.
5. **Given** a round written with `sim_seed = S` and a configured seed `S' ≠ S` (both non-zero)
   **when** it's restored **then** `RestoreEngine` returns `ErrSeedMismatch` naming both, and the
   projector exits with the `restore_mismatch` code and logs the same line with `reason=seed`.
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
  the field, for which the seed is derived. Architecture pins it in `state.v1` at contract review,
  and the implementing PR regenerates `gen/` with `make proto`. `state_version` doesn't change,
  because the field is additive.
- **Projector exit codes (`AW-SRV-019`):** add `5` `ExitRestoreMismatch`, which covers both
  `ErrRestoreMismatch` and `ErrSeedMismatch`. Codes `0`–`4` are unchanged. *(PM's proposal. The number
  is architecture's to confirm.)*
- **Server recovery (`AW-SRV-007`):** its exit table gains `ErrRestoreMismatch`, distinct from `2`
  `ErrHashMismatch` (a mismatch after replay). Architecture amends `AW-SRV-007`. PM proposes `6`, since
  `5` is taken.
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
    scrape of the stack after the process exits. The Test plan's Integration line still reads as a
    scrape, and the feedback file asks architecture to reword it.
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
- **Integration:** against the local stack, a projector bootstrapped from a deliberately corrupted
  round in the snapshot store exits `5`, and `andara_restore_total{caller="projector",
  outcome="hash_mismatch"}` is 1.
- **Manual/operator:** `make projector-rebuild ENV=dev` on a healthy `dev` logs `restore verified`.

## Definition of done

CLAUDE.md §8.

## Open questions

1. **For architecture: the exit codes.** Projector `5` and recovery `6` are PM's proposal. Confirm
   them or renumber, and amend `AW-SRV-007`'s exit table, which is `ready`, so it's yours to change.
2. **For architecture: the `AW-SRV-007` edge.** `AW-SRV-007` should depend on this story, so that
   recovery is built on a verifying restore. `make validate-stories` refuses a `ready` story that
   depends on a `draft`, so add `AW-SRV-043` to its `depends_on` (and `AW-SRV-007` to this story's
   `blocks`) in the commit that moves this story to `ready`.
3. `[ASSUMPTION]` Planned for SPRINT-04, ahead of `AW-SRV-007` (architecture, 2026-10-01).
