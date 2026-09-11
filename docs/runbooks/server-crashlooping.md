# AndaraServerCrashLooping

**Alert:** `increase(kube_pod_container_status_restarts_total{container="server", pod=~"andara-[0-9]+"}[10m]) > 3`.
**Severity:** page. **SLO:** `docs/specs/slo/world-write-availability.md`.
**Ships with:** `AW-INF-003`.

## What fired, and what the player is experiencing

The server container has restarted more than three times in ten minutes. Players see a World that
comes back and drops them again; each cycle re-enters linkdead grace and the third or fourth cycle
despawns anyone who reconnected. It is worse than a clean outage, because it looks recoverable.

## How to confirm

```
kubectl -n andara-<env> get pod andara-0                      # RESTARTS column
kubectl -n andara-<env> logs andara-0 -c server --previous | tail -40
```

The last log line before exit names the reason; the exit code is in
`kubectl describe pod andara-0` under `Last State`.

## How to mitigate

**Stop the loop before diagnosing.** A restarting server re-runs recovery each time, and each
recovery is a full read of the snapshot round and the log tail — the loop itself is load.

```
kubectl -n andara-<env> scale statefulset andara --replicas=0
```

Scaling to zero retains the snapshot claim (`whenScaled: Retain`). The World is now cleanly down
instead of flapping; `AndaraServerUnavailable` will fire, which is correct.

Then, by exit code (`AW-SRV-007`):

| Exit | Meaning | Action |
|-----:|---------|--------|
| `1` | configuration or store error before recovery | fix the values file; `make k8s-dry ENV=<env>` would have caught a schema error, so this is usually a Secret missing or a bad `content.*` path |
| `2` | State Hash mismatch | `recovery-state-mismatch.md`; pin an older round with `make rollback ENV=<env> ROUND=<tick>` |
| `3` | log gap: retention shorter than the round's age | `andara-cli snapshot list`, pick a newer complete round, `ROUND=` it; then fix retention (`AW-INF-005`) |
| `4` | binary older than the snapshot's `state_version` | this is a rollback that cannot read forward state; `make rollback ENV=<env> ROUND=<tick the old binary wrote>` |
| `137` / OOMKilled | memory below the World's footprint | raise `resources.requests.memory` (or re-run `make measure-tick`); a projector replica has the same footprint as the server (`AW-SRV-019`) |
| liveness restart, no exit line | tick loop wedged for `tick_budget × 100` | `AW-SRV-002`'s `SimulationLagging` runbook; this is the liveness probe doing its job |

Scale back to one replica once the cause is addressed:

```
kubectl -n andara-<env> scale statefulset andara --replicas=1
kubectl -n andara-<env> rollout status statefulset/andara --timeout=10m
```

## How to diagnose

1. Exit code and last log line, as above. Every refusal in `AW-SRV-007` is one `error` line with the
   fields the exit-code table names.
2. `kubectl -n andara-<env> describe pod andara-0` — `OOMKilled` vs `Error` vs probe failure events.
3. `andara-cli snapshot list` — is the newest round complete? An incomplete newest round with
   `recovery.require_snapshot=true` is exit `5`.
4. Was there a deploy in the last ten minutes? `helm -n andara-<env> history andara`. If so,
   `make rollback ENV=<env>` is the mitigation and the deploy is the cause.

## When to escalate

- Exit `2` on two consecutive rounds: non-determinism in the sim; escalate as a bug, do not keep
  trying rounds.
- OOM at a request already 2× the measurement: the fixture no longer matches the World; the
  implementation lane re-measures.
