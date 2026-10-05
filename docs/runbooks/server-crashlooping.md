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
recovery replays the whole log today (a snapshot round and the log tail once `AW-SRV-007` ships), so
the loop itself is load.

```
kubectl -n andara-<env> scale statefulset andara --replicas=0
```

Scaling to zero retains the snapshot claim (`whenScaled: Retain`). The World is now cleanly down
instead of flapping; `AndaraServerUnavailable` will fire, which is correct.

**On `dev` this doesn't stick.** Argo CD's self-heal puts the StatefulSet back to one replica within
seconds. Suspend the `andara-dev` Application's automated sync first, and restore it when you
scale back. No `make` target does that yet: `make server-stop`/`server-start` is #362, a §9
defect. Until it ships, leave the loop running on `dev` and diagnose from `--previous` logs.

Then, by exit code. Until `AW-SRV-007` and `AW-SRV-043` ship, a refused recovery has no code of
its own. It exits `1`, with a `tick loop` `error` line.

**Exits the server has today:**

| Exit | Meaning | Action |
|-----:|---------|--------|
| `1` | the boot failed. The last `error` line names the step: `content core not published`, a content finding `content store unavailable during …`, `accounts`, `tick loop`, or another | **By the line and its `detail`, in this order.** (a) **A dial, ping or timeout against the brokers, on any step**: it's the broker, not the values file. A broker outage fails each boot at its first broker call, usually content or `accounts`, not the tick loop. `world-read-only.md` covers the broker. If it began as a lost boundary, the first crash's log has `tick boundary lost; exiting into recovery` (the `5` row; the log backend, since `--previous` keeps only the last). A `tick loop` detail saying the log `could not be read to tell whether` it predates AW-SRV-012 is this case too. (b) **`tick loop` with `the command log predates AW-SRV-012`**: the log can't be recovered, and only a fresh log fixes it. On `dev`, `make world-reset ENV=dev CONFIRM=andara-dev`; locally, `make down VOLUMES=1`; on `prod` the reset is refused, so keep the World down and escalate. A reset discards the World **and every Account**, so confirm with Brian first. This wins over (c) when both phrases appear. (c) **`tick loop` with `recovery: state hash mismatch at tick …`**: today's refused recovery, which replays the whole log. Compare `sim.seed` with the last good deploy's; a changed seed is a values fix. Then check for a deploy before the loop (diagnose step 4): a new build that replays the old log differently is the likeliest cause on `dev`, and reverting it is the fix. Otherwise keep the World down (on `dev`, leave the loop running, as above) and escalate to implementation with both hashes. Never delete topics or the snapshot volume. (d) **`tick loop` with `tick boundary gap`**: if it says `follows tick 0`, `andara.events.v1`'s 30-day retention has expired the first boundaries, and today's whole-log recovery can't start (#363). Nothing is corrupt; the fix today is an empty log, as in (b), and it recurs every 30 days until `AW-SRV-007` ships. Any other `tick boundary gap`, or an `offset gap`, means the log is inconsistent: escalate to implementation. (e) **`no content in effect: the World has no Zones…`**: `server-unavailable.md`'s row of that name: escalate, don't reset. (f) **A line naming a config key, a Secret, or a path** is configuration: `make k8s-dry ENV=<env>` would have caught a schema error, so it's usually a missing Secret or a bad `content.*` path. (g) **Any other line** (`verb table`, `roster`, `content publish path`, `content core not in effect`, `content could not be brought into effect`, a `content core not published` digest conflict): find it in `server-unavailable.md`'s table, else escalate to implementation with the `error` line |
| `2` | a Go panic or runtime fatal error. No recovery path exits `2` | read the stack trace: `kubectl -n andara-<env> logs andara-0 -c server --previous`. Escalate to implementation with it |
| `5` | a Tick Boundary Record was lost (`AW-SRV-026`), and the running server exited into exact recovery. The log line is `tick boundary lost; exiting into recovery`, with `lost_tick` and `last_delivered_tick`. The counter is `andara_tick_boundary_lost_total` | look at the broker, not the server: the boundary's produce failed. Each restart while the broker is down fails at boot with exit `1`. Once the broker is healthy, the next boot recovers and ticks. `world-read-only.md` covers the broker |
| `137` / OOMKilled | memory below the World's footprint | raise `resources.requests.memory` (or re-run `make measure-tick`). A projector replica has the same footprint as the server (`AW-SRV-019`) |
| liveness restart, no exit line | tick loop wedged for `tick_budget × 100` | `AW-SRV-002`'s `SimulationLagging` runbook. This is the liveness probe doing its job |

**Exits that ship with `AW-SRV-007` and `AW-SRV-043`** (`AW-SRV-007`'s exit table). Pinning a round
is `make rollback ENV=<env> ROUND=<tick>` (`AW-INF-007`), which sets `recovery.pin_round`. **Neither
exists yet.** Until they do, a row that says "pin an older round" means: keep the World scaled to
zero and escalate to implementation with the `error` line.

| Exit | Meaning | Action |
|-----:|---------|--------|
| `3` | log gap: retention shorter than the round's age | if `recovery.pin_round` is set, a stale pin: clear it. If not, recovery already chose the newest complete round, so the history it needs is gone: exact recovery is impossible. Escalate, and fix retention (`AW-INF-005`) |
| `4` | binary older than the round's `state_version` | a rollback that can't read forward state: roll the image forward, or pin a round the old binary wrote |
| `6` | the round doesn't reproduce its own tick. The `error` line `recovery restore mismatch` has `reason` | by `reason`. `hash`: a corrupt round, so `recovery-state-mismatch.md` and pin an older round. `seed`: the configured `sim.seed` differs from the round's `recorded_seed`, so restore the seed value; an older round has the same seed. `content`: this binary builds different content bytes than the round recorded (`pack`, `recorded_digest`, `built_digest`), or a round Zone this build doesn't define (`zone_id`). On `content.source=kafka`, roll the image back to the build that wrote the round, or escalate to implementation; on `content.source=dir`, restore the content files. `recovery-state-mismatch.md` has the detail, and how to find the build |
| `7` | no complete snapshot round with `recovery.require_snapshot=true`, or a pinned round that isn't complete. The `error` line's `cause` is `missing`, `duplicate`, `hash` or `disagree` | pinned: recovery tries no other round, so clear or change `recovery.pin_round`. Not pinned: `SnapshotStale` and `snapshot-stale.md`, for why rounds stopped completing |
| `8` | State Hash mismatch after replay: the alerting condition | `recovery-state-mismatch.md`. Pin an older round |

Scale back to one replica once the cause is addressed:

```
kubectl -n andara-<env> scale statefulset andara --replicas=1
kubectl -n andara-<env> rollout status statefulset/andara --timeout=10m
```

## How to diagnose

1. Exit code and last log line, as above. Once `AW-SRV-007` ships, every refusal is one `error`
   line with the fields its exit-code table names.
2. `kubectl -n andara-<env> describe pod andara-0` — `OOMKilled` vs `Error` vs probe failure events.
3. **Once `AW-SRV-007` ships:** is the newest snapshot round complete? Today `andara-cli snapshot
   list --zone <zone>` reads the store directly, one Zone at a time, with no `complete` column
   (`recovery-state-mismatch.md`, step 1); a grouped form with one comes with `AW-SRV-007`. An
   incomplete newest round with `recovery.require_snapshot=true` is exit `7`. Today's recovery
   doesn't read snapshots at all (it replays the log), so this step can't explain today's crash
   loop.
4. Was there a deploy in the last ten minutes? On `dev`, Argo CD deploys: `make argocd-status
   ENV=dev` names the synced revision and image. If a deploy is the cause, revert it on `main`.
   Argo CD syncs the good build, and `make argocd-recover ENV=dev` replaces a pod stuck on the bad
   one. On a Helm-installed environment (kind, `prod`), `make helm-install ENV=<env>
   TAG=<previous>` reinstalls the previous build, with the StatefulSet still at zero (the
   mitigation above): an `OrderedReady` StatefulSet never replaces a pod that never became Ready.
   On kind, `make kind-load TAG=<previous>` first. `make rollback` is `AW-INF-007`'s, and it isn't
   shipped yet.

## When to escalate

- A hash mismatch that repeats: today, exit `1` with `state hash mismatch` on two boots with the
  same seed and build; once `AW-SRV-007` ships, exit `8` on two consecutive rounds: non-determinism in the sim; escalate as a bug, do not keep
  trying rounds.
- OOM at a request already 2× the measurement: the fixture no longer matches the World; the
  implementation lane re-measures.
