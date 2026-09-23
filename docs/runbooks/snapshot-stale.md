<!--
SPDX-FileCopyrightText: 2026 Valesor Development
SPDX-License-Identifier: CC-BY-SA-4.0
-->

# SnapshotStale

**Alert:** `andara_snapshot_age_seconds > 3 × andara_snapshot_interval_seconds` for 5 m — 180 s at
the default cadence, and it follows the cadence if you change it. The rule reads the configured
interval from the process rather than assuming the default, and does not fire at all when
snapshots are switched off (`snapshot.interval: 0`), because that is not a stale snapshot.
**Severity:** ticket, not a page. **SLO:** `docs/specs/slo/recovery.md` (RTO).
**Ships with:** `AW-SRV-006`.

## What fired, and what the player is experiencing

**Nothing. No player is experiencing anything, and no data is at risk.**

Read that first, because everything below depends on not treating this as an outage. Snapshots
are an RTO optimisation layered on a recovery path that is already correct: Kafka is the
authority on order, and replaying `andara.commands.v1` from its beginning reconstructs the World
exactly (ADR-0002, `AW-SRV-007`). RPO is zero and is decided by broker settings, not by anything
this alert covers. A stale snapshot costs **recovery time**, and only if the process dies before
it is fixed.

What has actually happened: the newest complete snapshot round is more than three cadences old,
so a restart would replay the log from further back than intended. The exposure grows linearly
with how long this stays broken, and it is realised only by an unplanned restart. That is why
this is a ticket — but it is a ticket that blocks a planned deploy, because `AW-INF-007`'s
rollback window is measured in snapshot rounds.

## How to confirm

```
curl -s http://<server>:8080/metrics | grep -E '^andara_snapshot_(age_seconds|last_tick|rounds_total|failures_total|duration_seconds_count|tick_stall_seconds_count|bytes) '
```

Then ask the store what it actually holds, which is the authority — the metric is the process's
belief about itself:

```
andara-cli snapshot list --zone <zone>
```

Expect one row per round, newest first. If the newest row's tick is recent and
`andara_snapshot_age_seconds` is high, the process is writing objects but not recording that it
did; if the newest row is old, no round has completed.

`andara_snapshot_rounds_total{outcome="incomplete"}` rising while `complete` is flat is the
common shape: rounds are running and failing, not failing to run.

## Diagnose, in this order

Stop at the first row that explains it.

| Step | Look at | Means |
|------|---------|-------|
| 1 | `andara_snapshot_failures_total{reason="store"}` | The store is refusing writes. For `fs`, the volume is full, read-only, or unmounted — `AW-INF-003` owns it. For `s3`, the bucket, endpoint, or credentials are wrong or the endpoint is unreachable. The `warn` log line names the Zone and the key. |
| 2 | `andara_snapshot_failures_total{reason="timeout"}` | A round did not finish inside `snapshot.upload_timeout` (30 s). The store is reachable but slow, or the body has outgrown the timeout. Check `andara_snapshot_bytes{zone}` against what it was. A round that times out is failed, not queued — the next one starts on schedule, so this can repeat indefinitely without any round ever completing. |
| 3 | `andara_snapshot_rounds_total{outcome="incomplete"}` rising | Some Zones are written and some are not, so no round is selectable even though objects are appearing. `ErrRoundIncomplete` names the missing Zones. One Zone failing repeatedly is usually one key — a name the store rejects, or a Zone whose body is far larger than its siblings. |
| 4 | `andara_snapshot_failures_total{reason="encode"}` | The body would not serialize. This is a bug, not an environment: a `repeated` field out of order, or a value the canonical encoder refuses. It will not clear on its own. |
| 5 | `andara_snapshot_failures_total{reason="stall"}` rising, with `andara_snapshot_tick_stall_seconds` p99 over budget | The in-tick copy is over `snapshot.max_stall_ms`. This is a warning and does **not** stop rounds completing — if age is also high, it is a symptom of a World that has grown, not the cause of the staleness. Check `andara_tick_overruns_total` too: the same copy is inside the Tick Budget. |
| 6 | No failures, no rounds, age rising | The round is not being scheduled at all. Confirm `snapshot.interval` is non-zero (`0` disables snapshots entirely) and that the tick loop is running: `andara_ticks_total` must be advancing. A stalled loop is `SimulationLagging`'s problem, and this alert is downstream of it. |

## How to mitigate

The fix is always "make the next round complete". There is no way to backfill a round for a tick
that has passed — a snapshot is a cut of state that no longer exists — so mitigation means
restoring the path and waiting one cadence.

| Cause | Action |
|-------|--------|
| Volume full or read-only (`fs`) | Free space or remount read-write. `AW-INF-007` decides retention; until it lands, old rounds are not pruned automatically, so a full volume is expected eventually and deleting the oldest rounds by key is safe — keep at least the newest two complete ones. |
| Bucket, endpoint, or credentials wrong (`s3`) | Fix the deployment. `snapshot.s3_bucket` is required when `snapshot.store=s3` and the server refuses to start without it, so a *running* server with this symptom has a reachability or permission problem, not a missing setting. |
| Store slow, rounds timing out | Raise `snapshot.upload_timeout` — but it must stay at or under `snapshot.interval`, which the config validates. If the two would have to meet, the body has outgrown the cadence: raise `snapshot.interval` and accept the RTO, and open the compression question the story left to a measured body size. |
| One Zone failing repeatedly | Nothing live. Take the key from the `warn` line and try it by hand against the store. |
| Rounds not scheduled | Restore the tick loop. Snapshots resume with it; nothing to restart separately. |

**Confirm the fix** by watching one full cadence, not by watching the alert clear:

```
andara-cli snapshot list --zone <zone>   # a new newest row within snapshot.interval
```

## When to escalate

- **Before any deploy or rollback.** `AW-INF-007`'s rollback path recovers from the newest
  snapshot the *older* binary can read. A stale snapshot narrows that window, and a rollback
  planned against a window that is not there is how a short outage becomes a long one. Hold the
  deploy until a round completes.
- **`reason="encode"`**, which is a code defect and will not clear.
- **Age past one hour** at the default cadence. Sixty missed rounds is no longer an incident of
  degraded RTO; it is an unbounded replay on the next restart, and it is worth knowing the log's
  age (`AW-INF-004` retention) before deciding whether the recovery would finish at all.

## What not to do

- **Do not restart the server to "kick" snapshots.** The restart is the event snapshots exist to
  make cheap, and performing it while they are stale realises exactly the cost the alert is
  warning about. Every cause above is fixable with the process running.
- **Do not lower `snapshot.interval` to catch up.** Rounds are not cumulative; a faster cadence
  does not make the newest one older-proof, and it raises the in-tick copy's duty cycle against a
  stall budget the story measured as having thin margin.
- **Do not delete the incomplete round's objects.** They are valid objects individually, they
  cost only storage, and `AW-SRV-007` already declines to select a round it cannot complete.
- **Do not treat this as data loss in an incident channel.** It is not, and saying so sends
  people looking for a problem that does not exist. The accurate sentence is "recovery would be
  slower than intended until the next round completes".
