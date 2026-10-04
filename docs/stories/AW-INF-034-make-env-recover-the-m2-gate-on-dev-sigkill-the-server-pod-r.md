---
id: AW-INF-034
title: make env-recover — the M2 gate on dev: SIGKILL the server pod, recover within the RTO
epic: EPIC-04
component: infra
type: infra
status: draft
size: M
depends_on: [AW-INF-032, AW-INF-011, AW-INF-009]
blocks: []
lane: sre
risk: medium
---

## Context

SPRINT-04 proves the M2 gate on the local stack with `make stack-recover` (`AW-INF-032`). SPRINT-05's
demo goal (Brian, 2026-10-03) is the same gate on `dev`. On the cluster, several things the
compose stack never exercises have to hold together:
- the kubelet restarts a killed container;
- the startup probe gives recovery its full budget instead of killing the pod mid-replay
  (`AW-INF-011`);
- the edge routes players back only once the pod is Ready;
- `RecoveryStateMismatch` can fire from a pod that never becomes Ready (`AW-INF-009`'s cluster
  clause, amended 2026-10-02).

None of these is proven today. Phase 1 exit criterion 3 asks the server to survive a deliberate
kill, and the place to prove it is a cluster, not compose. `dev` is the only cluster
environment running.

This story is the operator's one-command proof, the cluster counterpart of `stack-recover`. It
reuses that target's sequence and assertions, against `dev`. `AW-INF-007`'s deploy lifecycle is a
different path (a planned SIGTERM with a pre-stop snapshot), and it stays out of scope.

## User story

As an operator, I want one make target that kills `dev`'s server with SIGKILL while players are in
the World and proves the World comes back as it was, so that M2's gate is shown on the cluster and
not only on compose.

## Scope

### In scope
- `scripts/env_recover.py` and `make env-recover ENV=<env> CONFIRM=andara-<env>`, refusing the
  way `world_reset.py` refuses (exit 2).
- **The kill comes from outside the container's PID namespace.** The chart `exec`s
  `andara-server` from `/bin/sh`, so it's PID 1 in its container, and the kernel ignores a SIGKILL
  sent to a PID namespace's init from inside that namespace. That rules out `kubectl exec … kill -9 1`
  and an ephemeral debug container targeting `server`. The script sends SIGKILL from the node's PID
  namespace instead:
  - the node is `andara-0`'s `spec.nodeName`, one of the box's kind node containers;
  - the PID comes from `crictl inspect` of the `server` container's `containerID` (from
    `containerStatuses`), run through `docker exec` on that node, never from a process-name match.
    Another environment's server can share the node.

  So the target runs **on the box**, where the nodes are. Deleting
  the pod isn't the kill either: that's a reschedule with SIGTERM.
- Two throwaway player Accounts made through Admin with the operator credential
  (`ANDARA_BOOTSTRAP_OPERATOR`, the one `content-seed` uses). Admin is reached through `dev`'s edge,
  which admits the box and the tailnet (`admin.allowedCIDRs` in `deploy/helm/values/dev.yaml`).
  Players play through the same edge. A port-forward to `andara-0` doesn't survive the restart, so
  it isn't used. At the end, each Account is set `disabled` with `andara-cli account set-status`.
- `AW-INF-032`'s sequence: the first move, a complete round `R`, a second move held only in the log
  tail, the kill, and the reconnect.

### Out of scope
- The deploy lifecycle, rollback and `recovery.pin_round`: `AW-INF-007`, after its split.
- Running in CI. It's destructive to `dev`'s live sessions, so it's an operator's target, run at
  the sprint demo and by hand.
- `prod`, which it refuses until a story authorizes it, and `local`, which is `make stack-recover`.
- Making `RecoveryStateMismatch` fire. That needs a corrupted round, which belongs to `AW-INF-009`'s
  own proof. This target asserts only that the alert doesn't fire on a matching recovery.

## Acceptance criteria

1. **Given** `ENV=dev` with `CONFIRM` missing or not `andara-dev`, or `ENV=prod`, or `ENV=local`
   **when** `make env-recover` runs **then** it exits 2 before touching the cluster, with
   `env-recover: <the reason>`. The reasons are `refusing without CONFIRM=andara-dev; this kills
   dev's server`, `prod is not enabled`, and `local is make stack-recover`.
2. **Given** `dev` Ready **when** the target runs **then**:
   - it first confirms that `RecoveryStateMismatch` is loaded in the tenant's ruler (namespace
     `andara`), through `AW-INF-009`'s ruler credentials. A missing rule makes the run inconclusive,
     because AC-7 would pass without checking anything.
   - It reads `ALERTS{alertname="RecoveryStateMismatch",namespace="andara-dev"}`. If the alert is
     firing already, the run is inconclusive (AC-8).
   - Players A and B enter play through the edge, and A moves to Room 1.
   - The script waits for a complete round newer than the one it recorded before play, records it
     as `R`, then moves A to Room 2. It confirms that `snapshot list`'s newest complete round is
     still `R`, as `AW-INF-032` AC-1 to AC-3 do.
3. **Given** round `R` **when** the script sends SIGKILL from the node **then**, within 10 s,
   `andara-0`'s `server` container `restartCount` rises by exactly 1, the pod's UID is unchanged (a
   restart, not a reschedule), and `lastState.terminated.exitCode` is `137`. If `restartCount` hasn't
   moved in 10 s, the script exits 1 with `env-recover: SIGKILL didn't land; restartCount unchanged`.
4. **Given** the kill **when** the script polls the pod to a deadline of `ENV_RECOVER_RTO` (default
   120 s) from the kill, per `live-assertions.md` **then** `andara-0` is Ready, with no further
   restart in that window. The script prints the measured kill-to-Ready seconds.
5. **Given** the pod Ready **when** the script queries Grafana Cloud's metrics, polled to a
   deadline, the way `observe_check.py` (`AW-INF-008`) reads them **then**, for
   `namespace="andara-dev"`:
   - `andara_recovery_round_tick == R`. The killed process recovered at its own boot, before `R`
     existed, so its last sample holds an older round and can't satisfy this.
   - `andara_recovery_state_hash_match` is `1`, **and**
     `timestamp(andara_recovery_state_hash_match{namespace="andara-dev"})` is later than the kill
     time recorded in AC-3. `timestamp()` takes a bare selector here, because PromQL returns the
     sample's own time only for a bare selector. Around any other expression it returns the
     evaluation time, which is always after the kill.

   A restart keeps the pod's name and IP, so each metric is one series across both processes, and
   the labels don't tell them apart.
6. **Given** the recovery **when** A's and B's `play` clients, both run with `--show-protocol` as
   `stack_play.sh` runs them, reconnect through the edge **then**:
   - A's `look` shows Room 2, the move only the log tail held;
   - B's `look` shows the spawn Room;
   - neither transcript has `reason=already_live`, `Waiting for your previous session to end.`,
     `leaves the world` or `fades from the world` for either Character between the kill and the
     reconnect. (On a reconnect, `play` shows only the waiting line, never the reason. The reason
     shows only in the protocol view.)
7. **Given** the run complete **when** the script queries
   `ALERTS{alertname="RecoveryStateMismatch",namespace="andara-dev"}` over the window from AC-2's
   start to the end, through the same Prometheus API and read token **then** it has no sample with
   `alertstate="firing"`. `AndaraServerUnavailable` is printed as observed (pending, firing or
   inactive), not asserted, since a recovery inside its `for` never fires it.
8. **Given** any failure, or an inconclusive run (the rule isn't loaded, the alert is already firing
   at the start, or a round is newer than `R`) **when** the script exits **then** it exits 1 with `env-recover: <what failed>`,
   prints both transcripts and the `server` container's previous and current logs (last 50 lines
   each), attempts to disable both Accounts, naming any it couldn't reach, and leaves no
   `andara-cli` child running.

## Interface contract

- `make env-recover`: `## env-recover: the M2 gate on ENV — SIGKILL the server container mid-play,
  recover from a snapshot within the RTO with a matching State Hash, both players rebind — kills
  ENV's live sessions; runs on the box — ENV=<env> CONFIRM=andara-<env>`.
- Environment it reads:
  - `ANDARA_BOOTSTRAP_OPERATOR` (existing, as `content-seed`);
  - `ENV_RECOVER_RTO` (seconds, default `120`, in the Makefile);
  - the Grafana Cloud read credentials `observe_check.py` already uses (`GRAFANA_CLOUD_READ_TOKEN`
    and the endpoints beside it), for the `ALERTS` and metric queries in AC-2, AC-5 and AC-7;
  - `AW-INF-009`'s ruler credentials (`MIMIR_ADDRESS`, `MIMIR_TENANT_ID`, `MIMIR_API_KEY`), only for
    AC-2's check that the rule is loaded. `AW-INF-009` keeps them as CI secrets, so on the box the
    operator exports the same three values, from the source `AW-INF-009`'s runbook names. Without
    them, AC-2 exits 1 naming the missing variable.

  It adds no new credentials.
- Exit codes: `0` all assertions held; `1` an assertion failed, a precondition is missing, or the
  run was inconclusive; `2` usage or refusal (AC-1), as `world_reset.py`.
- Output: `env-recover: <step>` lines, including
  `env-recover: Ready <N>s after the kill (RTO <rto>s), restartCount <a>→<b>, round <R>, hash match`,
  and ending `env-recover: M2 gate on dev — killed, recovered from a snapshot, hash matched, both
  rebound — passes`.
- Credentials go in an isolated `$XDG_CONFIG_HOME`, as `stack_play.sh` does.

## Data / state impact

Each run leaves two disabled Accounts and two dormant Characters on `dev`, with random suffixes.
`make world-reset ENV=dev` clears them. Live Sessions on `dev` drop and reconnect at the kill. That
is the test, and the `CONFIRM` guard is why it exists.

## Observability requirements

- **Metrics:** none added. AC-5 reads `AW-SRV-007`'s series from the recovered server.
- **Logs:** the script's progress lines, plus the `server` container's previous and current logs on
  failure.
- **Traces:** none added. The script prints the `recovery.run` `trace_id` from the server's ready
  line, so §8 can resolve it in Tempo.
- **Alerts:** none added. AC-7 observes `AW-INF-009`'s rules.

## Test plan

- **Unit:** `scripts/tests`:
  - the guard (AC-1, each of its four cases);
  - the PID resolved from the `server` container's ID on `spec.nodeName`, never a name match, against
    fake pod and `crictl` JSON;
  - the restart-versus-reschedule check and the unlanded-kill message (AC-3);
  - AC-5 against a fake query response: a pre-kill sample (timestamp before the kill, value `1`)
    fails;
  - all three inconclusive paths (the rule isn't loaded, the alert is already firing, a newer round).
- **Integration:** none in CI. The target is destructive and runs against `dev`.
- **Manual/operator:** `make env-recover ENV=dev CONFIRM=andara-dev` ends with "M2 gate on dev …
  passes". It's run at SPRINT-05's demo, and the run is recorded in the §8 record.

## Definition of done

CLAUDE.md §8, plus:
- `AW-SRV-007`'s "fires on the cluster" line, which `AW-INF-009` inherits, isn't this story's to
  close, but AC-7's run is recorded where `AW-INF-009`'s §8 can cite it.
- **Inherited from `AW-SRV-043`'s §8 pass (2026-10-04):** the operator step. The §8 record shows
  `make projector-rebuild ENV=dev` logging `state projector restore verified` on a `dev` that runs
  the build with `AW-SRV-043` in it, with `andara_restore_total{caller="projector",outcome="ok"}` at 1.
- The §8 record shows `andara_recovery_*` read from `dev`'s recovered server, and the `recovery.run`
  trace resolved in Tempo.

## Open questions

- `[ASSUMPTION]` `dev`'s snapshot interval is the server's default of 60 s (`dev.yaml` doesn't set
  it), so the round wait adds up to 90 s.
- `[ASSUMPTION]` The kind node's container is reachable with `docker exec` from the box, as Brian's
  platform runs it (`scripts/kind_platform.sh`). If the node runtime changes, the kill mechanism
  changes with it. AC-3's observable result is the contract.
