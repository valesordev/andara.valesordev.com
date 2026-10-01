# AndaraServerUnavailable

**Alert:** `max by (namespace) (up{job="andara-server"}) == 0`, or no `up` at all for `andara-dev` or
`andara-prod`, for 2 m — the exact expression is in `deploy/helm/andara/files/alerts.yaml`. The
alert's `namespace` label is the environment that is down (`AW-INF-008`). On the cluster a pod that
is not Ready is dropped from the scrape, so "no `up`" is the usual way this fires there; `up == 0`
means a Ready pod whose `/metrics` the scraper cannot reach. In compose it is the reverse.
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
| startup probe failing (`/startedz` not yet true), `/readyz` returns `{"phase":"replay"}` | recovery is running; do nothing until `startupProbe` budget (600 s) elapses — the pod is working |
| pod `Running` and started but not Ready; `andara_content_zones_loaded` is `0`; the server logged its `warn` on entering the wait for content | **a fresh environment waiting for its content (AW-INF-021).** This page is true: no player can enter. The store has no pack with Zones, and the World has never had any. The server is up, serving Admin, and waiting. **Fix: `make content-seed ENV=<env>`.** It publishes and activates the fixture through a port-forward to the pod, and the server becomes Ready on that swap without a restart. On an environment with no fixture, activating any Builder pack with Zones does the same |
| exit `1` after an image rollback, the log naming `andara.core` | the older build can't load the newer core that's active (AW-SRV-013 AC-17). Roll the image forward again. While the newer build serves, move the pointers back in this order: (1) every active pack that pins the newer core, with `andara-cli content rollback <pack>`. `andara-cli content rollback andara.core` refuses with `core_version` and names each of them (`pack@version`) until they're back (AC-14). (2) Then `andara-cli content rollback andara.core`. (3) Then roll the image back. **An image rollback across a core bump is always dependent packs, then core, then image** |
| exit `1`, `content core not published` or `andara.core` named in the `error` line, on a store-backed server (`content.source=kafka`) | the server publishes and activates its build's `andara.core` at boot, and readiness waits until that core is in effect (`AW-SRV-013` AC-15 to AC-17). The `error` line names which rule stopped it: another digest for the same core version (AC-16, a build-pipeline fault, so escalate to implementation), or a store write that failed (check the broker, `kafka-broker-down.md`). Nothing to roll back in the store: the server wrote nothing it didn't finish |
| exit `1`, `no content in effect: the World has no Zones and there is no previous version to retain`, on a store-backed server | **a World that had content and has lost it.** The log holds a swap with Zones, and the store now refuses or lacks every version it could serve. This is not the fresh-environment wait above, which never exits. Don't reset: `make world-reset` keeps the content topics and would only discard the World. Escalate to implementation with the `error` line and `andara_content_load_failures_total{reason}` |
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
