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

1. **Two access policies** on the `solo7-local` stack, each with one token and one scope family, as
   Grafana Cloud advises:
   - `andara-alerts-ci-read`: `rules:read` (and `alerts:read`, which nothing here uses yet). Used by the
     pull-request diff, and by the sync to find what differs.
   - `andara-rules-ci-write`: `rules:write` only. Used by the sync on `main`, and only when the diff found
     something to write. `mimirtool rules sync` and `load` list before they write, which a write-only token
     can't do (HTTP 401 `invalid scope requested`, seen on `main`'s first run, 2026-10-08), so
     `alerts_sync.py` plans with the read key and POSTs and DELETEs to the ruler API itself with the write key.
2. **GitHub.** Repository variables `GRAFANA_CLOUD_PROM_URL` and `GRAFANA_CLOUD_PROM_USER` (the pair
   `make observe-check` uses; `alerts_sync.py` strips `/api/prom` for `mimirtool` and uses the user as
   the tenant). Repository secret `ANDARA_ALERTS_CI_READ` (the read token). Environment `andara-main`,
   deployable from `main` only, with the secret `ANDARA_RULES_CI_WRITE` (the write token). A job that
   doesn't declare that environment can't read the write token, so a branch can't use it.
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
| `make alerts-diff` | shows how the ruler differs from the file; the script's exit is `1` on drift, `3` if the key, address or tenant is unset (`make` itself reports any failure as exit `2`; the printed line says which) |
| `make alerts-sync` | writes the file to the ruler; a second run prints `wrote 0 created, 0 updated, 0 deleted` |

CI runs `alerts-diff` on a pull request that touches the file (the workflow `alerts`) and shows the
diff in the job summary. A merge to `main` runs `alerts-sync` and then a second sync that must change
nothing. The diff runs the base branch's scripts and reads the pull request's rule file as data
(`ALERTS_FILE`), so a branch's own code never runs with a token; a fork's pull request is skipped, and so is one aimed at a branch other than `main`. A pull request that
deletes the file fails the diff job with a message saying so.
Locally, source `.local/box.env` (which has `GRAFANA_CLOUD_PROM_URL` and `_USER`) and export
`MIMIR_API_KEY` with the read token. That is all `make alerts-diff` needs. `make alerts-sync` also needs
`MIMIR_API_KEY_WRITE` with the write token, which it reads only when the diff finds drift. Keep the read
token in `MIMIR_API_KEY` for a sync too: the write token can't list rules, so putting it there makes the
plan step fail with HTTP 401, and leaving the write variable unset with drift present exits 3.

`make alerts-sync` stages the file as `andara.yaml` because `mimirtool` takes the ruler namespace from
the file's name. Pointed at `alerts.yaml` directly it syncs namespace `alerts`, and its
`--namespaces andara` filter then matches nothing and reports `0 Groups`.

CI's `sync` step fails while `ANDARA_RULES_CI_WRITE` is unset in `andara-main`, so `main`'s `alerts` run
is red until the one-time setup above is done.

## Confirm a rule is loaded

`make alerts-diff` exits `0` and prints `no changes detected`. To read what is loaded, export `MIMIR_API_KEY`, `MIMIR_ADDRESS` (`GRAFANA_CLOUD_PROM_URL` without
`/api/prom`) and `MIMIR_TENANT_ID` (`GRAFANA_CLOUD_PROM_USER`), and run
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
