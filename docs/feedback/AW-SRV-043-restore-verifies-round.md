# AW-SRV-043: a restore verifies its round

Story: `AW-SRV-043` (`draft`).

## For architecture: SRE observability review, 2026-10-02

Amended. The cardinality bound and "no alert" are accepted. Six changes to §7:

1. **`caller` gains `verify`.** `AW-SRV-007`'s `Admin.VerifySnapshotRound` restores a scratch
   Engine in the server process, so it's a third caller. So is the one-shot
   `andara-server recover --verify`. Each process pre-seeds only the callers it hosts: projector 3
   series, server 6, 9 in total. This story wires the projector's. The server's 6 are wired by
   `AW-SRV-007`, which owns both callers. `sim` emits nothing, because it stays dependency-free
   (§10). The callers count.
2. **The projector's mismatch series is never scraped.** Exit `5` restarts the process at 0. No
   alert reads that series, so no `haltLinger` is needed. The live observation is `outcome="ok"`,
   and the mismatch series is asserted on the metric object in the integration test (§8).
3. **Log fields follow the divergence line's `<role>_hash` hex convention:**
   `recorded_hash`/`restored_hash`, plus
   `recorded_seed`/`configured_seed` for `reason=seed`. AC-5 didn't name the seed fields. The
   success line becomes `state projector restore verified`, under the projector's prefix.
   **AC-3 names the fields `recorded` and `restored`.** Align the AC with §7 at contract review.
   That's your section.
4. **No "existing restore span" exists in the projector.** Its bootstrap opens only `state.replay`
   and `state.verify` (`server/projector/run.go`). §7 adds a `state.bootstrap` root, with
   `restore.verify` under it. `restore.verify` wraps the `RestoreEngine` call, because `sim` can't
   open a span between build and compare.
5. **Runbooks.** The projector's exit `5` surfaces as `StateProjectorDown`.
   `state-projector-down.md`'s exit table gains a `5` row, which SRE writes at this story's §8
   check. Recovery's exit `6` goes in `server-unavailable.md` with `AW-SRV-007`'s runbook. SRE
   proposes that exit `6` also sets `andara_recovery_state_hash_match` to `0`, and lingers under
   `recovery.mismatch_linger` as exit `2` does. The reasoning is
   in `AW-SRV-007`'s amendment. If you number the exit, decide that with it.
6. **`andara_recovery_failures_total{reason}` gains `restore`** when you number recovery's exit.
   It's noted in `AW-SRV-007`'s amendment.
7. **The Test plan's Integration line can't pass as worded** (Codex on #352). It asserts
   `andara_restore_total{caller="projector", outcome="hash_mismatch"}` is 1 "against the local
   stack", after the projector has exited `5`. The process that counted it is gone, and its
   replacement pre-seeds it at 0. That's item 2 above. Reword it to read the projector's
   in-process registry with `testutil`, after `projector.Run` returns the mismatch, in the same
   test that bootstraps from the corrupted round. The Test plan is yours; §7 now says so.

## Architecture: contract review, 2026-10-02

Moved to `ready`. The amendments are in the story body.

- **Open question 1, exit codes:** projector `5` and recovery `6`, as proposed.
  - The server's `5` is already `AW-SRV-026`'s `ExitBoundaryLost` (PR #355), so `AW-SRV-007`'s
    incomplete-round exit moves from `5` to `7`.
  - `AW-SRV-007`'s exit table is amended to match.
- **Open question 2, the edge:** `AW-SRV-007` depends on this story, and this story blocks it.
- **`sim_seed` is pinned** as field `10` in `state/v1/snapshot.proto`, with `gen/` regenerated.
  Implementation doesn't touch the `.proto`.
- **SRE items 1–6:** accepted as amended in §7. On item 5, exit `6` sets the hash-match gauge to
  `0` and lingers like exit `2`. That's in `AW-SRV-007` as AC-13 and AC-14.
- **SRE item 7:** the Test plan's Integration line now reads the projector's in-process registry
  with `testutil` after `Run` returns. It no longer scrapes.
- **AC-3:** the fields are `recorded_hash` and `restored_hash`, aligned with §7.
- **AC-5:** compares with the **effective** seed. That's `sim.seed` if set, otherwise the derived
  default. The draft's "both non-zero" would have let an unset seed skip the check. Because the
  seed is in the State Hash, AC-2 would then have caught it, but as `reason=hash`.
- **Added to the contract:**
  - The check order inside `RestoreEngine`: version, digest, seed, build, hash.
  - The caller reads the round tick's own `TickCompleted`. For the projector that's
    `SeekAfter(round − 1)`.

### For implementation
`sim.RestoreEngine` and the projector's bootstrap. The server's half is `AW-SRV-007`: both server
callers, and pre-seeding `andara_restore_total`'s 6 server series.
