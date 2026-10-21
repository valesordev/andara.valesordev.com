<!--
SPDX-FileCopyrightText: 2026 Valesor Development
SPDX-License-Identifier: CC-BY-SA-4.0
-->

# A red Terraform run

**Not an alert.** What to do when a run of
[`.github/workflows/terraform.yaml`](../../.github/workflows/terraform.yaml) fails in its `apply`,
`apply-prod`, `drift` or `drift-prod-irm` job. **Written with:** `AW-INF-076` (`AW-INF-068`'s workflow;
`ADR-0012` decision 5).

**Who looks:**
- Brian, as the owner of the Grafana Cloud stacks and the only approver of `apply-prod`.
- An SRE session can read the run and the summary, and can fix a cause in code. It cannot approve
  `apply-prod` or hold a prod credential.

A failed `drift` or `drift-prod-irm` job opens or updates the issue labelled `drift:<root>`. A failed
`apply` or `apply-prod` opens nothing: it shows as a red run on `main`.

## First look

Open the run (`gh run view <run id>`), then its job summary.

| Job | Look at first | Then |
|---|---|---|
| `apply` | The summary's plan for `solo7dev` at the merged commit, and the step log's first `Error:` | `tf: … unset` (exit 3) is a missing secret in `andara-main` (`make tf-ci-environments`). A provider error or 5xx is below. |
| `apply-prod` | The `solo7-irm` addresses and actions and the `solo7` plan, then each plan's sha256 | Read the full plans with `make tf-plan-show RUN=<plan-prod run id> STACK=<solo7-irm\|solo7>`. A `superseded plan` refusal applied nothing (`terraform-state.md`). |
| `drift` | The `drift:solo7dev` issue's text: drift (a plan) or a failed job (a message) | Drift: revert the hand edit in the UI, or make it in code and merge. A failed job: the step log's first `tf:` or `Error:` line. |
| `drift-prod-irm` | The `drift:solo7-irm` issue: addresses and actions only, because the provider prints the IRM integration URL in clear | `make tf-plan-show` on the latest `plan-prod` run for the full plan. |

Common causes, in order:
1. **A credential**:
   - `tf: GRAFANA_<STACK>_…_TOKEN unset` (exit 3);
   - a `401` or `403` from the provider;
   - an `invalid token` from Grafana.

   Check the token against `credentials.yaml` and the open `credentials-expiry` issue, then rotate it as
   `grafana-credentials.md` says.
2. **A held state lock**: `tf: lock held: id=… who=…`. See `terraform-state.md`, "A held lock".
3. **A provider error or a 5xx from Grafana** in the middle of an apply. Terraform recorded what it
   finished, so re-running converges.
4. **A second plan that is not empty**: `did not converge`. The configuration fights itself, or a
   resource is managed by hand as well. Look at the two plans in the log, fix it in code, and merge.

## `solo7dev`: the dev box instead

For `solo7dev`, the dev box can do what the `apply` and `drift` jobs do, with the same state and the
`solo7dev` credentials in `.local/box.env`:

```
make tf-apply STACK=solo7dev     # plan, apply that plan, then a second plan that must be empty
make tf-drift STACK=solo7dev     # the drift check; exit 1 while it drifts
```

Use the dev box when the CI path itself is broken, for example a workflow bug, an outage of GitHub's OIDC,
or a workload identity binding that stopped matching. Don't use it to get past a red plan you haven't
read. A successful `make tf-apply` leaves the next CI run with nothing to change. Then fix the CI path;
`make tf-replan` re-runs the workflow on `main` to prove the fix.

`solo7` and `solo7-irm` have no dev-box path. Their state prefixes refuse the dev box identity, and the
targets refuse those roots outside GitHub Actions (exit 2). A red `apply-prod` is fixed in code and
re-planned (`make tf-replan`), or waits for its cause, such as an expired token.
