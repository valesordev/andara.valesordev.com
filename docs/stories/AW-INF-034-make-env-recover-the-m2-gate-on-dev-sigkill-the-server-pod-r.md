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
- the startup probe gives recovery its full RTO instead of killing the pod mid-replay
  (`AW-INF-011`);
- the edge routes players back only once the pod is Ready;
- `RecoveryStateMismatch` can fire from a pod that never becomes Ready (`AW-INF-009`'s cluster
  clause, amended 2026-10-02).

None of these is proven today. Phase 1 exit criterion 3 is about the server surviving a deliberate
kill in an environment that matters, and `dev` is the only one there is.

This story is the operator's one-command proof, the cluster counterpart of `stack-recover`. It
reuses that target's assertions, against `dev`'s edge and Admin. `AW-INF-007`'s deploy lifecycle is
a different path (a planned SIGTERM with a pre-stop snapshot), and it stays out of scope.

## User story

As an operator, I want one make target that kills `dev`'s server with SIGKILL while players are in
the World and proves the World comes back as it was, so that M2's gate is shown on the cluster and
not only on compose.

## Scope

### In scope
- `scripts/env_recover.py` (or `.sh`; SRE's choice, matching `world_reset.py`) and
  `make env-recover ENV=<env> CONFIRM=andara-<env>`.
- The kill is SIGKILL to the `server` container's process in `andara-0`, so no pre-stop hook and
  no SIGTERM handler runs, and the kubelet restarts the container in the same pod. The mechanism
  is SRE's to choose, and the build record states it. Deleting the pod is **not** the kill: that's
  a reschedule with SIGTERM, which `AW-INF-007`'s pre-stop snapshot will hook into.
- Two throwaway player Accounts made through Admin with the operator credential, as
  `content-seed` authenticates (`ANDARA_BOOTSTRAP_OPERATOR`). They play through `dev`'s edge. At the
  end each is set `disabled` with `andara-cli account set-status`.
- The same sequence as `AW-INF-032`: the first move, a complete round `R`, a second move held only in
  the log tail, the kill, and the reconnect.

### Out of scope
- The deploy lifecycle, rollback and `recovery.pin_round`: `AW-INF-007`, after its split.
- Running in CI. It's destructive to `dev`'s live sessions, so it's an operator's target, run at
  the sprint demo and by hand.
- `prod`. The target refuses `ENV=prod` until a story authorizes it.
- Making `RecoveryStateMismatch` fire. That needs a corrupted round, which belongs to `AW-INF-009`'s
  own proof. This target asserts only that the alert stays inactive on a matching recovery.

## Acceptance criteria

1. **Given** `ENV=dev` without `CONFIRM=andara-dev` **when** `make env-recover` runs **then** it
   exits 1 with `env-recover: refusing without CONFIRM=andara-dev; this kills dev's server`, and
   kills nothing. **Given** `ENV=prod` **then** it exits 1 with `env-recover: prod is not enabled`.
2. **Given** `dev` Ready **when** the target runs **then** players A and B enter play through the
   edge, and A moves to Room 1. The script waits for a complete round newer than the one it recorded
   before play, records it as `R`, and moves A to Room 2. It confirms that `snapshot list`'s newest
   complete round is still `R`, as `AW-INF-032` AC-1 to AC-3 do.
3. **Given** round `R` **when** the script sends SIGKILL to the `server` container **then**
   `andara-0`'s `server` container `restartCount` rises by exactly 1, the pod's UID is unchanged (a
   restart, not a reschedule), and `lastState.terminated.exitCode` is `137`.
4. **Given** the kill **when** the script polls the pod to a deadline of 120 s from the kill, per
   `live-assertions.md` **then** `andara-0` is Ready, with no further restart in that window. The
   script prints the measured kill-to-Ready seconds.
5. **Given** the pod Ready **when** the script queries Grafana Cloud's metrics for
   `namespace="andara-dev"`, polled to a deadline, the way `observe_check.py` (`AW-INF-008`) reads
   them **then** `andara_recovery_state_hash_match` is `1`, `andara_recovery_round_tick` equals `R`,
   and `andara_acknowledged_commands_lost_total` is `0`.
6. **Given** the recovery **when** A's and B's `play` clients reconnect through the edge **then** A's
   `look` shows Room 2, and neither transcript shows `already_live` or a despawn line for either
   Character between the kill and the reconnect.
7. **Given** the run complete **when** the script queries Grafana Cloud's ruler state for
   `RecoveryStateMismatch` and `AndaraServerUnavailable` **then** `RecoveryStateMismatch` never
   left `inactive` during the run. `AndaraServerUnavailable` is reported as observed (`pending`,
   `firing` or `inactive`), not asserted, since a recovery under its `for` never fires it.
8. **Given** any failure, including the inconclusive case of a round newer than `R` **when** the
   script exits **then** it exits 1 with `env-recover: <what failed>`, prints both transcripts and
   the `server` container's previous and current logs (last 50 lines each), disables both Accounts,
   and leaves no `andara-cli` child running.

## Interface contract

- `make env-recover`: `## env-recover: the M2 gate on ENV — SIGKILL the server container mid-play,
  recover from a snapshot within the RTO with a matching State Hash, both players rebind — kills
  ENV's live sessions — ENV=<env> CONFIRM=andara-<env>`.
- Environment it reads: `ANDARA_BOOTSTRAP_OPERATOR` (existing, as `content-seed`), plus
  `ENV_RECOVER_RTO` (seconds, default `120`, set in the Makefile like `STACK_RECOVER_RTO`). For
  AC-5 and AC-7, it reads the Grafana Cloud read credentials `observe_check.py` already uses
  (`GRAFANA_CLOUD_READ_TOKEN` and the endpoints beside it), and adds none.
- Exit codes: `0` all assertions held; `1` an assertion failed, a precondition is missing, the
  run was refused (AC-1), or the run was inconclusive.
- Output: `env-recover: <step>` lines, including
  `env-recover: Ready <N>s after the kill (RTO 120s), restartCount <a>→<b>, round <R>, hash match`,
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

- **Unit:** `scripts/tests`: the guard (AC-1), the restart-versus-reschedule check (AC-3) against
  fake pod JSON, and the inconclusive path.
- **Integration:** none in CI. The target is destructive and runs against `dev`.
- **Manual/operator:** `make env-recover ENV=dev CONFIRM=andara-dev` ends with "M2 gate on dev …
  passes". It's run at SPRINT-05's demo, and the run is recorded in the §8 record.

## Definition of done

CLAUDE.md §8, plus:
- `AW-SRV-007`'s "fires on the cluster" line, which `AW-INF-009` inherits, isn't this story's to
  close, but AC-7's run is recorded where `AW-INF-009`'s §8 can cite it.
- The §8 record shows `andara_recovery_*` read from `dev`'s recovered server, and the `recovery.run`
  trace resolved in Tempo.

## Open questions

- `[ASSUMPTION]` The `server` container's image has no shell. If so, the SIGKILL comes from an
  ephemeral debug container sharing the pod's process namespace, or from the kubelet's node. SRE
  chooses, and the contract only fixes AC-3's observable result (same UID, `restartCount` + 1,
  exit `137`).
- `[ASSUMPTION]` `dev`'s snapshot interval is the chart's 60 s, so the round wait adds up to 90 s.
- `[ASSUMPTION]` Players reach `dev` through the edge on the tailnet, as the Builder's Guide
  describes. The target runs from a tailnet device or the box.
