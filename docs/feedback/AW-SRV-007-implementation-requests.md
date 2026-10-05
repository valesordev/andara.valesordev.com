# AW-SRV-007 — implementation's requests (2026-10-04)

Raised by the implementation lane while building `server/recovery`. Each heading names the role that
should answer.

## SRE

1. **`recovery.mismatch_linger` Helm key.** The server now reads `ANDARA_RECOVERY_MISMATCH_LINGER`
   (duration, default `0s`, no negatives), so `make values-schema-check` will fail until
   `deploy/helm/andara/keys.yaml` lists it. Suggested line, beside the other `recovery.*` keys:
   `{key: recovery.mismatch_linger, env: ANDARA_RECOVERY_MISMATCH_LINGER, type: duration, default: 0s, story: AW-SRV-007}`.
   Compose sets `60s` (AC-14).
2. **Everything else in `deploy/` the story assigns to SRE's ops commit:** the `RecoveryStateMismatch`
   rule (`for: 0m`, `keep_firing_for: 15m`), `docs/runbooks/recovery-state-mismatch.md`, the CI job that
   runs the kill-and-recover integration test and publishes `recovery-timing.json` (AC-6, AC-7), and a
   replacement for the `make stack-boundary-lost` read-back.
3. **Exit codes.** The server's exit codes are now `0/1/3/4/5/6/7/8` as the story's table says. Anything
   in `deploy/` that reads "exit `2`" for a mismatch reads `8`.

4. **The integration tests need a CI job.** `make test-integration` runs the broker tests under `-tags
   integration` (`TestRecoveryAgainstTheBroker`, `TestRecoveryWithAHandoffInFlight`,
   `TestFiftyCharactersSurviveAKill`), and AC-6 wants them to gate every `server/sim` and `server/store`
   change. Two are off unless a variable is set, because they run for minutes and carry hundreds of MB of
   broker: `ANDARA_RECOVERY_TIMING=1` (AC-7, writes the file named by `ANDARA_RECOVERY_TIMING_OUT`, which is
   `recovery-timing.json`) and `ANDARA_AC12_HISTORY_TICKS=864000` (AC-12). The CI job should set the first and
   publish the artifact; the second is a measurement for a throwaway broker.
5. **`make stack-boundary-lost`'s read-back.** It requires `ticks_replayed == tick`. Boot's `recovered from the
   log` line now also carries `round_tick`, and `ticks_replayed` is the tail past the round, so on a snapshot
   recovery that check fails. The invariant it wants is `round_tick + ticks_replayed == tick`.
6. **Compose.** `ANDARA_RECOVERY_MISMATCH_LINGER=60s` on the server, and the `RecoveryStateMismatch` rule as the
   story's SRE amendment describes. The metric names and the gauge's register-on-first-set behavior are as
   that amendment says.

## Architecture

1. **Which Zones does a recovering server own?** `store.ListRounds`/`NewestComplete`/`RoundAt` need the
   owned Zone set to judge a round complete, and the store can't enumerate Zones (`WorldStore.List` takes
   one). The server boots on `sim.EmptyWorld` and learns its content from the log (AW-SRV-012), so the set
   isn't known until a round or a replay has told it. I'm passing the Zones of the content source's
   current World (`rt.World`, as the projector does). That is wrong in two cases the contract doesn't
   cover: a content swap that added or removed a Zone between the round and the current content (the round
   then lists `incomplete` for a missing Zone, or restores as exit `6`/`reason=content` for an extra one),
   and a World still waiting for its first content (no owned Zones, so every round is vacuously complete
   or none is). The strictly correct set is the Zones of the content the round recorded, which needs a
   two-step read (`state.Content` first, then completeness). Please rule on whether recovery should
   discover the owned set from the round's own recorded content, or whether the current content's Zones
   is the contract.

2. **"Consumer lag under `sim.tick_budget_ms × 10`" (Ready, in the contract's sequence).** The contract gives a
   duration for a quantity that the loop reports in two units: `andara_consumer_lag` is offsets, and
   `andara_simulation_lag_seconds` is how far the loop is behind its schedule. I read it as the second: `/readyz` is
   `200` once the first live tick has completed and the loop's schedule lag is under ten tick budgets. The
   schedule is anchored at the loop's start, so after a long recovery it starts at 0 and this is a check that the
   first ticks can keep pace, not that the log's tail is consumed (recovery has already applied every recorded
   boundary). If the contract meant the commands backlog, it needs an offset bound or a conversion; tell me which.
3. **A body a crash left present.** AW-SRV-015's inherited line says it is marked linkdead at recovery. I read "no
   Session" as every Character body present and not linkdead after recovery, since every Session is gone at a
   restart, and produce a `MarkLinkdead` for each (an `UnbindCharacter{QUIT}` when `session.linkdead_grace` is
   `0`, as `ReleaseSession` does) before the loop runs. `AW-SRV-014`'s README line "Nothing at boot invents an
   unbind" is now "nothing but this". An NPC is untouched (the test is `Template == andara.core.Character`).
