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
