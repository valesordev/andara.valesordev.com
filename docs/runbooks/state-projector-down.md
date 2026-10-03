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
| `2` | A digest divergence, if the last `error` line is `state projector diverged…`. Every start re-reads it from the checkpoint and exits `2` again. **A Go panic also exits `2`**: with no divergence line, read the stack trace (`kubectl -n andara-<env> logs deploy/andara-projector-state --previous`) and escalate to implementation instead | `state-projector-diverged.md`. Capture the evidence first; clear it with `make projector-rebuild ENV=<env>` only after that |
| `3` | A log gap: `andara.events.v1` has expired past the newest complete snapshot round | Fix the server's snapshots first (`SnapshotStale`, `snapshot-stale.md`). Then `make projector-rebuild ENV=<env>` bootstraps from the newest round |
| `4` | The snapshot round's `state_version` is newer than this binary | Roll the projector to the server's image. Argo CD does this on `dev`; the Deployment and the StatefulSet share `image.tag` |
| `1` | Configuration, the snapshot store, or the broker. The last `error` line names which | A missing `andara-snapshot-s3` Secret or an unreachable `andara-objectstore` is `make objectstore-install ENV=<env>`. A refused produce to `andara.state.v1` is the broker's (`topics-diff`, ADR-0011 credentials) |
| none, `Pending` | Not scheduled or not pulled | `kubectl -n andara-<env> describe pod -l app=andara-projector-state`, the Events section |
| none, running but not Ready | Still bootstrapping, or waiting for the World's first boundary | Wait for `state projector started`, then `state projector caught up`. It can take as long as the replay from the newest round. If the server isn't ticking, that's `AndaraServerUnavailable`'s |

After the fix, `make projector-start ENV=<env>` if the Deployment was stopped, and watch
`andara_state_projector_lag_seconds` fall under `andara_state_projector_lag_budget_seconds`.
