---
id: AW-INF-037
title: make server-stop and server-start take the server down and back up on an Argo-managed environment
epic: EPIC-05
component: infra
type: chore
status: draft
size: S
depends_on: []
blocks: []
lane: sre
risk: low
---

## Context

`docs/runbooks/server-crashlooping.md`'s first mitigation is "stop the loop before diagnosing":
`kubectl -n andara-<env> scale statefulset andara --replicas=0`. On `dev` that doesn't stick. The
`andara-dev` Application has `selfHeal: true`, and only the projector Deployment's `/spec/replicas` is in
`ignoreDifferences` (`deploy/argocd/andara-dev.yaml`), so Argo CD puts `statefulset/andara` back to one
replica within seconds and the crash loop resumes. `make world-reset` already suspends automated sync
before scaling (`scripts/world_reset.py`), but nothing exposes that on its own. The runbook says to
suspend sync first, and that isn't a target: a §9 defect, which SRE found in the review of the recovery
exit-code runbooks (#362) and left for PM, since SRE can't write the story.

`make projector-stop` and `make projector-start` (`AW-INF-025`) are the precedent for the pair.

## User story

As an operator, I want one command that stops the server on `dev` and one that starts it again, so that
I can stop a crash loop and diagnose it without Argo CD undoing the stop.

## Scope

### In scope
- `make server-stop ENV=<env>`: suspends the Application's automated sync, scales `statefulset/andara` to
  zero, and waits for the pods to be gone.
- `make server-start ENV=<env>`: scales back to the replica count the chart sets, restores the sync
  policy that was suspended, and waits for Ready.
- The runbook (`docs/runbooks/server-crashlooping.md`) names the targets in place of its hand-typed
  `kubectl` step. `docs/runbooks/` is SRE's, so the edit is part of this story.
- Both are in `.PHONY` and `make help`.

### Out of scope
- `make world-reset`'s own suspend and restore, which stay as they are.
- A server stop for `prod` or the local stack. `[ASSUMPTION]` `dev` is the only Argo-managed environment
  today; `prod` follows when it has an Application.
- The deploy lifecycle's planned stop (`AW-INF-007`).

## Acceptance criteria

1. **Given** a running `dev` server **when** `make server-stop ENV=dev` runs **then** the server pod is gone
   and `statefulset/andara` reads 0 replicas, and still reads 0 after 60 s.
2. **Given** the server stopped **when** `make server-start ENV=dev` runs **then** the StatefulSet is back at
   the chart's replica count, the pod is Ready, and the Application's automated sync policy is the one it
   had before the stop.
3. **Given** `server-stop` run twice **when** the second runs **then** it succeeds and changes nothing
   (idempotent, CLAUDE.md §9). The same holds for `server-start` on a running server.
4. **Given** an environment with no Argo CD Application **when** `server-stop` runs **then** it scales
   without suspending anything and says so, the way `world_reset.py` does.
5. **Given** a stopped server **when** the Application is synced by hand without `server-start` **then**
   [Open question 1: the target warns, or the suspended sync is what keeps it stopped].
6. **Given** a missing or unknown `ENV` **when** either target runs **then** it exits non-zero and says what
   it expected.

## Interface contract

- `make server-stop ENV=<env> [SERVER_STOP_TIMEOUT=120s]` and
  `make server-start ENV=<env> [SERVER_START_TIMEOUT=300s]`, as `projector-stop` and `projector-start` take
  theirs. `[ASSUMPTION]` the names and the timeout variables are SRE's to change.
- Exit codes: `0` done; `1` the wait timed out; `2` the environment is missing or unknown.
- `[ASSUMPTION]` no `CONFIRM=` argument: unlike `world-reset`, a stop destroys nothing and `server-start`
  undoes it. Open question 2.
- `server-start` restores the sync policy that `server-stop` recorded. Where it records it (an annotation on
  the Application, a local state file) is the implementation's to choose and is Open question 3.

## Data / state impact

None to the World. `statefulset/andara` is scaled and the Application's `syncPolicy.automated` is suspended,
then restored. The snapshot claim is retained (`whenScaled: Retain`).

## Observability requirements

A developer target with no server, CLI command or deployed workload of its own. It prints what it did.

### Metrics
None. `AndaraServerUnavailable` firing during a stop is correct, and the runbook says so.
### Logs
One stdout line per step: `server-stop: suspended automated sync on andara-dev`, `scaled to 0`, `pods gone`.
### Traces
None.
### Alerts
None. The runbook's existing text about `AndaraServerUnavailable` stays.

## Test plan
- **Unit:** the script's argument handling and exit codes under `make scripts-test` (AC-4 and AC-6), with a
  fake `kubectl`.
- **Integration:** on `dev` after the story merges: `make server-stop`, a 60 s wait, `make server-start`
  (AC-1 to AC-3). `[ASSUMPTION]` run by SRE and recorded in the story's verification record, as
  `AW-INF-025`'s was.
- **Manual/operator:** the same three commands.

## Definition of done
CLAUDE.md §8, plus:
- `docs/runbooks/server-crashlooping.md`'s hand-typed `kubectl` step names these targets, and the line about
  #362 is gone.

## Open questions
1. **AC-5.** Whether a manual sync during a stop warns, or the suspended sync is what keeps the server
   stopped. SRE's, with architecture if it needs a contract.
2. `[ASSUMPTION]` No `CONFIRM=` argument (see the contract).
3. Where `server-start` finds the sync policy it restores. The implementation's choice.
