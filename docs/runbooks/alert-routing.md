<!--
SPDX-FileCopyrightText: 2026 Valesor Development
SPDX-License-Identifier: CC-BY-SA-4.0
-->

# Alert routing

**Not an alert.** How the rules in `deploy/helm/andara/files/alerts.yaml` reach the Grafana Cloud ruler,
and how a firing rule reaches a person. **Written with:** `AW-INF-009`.

One file has three consumers: the compose Prometheus, the chart's ConfigMap, and the Grafana Cloud
ruler (Mimir) for the `solo7-local` stack. Series from `andara-dev` and `andara-prod` land in one
tenant, so one rule set in ruler namespace `andara` serves both; every rule carries `namespace`.

## One-time setup (Brian's account; nothing here can be scripted)

1. **Access policy.** Grafana Cloud, Administration, Access policies: create `andara-alerts-ci` on the
   `solo7-local` stack with the scopes `rules:read`, `rules:write` and `alerts:read`, and nothing wider.
   Add a token to it.
2. **Repository secrets.** On the GitHub repository, add `MIMIR_ADDRESS` (the stack's Prometheus
   details page, the URL without `/api/prom`), `MIMIR_TENANT_ID` (the same page, the numeric username)
   and `MIMIR_API_KEY` (the token). Never in a file, never in a log.
3. **Notification policy.** Grafana Cloud, Alerting, Notification policies: a child of the default
   policy matching `severity = page`, with the contact point that should wake someone. The default
   contact point is the account email until it is changed. This has no file form this repo owns, so the
   choice is recorded here: `severity=page` goes to _the account email (default)_. Change this line when
   the policy changes.
4. **Metrics the cluster clause needs.** In the tenant, both of these return a series for
   `namespace="andara-dev"`, `container="server"`; if either does not, the Grafana Cloud keep-list drops
   it and `RecoveryStateMismatch` cannot fire on the cluster (`AW-INF-025` changed the list for
   `kube_deployment_spec_replicas` the same way):

   ```
   kube_pod_container_status_last_terminated_exitcode{namespace="andara-dev", container="server"}
   kube_pod_container_status_ready{namespace="andara-dev", container="server"}
   ```

## Delivering rules

| Command | Does |
|---|---|
| `make alerts-diff` | shows how the ruler differs from the file; exit `1` on drift, `3` if the `MIMIR_*` variables are unset |
| `make alerts-sync` | writes the file to the ruler; a second run prints `wrote 0 created, 0 updated, 0 deleted` |

CI runs `alerts-diff` on a pull request that touches the file (the workflow `alerts`) and shows the
diff in the job summary. A merge to `main` runs `alerts-sync` and then a second sync that must change
nothing. Locally, export the three variables from the same source as the secrets, then run the targets.

`make alerts-sync` stages the file as `andara.yaml` because `mimirtool` takes the ruler namespace from
the file's name. Pointed at `alerts.yaml` directly it syncs namespace `alerts`, and its
`--namespaces andara` filter then matches nothing and reports `0 Groups`.

## Confirm a rule is loaded

`make alerts-diff` exits `0` and prints `no changes detected`. To read what is loaded, export the three variables and run
`bin/mimirtool rules print --address "$MIMIR_ADDRESS" --id "$MIMIR_TENANT_ID"`, or open Grafana Cloud,
Alerting, Alert rules, source `grafanacloud-…-prom`.

## Things that look wrong and are not

- **A page for `andara-prod` before prod is installed.** `AndaraServerUnavailable` has an `absent()`
  line for each of `andara-dev` and `andara-prod`; loading the group before prod exists pages for prod.
  Silence `alertname=AndaraServerUnavailable, namespace=andara-prod` in Grafana Cloud until prod is
  installed, and expire the silence then.
- **`andara-local` is silent.** It is not in the `absent()` list and the platform scrapes only Ready
  pods, so a local pod that is not Ready does not page. It pages only if a Ready local pod's scrape fails.
- **A rule edited in the Grafana UI is overwritten** by the next sync: the file is the source.
