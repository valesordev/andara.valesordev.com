<!--
SPDX-FileCopyrightText: 2026 Valesor Development
SPDX-License-Identifier: CC-BY-SA-4.0
-->

# Terraform state for Grafana Cloud

**Not an alert.** Where the state of the Grafana Cloud Terraform roots lives, who can reach it, how it is
bootstrapped, and what to do with a stale lock or a failed apply. **Written with:** `AW-INF-046`
(`ADR-0012` decisions 3, 4, 5, 10 and 12).

## What lives where

| Root | Directory | State object | Planned and applied by |
|---|---|---|---|
| `solo7dev` | `deploy/terraform/grafana/stacks/solo7dev` | `gs://andara-tfstate/grafana/solo7dev/default.tfstate` | the dev box (`make tf-plan`, `make tf-apply`), and CI on `main` (`AW-INF-068`) |
| `solo7` | `deploy/terraform/grafana/stacks/solo7` | `gs://andara-tfstate/grafana/solo7/default.tfstate` | CI only: `plan-prod`, then `apply-prod` after Brian approves (`AW-INF-068`) |
| `solo7-irm` | `deploy/terraform/grafana/stacks/solo7-irm` | `gs://andara-tfstate/grafana/solo7-irm/default.tfstate` | CI only, as `solo7` |

Each root locks by writing `default.tflock` beside its state; there is no lock table. State holds the
Slack webhooks (and, in `solo7-irm`, the IRM integration URL), so **reading state is a secret read**. Every
identity is granted one root's prefix only, by an IAM condition on the object name and on the listing
prefix (`ADR-0012` decision 3). The dev box's identity, `GCP_DEVBOX_IDENTITY`, can write `solo7dev`'s
prefix and nothing else, which is why `make tf-plan` and `make tf-apply` refuse `solo7` and `solo7-irm`
outside GitHub Actions (exit 2).

## Credentials on the dev box

`.local/box.env` (gitignored, mode `0600`: the targets refuse it, exit 1, if group or others can read
it). A variable already exported wins over the file. Keep one copy, in the main checkout: an agent
worktree (`.claude/worktrees/<name>`) with no `.local/box.env` of its own reads the main checkout's, and
`ANDARA_BOX_ENV=<path>` names any other file.

| Variable | For |
|---|---|
| `GRAFANA_SOLO7DEV_READ_TOKEN` | `make tf-plan STACK=solo7dev` |
| `GRAFANA_SOLO7DEV_APPLY_TOKEN` | `make tf-apply STACK=solo7dev`, `make tf-test-alert` |
| `SLACK_WEBHOOK_DEV` | both (`TF_VAR_slack_webhook`) |
| `GCP_STATE_PROJECT`, `GCP_DEVBOX_IDENTITY` | `make tf-bootstrap-gcp` |
| `GCP_STATE_REGION` | `make tf-bootstrap-gcp`, only when it creates the bucket |
| `GCP_BILLING_ACCOUNT` | `make tf-bootstrap-gcp`, only when the project has no billing account |

The tokens are made by hand (`ADR-0012` decision 4; the click paths are `AW-INF-076`'s
`grafana-credentials.md`). A missing one exits 3 and names it, before any network call. The token reaches
Terraform only as `GRAFANA_AUTH`. State is reached with Application Default Credentials: on the dev box,
`gcloud auth application-default login --disable-quota-project` signed in as `GCP_DEVBOX_IDENTITY`, never
as the owner. Without `--disable-quota-project` the login records gcloud's current project as the quota
project, on which `GCP_DEVBOX_IDENTITY` has no role, and every state request is refused (403,
`serviceusage.services.use`).

## Bootstrap (once, by Brian as the GCP project owner)

What has to exist first, by hand (`ADR-0012` decision 6): the GCP project, or an empty one
`GCP_STATE_PROJECT` names, and a billing account; the second Google identity `GCP_DEVBOX_IDENTITY`; the
stacks; their tokens; the Slack webhooks.

```
gcloud auth login                     # as the project owner
make tf-bootstrap-gcp                 # GCP_STATE_PROJECT, GCP_DEVBOX_IDENTITY (and GCP_STATE_REGION the first time)
```

It verifies, and creates or corrects only what does not match: the APIs, the bucket and its settings
(uniform access, public access prevention, versioning, the 90-day/newest-20 lifecycle rule), the six
keyless service accounts, their conditioned bucket bindings, the dev box's binding, the workload identity
pool `github` and provider `github-actions`, each account's `workloadIdentityUser` binding, and the
repository variables `TFSTATE_*_SA` and `GCP_WIF_PROVIDER`. It then reads the bucket's IAM policy back
and exits 1 naming any account whose binding is missing or whose condition differs; seeds an empty state
for each root as the owner; and runs `terraform init` of `solo7dev` as the dev box identity, which must
succeed, then of `solo7` with the same credentials, which must be refused (if it is not, Application
Default Credentials are not the dev box identity's, and it exits 1). A grant to one of its accounts that
differs from the expected one (a wider condition, none, another role), or an impersonation grant naming a
claim it no longer expects, is removed. A second run prints `tf-bootstrap-gcp: no changes; everything already matched`. Run it again
whenever the bindings should be checked; it is safe at any time.

If `terraform init` of `solo7dev` as the dev box fails at the end, sign in again as that identity:
`gcloud auth application-default login --disable-quota-project`, then re-run `make tf-bootstrap-gcp`. If
instead it exits 1 because those credentials **can** read `solo7`'s state, either ADC is another account's
(sign in again, as above) or the dev box identity holds a project-level role that reaches the bucket (Editor,
a Storage role): the message prints the `gcloud projects get-iam-policy` command that finds it; remove that
grant in the console (IAM & Admin → IAM) and re-run. A new
project allows only a few service accounts created per minute; the bootstrap waits out that quota
(`quota reached; retrying in 65s`) and carries on.

The impersonation bindings are written from the claims GitHub is *expected* to send (`event_name` of
`pull_request_target` or `schedule`; `environment` of `andara-main`, `andara-prod-plan`,
`andara-prod-apply`). `AW-INF-068`'s first runs print the real claims; if they differ, the values in
`scripts/tf_bootstrap_gcp.py` change and the bootstrap is re-run.

## Day to day: `solo7dev` from the dev box

```
make tf-plan  STACK=solo7dev          # prints the plan
make tf-apply STACK=solo7dev          # plan, apply that plan, then a second plan that must be empty
```

`tf-plan` takes no state lock (`-lock=false`): it writes nothing, and CI's plan accounts can only read.
`tf-apply` prints `tf: plan: N to add, N to change, N to destroy`. With nothing to change it prints
`0 to add, 0 to change, 0 to destroy` and applies nothing.

## CI: the `terraform` workflow

`.github/workflows/terraform.yaml` (`AW-INF-068`, `ADR-0012` decision 5) is the only path for `solo7` and
`solo7-irm`, and runs `solo7dev` on every merge:

| Job | When | Environment | What it does |
|---|---|---|---|
| `plan` | pull request touching the roots, `alerts.yaml` or the dashboards | none | runs the base branch's `scripts/tf_policy.py` over the pull request's files, then plans `solo7dev` and `solo7` with read credentials; the plans are in the job summary |
| `apply` | merge to `main` | `andara-main` | applies `solo7dev`, then a second plan that must be empty |
| `plan-prod` | after `apply` | `andara-prod-plan` | plans `solo7-irm` then `solo7`, uploads both encrypted with `TF_PLAN_KEY_PROD` (one-day artifact), prints each plan's sha256 |
| `apply-prod` | after `plan-prod`, only when it found changes | `andara-prod-apply` (Brian approves) | refuses a plan whose commit is not `main`'s head, applies exactly the approved plans, checks that a second plan is empty, deletes the artifact |
| `drift`, `drift-prod-irm` | daily, 06:23 UTC | none / `andara-prod-plan` | opens or updates the issue labelled `drift:<root>` on drift or on any failure of the job |

The `solo7-irm` root's summaries, logs and drift issue carry resource addresses and actions only, because
the provider prints the IRM integration URL in clear.

**Setting it up (once, by Brian).** Run these after `make tf-bootstrap-gcp`, which sets the repository variables:

```
make tf-ci-environments APPLY=1   # andara-main, andara-prod-plan, andara-prod-apply: main only; Brian reviews prod-apply
make tf-plan-key                  # TF_PLAN_KEY_PROD in both prod environments and .local/box.env
make tf-ci-environments           # lists every secret still missing, with the gh secret set line for each
```

Paste each token the report names with the `gh secret set` line it prints. `gh` prompts for the value, so
it never reaches your shell history. Where each token comes from is `grafana-credentials.md` (`AW-INF-076`). The
report exits 0 once nothing is missing. `make tf-plan-key ROTATE=1` replaces the key; an artifact
encrypted with the old key can then no longer be applied, so re-run `plan-prod`.

**Approving a prod change.** `plan-prod`'s summary gives the counts, the `solo7` plan, the `solo7-irm`
addresses and actions, and each plan's sha256. Read the full plans before approving:

```
make tf-plan-show RUN=<run id> STACK=solo7-irm
make tf-plan-show RUN=<run id> STACK=solo7
```

It decrypts in memory (no plaintext file) and prints the same sha256. Approve `apply-prod` only when the
sha256s match. A later merge to `main` makes the plan **superseded**: `apply-prod` refuses it, applies
nothing, and deletes the artifact. The next `plan-prod`, from the newer merge, is the one to read and
approve.

**Drift.** An issue labelled `drift:<root>` means a managed resource was changed outside Terraform, or
the drift job failed. The issue says which. Revert the hand edit in the UI, or make it in code and merge.
For `solo7dev`, `make tf-drift STACK=solo7dev` re-checks from the dev box (exit 1 while it drifts).

## A held lock

A run that finds the state locked fails at once (exit 1) and changes nothing:

```
tf: lock held: id=1760412345678901 who=brian@devbox
```

1. **Find the holder.** `who` is `user@host` for a dev-box run and `runner@<host>` for CI. If a run is
   still going (a terminal, or the Actions run for the `terraform` workflow), wait for it; the lock is
   released when it ends.
2. **Only when the holder is gone** (a killed terminal, a cancelled job): release it with the id it
   printed:

   ```
   make tf-unlock STACK=solo7dev LOCK_ID=1760412345678901
   ```

   Without `LOCK_ID` the target exits 2. Never release a lock a live run holds: two writers can lose
   resources from state.
3. Re-run what failed.

A `solo7` or `solo7-irm` lock is held by CI only, and only by `apply-prod`: `plan-prod` and
`drift-prod-irm` take no lock. The prod jobs share one concurrency group, so a stale lock means a cancelled
`apply-prod`. The dev box cannot release it (`make tf-unlock` refuses those roots outside GitHub Actions,
and the dev box identity cannot write their prefixes). A workflow job to release it is not built yet
(issue #587); until it is, the owner releases it as the GCP project owner in the console, by deleting
`grafana/<root>/default.tflock` from the `andara-tfstate` bucket, once no run is going.

## A failed apply

Terraform writes state after each resource, so a failed apply (a provider error, a 5xx from Grafana)
leaves the resources it finished applied, recorded, and the lock released by the exiting process. Fix the
cause if it is in the code, then run `make tf-apply STACK=solo7dev` again: the plan shows exactly the
remainder and converges. No `terraform state` command is part of recovering from it. There is no automatic
rollback; rolling back is reverting the commit and applying.

## Checking the notification path end to end

`make tf-test-alert STACK=solo7dev ENV=<local|dev|staging>` creates one always-firing rule,
`AndaraTestAlert`, labelled with that `environment` and `severity=warning`, in a folder of its own,
from a throwaway root with local state (`.local/tf-test-alert/`), never in the bucket. Within about two
minutes:

- `ENV=dev` or `staging`: `#andaras-world-dev` receives it.
- `ENV=local`: no contact point is notified; the route ends at `blackhole`, muted by `always`.

Then remove it: `make tf-test-alert-clean STACK=solo7dev ENV=<same>`. Record what was seen in the story's
§8 record. `solo7`'s post to `#andaras-world` is recorded at `AW-INF-068`'s first `apply-prod`.

## Recovering a state object

The bucket keeps every version (non-current ones for 90 days beyond the newest 20), and the default soft
delete keeps a deleted object for 7 days. Restoring a previous generation of a root's state is not yet a
`make` target (§9 defect, #579); until it is, the second recovery path is
`terraform import` of each resource from the live stack.
