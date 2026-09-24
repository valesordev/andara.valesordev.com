---
id: AW-INF-009
title: Alert rule delivery — evaluate files/alerts.yaml in Grafana Cloud
epic: EPIC-07
component: infra
type: infra
status: ready
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
Cloud access policy with `alerts:write`/`rules:write` on the tenant — Brian's account. **Decided
2026-09-17 (Brian): the key lives in a GitHub repository secret.** Creating the policy is the one
operator step this story cannot script; it is the first line of the runbook.

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

### Make targets

| Target | Does | Exit |
|--------|------|-----:|
| `make alerts-sync` | `mimirtool rules sync --namespaces andara deploy/helm/andara/files/alerts.yaml`; prints groups created/updated/deleted | `0` ok · `1` API error · `3` secrets unset |
| `make alerts-diff` | `mimirtool rules diff --namespaces andara …`; prints the diff | `0` no drift · `1` drift · `3` secrets unset |

Both read `MIMIR_ADDRESS`, `MIMIR_TENANT_ID`, `MIMIR_API_KEY` from the environment and nothing else;
`make bootstrap` pins `mimirtool` (`github.com/grafana/mimir/cmd/mimirtool`) like the other Go tools.
The ruler namespace is `andara` in every environment: rules are grouped `by (namespace)` (`AW-INF-008`),
so one rule set serves `dev` and `prod` and there is nothing per-environment to sync.

### CI

`check` workflow: `alerts-diff` as a named step, skipped with a visible notice when the secrets are
absent (a fork). `kind` workflow on `push` to `main`: `alerts-sync`. The secrets are repository
secrets; the policy's scopes are `rules:read`, `rules:write`, `alerts:read` on the `solo7-local` stack
and nothing wider.

### Runbook

`docs/runbooks/alert-routing.md`: creating the access policy, adding the three secrets, the
notification policy that routes `severity=page` to the contact point, and how to confirm a rule is
loaded (`mimirtool rules get`). The one document in this story that describes clicking, because
Grafana Cloud's notification policy has no file form this repo owns.

## Data / state impact

None.

## Observability requirements

`alerts-sync` prints the rule groups it wrote; CI's job summary shows the diff. The ruler's own
`cortex_prometheus_rule_evaluation_failures_total` for the tenant is the alert on the alerts —
symptom-based, tied to `docs/specs/slo/edge-availability.md` and `world-write-availability.md`,
whose alerts this story makes real.

## Test plan

- **Unit:** none — the file is already `promtool`-checked by `AW-INF-003`.
- **Integration (CI):** AC-1 on `main` (sync twice, second is a no-op); AC-2 on a pull request with a
  deliberately edited rule, once, recorded.
- **Manual/operator (recorded in the verification table):** AC-3 — scale `andara-dev` to zero, watch
  `mimirtool alerts list`, receive the page, scale back, watch it resolve. AC-4 by `make up`.

## Definition of done

CLAUDE.md §8, plus: every alert in `files/alerts.yaml` has been observed `firing` and `resolved` in
the tenant at least once.

## Open questions

- **Handed over by `AW-INF-008` (2026-09-23), for the routing this story owns.** Every rule now
  carries `namespace`. Two consequences land here: `AndaraServerUnavailable`'s `absent()` lines page
  for `andara-dev` and `andara-prod` until each is installed — loading the group before prod exists is
  a page for prod. `andara-local` on the box is the other way round: it is not in the `absent()` list,
  so a local pod that is not Ready is silent (the platform scrapes only Ready pods), and it pages only
  when a Ready local pod's scrape fails — route or drop that as this story decides.
- **Resolved 2026-09-17 (Brian): a GitHub repository secret.**
- `[ASSUMPTION]` Pages go to the stack's default contact point (the account email) until Brian says
  otherwise. The routing is a Grafana Cloud setting and does not touch the interface contract; the
  runbook records whichever it is.
