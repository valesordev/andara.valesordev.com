---
id: AW-INF-009
title: Alert rule delivery — evaluate files/alerts.yaml in Grafana Cloud
epic: EPIC-07
component: infra
type: infra
status: draft
size: S
depends_on: [AW-INF-008]
blocks: []
lane: architecture
risk: medium
---

## Context

`AW-INF-003` chose to ship alert rules as a ConfigMap rather than a `PrometheusRule`, so that the same
`files/alerts.yaml` is evaluated by the compose Prometheus and mounted by "the cluster Prometheus".
There is no cluster Prometheus (`AW-INF-008`): metrics go to Grafana Cloud Mimir, and the ConfigMap has
no consumer. Every alert the chart carries — `AndaraServerUnavailable`, `AndaraServerCrashLooping`,
`CertificateExpiringSoon`, `IngressErrorRateHigh` — is validated, loaded locally, and evaluated by
nothing that watches the cluster.

The rules can be evaluated where the series are: Mimir's ruler accepts Prometheus rule files as they
are. Keeping one file with three consumers (compose, ConfigMap, ruler) preserves the INF-003 decision;
what changes is that a CI job syncs the file to the tenant on merge to `main`. That needs a Grafana
Cloud access policy with `alerts:write`/`rules:write` on the tenant — Brian's account — which is why
this is `draft` and not `ready`.

## User story

As an operator, I want the alerts the chart declares to actually page, so that a runbook is reached
by an alert and not by a player.

## Scope

### In scope
- `make alerts-sync ENV=<env>`: `mimirtool rules sync` of `files/alerts.yaml` to the tenant, namespace
  `andara`, idempotent; `make alerts-diff` shows drift and exits non-zero on it. `mimirtool` pinned in
  `make bootstrap` like the other Go tools.
- CI: `alerts-diff` on pull requests touching the file; `alerts-sync` on merge to `main`. Secrets:
  `MIMIR_ADDRESS`, `MIMIR_TENANT_ID`, `MIMIR_API_KEY` — repository secrets, never values.
- Contact-point routing: alerts labelled `severity=page` reach Brian; the mechanism is a Grafana Cloud
  notification policy, recorded as a runbook procedure because it is clicked, not applied.
- Prove it: `AndaraServerUnavailable` fires in Grafana Cloud within `for + 1 evaluation` of scaling
  `andara-dev` to zero, and resolves after scaling back.

### Out of scope
- Grafana-managed alerting (unified alerting rules in Grafana rather than the Mimir ruler). Rejected
  for now: the rules are Prometheus rule files by INF-003's decision, and the ruler evaluates those
  unchanged.
- Dashboards.

## Acceptance criteria

`[NEEDS BRIAN]` before this story is groomed to `ready`: an access policy on the `solo7-local`
tenant with ruler read/write, and whether the three secrets may live in the GitHub repository. Until
then the criteria below are the shape, not the contract.

1. **Given** `make alerts-sync ENV=prod` **when** it runs twice **then** the second run reports no
   change and exits `0`.
2. **Given** a rule edited on a branch **when** the pull request runs **then** `alerts-diff` names the
   rule group that differs and exits `1`.
3. **Given** `andara-dev` scaled to zero **when** `2m` + one evaluation interval passes **then**
   `AndaraServerUnavailable{namespace="andara-dev"}` is `firing` in the tenant's ruler API and a page
   arrives; scaling back resolves it.
4. **Given** the compose stack **when** `make up` runs **then** the same file still loads there
   (`AW-INF-003`'s check), unchanged.

## Interface contract

To be written at grooming. Fixed points: one file, `deploy/helm/andara/files/alerts.yaml`; ruler
namespace `andara`; `mimirtool` version pinned; secrets named above; runbook
`docs/runbooks/alert-routing.md` for the contact-point procedure.

## Data / state impact

None.

## Observability requirements

`alerts-sync` prints the rule groups it wrote; CI's job summary shows the diff. The ruler's own
`cortex_prometheus_rule_evaluation_failures_total` for the tenant is the alert on the alerts —
symptom-based, tied to `docs/specs/slo/edge-availability.md` and `world-write-availability.md`,
whose alerts this story makes real.

## Test plan

At grooming. The fixed point: AC-3 is executed against the tenant and recorded, not reasoned.

## Definition of done

CLAUDE.md §8, plus: every alert in `files/alerts.yaml` has been observed `firing` and `resolved` in
the tenant at least once.

## Open questions

- `[NEEDS BRIAN]` Grafana Cloud access policy for the ruler, and where its key lives (GitHub secret
  recommended; the alternative is a one-machine `make alerts-sync` from Brian's shell, which is a
  documented sequence of commands nobody else can run — CLAUDE.md §9 calls that a defect).
- `[NEEDS BRIAN]` Who is paged, and how (Grafana OnCall, email, a phone). The routing is a Grafana
  Cloud setting; the story records the procedure.
