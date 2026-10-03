<!--
SPDX-FileCopyrightText: 2026 Valesor Development
SPDX-License-Identifier: CC-BY-SA-4.0
-->

# ProjectionStale

**Alert:** `ProjectionStale{projection="state"}`: `andara_state_projector_lag_seconds >
andara_state_projector_lag_budget_seconds` for 5 m. **Severity:** ticket.
**SLO:** `docs/specs/slo/projection-freshness.md` (freshness). **Ships with:** `AW-SRV-019`; shared
with `AW-SRV-017` and `AW-SRV-018`, which add their own `projection` values.

## What fired, and what the player is experiencing

**Nothing a player can see.** A projection is behind the World by more than its budget, which is
5 s by default for `state`. Builder and operator tools that read the indexes see an older World. The
game reads none of them.

The lag is measured to the last tick the projector *verified*, not the last it read. A projector
that is reading but has stopped verifying shows as stale, which is intended.

## How to confirm

```
curl -s http://<projector>:8080/metrics | grep -E '^andara_state_(projector_lag_seconds|projector_lag_budget_seconds|projector_tick|records_produced_total|topic_bytes) '
```

Sample `andara_state_projector_tick` twice, a minute apart. Rising faster than the tick rate means
the projector is catching up: watch it and do nothing else. Flat means it has stopped. First confirm
the World is ticking (`rate(andara_ticks_total[1m]) > 0` on the server). A World that is not ticking
has nothing to project.

## Diagnose, in this order

**First, check whether a rebuild is running.** A `make projector-rebuild` longer than this rule's
5 minutes fires it, which is correct: rebuilds count against the budget. Its final line
(`projector-rebuild: rebuilt to tick <t> in <n>s`), or the Job `andara-projector-state-rebuild`
still running, answers it. If so, wait for the lag to return under budget.

| Step | Look at | Means |
|------|---------|-------|
| 1 | Pod restarts and the last exit code | `2`: a divergence if the log has a `state projector diverged…` line (it ends with `holding /metrics for the divergence to be scraped`), so see `state-projector-diverged.md`. This alert is its echo. A `panic:` or `fatal error:` stack trace with no `state projector stopped` line is a Go panic: escalate to implementation; don't rebuild. `3`: a log gap, and the `error` line names the topic and Partition. `andara.commands.v1` keeps everything, so in practice it is `andara.events.v1` (30-day retention) with no complete snapshot round newer than the gap. Fix the server's snapshots first (`SnapshotStale`, #74 locally). A plain restart then bootstraps from the newest round, with no rebuild needed. `4`: the snapshot round was written by a newer binary; roll the projector forward to the server's version. `1`: configuration or the broker; the last `error` line names it. That includes a produce to `andara.state.v1` the broker refused: it fails after the one-minute delivery timeout and the process exits `1`, so a refused write shows up as a crash loop, not as a stall. |
| 2 | `andara_state_rebuild_duration_seconds{phase="replay"}` has no sample yet | The projector is still catching up after a start or rebuild, which is expected after one. The time is snapshot load plus tail replay; a long tail means the newest round is old (`SnapshotStale` on the server). |
| 3 | `/readyz` failing, lag gauge at 0 | The projector has not verified its first batch: still in bootstrap, or waiting for the World's first boundary. The lag gauge reads 0 until then, so this state looks fresh on the dashboard. The `state projector started` line (or its absence) says which. |
| 4 | `andara_state_topic_bytes` climbing steadily (it reads 0 against the local Redpanda today; `docs/feedback/AW-SRV-019-state-projector.md`) | Churn is outpacing the compactor, and the topic is becoming a second history. Check the topic's `min.compaction.lag.ms` and the broker's cleaner. This is a capacity problem, not a projector bug. |
| 5 | CPU on the projector pod at its limit | The replica runs the whole simulation. It needs what the server needs (`AW-INF-003`'s measurements); a projector sized smaller than the server falls behind under load. |

## Recover

A lagging projector recovers on its own once the cause is gone: it catches up from its checkpoint.
A rebuild is for when the checkpoint itself is unusable, not a way to catch up faster:
`make projector-rebuild ENV=<env>`. Never run `andara-projector state --rebuild` by hand beside the
Deployment: two writers on one consumer group corrupt its checkpoint, which is what the target's
stop-first order prevents.
