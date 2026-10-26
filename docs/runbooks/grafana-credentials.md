<!--
SPDX-FileCopyrightText: 2026 Valesor Development
SPDX-License-Identifier: CC-BY-SA-4.0
-->

# Grafana Cloud credentials

**Not an alert.** This page covers every credential the Grafana Cloud work runs on: what it is, how to make
it by hand, where it lives, what reads it, and how to rotate it. **Written with:** `AW-INF-076`
(`ADR-0012` decision 4). The click paths follow Grafana Cloud's and Slack's UI as of **2026-10-20**. If a
menu has moved, the name and the role or scopes below are what matter.

Terraform never creates the credentials it authenticates with. They are all made by hand, once, by Brian,
and rotated by hand. The rules every entry follows:

- **No value is in the repo.** The values live in GitHub (a repository secret, or an environment secret)
  and in `.local/box.env` on the dev box (gitignored, mode `0600`). The repo holds names, plus expiry dates
  in `deploy/terraform/grafana/credentials.yaml`.
- **Expiry is at most 90 days.** The date goes into `credentials.yaml`, and `make tf-credentials-check`
  reads it there.
  - The daily `credentials-expiry` job (`.github/workflows/credentials.yaml`) opens one issue labelled
    `credentials-expiry` when an entry is within its window, 14 days by default and 30 for a telemetry
    token. It closes the issue on the first clean run.
  - A credential with no expiry has no entry, and its own entry below says so.
- **A pull-request job never holds a credential that can change Grafana's configuration or routing.**
  - It may hold the read tokens, the telemetry token and the Slack webhooks, which are low-harm.
  - It may not hold an apply token, a Fleet apply token or the IRM token.
  - `make tf-workflow-check` enforces this on `terraform.yaml`.
- **Prod isn't built yet** (2026-10-19; `AW-INF-078`). The `solo7` items that only prod jobs read are
  described here so they can be made later. Each one is marked *not made yet*.

## Rotating a token

These steps are for the tokens in `credentials.yaml`. `TF_PLAN_KEY_PROD` rotates another way, given in its
entry.

1. Make the new token next to the old one, from the entry's click path. Give it the same name, the same
   role or scopes, and an expiry no more than 90 days away.
2. Store it where the entry's **Stored** line says:
   - repository secret: `gh secret set <NAME>`;
   - environment secret: `gh secret set <NAME> --env <environment>`;
   - dev box: edit `.local/box.env` in the main checkout.

   `gh` prompts for the value, so it never reaches your shell history.
3. Check the new token the way its entry says, for the stack whose token changed. If the check fails, fix
   or re-make the new token before going on.
4. Then delete the old token in the same place you made the new one.
5. Change its `expires` in `deploy/terraform/grafana/credentials.yaml` and merge that change. The open
   `credentials-expiry` issue closes on the next daily run.

`make tf-ci-environments` lists every secret the `terraform` workflow is still missing, with the
`gh secret set` line for each.

## The credentials

`<STACK>` is `SOLO7DEV` or `SOLO7`, and `<stack>` is `solo7dev` or `solo7`. A stack's own pages are at
`https://<stack>.grafana.net`.

### `GRAFANA_SOLO7DEV_READ_TOKEN`, `GRAFANA_SOLO7_READ_TOKEN`

- **Click path:** the stack → Administration → Users and access → Service accounts → *Add service
  account*. Set the display name and the role, then *Add service account token* with an expiration date.
- **Name:** service account `andara-read`.
- **Role or scopes:** Viewer. **Observed role:** Viewer in both stacks, read 2026-10-20 (Administration →
  Service accounts).
  - Viewer is enough. `AW-INF-075`'s `[verify]` recorded its reach on both stacks: rule groups,
    dashboards, datasource queries, and `irm oncall` on `solo7`
    ([AW-INF-075's record](https://github.com/valesordev/andara.valesordev.com/issues/546#issuecomment-6278701007)).
  - So the custom role `andara-reader` is not needed. The link is the `[verify]` outcome posted on the
    story. AW-INF-075's `§8` record will repeat it once it is posted.
- **Expiry:**
  - `solo7dev`: recorded in `credentials.yaml`.
  - `solo7`: recorded in `credentials.yaml`. The first token (2026-10-17) had no expiry, so it was
    re-made with one on 2026-10-20. An expiry can't be added to an existing token; re-making it is the
    only fix.
- **Stored:** a repository secret, and `.local/box.env`. Today only `GRAFANA_SOLO7DEV_READ_TOKEN` is a
  repository secret. `solo7`'s becomes one when the prod gate opens.
- **Read by:**
  - `gcx`, through `make gcx-config`: every drill and `make observe-check`;
  - the `terraform` workflow's `plan` (pull request), `drift`, `plan-prod` and `drift-prod-irm` jobs;
  - `make tf-plan STACK=solo7dev`.
- **Check after a rotation**, on the stack whose token changed:
  - `solo7dev`: `make gcx-check STACK=solo7dev`, then `make tf-plan STACK=solo7dev`.
  - `solo7`: `make gcx-check STACK=solo7`. The box can't plan `solo7`, and `observe-check` needs prod to be
    running.
  - An unset token exits 3 and names the variable.
  - A revoked or mistyped token exits 1, with a `401` from the provider or from gcx.

### `GRAFANA_SOLO7DEV_APPLY_TOKEN`, `GRAFANA_SOLO7_APPLY_TOKEN`

- **Click path:** as `andara-read`, above.
- **Name:** service account `andara-tf-apply`.
- **Role or scopes:** Admin, on that stack only. **Observed role:** Admin in both stacks, read 2026-10-20.
- **Expiry:**
  - `solo7dev`: recorded in `credentials.yaml`.
  - `solo7`: recorded in `credentials.yaml`. It was re-made 2026-10-20, as for the read token.
- **Stored:**
  - `solo7dev`: a secret of the `andara-main` environment, and `.local/box.env` (the dev box's apply
    path).
  - `solo7`: a secret of `andara-prod-apply`, *not made yet*.
  - Never `.local/box.env` for `solo7`'s apply token: `ADR-0012` decision 4 gives the box `solo7dev`'s apply
    token only, and `make tf-apply` refuses `solo7` outside GitHub Actions. It was removed from the box
    2026-10-20, so the token made that day is held nowhere. A secret can't be read back, so when
    `andara-prod-apply` is made, make a new token for it and revoke that one.
- **Read by:**
  - the `apply` job on `main`;
  - `make tf-apply STACK=solo7dev` and `make tf-test-alert`;
  - for `solo7`, `apply-prod`, which also uses it for the `solo7-irm` root's contact point.
- **Check after a rotation**, on the stack whose token changed:
  - `solo7dev`: `make tf-apply STACK=solo7dev`. With nothing to change it prints
    `0 to add, 0 to change, 0 to destroy`.
  - `solo7`: no check exists yet. The box can't use it, and `apply-prod` runs only when `plan-prod` finds a
    change (and not at all until `TF_PROD_ENABLED` is set, `AW-INF-078`), so a mistyped token shows up at
    the next real `apply-prod`. A protected check is requested on `AW-INF-078` (2026-10-20), not yet in
    its scope.

### `GRAFANA_SOLO7DEV_TELEMETRY_TOKEN`, `GRAFANA_SOLO7_TELEMETRY_TOKEN`

- **Click path:** `grafana.com` → My Account → Security → Access Policies → *Create access policy*. Set
  the realm to the one stack, add the scopes, then *Add token* with an expiration date.
- **Name:** access policy `andara-<stack>-telemetry`.
- **Role or scopes:** `metrics:write logs:write traces:write fleet-management:read`, and nothing else. The
  collector's `remotecfg` needs the last scope to pull its pipelines.
- **Expiry:** at most 90 days. When it is made, record it in `credentials.yaml` with `warn_days: 30`: a
  lapsed telemetry token blinds every SLO at once (`AW-INF-065`).
  - `solo7dev`: recorded in `credentials.yaml`.
  - `solo7`: not made yet.
- **Stored:**
  - `solo7dev`: `.local/box.env`, and the Secret `observability/grafana-telemetry` on the box's cluster,
    key `token`, which `make platform-up` writes from it (`AW-INF-065`). CI holds none yet, so CI's kind
    job installs the collectors with an empty token and ships nothing; the repository secret that changes
    that is a later SRE change.
  - `solo7`: `.local/box.env` and prod's cluster's copy of that Secret only, never a repository secret
    (`AW-INF-078`).
- **Read by:** the `k8s-monitoring` collectors, through the Secret: each destination's `passwordKey: token`
  and `fleet`'s `remoteConfig`. No collector holds it in its environment (`fleet`'s `GCLOUD_RW_API_KEY` is
  set empty on purpose, so Grafana's default Fleet pipelines can't use it).
- **Check after a rotation:** `make platform-up` rewrites the Secret (`secret observability/grafana-telemetry
  written`); the collectors re-read it within a minute, with no restart. Then `make collectors-check` says
  `token set`, and `make observe-check` finds fresh series from the stack whose token changed: `ENV=dev` for
  `solo7dev`, `ENV=prod` for `solo7`.

### `GRAFANA_SOLO7DEV_FLEET_READ_TOKEN`, `GRAFANA_SOLO7_FLEET_READ_TOKEN`

- **Click path:** as the telemetry token, above.
- **Name:** access policy `andara-<stack>-fleet-read`.
- **Role or scopes:** `fleet-management:read`, with the realm set to the one stack.
- **Expiry:** at most 90 days.
  - `solo7dev`: recorded in `credentials.yaml`. That date is an estimate: the token was made with a
    3-month expiry and stored 2026-10-19, and a stack token can't read an access policy's dates. To get the
    exact date, read the policy's token list and correct the file.
  - `solo7`: not made yet.
- **Stored:** a repository secret for each stack, read-only like the stack read tokens. Today only
  `solo7dev`'s is made: a repository secret, and `.local/box.env`.
- **Read by:** the Fleet provider in `solo7dev`'s `plan` and `drift` jobs and `make tf-plan STACK=solo7dev`
  (`AW-INF-051`), as `GRAFANA_FLEET_MANAGEMENT_AUTH`, `<fleet_user>:<token>`, which `scripts/tf.py`
  composes from the committed `fleet_user`. A plan without it exits 3 naming it.
  - `solo7`'s root has no Fleet provider until `AW-INF-078`, so `plan-prod` passes `solo7`'s along unread.
  - The IRM root has no Fleet resources.
- **Check after a rotation:** in the access policy's token list. A plan doesn't prove the token yet: with no
  Fleet resource in the root, the provider never calls Fleet, so a bad token still plans clean. Once
  `shared_kafka` is in the root (`AW-INF-051`'s second merge), `make tf-plan STACK=solo7dev` reads it
  and is the check.

### `GRAFANA_SOLO7DEV_FLEET_APPLY_TOKEN`, `GRAFANA_SOLO7_FLEET_APPLY_TOKEN`

- **Click path:** as the telemetry token, above.
- **Name:** access policy `andara-<stack>-fleet-apply`.
- **Role or scopes:** `fleet-management:read fleet-management:write`, with the realm set to the one stack.
- **Expiry:** at most 90 days.
  - `solo7dev`: recorded in `credentials.yaml`, an estimate, as for the Fleet read token.
  - `solo7`: not made yet.
- **Stored:**
  - `solo7dev`: a secret of `andara-main`, and `.local/box.env`;
  - `solo7`: a secret of `andara-prod-apply`, *not made yet*.
- **Read by:** the Fleet provider in `apply` and `make tf-apply STACK=solo7dev` (`AW-INF-051`), as
  `GRAFANA_FLEET_MANAGEMENT_AUTH`, `<fleet_user>:<token>`, composed from the committed `fleet_user`. An
  apply without it exits 3 naming it. `apply-prod` reads `solo7`'s from `AW-INF-078`.
- **Check after a rotation:** in the access policy's token list, until `shared_kafka` is in the root, for
  the reason given for the read token. After that, the next `apply` run on `main` (or
  `make tf-apply STACK=solo7dev`) ends with an empty second plan.

### `GRAFANA_SOLO7_IRM_TOKEN` (the provider reads it as `GRAFANA_ONCALL_ACCESS_TOKEN`)

- **Click path:** the `solo7` stack → IRM → Settings → API → *Create*.
- **Name:** `andara-terraform`. The ADR names none, so this page chose it; use it when the token is made.
- **Role or scopes:** IRM's API token. It can write the integration, schedule and escalation chain.
- **Expiry:** IRM's API tokens carry no expiry of their own. Rotate it at most every 90 days anyway. Its
  `credentials.yaml` date is the rotation due date, so the job reminds you.
  - `solo7`: not made yet.
- **Stored:** secrets of `andara-prod-plan` and `andara-prod-apply`, both reachable from `main` only.
  *Not made yet.*
- **Read by:** `plan-prod`, `drift-prod-irm` and `apply-prod`, for the `solo7-irm` root only.
  `scripts/tf_ci.py` hands it to Terraform as `GRAFANA_ONCALL_ACCESS_TOKEN`.

### `SLACK_WEBHOOK_DEV`, `SLACK_WEBHOOK_PROD`

- **Click path:** `api.slack.com/apps` → the alerts app → Incoming Webhooks → *Add New Webhook to
  Workspace*, then pick the channel.
- **Name:** the webhook's channel: `#andaras-world-dev` for `DEV`, `#andaras-world` for `PROD`.
- **Role or scopes:** posts to that one channel. This is low-harm, which is why a pull-request job may
  hold it.
- **Expiry:** none. A webhook lives until it is revoked, so it has no `credentials.yaml` entry.
- **Stored:**
  - repository secrets. Today only `SLACK_WEBHOOK_DEV` is one; `SLACK_WEBHOOK_PROD` becomes one with the
    prod gate;
  - `.local/box.env` for both.
- **Read by:** the `plan`, `drift` and `apply` jobs, and `make tf-plan` and `make tf-apply`, as
  `TF_VAR_slack_webhook`. It is the one credential that is a Terraform variable, by design. State holds
  it, so reading state is a secret read (`terraform-state.md`).

### `TF_PLAN_KEY_PROD`

- **Click path:** none. `make tf-plan-key` generates it and stores it everywhere it goes.
- **Name:** `TF_PLAN_KEY_PROD`.
- **Role or scopes:** a symmetric key that encrypts the prod plan files before they are uploaded.
- **Expiry:** none, so it has no `credentials.yaml` entry. `make tf-plan-key ROTATE=1` replaces it, and
  an artifact encrypted with the old key can then no longer be applied.
- **Stored:** secrets of `andara-prod-plan` and `andara-prod-apply`, and a copy in `.local/box.env`,
  because a GitHub secret can't be read back. *Not made yet;* `make tf-plan-key` refuses while the prod
  gate is shut.
- **Read by:** `plan-prod`, `apply-prod`, and Brian's `make tf-plan-show`.

### `TFSTATE_SOLO7DEV_PLAN_SA`, `TFSTATE_SOLO7_PLAN_SA`, `TFSTATE_SOLO7DEV_APPLY_SA`, `TFSTATE_SOLO7_APPLY_SA`, `TFSTATE_SOLO7_IRM_APPLY_SA`, `TFSTATE_PRODPLAN_SA`, `GCP_WIF_PROVIDER`

- **Click path:** none. `make tf-bootstrap-gcp` creates the six keyless service accounts and the workload
  identity provider, and sets these variables (`terraform-state.md`).
- **Name:** the service-account emails and the provider's resource name.
- **Role or scopes:** not secrets. Each account can reach one root's state prefix, and only from the jobs
  its workload identity condition names (`ADR-0012` decision 3).
- **Expiry:** none. They are keyless, so there is nothing to rotate and no `credentials.yaml` entry.
- **Stored:** repository variables.
- **Read by:** the `terraform` workflow's jobs, through `google-github-actions/auth`.

### `GCP_DEVBOX_IDENTITY`

- **Click path:** none in Grafana. It is the second Google identity Brian made for the dev box
  (`terraform-state.md`).
- **Name:** that identity's email.
- **Role or scopes:** it can write `solo7dev`'s state prefix and nothing else.
- **Expiry:** none of its own. Its sign-in on the box is `gcloud auth application-default login
  --disable-quota-project`, which Google expires on its own schedule.
- **Stored:** `.local/box.env`.
- **Read by:** `make tf-bootstrap-gcp`. The dev box's Terraform runs as this identity.

**The dev box's Grafana set**, in `.local/box.env`, for `make tf-apply STACK=solo7dev`:

- `GRAFANA_SOLO7DEV_APPLY_TOKEN`;
- `GRAFANA_SOLO7DEV_FLEET_READ_TOKEN` and `GRAFANA_SOLO7DEV_FLEET_APPLY_TOKEN`;
- the read token and `SLACK_WEBHOOK_DEV`.

Each is described above. No `solo7` apply or IRM token belongs on the box.

## The collectors and the telemetry token

The `k8s-monitoring` release in `observability` is `make platform-up`'s (`ADR-0014`). Its values are
`deploy/platform/collectors/`, and the token reaches it only through the Secret above.

- **A hand `helm upgrade` doesn't stay.** The next `make platform-up` compares the release's values with
  what it renders and reinstalls on any difference (`k8s-monitoring 4.5.3 installed (values differ)`).
  Change the files instead.
- **Without the token** the release still installs, the Secret holds an empty `token`, the collectors run
  and ship nothing, and `platform-up`'s last line says so (`ADR-0012` decision 14).
- **A collector can't report its own failure through itself.** `observability` is in no pod-log list, so a
  failed send, pull or forward is read on the cluster: `make collectors-logs COLLECTOR=<metrics|logs|receiver|fleet>`
  prints that collector's warnings and errors. A `401 Unauthorized` there is a lapsed, revoked or empty
  token.
- **The release's state:** `make collectors-check` (each collector Ready, the Secret's keys, whether `token`
  is set, and on the box the one active Fleet collector).
- **What `solo7dev` may hold.** The stack is on Grafana Cloud's Pro tier, on an employee account, so
  overages aren't billed today. Brian's rule (2026-10-26): don't go far past the allowance; how far over is
  tolerated is still being checked. Other Grafana products share the same allowance.

  | Allowance | Included | Ours on 2026-10-25 |
  |---|---|---|
  | Active series | 20k | 41 (Kafka 24) |
  | Logs ingested | 50 GB | first week owed (`AW-INF-065`) |
  | Traces ingested | 50 GB | none yet: no server runs on the box |
  | Profiles ingested | 50 GB | none: nothing sends profiles |
  | Host hours | 2k | none: node-exporter isn't deployed |
  | Container hours | 37k | not read yet |

## Retiring

These are the ruler-era credentials. Each one goes when the story named next to it removes its last use,
and that story also deletes its line here. A retiring name that nothing uses any more fails
`scripts/tests/test_runbook_credentials.py`.

- `GRAFANA_CLOUD_READ_TOKEN`: removed by whichever of `AW-INF-069` and `AW-INF-077` removes its last use.
  - A Cloud access policy on the `solo7` stack's realm, with at least `metrics:read` and `rules:read`.
    `make env-recover` reads rule state with it.
  - Exported by the operator before a run. It is not in `.local/box.env` today, so both targets stop and
    name it until it is set.
  - Read by `make env-recover` and `make observe-unavailable` (through `scripts/grafana_http.py`), and by
    `alerts_sync.py` as a fallback key.
- `ANDARA_ALERTS_CI_READ`: removed by `AW-INF-077`.
  - Access policy `andara-alerts-ci-read` (`rules:read alerts:read`).
  - A repository secret.
  - Read by the `alerts` workflow's diff, and by its sync to plan.
- `ANDARA_RULES_CI_WRITE`: removed by `AW-INF-077`.
  - Access policy `andara-rules-ci-write` (`rules:write`).
  - A secret of `andara-main`.
  - Read by the `alerts` workflow's sync on `main`.

Click path for all three: `grafana.com` → My Account → Security → Access Policies, on the `solo7` stack's
realm.

None of the three has a `credentials.yaml` entry, so nothing warns before one stops working:
- The two `ANDARA_*` tokens were made without an expiry, before the 90-day rule.
- `GRAFANA_CLOUD_READ_TOKEN`'s expiry is not known.

On 2026-10-19 the `alerts` sync on `main` got `401 invalid token` with `ANDARA_ALERTS_CI_READ`. If the
ruler path still needs one of them before `AW-INF-077`, re-make it with an expiry (as above) and add its
entry.

## On the stacks, and not this repo's

The service-account lists hold other accounts. None of them is read by anything in this repo, and none
has a `credentials.yaml` entry:

- `extsvc-*` and `sa-autogen-*`: Grafana's own plugin accounts.
- `lanes-gcx-read-only`: Viewer on both stacks, made 2026-10-18.
- `gcx` and `MCP Server`: Admin on `solo7`, with no expiry. These are Brian's personal tools.

If one of them starts to be read by a job or a target, it gets an entry above.
