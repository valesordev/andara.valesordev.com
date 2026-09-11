# AndaraServerUnavailable

**Alert:** `max(up{job="andara-server"}) == 0 or absent(up{job="andara-server"})` for 2 m.
**Severity:** page. **SLO:** `docs/specs/slo/world-write-availability.md`.
**Ships with:** `AW-INF-003`.

## What fired, and what the player is experiencing

No `andara-server` pod has been scrapeable for two minutes. Players cannot connect; connected
players' streams have dropped and their Characters are linkdead (`AW-SRV-015`). Every second past
`session.linkdead_grace` (180 s) despawns them — that is the clock you are on.

## How to confirm

```
kubectl -n andara-<env> get statefulset andara            # READY 0/1 confirms it
kubectl -n andara-<env> get pod andara-0 -o wide          # Pending, CrashLoopBackOff, or absent?
kubectl -n andara-<env> describe pod andara-0 | tail -30  # events: scheduling, image pull, probe failures
```

If the pod is `Running` and `1/1` but the alert still fires, the scrape is the problem, not the
server: check the observability stack's target list for `job="andara-server"`.

## How to mitigate

The fastest safe action depends on what `describe` says:

| Symptom | Action |
|---------|--------|
| Pod absent, StatefulSet present | `kubectl -n andara-<env> rollout restart statefulset/andara` |
| StatefulSet absent | `make helm-install ENV=<env>` — the snapshot claim is retained (`helm.sh/resource-policy: keep`) and reattaches by name |
| `Pending`, volume events | the PVC is bound to a node that is gone; see "Diagnose" |
| `CrashLoopBackOff` | this is `AndaraServerCrashLooping` — follow `server-crashlooping.md` |
| startup probe failing, `/readyz` returns `{"phase":"replay"}` | recovery is running; do nothing until `startupProbe` budget (600 s) elapses — the pod is working |
| startup probe failing with exit `2` in logs | `RecoveryStateMismatch`; follow `recovery-state-mismatch.md`. **Do not** delete the snapshot volume to "reset" — that is the one action that turns a 60 s recovery into a full-history replay |

## How to diagnose

1. `kubectl -n andara-<env> logs andara-0 -c partitions` — did the init container run? It prints the
   ordinal and Partition set.
2. `kubectl -n andara-<env> logs andara-0 -c server --previous` — the last exit reason and code
   (`AW-SRV-007`'s table: `2` hash, `3` log gap, `4` state_version).
3. `kubectl -n andara-<env> get pvc snapshots-andara-0` — `Bound`? If `Lost`, the PV is gone; recovery
   will replay from the log, which is correct and slow (ADR-0002). Set `recovery.require_snapshot=false`
   for one boot if prod has it `true`.
4. `kubectl -n andara-<env> get events --sort-by=.lastTimestamp | tail -20`.
5. Image: `kubectl -n andara-<env> get pod andara-0 -o jsonpath='{.spec.containers[0].image}'` — does the
   tag exist in the registry? A `make deploy` with an unpublished tag lands here.

## When to escalate

- `/readyz` has shown `replay` for longer than the startup budget: the log tail is larger than the
  RTO assumes — page the implementation lane; this is an `AW-SRV-007` regression, not an ops fix.
- Recovery exits `2` twice in a row: the World is non-deterministic. Stop restarting; escalate as a
  sim bug with both hashes from the log line.
