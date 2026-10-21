<!--
SPDX-FileCopyrightText: 2026 Valesor Development
SPDX-License-Identifier: CC-BY-SA-4.0
-->

# Alert routing

**Not an alert.** This page covers how a firing rule reaches a person, how to read rule state, and, until
`AW-INF-077`, how the rules reach the legacy ruler. **Written with:** `AW-INF-009`. Rewritten for the
Terraform setup by `AW-INF-076` (`ADR-0012` decision 10).

## Where routing lives

Routing is code. The contact points and the notification policy tree of each stack are Terraform:

| What | Where |
|---|---|
| Contact points `slack-dev` / `slack-prod` and `blackhole`, mute timing `always`, the policy tree | `deploy/terraform/grafana/modules/stack/main.tf` |
| Which environments a stack holds, and its page receiver (`page_contact_point`) | `deploy/terraform/grafana/stacks/<stack>/terraform.tfvars` |
| `irm-prod`, the IRM integration, schedule and escalation chain | `deploy/terraform/grafana/stacks/solo7-irm/` (`AW-INF-051`) |
| A test of every route in the tree, against the committed `terraform.tfvars` | `deploy/terraform/grafana/stacks/<stack>/tests/*.tftest.hcl` (`make tf-test`) |

The policy tree is one Terraform resource that owns the whole tree. A routing change is a pull request:
- the `plan` job shows the change, and `make tf-test` walks the new tree;
- the merge applies it to `solo7dev`;
- `solo7` waits for the prod gate (`terraform-state.md`).

A contact point or policy changed by hand in the Grafana UI is drift. The daily drift check opens an issue
labelled `drift:<stack>`, and the next apply puts the code's version back.

## The routing table

These are the trees as committed. Every alert lands in exactly one row of its stack.

| Stack | `environment` | `severity` | Receiver | Notes |
|---|---|---|---|---|
| `solo7dev` | `local` | any | `blackhole` | Muted by `always`: nothing is sent. The rules still evaluate, and `gcx` shows them. |
| `solo7dev` | `dev`, `staging` | any | `slack-dev` | The root receiver posts to `#andaras-world-dev`. `page` does not page in this stack. |
| `solo7dev` | none, or any other | any | `slack-dev` | The root receiver: a rule that loses its `environment` label is loud, not silent. |
| `solo7` | `prod` | `page` | `irm-prod` | From AW-INF-051. The first child, with `continue`, so the next row also matches. |
| `solo7` | `prod` | `page` | `slack-prod` | The second child posts to `#andaras-world`. One page is one IRM alert group plus one Slack post. |
| `solo7` | `prod` | any other | `slack-prod` | The root receiver. |
| `solo7` | none, or any other | any | `slack-prod` | The root receiver: Slack only, never IRM. |

The `irm-prod` row exists once `AW-INF-051` sets `page_contact_point = "irm-prod"` in `solo7`'s
`terraform.tfvars`. Until then that stack's `page` alerts go to `slack-prod` alone.
`scripts/tests/test_runbook_credentials.py` checks that the receivers in this table are the committed
trees' receivers.

**What is live today** (2026-10-20):
- `solo7dev` holds this tree, applied by the `terraform` workflow.
- `solo7` does not hold it yet. There is no prod (`AW-INF-078`), so nothing has applied `solo7`.
- Its live policy is still the hand-made one, which sends `severity = page` to the account email. The first
  `apply-prod` replaces it with the tree above (`ADR-0012` decision 8).
- Don't edit the hand-made policy in the meantime: it goes as soon as prod exists.

To check a route end to end, run `make tf-test-alert STACK=solo7dev ENV=<local|dev|staging>`, then
`make tf-test-alert-clean STACK=solo7dev ENV=<the same>` (`terraform-state.md`).

## Reading rule state

Every read goes through `gcx` (`ADR-0012` decision 2; `AW-INF-075`). First write the config, which holds
one context per stack whose read token is set:

```
make gcx-config
```

Then pick the command for the question:

| Question | Command |
|---|---|
| Which Grafana-managed rule groups and rules exist, paused or not, with health and last evaluation | `bin/gcx --config .local/gcx.yaml --context solo7dev alert rules list --limit 0 -o json` |
| Which instances of a rule are firing or pending, with their labels | `bin/gcx --config .local/gcx.yaml --context solo7dev alert instances list --name '<rule regex>' --json ruleName,labels,state,activeAt -o json` |
| What the legacy ruler holds (until `AW-INF-077`) | `bin/gcx --config .local/gcx.yaml --context solo7 alert instances list --datasource grafanacloud-prom -o json` |

Use `--context solo7` for `prod`'s rules. `scripts/gcx.py` maps the states to one vocabulary:
- `firing` (gcx prints `Alerting` or `firing`);
- `pending`;
- `normal` (`Normal`, or `Normal (NoData)`).

**Where the rules are today.** The rules are still in the legacy ruler on `solo7`. Grafana-managed rules
arrive with `AW-INF-047`, and until then the first two commands find none in `solo7dev`.

`make observe-check ENV=<env> KEEP_LIST=1` checks that every series the rules read exists.

## Delivering rules to the legacy ruler — removed by AW-INF-077

**Removed by `AW-INF-077`**, with the ruler path. Until then, this section describes how
`deploy/helm/andara/files/alerts.yaml` reaches the Grafana Cloud ruler (Mimir) on `solo7`, in ruler
namespace `andara`. Every rule carries `namespace`, so one rule set serves `andara-dev` and `andara-prod`.

### Credentials

`ANDARA_ALERTS_CI_READ` (repository secret) and `ANDARA_RULES_CI_WRITE` (secret of `andara-main`) are on
`grafana-credentials.md`'s Retiring list, which gives their click paths and scopes. The address and
tenant are the repository variables `GRAFANA_CLOUD_PROM_URL` and `GRAFANA_CLOUD_PROM_USER`.
`alerts_sync.py` strips `/api/prom` from the URL for `mimirtool`, and uses the user as the tenant.

`mimirtool rules sync` and `load` list rules before they write, which a write-only token can't do. So
`alerts_sync.py` plans with the read key, and POSTs and DELETEs to the ruler API itself with the write
key.

### Commands

| Command | Does |
|---|---|
| `make alerts-diff` | Shows how the ruler differs from the file. The script exits `1` on drift, and `3` if the key, address or tenant is unset. `make` itself reports any failure as exit `2`; the printed line says which. |
| `make alerts-sync` | Writes the file to the ruler. A second run prints `wrote 0 created, 0 updated, 0 deleted`. |

CI runs `alerts-diff` on a pull request that touches the file (the `alerts` workflow), and shows the diff
in the job summary. A merge to `main` runs `alerts-sync`, then a second sync that must change nothing.

The diff runs the base branch's scripts and reads the pull request's rule file as data (`ALERTS_FILE`), so
a branch's own code never runs with a token. Pull requests are handled like this:
- a fork's pull request is skipped, and so is one aimed at a branch other than `main`;
- a pull request that deletes the file fails the diff job, with a message saying so.

To run the commands locally:
- `make alerts-diff` needs `GRAFANA_CLOUD_PROM_URL` and `_USER`, plus the read token exported as
  `MIMIR_API_KEY`.
- `make alerts-sync` also needs the write token as `MIMIR_API_KEY_WRITE`, which it reads only when the
  diff finds drift.
- Keep the read token in `MIMIR_API_KEY` for a sync too. The write token can't list rules, so putting it
  there makes the plan step fail with HTTP 401.
- With drift present and the write variable unset, the sync exits 3.

`make alerts-sync` stages the file as `andara.yaml`, because `mimirtool` takes the ruler namespace from the
file's name.

**A 401 `invalid token` on the diff step** means `ANDARA_ALERTS_CI_READ` has expired or been revoked. That
happened on `main` on 2026-10-19. Rotate it (`grafana-credentials.md`), or leave `main`'s `alerts` run red
until `AW-INF-077` deletes the workflow.

### Things that look wrong and are not

- **A page for `andara-prod` before prod is installed.** `AndaraServerUnavailable` has an `absent()` line
  for each of `andara-dev` and `andara-prod`. Silence
  `alertname=AndaraServerUnavailable, namespace=andara-prod` until prod is installed.
- **`andara-local` is silent.** It is not in the `absent()` list, and the platform scrapes only Ready pods.
- **A rule edited in the Grafana UI is overwritten** by the next sync: the file is the source.
