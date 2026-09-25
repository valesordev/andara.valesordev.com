<!--
SPDX-FileCopyrightText: 2026 Valesor Development
SPDX-License-Identifier: CC-BY-SA-4.0
-->

# StateProjectorDiverged

**Alert:** `max_over_time(andara_state_digest_mismatches_total[6h]) > 0`. **Severity:** ticket.
**SLO:** `docs/specs/slo/projection-freshness.md` (integrity, no budget). **Ships with:** `AW-SRV-019`.
*(Rewritten 2026-09-24 at architecture's §8 review. The first version assumed a restarted projector
diverges again at the same tick. It usually does not; see below.)*

## What fired, and what the player is experiencing

**Nothing a player can see.** The state projector replays the same Commands at the same recorded
tick boundaries as the live server, and after every tick it compares its State Hash with the one
the server recorded. At one tick, they differed. The projector then:
- produced nothing for that tick;
- committed nothing past the tick before it;
- held `/metrics` up for a minute so the counter was scraped;
- exited `2`.

**Two processes applied the same records from the same state and reached different hashes. Treat
that as a determinism failure until a row in the table below explains it.** It is the same failure
that would make recovery (`AW-SRV-007`) refuse to reproduce the World the server is running.

**What happens next depends on whether a complete snapshot round is readable.**
- **If none is** (today: compose never completes a round, #74, and the in-cluster Deployment
  does not mount the store, #80), the restart replays from zero and diverges again at the same
  tick, in a crash loop.
- **If one is** (once #74 and #80 land), it is **not a repeat**. The restart bootstraps from the
  newest complete round. Rounds are written every `snapshot.interval` (60 s), so that round is
  usually newer than the divergent tick. The projector dumps the state it loaded, tombstones stale
  keys, continues, and **skips the divergent tick without diverging again**. The indexes then
  describe the World the server has, whether or not that World is the one the log implies.

So:
- **The evidence is the first divergence.** A clean run afterwards proves nothing either way.
- **The alert holds for 6 hours** (`max_over_time`), not for the one minute the counter lived.
  The process that saw the divergence is gone, and the ticket must not resolve with it.

Whether a projector should refuse to bootstrap past an unresolved divergence is an open design
question on `AW-SRV-019` (`docs/feedback/AW-SRV-019-state-projector.md`).

## How to confirm

The divergence is in the projector's `error` line. Read it from Loki, because the pod that logged
it has been replaced:

```
{service_name="andara-projector-state"} |= "state projector diverged"
```

It carries:
- `tick`, the tick that diverged;
- `recorded_hash` and `replayed_hash`, in full;
- `last_good_offsets`, the next-to-read offset per `andara.commands.v1` Partition after the last
  verified tick.

The next `state projector started` line shows where the restart resumed: `round_tick`, `tick`.

## Respond, in this order

1. **Capture the evidence** from the lines above: the divergent tick, both hashes, the offsets,
   and the `round_tick` the restart resumed from. Also capture the server's and the projector's
   boot lines (content versions, seed).
2. **Rule out the known causes** in the table below. If one explains it, follow that row.
3. **Otherwise, escalate as a simulation bug** with that evidence. Do not wait for it to recur:
   it will not, because the restart moved past it.
4. **Freshness usually needs nothing where rounds are readable.** The restarted projector is already producing from the
   newer round. Only if it crash-loops on the same tick (no newer round exists yet) does it need
   a rebuild. There is no target for that yet: `§9 defect → #80`. Until #80 lands, a rebuild must
   not be run beside a live Deployment, because two writers on one consumer group corrupt its
   checkpoint. Leave it crash-looping. Once rounds are readable (#74, #80), the next round moves it on.

## Known causes worth ruling out before escalating
## Known causes worth ruling out before escalating

| Check | Means |
|-------|-------|
| The projector and the server loaded different content | The replica derives its seed from the World, as the server does. Different content means a different seed and a divergence at tick 1. Compare the `content_version` in both processes' boot logs. The projector must follow the same `content.packs`. |
| `ANDARA_SIM_SEED` differs between the two | The projector reads the server's ConfigMap. A seed set on the server's StatefulSet alone, and not in the ConfigMap, is not seen. |
| The divergence tick is a tick where a Zone faulted | Replaying through a fault does not reproduce it today. The panicking record stays unapplied and the boundary excludes it. `AW-SRV-027` owns making that exact. Until it lands, this divergence is known and expected. |
| A content swap under the log | `ContentSwap` enters the protocol with PR #79 (`log.proto` 17) and is not applied until `AW-SRV-012`'s second half. From then on, a projector that did not follow the swap diverges at the swap tick. |
