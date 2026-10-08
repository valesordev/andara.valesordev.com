# Deploy lifecycle contract

> **Status: decided 2026-10-08, `AW-INF-007`.** The sequence, the round tags, the configuration keys, the
> exit codes of the two scripts, the pin lifecycle, and the ruling on how a deploy's interruption is
> measured. Built by `AW-SRV-054` (pre-stop), `AW-SRV-055` (round tags and retention), `AW-INF-041`
> (`make deploy`, `make rollback`) and `AW-INF-042` (chart hook, grace period, rolling-update test).

ADR-0001 accepts that a deploy interrupts the World until sharding is active: a rolling update of a
one-replica `StatefulSet` is a scheduled restart. What makes it acceptable is that recovery is exact: the
new pod resumes from a Snapshot Round and the log tail and proves it with a matching State Hash
(ADR-0002 §4, `AW-SRV-007`).

## Sequence

```
kubectl rollout ──▶ preStop exec: andara-server prestop
                       └─ calls Lifecycle.PrepareStop on deploy.control_socket (the serving process does the work)
                            ├─ write ServerStopping{message, expected_back_seconds} to every open Subscribe stream
                            ├─ sleep deploy.notice_lead (default 10 s); the World keeps running and accepting Submit
                            ├─ refuse Submit: UNAVAILABLE reason "server_restarting" (the draining flag, NOT the degraded state)
                            ├─ wait for the Submits already admitted to resolve
                            ├─ at the next tick boundary: SnapshotAll → Put ×zones → tag the round "deploy:<image tag>"
                            ├─ commit offsets; log "prestop complete tick=T hash=H"
                            └─ respond; prestop prints the response and exits (0 complete, 1 timeout, 2 store_error, 3 no socket)
                    SIGTERM ──▶ drain (grpc.drain_timeout, sim.drain_timeout_ms) ──▶ exit
new pod ──▶ recover (AW-SRV-007) ──▶ verify hash ──▶ /readyz serving ──▶ endpoints
                       └─ observes andara_deploy_interruption_seconds once, on becoming serving
```

The kubelet ignores a preStop hook's exit code and sends `SIGTERM` when it returns or when the grace
period's budget for it is spent. A `prestop` that times out or fails therefore leaves the previous
complete round as the recovery point: the replay is longer, never wrong.

### Rulings

**1. `prestop` is a client; the serving process does the work.** `andara-server prestop` is exec'd by the
kubelet and exits before a Prometheus scrape could read anything it registered, and it holds none of the
Engine, the Gateway streams or the Ingress. So it calls `Lifecycle.PrepareStop`
(`andara/admin/v1/lifecycle.proto`) on a Unix socket, `deploy.control_socket`, served by the running
server. The socket is created `0600` for the server's uid; the exec'd process is the same image and uid.
It is not on the Admin endpoint and carries no operator credential, and the one thing it can do is start
the stop the kubelet is about to perform. The chart mounts an `emptyDir` at the socket's directory (the
root filesystem is read-only).

**2. `ServerStopping` is a stream frame, not an Event.** Field 25 of `EventEnvelope.payload`, event_id 0,
written by the Gateway to every open Subscribe stream; the sim never emits it, it is not in the log or the
Resume Window, and it moves no client's resume point. Replay never sees it, so it cannot make a hash
differ. It is best effort: a stream opened after it was written does not receive it. A client must handle
a close without one. (`AW-INF-007` called it "world scope". An Event with a scope would have to be
deterministic and replayable, which a notice is not.)

**3. Refusing Submit during pre-stop is not the degraded state.** `andara_ingress_degraded` is untouched,
so `WorldReadOnly` does not page on every deploy. The Ingress has a separate draining flag; a Submit
refused by it is `UNAVAILABLE` with reason `server_restarting`. (The message wording is Brian's, as for
`ServerStopping.message`; the reason is the typed contract.)

**4. The deploy tag names the leaving image.** The round `prestop` takes is tagged `deploy:<v>` where `<v>`
is the image tag the pod was started with (`deploy.image_tag`, set by the chart from `image.tag` with any
`@sha256:` digest stripped, so `sha-<12 hex>` in CI and `dev` locally). It is not `GetServerInfo.version`,
which is `git describe` and does not match an image tag. Two local builds both tagged `dev` share a name;
the newest round with the name wins. The old pod cannot know the
tag of the pod replacing it. It means "the last round written by binary `<v>`", which is exactly what
`make rollback` to `<v>` wants to pin: a round `<v>` can read.

**5. The deploy's trace id is `<image tag>@<tick>`.** `PrepareStopResponse.deploy_id`: the leaving image tag and
the tick of the pre-stop round. The next pod puts the same string as `deploy_id` on its `deploy.recovery`
span when the round it restores carries a `deploy:` tag, so a deploy is one trace end to end without the
old pod knowing the new tag.

**6. The pre-stop instruments are a span and a log line, not metrics.** `prestop` runs in the old pod, which
exits seconds after the hook returns; a counter or histogram written there is never scraped and the new
pod starts at zero. The record of a pre-stop is the `info` log line `prestop complete` (fields `tick`,
`hash`, `tag`, `duration_ms`, `outcome`, `trace_id`), the `deploy.prestop_snapshot` span (pushed to the trace
backend as it ends, so it survives the pod), and `PrepareStopResponse`. `andara_prestop_snapshot_duration_seconds` and
`andara_prestop_outcome_total` are **withdrawn**. `andara_snapshot_rounds_retained{kind}` (`AW-SRV-055`)
lives in the serving process and is unaffected.

## Measuring the interruption

`andara_deploy_interruption_seconds` is a histogram with no labels (buckets 5, 10, 20, 30, 60, 120, 300).

- **Emitter: the new pod,** once per boot, at the moment it first reports `serving`. The old process has no
  clock across the gap, and a script cannot write a series for `prod`.
- **Clock: it starts at the head Tick Boundary Record's timestamp (the Kafka record's CreateTime), and stops at
  the new process's wall clock when `/readyz` first returns `serving`.** The head record is the last instant
  the old World was ticking. Recovery does not expose it today: `AW-SRV-054` adds the read to
  `server/recovery` (`Report.HeadProducedAt`), including when the restored round is already at head, where
  recovery otherwise reads no record. This measures **the World not ticking**, the same span as the RTO in
  `docs/specs/slo/recovery.md` (stop to accepting connections). The window in which Submit was refused but
  the World still ticked (the snapshot, the drain) is not in it; `prestop complete` logs it as
  `refused_ms`, and neither number includes the notice lead. The start is the old pod's
  clock and the stop the new pod's. On the kind box those are one machine; on a cluster with nodes the
  error is the NTP skew between them, and the report says so. The notice lead is not in the interruption:
  the World runs through it.
- A boot with no Tick Boundary Record on the log (a first boot) observes nothing. An unplanned restart
  observes the same series, which is right: it is the same gap to the player. `make deploy` and CI read
  the deploy's own value from the new pod's `deploy interruption` log line (`seconds`, `trace_id`), not
  by querying a series, and compare it with the milestone RTO (120 s at M2, 60 s at Phase 1 exit).
- `AW-INF-007` AC-5 is held here and carried by two stories: `AW-SRV-054` emits the instrument and the log
  line, `AW-INF-041` prints it against the RTO.

## Round tags

A tag is a zero-byte object at `{zone_id}/{tick}/{state_version}/{offset}.tag/{name}`, one per Zone object
in the round. The tick is in the path because a tag is a statement about a *round*, and an offset does not
identify one (an idle Zone repeats its offset across rounds). The name grammar is
`^(deploy|rollback):[A-Za-z0-9._@+-]{1,200}$`.

- `Admin.TagSnapshotRound` writes them (OPERATOR only, idempotent, refuses a round that isn't Complete).
  `Admin.ListSnapshotRounds` reports a name present on every object of the round; retention treats a round
  with a tag on *any* object as tagged, so a crash between two tag Puts fails safe.
- **Retention.** The sweep keeps the newest `snapshot.keep_rounds` untagged rounds, and the newest
  `snapshot.keep_deploy_rounds` tagged rounds, where N counts tagged rounds, not minutes. It never deletes
  the newest complete round. It deletes any round older than `snapshot.max_round_age` (default `552h`, 23
  days), tagged or not, except that same newest complete round: `broker-contract.md` makes a round older
  than `andara.events.v1` retention minus 7 days unusable, and keeping it would promise a recovery that
  `ErrLogGap` refuses. The one exception besides the newest complete round: **the sweep never deletes the
  round `recovery.pin_round` names**, whatever its age or tag count, so a pin cannot outlive its round. A
  pin past 23 days is then an `ErrLogGap` at recovery, which is the honest failure. `make check` (SRE) asserts `snapshot.max_round_age <= retention.ms(events) - 7 d`.
- A tag for a round that is gone is an error before anything moves (`make rollback ROUND=T`).

## Configuration

| Key | Env | Default | Notes |
|-----|-----|---------|-------|
| `deploy.notice_lead` | `ANDARA_DEPLOY_NOTICE_LEAD` | `10s` | between `ServerStopping` and the Submit refusal |
| `deploy.expected_back` | `ANDARA_DEPLOY_EXPECTED_BACK` | `60s` | copied into `ServerStopping.expected_back_seconds` |
| `deploy.image_tag` | `ANDARA_DEPLOY_IMAGE_TAG` | `dev` | set by the chart from `image.tag`, digest stripped; names the `deploy:` tag |
| `deploy.control_socket` | `ANDARA_DEPLOY_CONTROL_SOCKET` | `/run/andara/control.sock` | the Lifecycle service; the chart mounts `emptyDir` there |
| `recovery.pin_round` | `ANDARA_RECOVERY_PIN_ROUND` | `0` | `0` is unset. Set by `make rollback ROUND=T`, cleared by the next `make deploy` or `make rollback`; `AW-SRV-007`'s Configuration table has the server side |
| `snapshot.keep_rounds` | `ANDARA_SNAPSHOT_KEEP_ROUNDS` | `120` | two hours of minute-rounds |
| `snapshot.keep_deploy_rounds` | `ANDARA_SNAPSHOT_KEEP_DEPLOY_ROUNDS` | `30` | counts tagged rounds |
| `snapshot.max_round_age` | `ANDARA_SNAPSHOT_MAX_ROUND_AGE` | `552h` | see Round tags |

Chart value `terminationGracePeriodSeconds`, default `90`, is at least `snapshot.upload_timeout +
deploy.notice_lead + 2 × tick interval + grpc.drain_timeout + sim.drain_timeout_ms + 10 s` (the hook and the
SIGTERM drain share one kubelet clock; the default sum is 72 s); `make check` fails naming the terms if not
(`AW-INF-042`). The keys `deploy.image_tag`, `deploy.control_socket` and `snapshot.max_round_age` are new: SRE registers them in
`deploy/helm/andara/keys.yaml` with the chart child.

## Make targets

| Target | Does | Exit |
|--------|------|-----:|
| `make deploy ENV=<env> TAG=<tag>` | publish+activate `andara.core@<tag>`; `helm upgrade --install --set image.tag`; `kubectl rollout status --timeout`; reads the new pod's `deploy interruption` log line (`seconds`, see Measuring the interruption), prints it against the RTO | `0` ok · `2` hash mismatch · `1` rollout timeout · `6` core pack rejected |
| `make rollback ENV=<env> [ROUND=T] [TAG=<tag>]` | the same upgrade as `make deploy`: `scripts/deploy.sh` and `scripts/rollback.sh` each call `scripts/helm_install.sh`, which gains optional pass-through for extra `--set` values and `--description` (an SRE change to an `AW-INF-003` script; every guard failure in it exits `1`), and it carries (`--values <env>.yaml`, no `--reuse-values`, the Argo CD and Kafka-Ready guards, digest pinning), with `image.tag=<previous>` (which may be `tag@sha256:…` from the digest-pinning step, and `helm_install.sh` passes that through) and `recovery.pin_round=<T or 0>`. **Not** `helm rollback`, which takes no `--set` and so could neither set nor clear the pin. `<previous>` is `TAG`; or, when the current revision is **failed** and its values carry `release.rolled_back_to=<tag>` (the refuse-then-retry flow after a pod exit `4`), that `<tag>`, so `make rollback ROUND=T` retries the old binary it was refused for; or else the `image.tag` of the newest older revision whose status isn't `failed` (`helm history`, then `helm get values --revision <n> --all`). The marker is a value, not the upgrade's `--description`, because Helm overwrites the description of a failed revision. `make rollback` sets `release.rolled_back_to=<previous>`, and `make deploy` sets it empty, so the marker is exactly the revisions that were rollbacks (`helm get values --revision <n> --all`). Without `TAG` it refuses on a **deployed** revision carrying the marker, since "previous" would be the bad image. The description is only the human label (`rollback to <tag>`). `release.rolled_back_to` is a hand-written top-level chart value, not a server key: it's declared in `deploy/helm/andara/schema.base.yaml` (a string) and `values.yaml` (default empty), never in `keys.yaml`, and gets no environment variable. The schema check gains a `helm template --set release.rolled_back_to=x` case. A tag failure (no such round, or one that isn't complete) exits `1` before `helm upgrade`, with `rollback: round <T> not found` or `… is not complete`, and re-tagging a name a round already carries is a no-op success. Exit `7` stays the pod's. After a failed rollback followed by a failed deploy, "previous" would be the pre-rollback image, so the runbook tells the operator to pass `TAG`. With `ROUND`, it first tags `T` `rollback:<T>` (`make snapshot-tag`). The pin stays until the next `make deploy` or `make rollback` | script exits: `0` ok · `1` rollout timeout or another pod exit · `4` state_version · `6` the previous core pack was rejected · `7` the pinned round is incomplete · `8` hash mismatch under the pin |
| `make snapshot-tag ENV=<env> TAG=<t> [ROUND=<tick>]` | a thin wrapper over `andara-cli snapshot tag --name <t> [--round <tick>]`, which tags the newest round, or the round at `<tick>` when given (it refuses a round that isn't `Complete`) (`prestop` tags in-process). `make rollback ROUND=T` passes `ROUND=T`, so the pinned round is the one protected | |

The exit codes are the scripts' (`scripts/deploy.sh`, `scripts/rollback.sh`). Through `make` every failure
is `2`, and the evidence is the script's printed line (`AW-INF-029`'s pattern). A pod's exit code reaches
the script by `kubectl get pod <the pod the rollout is waiting on> -n andara-<env> -o
jsonpath={.status.containerStatuses[0].lastState.terminated.exitCode}`, falling back to
`.state.terminated` before the first restart. Its meanings are `AW-SRV-007`'s: `3` log gap and `6` restore
mismatch both map to the script's `1` with the pod's code in the printed line, `8` hash mismatch maps to
`2` for `make deploy` and stays `8` for `make rollback`. `make deploy`'s own `6` is the core pack step
and isn't the pod's `6`; likewise `make rollback`'s `6` is the previous core pack's rejection, and the pod's `6` maps to its `1`. For `make deploy`, any pod exit not named here (`1`, `4`, `7`) maps to the
script's `1` with the pod's code printed.


## The pin lifecycle

`make rollback ROUND=T` pins recovery to round `T` through `recovery.pin_round`. The rulings of 2026-10-03
(PR #356's four questions), kept here as the contract:

1. **The next `make deploy` or `make rollback` clears the pin.** `make rollback` is the deploy's own
   upgrade with `image.tag=<previous>`, not `helm rollback`, which takes no `--set`. Every deploy and every
   rollback passes the key explicitly, `0` unless `ROUND=T` is given, so clearing costs no extra rollout.
   It stays set across a restart that isn't one of those; that recovers from `T` again, slower, to the same
   state, because the log tail is replayed.
2. **`make rollback ROUND=T` tags `T` `rollback:<T>` before it sets the pin, when a server answers.**
   Retention counts any tag, so a late restart doesn't exit `7` `missing` and crash-loop. In the retry after
   a pod exit `4` no server is running (one replica, the crash-looping old binary), so the Admin call cannot
   be made: the script then skips the tag with a warning, and the round is still protected because the
   sweep never deletes the round `recovery.pin_round` names (Round tags). A connection failure skips; a
   `NOT_FOUND` or `FAILED_PRECONDITION` answer still exits `1`. `ROUND=deploy:<v>` is accepted in place of a
   tick and resolves to the newest round carrying that tag, when a server answers.
3. **`make rollback`'s exits are the script's** (table above), read from the pod's last termination code,
   and it prints `rollback: pod exited <n> (<meaning>): round <T> cause=<c>`.
4. **The spelling is `ROUND=T`.**

**The consequence we won't like.** Between a rollback and the next deploy, a pod restart recovers from the
pinned round. It is correct and slower, and if the round's tag were removed by hand it would exit `7`.
The runbook names both.

## The `state_version` rollback

A binary can always be rolled back to a round it can read, and the log replays the interval. That works
because the log holds Commands and Commands carry no semantics (ADR-0005: replay runs only `Apply`). It
fails, loudly, when the newer binary changed what a Command *means*: the pod exits `8` and the rollback
needs a fix-forward. A pod that cannot read the current `state_version` exits `4` naming both versions,
and `make rollback ROUND=T` with a round it can read is the way through. `andara.events.v1` retention
(30 days) bounds how old a pinned round may be, as Round tags says.

## Dev under Argo CD

`dev` is deployed by Argo CD from `main` (`AW-INF-019`), and `make deploy ENV=dev` refuses while that
Application exists. The pre-stop snapshot and post-start recovery live in the pod, so `dev` gets them
whoever triggers the roll. The `andara.core` publish-and-activate step does not run on `dev` (it reads its
Templates from a ConfigMap, `content.source: dir`); the story that moves `dev` to the store carries it as a
`PreSync` Job. `andara_deploy_interruption_seconds` is recorded on `dev` rolls too; the RTO comparison reads
`prod`'s series (`namespace="andara-prod"`) and CI's `local` run, never `dev`'s.

## Revisit when

- Sharding is active: a rolling update stops being a World-wide interruption and the clock above becomes
  per Zone.
- The cluster has more than one node: the clock's skew term becomes real.
- A hook needs more than the Lifecycle socket: that is a decision about the operator surface, ADR-0003.
