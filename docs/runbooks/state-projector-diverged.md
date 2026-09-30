<!--
SPDX-FileCopyrightText: 2026 Valesor Development
SPDX-License-Identifier: CC-BY-SA-4.0
-->

# StateProjectorDiverged

**Alert:** `max_over_time(andara_state_digest_mismatches_total[6h]) > 0`. **Severity:** ticket.
**SLO:** `docs/specs/slo/projection-freshness.md` (integrity, no budget). **Ships with:** `AW-SRV-019`.
*(Rewritten 2026-09-24 at architecture's §8 review. Rewritten again 2026-09-26, when #103 made a
divergence survive the restart: every start now halts on it until an operator clears it.)*

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

**The divergence is recorded and sticky.** The projector's last commit carries it as offset
metadata (`tick=<T−1>;diverged=<T>:<recorded>:<replayed>`). Every later start reads that record
first, and exits `2` again before bootstrapping, **whatever snapshot rounds exist**. It logs
`state projector diverged earlier and it is unresolved; run with --rebuild once it has been dealt
with`, with the same tick and both hashes, and counts the mismatch again. A newer round can't carry
it past the evidence.

So:
- **A crash loop on the same tick is the expected state**, not a second failure. Each restart
  re-counts the mismatch, so the alert stays firing for as long as the divergence is unresolved.
- **The alert holds for 6 hours** (`max_over_time`) past the last restart. A projector left
  crash-looping keeps it firing, so it can't resolve on its own.
- **Freshness is lost until you act.** The state indexes stop at T−1. That's the price of keeping
  the evidence, and the reason this is a ticket and not something to leave.

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

Every restart after it logs `state projector diverged earlier and it is unresolved`, with the same
`tick` and hashes. That's the recorded divergence, not a new one. If a restart names a *different*
tick, the checkpoint was cleared, or a newer divergence was recorded after a `--rebuild`.

## Respond, in this order

1. **Capture the evidence** from the lines above: the divergent tick, both hashes, and the
   offsets. Also capture the server's and the projector's boot lines (content versions, seed).
   The checkpoint keeps the tick and hashes, but the offsets and the boot lines are only in Loki.
2. **Rule out the known causes** in the table below. If one explains it, follow that row.
3. **Otherwise, escalate as a simulation bug** with that evidence. The projector keeps halting
   on the same tick meanwhile, and nothing is lost by leaving it there.
4. **Clear it with `make projector-rebuild ENV=<env>` once the cause is understood**, or once
   it's escalated with the evidence captured. The target stops the projector first
   (`projector-stop`, so there are never two writers on the group), runs `--rebuild` as the Job
   `andara-projector-state-rebuild`, and starts the Deployment once the Job has caught up. It ends
   `projector-rebuild: rebuilt to tick <t> in <n>s`. `--rebuild` wipes the group, logs
   `--rebuild discards an unresolved divergence` with the tick, and bootstraps from the newest
   complete round. If the rebuilt projector diverges again at a later tick, that's a new
   divergence. Start again at step 1.

## Known causes worth ruling out before escalating
## Known causes worth ruling out before escalating

| Check | Means |
|-------|-------|
| The projector and the server loaded different content | The replica derives its seed from the World, as the server does. Different content means a different seed and a divergence at tick 1. Compare the `content_version` in both processes' boot logs. The projector must follow the same `content.packs`. |
| `ANDARA_SIM_SEED` differs between the two | The projector reads the server's ConfigMap. A seed set on the server's StatefulSet alone, and not in the ConfigMap, is not seen. |
| The divergence tick is a tick where a Zone faulted | Replaying through a fault does not reproduce it today. The panicking record stays unapplied and the boundary excludes it. `AW-SRV-027` owns making that exact. Until it lands, this divergence is known and expected. |
| A content swap under the log | `ContentSwap` enters the protocol with PR #79 (`log.proto` 17) and is not applied until `AW-SRV-012`'s second half. From then on, a projector that did not follow the swap diverges at the swap tick. |
