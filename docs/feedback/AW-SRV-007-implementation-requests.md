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
4. **A second `VerifySnapshotRound` while one runs.** Each verify holds a whole scratch Engine in the serving process
   (hundreds of MB at the sizing fixture), so the server runs one at a time and refuses the next with
   `FAILED_PRECONDITION`, ErrorInfo reason `verify_busy`. The pinned `admin.proto` lists the statuses and not the
   reasons, so this is inside it; tell me if you'd rather it queue.

## Architecture: the rulings (2026-10-05)

Two of the four change the contract (1 and 3) and need implementation. The story is amended; it stays at
`review`, because AC-16 and AC-17 are new and neither is built.

### 1. The owned Zone set comes from the round's own recorded content (AC-16)

Your reading, the loaded content's Zones, is wrong in a case that isn't rare. A round is taken every 60 s
and a content swap can land any time after it, so a swap that adds or removes a Zone between the last round
and the kill is routine. The round then lists `incomplete` for a Zone the current content lacks, and
recovery falls back to an older round or to a full-log replay, which is the RTO this story exists to hold, or
exit `3` if retention doesn't reach back. An extra Zone in the current content fails as exit `6` for a round
that is fine.

The round already says which Zones it covers. Every envelope carries `content` and `content_digest`
(`snapshot.proto` fields 8 and 9: "Every Zone's envelope in a round carries the same values"), and the field's
own comment says recovery resolves those versions and rebuilds the topology before loading the body. So:
- read one envelope's header from the group, resolve its `content` (as recovery must anyway), and take that
  topology's Zones, narrowed to the Partitions this process owns (all 64 today, ADR-0002), as the owned set
  for that round. Completeness is then judged against it, and per round, so `ListRounds` and `NewestComplete`
  take the set from each group and not from one argument;
- a Zone object the round's content doesn't list is the existing `ErrRoundZoneUnknown` (exit `6`,
  `reason=content`). A listed Zone with no object is `missing`;
- envelopes in one group that disagree on `content` are `disagree` (AC-11's list already names it);
- content that can't be resolved at those versions is exit `6` with `reason=content` naming the pack
  versions. It is not `incomplete`: nothing is wrong with the round's objects.

A World with no content yet writes no round, so "no owned Zones, vacuously complete" can't arise from a real
round. The test for it is a `ListRounds` over a store with no objects.

**Test (AC-16):** write a round at V, swap content to V+1 with an added Zone (and again with a removed one),
kill, recover: the round is selected and the swap replays. `server/store` also needs the unit case the other
way: an object for a Zone the round's content doesn't list.

### 2. "Consumer lag" in Ready is the loop's schedule lag, as you read it

Your reading stands, and the story is amended to say so. `andara_consumer_lag` is offsets per Partition (64
series) and has no duration to compare with `sim.tick_budget_ms × 10`. `andara_simulation_lag_seconds` is the
SLI `docs/specs/slo/tick-health.md` already names. What it doesn't give is "the commands backlog is drained",
and I'm not adding that: commands accepted before the crash are applied in order at `max_per_tick`, visible on
`andara_tick_deferred_records`, so a long backlog delays players and loses nothing. I would revisit if
`/readyz` turning true while a backlog drains shows up as player-visible lag in an RTO test. The amendment
says the consequence.

### 3. Bodies a crash left present: your reading is the contract, with one hole closed (AC-17)

The story's inherited line already says a body present with no Session is marked linkdead at recovery. Your
reading (every Character body, `Template == andara.core.Character`, a `MarkLinkdead` each, an
`UnbindCharacter{QUIT}` when `session.linkdead_grace` is `0`, before the loop runs; an NPC untouched) is that
line, and `AW-SRV-014`'s README line "nothing at boot invents an unbind" becoming "nothing but this" is right.

The hole: a Character in a `Transit` record at the kill isn't in any Zone's `Entities`, so a sweep over
present bodies skips it, and `MarkLinkdead` rejects `in_transit` (my `AW-SRV-028` ruling, item 3). The
retry then lands it, non-linkdead and with no Session. `BindCharacter` doesn't reject a present,
non-linkdead body (`AW-SRV-015` struck `ErrNotLinkdead`), so the Account can still rebind it. But until it
does, nothing times the body out: it stays in the world with no linkdead deadline, which is the "present
forever" the story's inherited line is there to prevent. The sweep therefore keeps the IDs of Characters it found in `Transit` and retries their mark until the Entity is
placed or gone. That is AC-17. The existing `TestRecoveryWithAHandoffInFlight` is the natural place to add
a Character.

### 4. `verify_busy` stands

One verify at a time, refused with `FAILED_PRECONDITION` and reason `verify_busy`, is inside the pinned
`admin.proto` (it lists statuses, not reasons). A queue would hold a hundreds-of-MB scratch Engine per
waiting caller. It's in the story's error taxonomy now. `snapshot verify` exits `1`.

### SRE's correction (2026-10-05)

Applied: the story's amendment no longer says compose has no restart policy. It says a refused recovery
loops under `restart: on-failure` and that `keep_firing_for` bridges the stale gaps and holds the page after
the loop is stopped.

