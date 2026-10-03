<!--
SPDX-FileCopyrightText: 2026 Valesor Development
SPDX-License-Identifier: CC-BY-SA-4.0
-->

# StateProjectorDown

**Alert:** `max by (namespace) (kube_deployment_spec_replicas{deployment="andara-projector-state"}) > 0
unless on(namespace) max by (namespace) (up{job="andara-projector-state"} == 1)` for 10 m.
**Severity:** ticket. **SLO:** `docs/specs/slo/projection-freshness.md`. **Ships with:** `AW-INF-025`.

## What fired, and what the player is experiencing

**Nothing a player can see.** The state projector's Deployment wants a replica, and no ready
projector pod has been scraped for 10 minutes. The state indexes stop where the projector last
verified, and builder and operator tools that read them see an older World. The game reads none
of them.

A planned stop can't fire this. `make projector-stop` and `make projector-rebuild` scale the
Deployment to 0, and a disabled projector has no Deployment. So something is keeping a wanted
projector from running: a crash loop, a pod that never becomes Ready, or one that can't be
scheduled or pulled.

## How to confirm

```
kubectl -n andara-<env> get deploy,pod -l app=andara-projector-state     # READY, RESTARTS, STATUS
kubectl -n andara-<env> logs deploy/andara-projector-state --previous | tail -20
kubectl -n andara-<env> get pod -l app=andara-projector-state \
  -o jsonpath='{.items[0].status.containerStatuses[0].lastState.terminated.exitCode}'
```

## Respond, by the last exit code

| Exit | Means | Do |
|------|-------|----|
| `2` | A digest divergence, if the log has a `state projector diverged…` line and ends with `holding /metrics for the divergence to be scraped`. Every start re-reads it from the checkpoint and exits `2` again. **A Go panic also exits `2`**: a `panic:` or `fatal error:` stack trace, with no `state projector stopped` line | Divergence: `state-projector-diverged.md`. **Panic: read the stack trace (`kubectl -n andara-<env> logs deploy/andara-projector-state --previous`) and escalate to implementation; don't rebuild.** For a divergence, capture the evidence first; clear it with `make projector-rebuild ENV=<env>` only after that |
| `3` | A log gap: `andara.events.v1` has expired past the newest complete snapshot round | Fix the server's snapshots first (`SnapshotStale`, `snapshot-stale.md`). Then `make projector-rebuild ENV=<env>` bootstraps from the newest round |
| `4` | The snapshot round's `state_version` is newer than this binary | Roll the projector to the server's image. Argo CD does this on `dev`; the Deployment and the StatefulSet share `image.tag` |
| `5` | The snapshot round doesn't reproduce its own recorded tick. One `error` line, `state projector restore mismatch`, with `round_tick` and `reason`: `hash` (`recorded_hash`, `restored_hash`) or `seed` (`recorded_seed`, `configured_seed`). Each start restores the newest complete round, so a newer round is tried on every restart | **Don't rebuild:** `--rebuild` runs the same restore check (AW-SRV-043 AC-4) and only wipes the consumer group. Capture the `error` lines first. **`seed`:** the round was written under `recorded_seed`, and the configured seed is now `configured_seed`. The projector reads the server's ConfigMap, so they can't differ from each other. If `server.sim.seed` changed (or was set or removed) since the round was written, set it back to `recorded_seed` in the values file, or remove it if it was unset when the round was written; that rolls both together. Once `AW-SRV-007` ships, the server's own recovery fails on the same change (`server-crashlooping.md`, exit `6`). If `server.sim.seed` was unset then and is unset now, the derived default differs between builds: escalate to implementation with both seeds and the image tag. **`hash`:** first read the server's `andara_snapshot_age_seconds` (the query is in `snapshot-stale.md`). If it's under `snapshot.interval` + `snapshot.upload_timeout` (90 s at the defaults), the server is completing rounds, so each restart is restoring a newer one. If `round_tick` advances across restarts and still mismatches, it's systematic: check that the projector and the StatefulSet run the same `image.tag`, then escalate to implementation as a restore or determinism bug with every `round_tick` and hash pair. If the age keeps growing past that, the server isn't completing rounds: `snapshot-stale.md` first (`SnapshotStale` follows about 8 minutes after the last good round), and the projector recovers on its own once a good round lands. A single bad round older than a good one is a corrupt round: escalate with `round_tick` and both hashes, and take no action, since the next start is already past it |
| `1` | Configuration, the snapshot store, or the broker. The last `error` line names which | A missing `andara-snapshot-s3` Secret or an unreachable `andara-objectstore` is `make objectstore-install ENV=<env>`. A refused produce to `andara.state.v1` is the broker's (`topics-diff`, ADR-0011 credentials). `content digest mismatch … (snapshot round)` or `rebuild its content` means the projector's content differs from the round's: check that its `image.tag` and content mounts match the server's (`state-projector-diverged.md`) |
| none, `Pending` | Not scheduled or not pulled | `kubectl -n andara-<env> describe pod -l app=andara-projector-state`, the Events section |
| none, running but not Ready | Still bootstrapping, or waiting for the World's first boundary | Wait for `state projector started`, then `state projector caught up`. It can take as long as the replay from the newest round. If the server isn't ticking, that's `AndaraServerUnavailable`'s |

After the fix, `make projector-start ENV=<env>` if the Deployment was stopped, and watch
`andara_state_projector_lag_seconds` fall under `andara_state_projector_lag_budget_seconds`.
