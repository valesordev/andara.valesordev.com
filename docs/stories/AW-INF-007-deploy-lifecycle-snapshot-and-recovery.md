---
id: AW-INF-007
title: Deploy lifecycle — pre-stop snapshot, post-start recovery, and rollback
epic: EPIC-01
component: infra
type: infra
status: ready
size: M
depends_on: [AW-INF-003, AW-SRV-007, AW-SRV-030]
blocks: []
lane: architecture
risk: high
---

## Context

ADR-0001 accepts that a deploy interrupts the World until sharding is active: a rolling update of a
one-replica `StatefulSet` is a scheduled restart. What makes it acceptable rather than alarming is that
recovery is exact — the new pod resumes from a snapshot and the log tail with a matching State Hash.

That has to be expressed as lifecycle, not as a runbook step someone remembers. A pre-stop hook takes a
snapshot so the replay is short; the new pod recovers before it reports ready; a rollback is the same
mechanism in reverse, with one hard case — a previous binary that cannot read the newer `state_version`
— that this story makes a documented path rather than a surprise.

## User story

As an operator, I want deploying to be a routine interruption measured in seconds, so that shipping is
not an event.

## Scope

### In scope
- Pre-stop: `andara-server prestop` — emit `ServerStopping` (world scope), wait `deploy.notice_lead`,
  stop accepting `Submit`, snapshot round at the next boundary, commit offsets, exit. Bounded by
  `terminationGracePeriodSeconds`, which this story derives.
- Post-start: `AW-SRV-007` recovery, readiness only after verify (already the contract; this story tests
  it under a rolling update).
- `make deploy ENV=<env> TAG=<tag>` and `make rollback ENV=<env> [--round T]` as the only deploy path.
- The `andara.core` step (ADR-0010 §8): `make deploy` publishes and activates the core pack built
  with the image (`content/core/` in the repo, compiled by `AW-CLI-006`) before the new pod reports
  ready, as `operator`, no second approver (`AW-SRV-013` AC-11). Rollback activates the previous
  core version the same way.
- The `state_version` rollback path: refuse, then `rollback --round` pins an older round and the old
  binary recovers from it, replaying the tail the newer binary wrote — which is safe because the log is
  Commands, not state (ADR-0005: replay never re-runs anything but `Apply`).
- Interruption measurement: `andara_deploy_interruption_seconds` from `ServerStopping` to first
  `serving`, compared to the RTO SLO in the CI job summary.
- Retention of snapshot rounds: keep `snapshot.keep_rounds` (default 120 = 2 h) plus every round tagged
  by a deploy for `snapshot.keep_deploy_rounds` (default 30).

### Out of scope
- The snapshot and recovery mechanisms — `AW-SRV-006`, `AW-SRV-007`.
- Image build and publish — `AW-INF-001`'s CI.
- Zero-downtime deploys, which require sharding and partition draining. Not Phase 1.
- The words players see. `ServerStopping` carries a `message` field; its content is Brian's.

## Acceptance criteria

1. **Given** a running deployment **when** `make deploy` triggers a rolling update **then** the pre-stop
   hook completes a snapshot round before `SIGTERM`, `andara_prestop_snapshot_duration_seconds` is
   recorded, and the hook finishes inside `terminationGracePeriodSeconds − 10 s`.
2. **Given** the new pod **when** it recovers **then** its State Hash at the pre-stop round's tick equals
   the hash the old pod logged at pre-stop, and only then does `/readyz` return `serving`.
3. **Given** a recovery whose hash does not match **when** it completes **then** the pod never becomes
   ready, `RecoveryStateMismatch` fires, the StatefulSet rollout stalls, and `make deploy` exits `2`
   printing the runbook path.
4. **Given** `make rollback` to a binary that cannot read the current `state_version` **when** the old
   pod starts **then** it exits `4` naming both versions; **given** `make rollback --round T` with a
   round the old binary can read **then** it recovers from `T`, replays the newer binary's tail, and
   reaches `serving` with a hash equal to the newer binary's `TickCompleted` at head — or exits `2` if
   the newer binary's semantics changed, which is the honest signal that a rollback needs a fix-forward.
5. **Given** a deploy **when** it completes **then** `andara_deploy_interruption_seconds` is recorded, and
   the CI job summary shows it against the milestone RTO (120 s at M2, 60 s at Phase 1 exit).
6. **Given** connected players **when** pre-stop begins **then** each receives `ServerStopping` at least
   `deploy.notice_lead` before the stream closes, and `andara-cli play` reconnects with backoff and
   rebinds within `session.linkdead_grace` (`AW-SRV-015` AC-9).
7. **Given** `snapshot.keep_rounds` exceeded **when** the retention sweep runs **then** the oldest
   untagged rounds are deleted, deploy-tagged rounds are kept, and never the newest complete round.
8. **Given** a deploy whose image carries `andara.core` version `V` **when** `make deploy` runs
   **then** `andara.core@V` is published and active before the new pod's `/readyz` is polled, the
   activation audit record names the deploy tag, and a Builder pack compiled against `V-1` keeps
   loading (`AW-SRV-012` AC-8 is the reverse case).
9. **Given** `terminationGracePeriodSeconds` in values **when** `make check` runs **then** it is at
   least `snapshot.upload_timeout + deploy.notice_lead + 2 × tick interval + 10 s`, or the check fails
   naming the terms.

## Interface contract

### Lifecycle

```
kubectl rollout ──▶ preStop exec: andara-server prestop
                       ├─ emit ServerStopping{message, expected_back_seconds}   (world scope)
                       ├─ sleep deploy.notice_lead (default 10 s)
                       ├─ ingress.degraded=1: Submit → UNAVAILABLE "server restarting"
                       ├─ at next boundary: SnapshotAll → Put ×zones → tag round "deploy:<tag>"
                       ├─ commit offsets; log "prestop complete tick=T hash=H"
                       └─ exit 0
                    SIGTERM ──▶ drain (sim.drain_timeout_ms) ──▶ exit
new pod ──▶ recover (AW-SRV-007) ──▶ /readyz serving ──▶ endpoints
```

```protobuf
// CONTRACT SKETCH — addition to andara/game/v1/event.proto payload oneof
ServerStopping { string message = 1; uint32 expected_back_seconds = 2; }
```

### Make targets

| Target | Does | Exit |
|--------|------|-----:|
| `make deploy ENV=<env> TAG=<tag>` | publish+activate `andara.core@<tag>`; `helm upgrade --install --set image.tag`; `kubectl rollout status --timeout`; reads `andara_deploy_interruption_seconds`, prints it against RTO | `0` ok · `2` hash mismatch · `1` rollout timeout · `6` core pack rejected |
| `make rollback ENV=<env> [ROUND=T]` | `helm rollback` to previous revision; with `ROUND`, sets `recovery.pin_round=T` for one boot | as above · `4` state_version |
| `make snapshot-tag ENV=<env> TAG=<t>` | tags the newest round (used by `prestop`) | |

### Configuration

| Key | Env | Default | Notes |
|-----|-----|---------|-------|
| `deploy.notice_lead` | `ANDARA_DEPLOY_NOTICE_LEAD` | `10s` | AC-6 |
| `deploy.expected_back` | `ANDARA_DEPLOY_EXPECTED_BACK` | `60s` | copied into `ServerStopping` |
| `recovery.pin_round` | `ANDARA_RECOVERY_PIN_ROUND` | — | one-boot override used by `rollback` |
| `snapshot.keep_rounds` | `ANDARA_SNAPSHOT_KEEP_ROUNDS` | `120` | |
| `snapshot.keep_deploy_rounds` | `ANDARA_SNAPSHOT_KEEP_DEPLOY_ROUNDS` | `30` | |

Chart value `terminationGracePeriodSeconds` default `90`, validated by AC-9.

### Round tags

A tag is a zero-byte object at `{zone_id}/{state_version}/{offset}.tag/{name}`; `ListRounds` reports
tags. Retention never deletes a tagged round while it is within `keep_deploy_rounds` of the newest tag.

## Data / state impact

The `state_version` compatibility question only appears on rollback and is answered by AC-4: a binary
can always be rolled back to a round it can read, and the log replays the interval. That works because
the log holds Commands and Commands are semantics-free; it fails, loudly, when the newer binary changed
what a Command *means*, which is exactly the case where rollback should not be silent. Retention of
`andara.commands.v1` is infinite (`AW-INF-004`), so the interval is always available; `andara.events.v1`
retention (30 d) bounds how old a pinned round may be, and `AW-SRV-007` exits `3` past it.

## Observability requirements

- **Metrics:** `andara_deploy_interruption_seconds` (histogram), `andara_prestop_snapshot_duration_seconds`
  (histogram), `andara_prestop_outcome_total{outcome}` (`complete`, `timeout`, `store_error`),
  `andara_snapshot_rounds_retained` (gauge), `andara_recovery_duration_seconds` (`AW-SRV-007`).
- **Logs:** `info` "prestop complete" with `tick`, `hash`, `tag`; `info` "deploy interruption" with
  seconds; both carry `trace_id` of the deploy span.
- **Traces:** `deploy.prestop_snapshot` and `deploy.recovery` as spans sharing a `deploy_id` attribute
  (`TAG`), so a deploy is one trace end to end.
- **Alerts:** none new. `RecoveryStateMismatch` (`AW-SRV-007`) is the one that pages.

## Test plan

- **Integration (kind, CI):** rolling update with 20 connected fixture clients asserting AC-1, AC-2,
  AC-5, AC-6; injected hash corruption asserting AC-3 and that the rollout stalls; a deliberate
  `state_version` bump in a test image asserting both halves of AC-4; retention sweep asserting AC-7;
  AC-9 as a `helm unittest`.
- **Manual/operator:**
  ```
  make deploy ENV=local TAG=$(git rev-parse --short HEAD)   # expect: "interruption 23.4s (RTO 120s)"
  andara-cli snapshot list                                  # expect: newest round tagged deploy:<sha>
  make rollback ENV=local                                   # expect: previous revision serving, hash match
  ```

## Definition of done

CLAUDE.md §8, plus: the rolling-update test runs in CI; `docs/runbooks/deploy-and-rollback.md` states
the expected interruption as a number from the last CI run, and every step is a make target.
- **Gate, 2026-09-19:** `replicaCount > 1` is not a supported configuration until `AW-SRV-030`
  lands — the Gateway's routing table and Hub read the local engine, so a Character crossing to a
  Zone another pod consumes leaves its Session `in_transit` and unperceiving. The chart pins one
  replica; this story's rebalance procedure assumes `AW-SRV-030`.

## Open questions

- `[NEEDS BRIAN]` The `message` in `ServerStopping` — countdown, in-world notice, or nothing. The
  field and the lead time exist either way; the words do not affect this contract.
- `[ASSUMPTION]` Deploy cadence is bounded by the write-availability budget (~40/28 d at 99.9%,
  `slo/world-write-availability.md`), not by this story. `make deploy` prints the budget remaining.
- `[ASSUMPTION]` Retention numbers 120 / 30 rounds. Two hours of minute-rounds plus a month of deploy
  points is enough to pin any rollback anyone would attempt; disk cost is `andara_snapshot_bytes ×
  rounds`.
