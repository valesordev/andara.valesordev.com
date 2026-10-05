<!--
SPDX-FileCopyrightText: 2026 Valesor Development
SPDX-License-Identifier: CC-BY-SA-4.0
-->

# RecoveryStateMismatch

**Alert:** `andara_recovery_state_hash_match == 0`, `for: 0m`, `keep_firing_for: 15m`.
**Severity:** page. **SLO:** `docs/specs/slo/recovery.md`. **Ships with:** `AW-SRV-007`.

## What fired, and what the player is experiencing

**No server took traffic, and none will until an operator chooses what to do.** A recovery rebuilt
the World from a snapshot round and the log, and the result was not the State Hash the log recorded,
or the round did not reproduce its own tick. `andara-server` set `andara_recovery_state_hash_match`
to `0`, logged the refusal, never bound `grpc.listen`, and exited. Players cannot connect.

**Nothing is lost.** Every acknowledged Command is in the log, and the log is untouched. Refusing is
deliberate: a World that recovered to the wrong state must never take traffic, and there is no automatic
search for an older round (Brian, 2026-09-11). Which round to trust is a decision an operator makes
with the evidence.

**Where this alert can be seen.** On compose, the server holds `/metrics` and `/livez` up for
`recovery.mismatch_linger` (compose sets 60 s) so the gauge is scraped. Compose restarts a failed server
(`restart: on-failure`), so a refused recovery **loops**: each cycle recovers, mismatches, lingers and exits,
and the page keeps firing while it does. `keep_firing_for` keeps it visible for 15 minutes after the loop is
stopped (`docker compose stop andara-server`) or its cause is fixed. **On `dev` and `prod` this rule can't
fire**: the annotation scrape keeps only Ready pods, and a refused recovery was never Ready, so the `0` is
never scraped. What pages there is `AndaraServerUnavailable` (`for: 2m`), and `server-unavailable.md` sends
exit `8` and exit `6` here. `AW-INF-009` (planned for SPRINT-05) adds a second clause to this same rule, read
from kube-state-metrics, so that one alert name covers both.

## How to confirm

| Where | Command | Looking for |
|-------|---------|-------------|
| compose | `make logs SVC=andara-server` | the refusal's `error` line, then `holding /metrics for the hash mismatch to be scraped` with `for` |
| compose, during any linger window (each restart cycle) | `curl -s localhost:${ANDARA_HTTP_PORT:-8080}/metrics \| grep -E '^andara_recovery_(state_hash_match\|failures_total)'` | `andara_recovery_state_hash_match 0`, and `andara_recovery_failures_total{reason="hash"}` (exit `8`) or `{reason="restore"}` (exit `6`) at `1` |
| cluster | `kubectl -n andara-<env> describe pod andara-0`, the `Last State` of `server` | exit code `8` or `6` |
| cluster | `kubectl -n andara-<env> logs andara-0 -c server --previous` | the refusal's `error` line |

The `error` line says which refusal it was:
- **Exit `8`**: the State Hash differs after replay. The line names the tick, both hashes (recorded and
  replayed) and the round used. Replay stops at the first boundary that differs.
- **Exit `6`**: the round doesn't reproduce its own tick. The line is `recovery restore mismatch`, with
  `round_tick` and a `reason`: `hash`, `seed` or `content`.

## Diagnose, by exit code and reason

| Exit, reason | Means | Do |
|--------------|-------|----|
| `8` | Replaying the log from the round reached a tick whose hash differs from the recorded one | List the rounds, and ask each older one whether it reproduces the World (below). If an older round does, the newest round was the bad part. **If every round mismatches at the same tick, it isn't a round**: replay isn't deterministic, or a log record changed. That is a determinism failure; stop, keep the evidence, and escalate to implementation with the `error` line and the tick |
| `6`, `hash` | The newest round doesn't reproduce its own recorded hash: a corrupt round | Choose an older round, as below |
| `6`, `seed` | The configured `sim.seed` differs from the round's `recorded_seed` | Not a data fault: set `sim.seed` back to `recorded_seed` (or remove it, if it was unset when the round was written). An older round written under the same seed has the same problem |
| `6`, `content` | The round doesn't restore onto the content in effect: a digest differs (`pack`, `recorded_digest`, `built_digest`) or a Zone is unknown (`zone_id`) | Content changed since the round was written. Roll the content back (`andara-cli content rollback`), or choose a round written under the current content |

## How to mitigate

Keep the World down while you choose. On the cluster that is `server-crashlooping.md`'s scale-to-zero (and
on `dev`, suspend the Application's automated sync first, as it says). Then:

1. **List the rounds.** `andara-cli snapshot list` (available today) prints each round's tick,
   `state_version`, Zones, whether it is complete, and its age. A round that isn't complete is never
   selected on its own.
2. **Ask the one-shot about a round** *(`AW-SRV-007`, not built yet)*. `andara-server recover --verify --round
   <tick>`, run in the server image against the same store, prints `match` or `mismatch` with both hashes and
   the phase timings, and exits `0` on a match, `8` on any mismatch, and `7` if the named round isn't
   complete. It never lingers and never touches the live server. `andara-cli snapshot verify --round <tick>`
   asks the same of a running server's scratch Engine.
3. **Deploy pinned to a round that matches** *(`AW-INF-007`, not built yet)*. That is `recovery.pin_round` and
   its `make rollback ROUND=<tick>`.

**Until steps 2 and 3 exist, `snapshot list` is the only step available:** keep the World scaled to zero and
escalate to implementation with the `error` line and the tick of the newest round, as `server-crashlooping.md`
says for the same reason.

An older round helps only when the round was the bad part. It replays the log forward from that round, so
nothing acknowledged is lost, but a determinism fault at a later tick fails again on the way.

## What not to do

- **Do not delete the snapshot volume to "reset".** It is the one action that turns a 60 s recovery into a
  full-history replay (`server-unavailable.md`), and it removes the rounds you are choosing between.
- **Do not roll the image back to "try the older build".** A binary older than a round's `state_version`
  exits `4`, and a build with different simulation code is what causes an exit `8`.
- **Do not raise `recovery.verify_timeout`.** A mismatch is not a timeout.
- **Do not set `recovery.mismatch_linger` on the cluster.** It would add 60 s to every crash-loop cycle and
  delay `AndaraServerCrashLooping`, and the Ready-only scrape would never see it.
- **Do not read a quiet gauge as "fixed".** The gauge has no sample until a recovery sets it. A recovery that
  matches sets it to `1`, and the page still stays visible for 15 minutes after the last `0`.

## After

A mismatch that repeats across rounds, or a determinism failure, is the SLO's incident class: its policy is
that the next change to the simulation core or the persistence adapters is the fix
(`docs/specs/slo/recovery.md`). Open a GitHub issue with the tick, both hashes, the round, the image tag and
the `error` lines, and keep the pod's `--previous` log. If the cause was a bad round, say which round and what
wrote it: `SnapshotStale` and `snapshot-stale.md` cover why rounds stop being written, and
`state-projector-diverged.md` covers the projector, which compares the same hashes.
