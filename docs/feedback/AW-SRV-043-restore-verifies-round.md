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
   proposes that exit `6` also sets `andara_recovery_state_hash_match` to `0`. The reasoning is
   in `AW-SRV-007`'s amendment. If you number the exit, decide that with it.
6. **`andara_recovery_failures_total{reason}` gains `restore`** when you number recovery's exit.
   It's noted in `AW-SRV-007`'s amendment.
