<!--
SPDX-FileCopyrightText: 2026 Valesor Development
SPDX-License-Identifier: CC-BY-SA-4.0
-->

# StateProjectorDiverged

**Alert:** `andara_state_digest_mismatches_total > 0`. **Severity:** ticket.
**SLO:** `docs/specs/slo/projection-freshness.md` (integrity, no budget). **Ships with:** `AW-SRV-019`.

## What fired, and what the player is experiencing

**Nothing a player can see.** The state projector replays the same Commands at the same recorded
tick boundaries as the live server, and after every tick it compares its State Hash with the one
the server recorded. They differed. The projector has stopped: it produced nothing for the tick that
diverged, committed nothing past the tick before it, holds `/metrics` up for a minute so this alert
can see it, and exits `2`. Kubernetes restarts it, and it will diverge again at the same tick.

What that means: `andara.state.v1`, and every index built from it (Redis, Postgres), is **frozen at
the last good tick**. It is not corrupt, just old, and it stays old until this is resolved. Builder
and operator tools read an old World. The game does not read these indexes at all.

## How to confirm

The projector's last `error` line names everything:

```
kubectl logs deploy/andara-projector-state --previous | grep 'state projector diverged'
```

It carries `tick` (the tick that diverged), `recorded_hash` and `replayed_hash` in full, and
`last_good_offsets`, the next-to-read offset per `andara.commands.v1` Partition after the last
verified tick. The pod's last exit code is `2`.

## Respond, in this order

1. **Stop the downstream projectors** (`AW-SRV-017`, `AW-SRV-018`) if they are running. They are
   reading a topic that will not advance, and their own staleness alerts are this one's echo.
2. **Rebuild once:**
   ```
   andara-projector state --rebuild
   ```
   The rebuild wipes the consumer group, restores the newest complete snapshot round, replays the
   log from it, and rewrites the topic to match. If the divergence came from something the
   projector held (a bad in-memory state, a round it restored from and then disagreed with), this
   clears it, and the projector goes ready with `andara_state_digest_mismatches_total` at 0.
3. **If it diverges again, at the same tick or at another, the server is non-deterministic.** Two
   processes applied the same records from the same state and reached different hashes. That is a
   simulation bug. It means recovery (`AW-SRV-007`) would also refuse to reproduce the World the
   server is running. **Escalate as a sim bug**, with the tick, both hashes, and the round the
   rebuild used. Leave the projector stopped. A projector that restarts in a loop only repeats the
   evidence.

## Known causes worth ruling out before escalating

| Check | Means |
|-------|-------|
| The projector and the server loaded different content | The replica derives its seed from the World, as the server does. Different content means a different seed and a divergence at tick 1. Compare the `content_version` in both processes' boot logs. The projector must follow the same `content.packs`. |
| `ANDARA_SIM_SEED` differs between the two | The projector reads the server's ConfigMap. A seed set on the server's StatefulSet alone, and not in the ConfigMap, is not seen. |
| The divergence tick is a tick where a Zone faulted | Replaying through a fault does not reproduce it today. The panicking record stays unapplied and the boundary excludes it. `AW-SRV-027` owns making that exact. Until it lands, this divergence is known and expected. |
| A content swap under the log | Not possible yet: `ContentSwap` (`AW-SRV-012`'s protocol half) is not in the log. When it is, a projector that did not follow the swap diverges at the swap tick. |
