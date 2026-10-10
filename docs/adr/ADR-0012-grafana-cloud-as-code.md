---
id: ADR-0012
title: Grafana Cloud as code
status: proposed          # draft | proposed | accepted | rejected | superseded by ADR-XXXX
date: 2026-10-09
deciders: [brian]
gates: [AW-INF-046, AW-INF-047, AW-INF-048, AW-INF-049]   # cannot reach `ready` until this is accepted
---

## Context

Grafana Cloud is where the cluster's telemetry lands, and today it is configured three ways, none of them
as code a review can see: the 12 rules in `deploy/helm/andara/files/alerts.yaml` reach the
**data-source-managed** (Mimir) ruler through `scripts/alerts_sync.py` and `mimirtool` (`AW-INF-009`); the
contact point and the `severity = page` notification policy are set by hand in the UI
(`docs/runbooks/alert-routing.md`, step 3); and the access policies and tokens CI uses are created by hand
(step 1). All of it lives in one stack, `solo7-local`, which holds `dev` and `prod` series and a hand-made
email contact point.

Brian's direction (2026-10-09), all of which this ADR takes as given:

- Alerts move from the ruler to **Grafana-managed rules**, and every Grafana Cloud piece goes under Terraform.
- **Grafana Cloud is the one observability backend at every stage.** The compose stack's Prometheus, Tempo,
  Loki and Grafana are removed (`AW-INF-048`); "verified against a real backend" (CLAUDE.md §8) is checked
  there.
- **Three stacks:** `solo7local`, `solo7dev`, `solo7prod`. Nothing notifies from `solo7local`; `solo7dev`
  posts to Slack `#andaras-world-dev`; `solo7prod` posts to `#andaras-world` and pages through Grafana IRM,
  whose schedule is Brian alone.
- Brian creates the stacks by hand, and rebuilds the cluster with `k8s-monitoring` reinstalled, from the
  cluster repo, once this ADR is accepted.

The constraint that makes this non-obvious is that **three things now disagree about the same rule file**:
`alerts.yaml` is read by the compose Prometheus (until `AW-INF-048`), the chart's ConfigMap, the helm tests,
and `observe_unavailable.py` (which evaluates one rule's `expr` against the data source, `rule_expr()`);
Grafana-managed rules **do not write the `ALERTS` series** that `env_recover.py` (AC-2, AC-7) and
`stack_recover_mismatch.sh` assert on, so the M2 gate on `dev` breaks silently unless something replaces that
read (`observe_unavailable.py` never read `ALERTS`, and so asserts nothing about rule state today: decision 2
gives it a state read); and `AndaraServerUnavailable` names `andara-dev` and `andara-prod` in its `absent()`
lines, so one rule set across three stacks would fire in `solo7dev` for a prod that is not there. Also: the
ruler namespace `andara` exists **only in the existing hyphenated stack `solo7-local`**; the three new stacks
have never held ruler rules, so the cutover (decision 7) is a migration from that tenant, not an in-place
switch.

Checked before deciding (the repo's own rule for infra picks): no accepted ADR covers Terraform, a state
backend or Grafana. ADR-0002 §7 ("Redpanda locally, Kafka on `dev` and `prod`") is the precedent for decision
14: a local stand-in is acceptable where the contract it exercises is the same.

Facts about the provider and Grafana this ADR leans on, from the provider's published documentation.
Items marked **[verify in 046/047]** are what the first story to touch them must prove before
its criterion that depends on them is written as passing:

- `grafana_rule_group`'s `rule` block takes `for`, `keep_firing_for`, `no_data_state` and `exec_err_state`.
  `RecoveryStateMismatch` needs `keep_firing_for: 15m`. Durations must use `s|m|h|d`, never `y`.
- A Grafana-managed rule has a query/expression pipeline and a condition, where the ruler's rule has a single
  PromQL expression. Whether the 12 expressions survive the move label-for-label is decision 1's test.
- Grafana-managed alert **state** is read from the Prometheus-compatible rules endpoint
  `GET /api/prometheus/grafana/api/v1/rules` (per rule: `state`, `health`, `alerts[]`), not from a series
  **[verify in 047: exact fields on the current Grafana Cloud version]**.
- IRM resources are in the same provider (`grafana_oncall_*`) with a separate `oncall_access_token`
  **[verify in 046: current resource names, the provider may be renaming them for IRM]**.
- The S3 backend locks with a `.tflock` file beside the state when `use_lockfile = true` (stable from Terraform 1.11).

## Decisions

Each heading carries its options and trade-offs, and one **Decision** line.

### 1. Rule source of truth

**Options.**
**A — `alerts.yaml` stays the source; Terraform renders `grafana_rule_group` from it** with `yamldecode(file())`.
Good: the compose Prometheus (until removed), the chart ConfigMap, the helm tests, `observe_check.py`,
`observe_unavailable.py` and `mimirtool rules check` keep working on the file they read today; the 12 rules and
their long comments stay where reviewers know them; no generated file is committed. Bad: Terraform must
express a PromQL-only rule as a pipeline, and that conversion is code that can be wrong.
**B — HCL is the source; render `alerts.yaml` for compose and the chart.** Good: rules are native to the
provider. Bad: every other consumer then reads a generated file (CLAUDE.md §2 forbids hand edits to those, and
a committed generated file needs its own `make` target and drift check) for a compose Prometheus that is being
deleted anyway; the 12 rules' comments, which carry the reasoning, do not survive translation.
**C — split local and cloud rule sets.** Good: none of the conversion risk for compose. Bad: two copies of a
rule the §8 check is supposed to exercise; `make up` would be verifying a rule that is not the one that pages.

**Decision: A.** The conversion is fixed so that it is mechanical and testable:

- Each rule becomes one `rule` block: query `A` is the rule's `expr` as an instant query against the stack's
  Prometheus data source (the stack's own `grafanacloud-<stack>-prom`, uid carried as a module variable);
  expression `B` is `Math` `$A * 0 + 1`; condition `C` is `Threshold` `B > 0`. That keeps Prometheus's
  semantics, "a rule fires for each series its expression returns", and keeps the series' labels (so
  `namespace` rides through), where a plain threshold on `A` would not fire for `up == 0`, which returns a
  series whose value is `0`.
- `no_data_state = "OK"` (an expression that returns nothing means healthy, as in Prometheus),
  `exec_err_state = "Error"` (a failed evaluation raises Grafana's own `DatasourceError` alert, which is routed
  and so surfaces, instead of being silent), `for`, `keep_firing_for`, `labels` and `annotations` copied
  as written, annotation templates translated by the renderer if the template syntax differs
  **[verify in 047]**.
- Folder `Andara`; one `grafana_rule_group` per `groups[]` entry of `alerts.yaml`, **named as the file names
  it** (`andara-server`, `andara-recovery`, `andara-tick`, `andara-snapshots`, `andara-projections`,
  `andara-content`, `andara-sessions`, `andara-edge`). `andara` was only ever the ruler's namespace. The file
  has no `interval` key, so the module's `rule_interval` defaults to `60s` (the ruler's implicit 1 m) and a
  group may override it with an `interval:` key the renderer reads when `AW-INF-047` adds one.
- **How `make up`'s §8 check stays honest:** the compose Prometheus's local evaluation of `alerts.yaml` ends
  with `AW-INF-048`. What replaces it is the rule loaded in `solo7local` by the same Terraform root, and
  `stack-recover-mismatch` observing it fire there (decision 2). The unit-level coverage of each expression
  (`promtool test rules` over the file, run by `make check` through the helm tests) is unchanged, because it
  tests the expression, which is the thing both engines run.
- **The equivalence test** belongs to `AW-INF-047`: for each of the 12 rules, a rendered-pipeline check that
  the `expr` text in the Terraform plan is byte-identical to the file's (after the per-stack rendering of
  decision 12), and a live drill that makes a rule fire and **asserts its state through the rules endpoint**
  (decision 2): `observe-unavailable` in `solo7dev` (047's), and `stack-recover-mismatch` in `solo7local`
  (048's, once compose Alloy ships; 047's own `solo7local` criterion is **defined**, decision 7). A pipeline that
  changes what an expression means fails a drill, not review. `solo7prod` has no drill: prod's server cannot
  be taken away to prove a rule, so its rules are covered by the same module, the rendered-expression test,
  the "defined" and "live" checks of decision 2, and the first healthy evaluation after un-pause.

### 2. Reading rule state

**Options.** (a) the Prometheus-compatible rules endpoint of the stack's Grafana; (b) Grafana's alert **state
history** (Loki-backed); (c) a recording rule that re-exports state as a series, so the existing `ALERTS{…}`
queries keep working; (d) Alertmanager's `GET /api/alertmanager/grafana/api/v2/alerts`.
(a) is live, per rule, carries `health` (which is also the "loaded and evaluating" check); (b) is
retrospective but eventually consistent and a second store; (c) makes the rule set carry its own test
harness, writes a series that is not what pages, and silently diverges the day a rule is edited and its
twin is not; (d) shows only alerts already routed, so a `pending` or a not-yet-firing rule is invisible.

**Decision: (a).**

- **The call:** `GET https://<stack>.grafana.net/api/prometheus/grafana/api/v1/rules?type=alert`
  (`Authorization: Bearer <stack service-account token>`). Select by the folder (`data.groups[].file ==
  "Andara"`), `rules[].name`, and the `namespace` label in `rules[].alerts[].labels`.
  - **Defined** (also true for a paused rule): the rule is returned by
    `GET /api/v1/provisioning/alert-rules` with the file's title and `isPaused` as planned **[verify in 047]**.
    `terraform plan` showing no diff is the other half.
  - **Live** (a rule that is evaluating): the rules endpoint lists it, `health == "ok"`, and `lastEvaluation`
    is within twice its group's interval. A paused rule is defined and never live; a check that needs a live
    rule fails on a paused one.
  - **State for `namespace="andara-dev"`:** `alerts[]` filtered to `labels.namespace == "andara-dev"`, and
    `state` in `firing` (the Prometheus-API word for Grafana's "Alerting", and for "Recovering" while
    `keep_firing_for` runs, which is what AC-7's "the alert outlives the process" reads).
- **Windows are observed, not reconstructed.** `env_recover.py` AC-2/AC-7 and `stack_recover_mismatch.sh`
  today assert over `ALERTS` (a range query in the first, instant in the second). The rules endpoint answers "now". The scripts therefore poll it at
  ≤ 10 s from the moment they cause the fault, record every `(time, state)` they see, and assert over their
  own record. This is `docs/specs/testing/live-assertions.md` rule 1 (poll to a deadline), applied to a
  subject that has no history API worth trusting. `ALERT_SETTLE` (130 s, "two of the ruler's 1 m evaluations")
  becomes the group's interval times two plus the poll interval, read from the group rather than hard-coded.
- **Credential:** yes, a new one per stack, a **service account token with role `Viewer`**
  (`andara-alerts-read`; decision 4). The ruler read key (`ANDARA_ALERTS_CI_READ`, `rules:read`) does not
  authorize Grafana's own API. `GRAFANA_<STACK>_READ_TOKEN` (`metrics:read`, decision 4) serves the
  series assertions; the unsuffixed `GRAFANA_CLOUD_READ_TOKEN` is retired with the legacy path.
- **"Not already firing" preflight** (`stack_recover_mismatch.sh` today asserts `ALERTS` has no series before
  it starts): the same call, "no `alerts[]` for the rule in `solo7local`", and the 15 m `keep_firing_for`
  wait-out message stays.
- `alerts_sync.py`, `mimirtool` and the ruler read key are retired by decision 7's last step. `rule_loaded`
  in `env_recover.py` is rewritten to the **live** check above. `observe_unavailable.py` keeps evaluating the
  rule's `expr` from `alerts.yaml` (`rule_expr()`) and **gains** a step: the rule reaches `firing` for
  `andara-dev` through the rules endpoint, then clears. It stays `ENV=dev` only (it exits `3` for any other,
  because it scales a server to zero).

### 3. State backend and locking

**Options.** (a) **S3-compatible bucket** with `use_lockfile`, one key per stack; (b) HCP Terraform
(state only, local execution); (c) state in the git repo (encrypted, e.g. SOPS); (d) the cluster's object
store.
(a) is one dependency Brian already understands, versioned, and lock-capable without a database; it needs a
bucket and an identity that GitHub Actions can assume. (b) is the least to operate and has workspace-level
locking and access control, at the price of a vendor account and a pricing model that has changed more than
once. (c) has no lock, so two applies race, and the history of secrets-in-git is permanent. (d) is rejected
outright: the cluster is Brian's, shared with six projects, and the state of the thing that observes it cannot
live in it (CI cannot reach it either).

**Decision: (a).** A private bucket `andara-tfstate` (the bucket's account and region are Brian's choice at
creation; the contract is the name and the keys), versioning on, server-side encryption on, public access
blocked. Keys: `grafana/solo7local.tfstate`, `grafana/solo7dev.tfstate`, `grafana/solo7prod.tfstate`, and `grafana/solo7prod-irm.tfstate` (the IRM root, decision 12).
`use_lockfile = true`; no DynamoDB table. Terraform `>= 1.11, < 2` (`required_version`), pinned by
`.terraform-version` and installed by `make bootstrap`.

- **Who may read the state:** the state holds the Slack webhook URL(s) and, in the IRM root, the IRM
  integration URL, because the provider stores contact-point settings and integration URLs in it. So state
  read is a **secret read**. There are **four roots, four states** (`solo7local`, `solo7dev`, `solo7prod`,
  `solo7prod-irm`; decision 12), and a role is bound to one root, matched exactly (`s3:prefix` =
  `grafana/<root>.tfstate*`, so `solo7prod` does not match `solo7prod-irm`). The roles:
  - an **apply role per root**: get, put and delete on the root's key **and its lock object `<key>.tflock`**,
    and `ListBucket` for that prefix (which `use_lockfile` needs); trust bound to the environment the root
    applies from: `andara-main` for `solo7local` and `solo7dev`, `andara-prod-apply` for both prod roots;
  - a **PR/drift plan role for `solo7local`, `solo7dev` and `solo7prod` only**: read on that root's key, the
    same `ListBucket`, no write, no delete, no lock (`plan -lock=false`); trust bound to the repository's
    pull-request and schedule subjects. **It does not exist for `solo7prod-irm`**: no pull-request job, and no scheduled job outside
    `andara-prod-plan`, can read the IRM state;
  - a **`plan-prod` role** (read on `solo7prod` and `solo7prod-irm`, no write) bound to
    `environment:andara-prod-plan`, which only `main` can deploy to. Its jobs (`plan-prod`, `drift-prod-irm`)
    run `plan -lock=false`, since a plan would otherwise take the lock object, which needs a write; the
    shared concurrency group serialises them against `apply-prod`;
  - Brian's own cloud identity, the bucket's owner, which can read every key (the dev box's day-to-day
    credential is limited to `solo7local`'s key).
  The PR plan job is policed because it runs a pull request's Terraform (decision 5). A fixture in
  `scripts/tests` asserts the workflow's pull-request jobs, and scheduled jobs outside `andara-prod-plan`, reference no
  `SOLO7PROD_IRM` role, no `TFSTATE_PRODPLAN_ROLE`, no IRM token and no `TF_PLAN_KEY_PROD`. The trust
  conditions name the actual OIDC `sub` of each job (for `pull_request_target`, the base-branch ref claim);
  `AW-INF-046` checks the claim against a real run.
  **Terraform never creates the credentials CI uses** (decision 4), so state holds no token that can write to
  Grafana. What a leaked webhook buys is a post to a Slack channel; what a leaked IRM integration URL buys is
  a false page to Brian, which is why that URL lives only in the IRM root's state; rotating each is
  a documented step of the runbook.
- **Access is by OIDC federation** from GitHub Actions to the bucket's cloud account (no static cloud key in
  GitHub): a trust policy keyed on the repository and, for an apply role, on the environment that stack applies from:
  `environment:andara-main` for `solo7local` and `solo7dev`, `environment:andara-prod-apply` for both `solo7prod` roots (the prod plan job, which also writes nothing to state, uses a plan role bound to `environment:andara-prod-plan`).
  The dev box uses Brian's own cloud identity.
- State backups: versioning is the recovery path; `terraform import` from the live stack is the second.

### 4. Credentials

**Options.** (a) one org-wide Cloud access policy with a broad token for everything; (b) a Cloud access
policy and stack service account **per stack, per job**; (c) the same as (b) but created by Terraform.
(a) is simplest and gives the PR plan job a write credential, which breaks the repo's existing property.
(c) makes the credential that creates credentials the thing in the state.

**Decision: (b), created by hand once, rotated by hand, named and stored as below.** Terraform manages
what is *inside* a stack and never the credentials it authenticates with.

**There is no Cloud-API credential in the repo.** Everything Terraform manages is reached through the stack's own
endpoint with a stack service account (alerting, folders, dashboards, contact points, policies), plus an IRM
token for `solo7prod`. The stacks' endpoints are not secrets and are **committed** in each stack's
`terraform.tfvars` (decision 9); data-source UIDs are read with the `grafana_data_source` data source by name.
The org-level credential that could create stacks and access policies stays with Brian (decision 6). The
property the repo keeps is stated precisely: **a pull-request job never holds a credential that can change
Grafana's configuration or routing** (apply tokens, the IRM token). It may hold read credentials, and the
low-harm secrets (the telemetry token and the Slack webhooks), which state also holds.

| Credential | Kind and scope | Stored | Read by |
|---|---|---|---|
| `GRAFANA_<STACK>_PLAN_TOKEN` (×3) | stack service account `andara-tf-plan`, role **Viewer** **[verify in 046: if Viewer cannot read rule groups, contact points and policies through the provider, use a custom RBAC role `andara-alerting-reader` with read-only alerting and dashboard permissions, never a write role]** | repository secret | `plan` (pull request), `drift`, `plan-prod` and `drift-prod-irm` jobs |
| `GRAFANA_<STACK>_APPLY_TOKEN` (×3) | stack service account `andara-tf-apply`, role **Admin** on that stack only | secret of `andara-main` (`solo7local`, `solo7dev`) or `andara-prod-apply` (`solo7prod`; also used by the `solo7prod-irm` root for its contact point) | `apply` jobs on `main` |
| `GRAFANA_<STACK>_ALERTS_READ_TOKEN` (×3) | stack service account `andara-alerts-read`, role **Viewer** or the same custom read role **[verify in 046]**; it serves decision 2's rules-endpoint and provisioning reads, including the **defined** check | repository secret; `.local/box.env` | the drills (`env-recover`, `observe-unavailable`, `stack-recover-mismatch`) |
| `GRAFANA_<STACK>_READ_TOKEN` (×3; replaces `GRAFANA_CLOUD_READ_TOKEN`) | Cloud access policy `andara-<stack>-read`: `metrics:read logs:read traces:read` | repository secret; `.local/box.env` | `make observe-check`, the drills |
| `GRAFANA_<STACK>_TELEMETRY_TOKEN` (×3) | Cloud access policy `andara-<stack>-telemetry`: `metrics:write logs:write traces:write` and nothing else | `solo7local`: `.local/box.env` and a repository secret for CI's kind and `stack` jobs. `solo7dev`, `solo7prod`: the cluster repo's `k8s-monitoring` secret, never in this repo | compose Alloy; CI kind/`stack` jobs; `k8s-monitoring` |
| `GRAFANA_SOLO7PROD_IRM_TOKEN` | IRM access token | secrets of `andara-prod-plan` and `andara-prod-apply` (both reachable from `main` only) | the `plan-prod` and `apply-prod` jobs, for the IRM root only |
| `SLACK_WEBHOOK_DEV`, `SLACK_WEBHOOK_PROD` (`TF_VAR_slack_webhook`) | Slack incoming webhook, one per channel | repository secrets | `plan`, `drift` and `apply` jobs (low-harm: posts to a channel) |
| `TFSTATE_<ROOT>_PLAN_ROLE` (`<ROOT>` = `SOLO7LOCAL`, `SOLO7DEV`, `SOLO7PROD`), `TFSTATE_<ROOT>_APPLY_ROLE` (also `SOLO7PROD_IRM`), `TFSTATE_PRODPLAN_ROLE` | not secrets: OIDC role ARNs (decision 3) | repository variables | the jobs |
| `TF_PLAN_KEY_PROD` | symmetric key that encrypts prod plan files before upload (decision 5) | secrets of `andara-prod-plan` and `andara-prod-apply` | `plan-prod`, `apply-prod` |
| the dev box's write credential | `GRAFANA_SOLO7LOCAL_APPLY_TOKEN` | `.local/box.env` (gitignored, mode 0600) | `make tf-apply STACK=solo7local`, by Brian |

- **A telemetry token on a pull-request job is accepted.** It is write-only for series, logs and spans in
  `solo7local`, which notifies nobody (decision 10); the worst a PR can do with it is add series. It is not a
  configuration credential, and `solo7dev` and `solo7prod`'s telemetry tokens are not in this repo at all.
- **Provider credentials are never Terraform variables.** They reach the providers only through the
  providers' environment variables (`GRAFANA_AUTH`, the IRM token's variable), never `TF_VAR_*` or `-var`, so
  no plan file or state holds a token; `scripts/tf_policy.py` and a workflow fixture refuse a token passed
  any other way. The Slack webhooks are the exception, deliberately: `TF_VAR_slack_webhook`.
- **IRM is outside the PR `plan` and `drift` jobs, by being its own root.** Its token can write, so
  everything that needs it is in `stacks/solo7prod-irm/` with its own state (decision 12): the IRM integration,
  schedule and escalation chain, **and the `irm-prod` contact point**, whose URL comes from the integration.
  The `solo7prod` root refers to that contact point by its name as a string, with no Terraform dependency, so
  planning it (a pull request, the drift job, `-target`-free) never traverses into IRM and never needs the
  token. The IRM root uses the Grafana provider (for the contact point; the `solo7prod` plan and apply
  tokens) and the IRM provider (the IRM token), and is planned only by jobs in `andara-prod-plan` or
  `andara-prod-apply` (decision 5).
- **Bootstrap tokens** (the chicken-and-egg step): Brian creates, per stack and by hand, the three service
  accounts above (stack → Administration → Service accounts), the Cloud access policies
  (`grafana.com` → Access policies) and `solo7prod`'s IRM token (IRM → Settings → API), and pastes the values into the places above. They are the only manual
  credential step that cannot be removed. `docs/runbooks/grafana-credentials.md` (`AW-INF-046`) gives each
  one's click path, name, role and expiry. (This changes `AW-INF-046`'s scope: Terraform does not create the
  Cloud access policies or tokens.)
- **Rotation:** every token carries an expiry of at most 90 days, recorded as a date in
  `deploy/terraform/grafana/credentials.yaml` (names and dates, never values). `make tf-credentials-check`,
  a scheduled job that reads that file (the IRM token included), opens a GitHub issue when any is within 14 days. It calls no API,
  so it needs no credential and never waits on an environment approval.
- The dev-box credential is the first credential that leaves CI; the box holds only `solo7local`'s apply
  token, and `solo7dev` and `solo7prod` apply from CI only.

### 5. Delivery

**Options.** (a) plan on pull request, apply on `main`, all three stacks in one job; (b) the same, one job
per stack in a fixed order; (c) apply from the dev box only.
(a) fails a later stack's plan with an earlier stack's change. (c) has no review gate.

**Decision: (b).**

- **Pull request:** the `plan` job of `.github/workflows/terraform.yaml` (046's single workflow, jobs `plan`,
  `apply`, `drift`, and for prod `plan-prod`, `apply-prod`, `drift-prod-irm`), triggered by `pull_request_target` on changes under `deploy/terraform/**`, `alerts.yaml`
  and the dashboard source; same-repository pull requests only. It is a **matrix of three jobs, one per
  stack**, each with only that stack's `PLAN_TOKEN`, plan role and webhook. It runs the **base branch's**
  scripts and reads the pull request's Terraform as **data**. Because `terraform plan` executes provider code
  and evaluates every HCL function, a branch could otherwise read the runner's environment into a plan
  output, so a base-branch script (`scripts/tf_policy.py`, `AW-INF-046`) first checks the pull request's
  `deploy/terraform/**` against an **allow-list** and refuses to plan on anything outside it:
  - blocks and providers: only `grafana/grafana` (and `hashicorp/terraform`'s built-in `terraform_data`
    without provisioners), `resource`, `data "grafana_*"`, `variable`, `locals`, `output`, `module` with a
    local `source` under `deploy/terraform/grafana/modules/`;
  - functions: no `file`, `fileexists`, `templatefile`, `filebase64`, `fileset` reaching outside
    `deploy/terraform/grafana/` or `deploy/helm/andara/files/alerts.yaml` and the dashboard JSON, no
    `nonsensitive`, no `terraform_remote_state`, no `external`, no provisioner of any kind; every
    `variable` and `output` block is **byte-identical to the base branch's** (so `slack_webhook` and every
    other secret variable keep `sensitive = true`, and no output can be added to print one; a change to
    either (and the PR that first creates `deploy/terraform/grafana/`, which has no base to compare to) goes
    through the **separate-merge path**: the job refuses to plan, prints the diff, and the maintainer
    merges that change on its own; the plan that then runs on `main` (and, for `solo7prod`, the one the
    `andara-prod-apply` reviewer reads) is the first to include it. The rule covers the `variable` and `output`
    blocks under `modules/` as well, so a story that needs a new variable lands it as its own small change);
  - the `backend` block and every `provider` block are byte-identical to the base branch's; each stack's
    `terraform.tfvars` endpoint values are checked against the stack they belong to: every `*_url` must equal
    `https://<host>` with `<host>` one of the stack's own hosts as the base branch's tfvars records it, matched
    whole with a `$` anchor (fixtures: `https://x.grafana.net.evil.com`, the `user@host` form, a port, a
    path), and every `*_user` must match `^[0-9]+$`. The endpoint `variable` blocks have **no `default`**, and
    a change to any `variable` default, or to an endpoint value, makes the job **refuse to plan** and print
    the diff, until the maintainer merges the change to those values separately from other changes (so the
    plan token never goes to a host the base branch did not name); no
    `*.auto.tfvars` or other variable file besides `terraform.tfvars`; the job sets no `TF_CLI_ARGS*` and
    passes no `-var`/`-var-file` the base script does not; and `.terraform.lock.hcl` changes the
    `grafana/grafana` hashes only together with the version pin.

  A fixture per refusal lives in `scripts/tests`. The plan is posted in the job summary with sensitive values
  redacted by Terraform. Fork pull requests, and pull requests aimed at another branch, are skipped, as
  `alerts` does today. The residual is stated in Consequences.
- **Merge to `main`:** an `apply` job of the same workflow, one per stack in the order **`solo7local` →
  `solo7dev` → `solo7prod`**, each `needs:` the one before it (`plan-prod` needs the `solo7dev` apply). Each of the first two: `terraform init`, `plan -out`,
  `apply` of that plan, then `plan -detailed-exitcode` which must exit 0 (a second plan changes nothing, as
  `alerts-sync` does today). A failed or non-converging job stops the chain, so a change that breaks on
  `solo7local` never reaches `solo7dev` or `solo7prod`. Prod is the two jobs below.
- **`solo7prod` waits for approval, after a complete plan exists.** A job that references an environment with
  required reviewers does not start until it is approved, so the plan cannot be inside it. Prod is therefore
  two jobs: `plan-prod` (environment `andara-prod-plan`: no reviewers, deployable from `main` only, holds the
  plan credentials and the IRM token) plans **both** prod roots, `solo7prod-irm` first, with `-out`, uploads
  the plan files, **encrypted with `TF_PLAN_KEY_PROD`** (a saved plan embeds state, config and variable
  values, so the webhook and the IRM integration URL are in it), as a one-day artifact and posts the
  redacted plan text in the job summary; then `apply-prod`
  (environment `andara-prod-apply`, Brian the required reviewer) starts only when he approves, downloads
  those plan files and applies exactly them, in the order `solo7prod-irm`, `solo7prod`, and finishes with
  the empty-second-plan check of both. What Brian approves is therefore the complete plan, IRM included.
  Anyone who can read the repository's artifacts sees only ciphertext; the key is in the two prod
  environments alone. `apply-prod` deletes the artifact when it ends (`if: always()`), and it **refuses a
  superseded plan**: the plan records the commit it was made from, and `apply-prod` fails unless that is the
  current head of `main`; if a later merge moved the head, the approved plan is discarded, `plan-prod` is re-run on the new head, and Brian approves again. `plan-prod` and `apply-prod` share a `concurrency` group, so two merges queue
  rather than race, and approving the older run first applies nothing.
- **What a failed apply leaves behind:** Terraform's state is written after each resource, so a failed apply
  leaves the resources it finished applied and the rest not, and the state says which. Every resource here is
  idempotent and independently valid (a rule group, a contact point), so a partial apply is a stack in a
  between state, not a broken one. The job fails red; the next push re-runs `plan` which shows exactly the
  remainder. There is no automatic rollback: rolling back is reverting the commit, which the same pipeline
  applies. The one ordering hazard, a notification policy that points at a contact point not yet created, is
  removed by Terraform's dependency graph (the policy references the contact point).
- **How an apply failure and drift are surfaced:** an apply failure is a red workflow, which notifies Brian
  through GitHub. **Drift** (someone edited a rule or a policy in the UI) is surfaced by a scheduled
  `drift` job of the same workflow, on a schedule, daily, running `plan -detailed-exitcode` per stack with the read-only
  credentials (the IRM root excluded, decision 4): exit 2 opens or updates one GitHub issue labelled `drift:<stack>` containing the plan. A fourth job, `drift-prod-irm`, runs the same daily check for the IRM root in the `andara-prod-plan` environment (no reviewers, `main` only) and opens `drift:solo7prod-irm`. It is
  never an alert in Grafana, because the thing that has failed is the thing that delivers alerts.
- **`STACK` values** for `tf-plan`, `tf-apply` and `tf-drift`: `solo7local`, `solo7dev`, `solo7prod`,
  `solo7prod-irm` (the last takes `solo7prod`'s endpoints in its own `terraform.tfvars`).
- **Targets:** `make tf-fmt-check tf-validate tf-test` (in `make check`, no credentials needed, mock
  provider), `make tf-plan STACK=<stack>`, `make tf-apply STACK=<stack>`, `make tf-drift STACK=<stack>`.

### 6. Scope of "all Grafana Cloud pieces"

**Decision: in Terraform, in each stack:** the folder `Andara`; the rule groups of `alerts.yaml` (Grafana-managed, decision 1);
contact points; the notification policy tree; mute timings; the `tick-health` dashboard (and its folder
permissions where they differ from default); for `solo7prod`, the IRM integration, schedule and escalation
chain (decision 10).

**Outside Terraform, by hand, and why:**

| Piece | Why it stays manual |
|---|---|
| The three stacks themselves | Creating a stack needs the org-level Cloud credential, which is exactly the broad credential decision 4 refuses to put in CI; created once. |
| The service accounts, Cloud access policies and tokens decision 4 lists | Chicken and egg (Terraform cannot authenticate with a token it is about to create), and creating them needs the org-level Cloud credential. |
| The Slack workspace, the two channels, the webhooks | Slack's, not Grafana's. |
| IRM's on-call *people* and their notification preferences (phone, push) | Personal data, entered by the person. The *schedule* referencing them is Terraform's. |
| The `k8s-monitoring` release and its telemetry token | Brian's cluster repo (decision 13). |
| Data sources | Grafana Cloud provisions each stack's `grafanacloud-<stack>-prom`, `-logs` and `-traces` data sources; Terraform reads their UIDs and does not manage them. |
| Billing, org membership, SSO | Not observability. |

### 7. Cutover (the ruler in `solo7-local` → Grafana-managed rules in the three stacks)

The ruler namespace `andara` exists only in the legacy stack `solo7-local`, whose tenant still receives
`dev`'s and (once installed) `prod`'s series until Brian's rebuild. `solo7local`, `solo7dev` and `solo7prod`
have never had ruler rules. So this is a migration of one live rule set from one tenant to three, tied to where
each environment's series go. A rule must not be live in both places. It may be live in neither only
during the planned outage, which runs from the deletion of the legacy namespace (step 4(ii)) to the un-pause
of `solo7dev`'s rules (step 4(v)): the environment is being rebuilt for most of it, and for the
tail, minutes after the rebuild while Brian runs the keep-list check and merges the un-pause, it is a fresh
World nobody is playing in. That tail is accepted, not hidden.

**Options.** (a) delete from the ruler, then create in Grafana; (b) create the Grafana-managed rules
**paused** in the new stacks, then move each environment's telemetry and its rules together; (c) both live,
with routing hiding one.
(a) leaves the environment unwatched for as long as the apply takes. (c) pages twice, or relies on a mute that
can hide a real page.

**Decision: (b).** Definitions: "live" means evaluating (a paused rule is defined, not live); the **outage**
is the planned window in which Brian rebuilds the cluster, which discards `dev`'s World anyway.

1. `AW-INF-046` is applied to the three stacks (for `solo7prod`, the IRM root first, through `plan-prod` and
   the approved `apply-prod`): folder, contact points, notification policy, mute timings.
   Nothing evaluates in them.
2. `AW-INF-047` applies the rule groups to the three stacks with `is_paused = true` on every rule. The legacy
   ruler is the only live evaluator, for the series still arriving in `solo7-local`. `terraform plan` is clean
   and the **defined** check (decision 2) passes for all rules in each stack.
3. `solo7local`: when compose Alloy ships (`AW-INF-048`), un-pause its rules and run `stack-recover-mismatch`,
   which asserts the rule fires there through the rules endpoint (and notifies nobody). The legacy ruler never
   evaluated compose series, so nothing is doubled.
4. `solo7dev`, as one ordered run, starting at the outage: (i) the precondition, checked by the script and
   refusing to continue if unmet: no `andara` alert for `andara-dev` is pending or firing in `solo7-local`;
   (ii) Brian deletes the legacy ruler namespace with `make alerts-delete` (a target `AW-INF-047` adds to
   `alerts_sync.py`, using the existing ruler write key, which Brian exports by hand as `MIMIR_API_KEY_WRITE` (it is not in
   `.local/box.env`); it is run by hand, once,
   and the credential is not given to any CI job beyond what `alerts` holds today); (iii) the rebuild
   completes and `k8s-monitoring` ships `dev` to `solo7dev`; (iv) `make observe-check ENV=dev --keep-list`
   (decision 13) passes; (v) a merge sets `is_paused = false` on `solo7dev`'s rules; (vi) `make
   observe-unavailable ENV=dev` fires and clears the alert through the rules endpoint. Between (ii) and (v)
   nothing watches `dev`, which is the outage. No step has the same rule live in the ruler and in Grafana.
   (Ordering with `AW-INF-048`: `AW-INF-047` supplies the rules and the reader; `solo7local`'s fire-and-state
   evidence is `AW-INF-048`'s `stack-recover-mismatch`, so 047's own criterion for `solo7local` stops at
   "defined" (rules applied, plan empty), and 048 carries "live" and the firing assertion.)
5. `solo7prod`, when `prod` is installed: apply `prod`'s telemetry to `solo7prod`, pass `observe-check
   ENV=prod --keep-list`, merge the un-pause (which reaches prod through `plan-prod` and Brian's approval of
   `apply-prod`). `prod` never reported to a working ruler rule set for any
   user-facing purpose (the runbook has its absence silenced until it exists), so there is nothing to
   hand over.
6. Remove the ruler path in one change after step 4(vi): `alerts_sync.py`, `make alerts-sync alerts-diff
   alerts-delete`, the `alerts` workflow, `ANDARA_ALERTS_CI_READ`, `ANDARA_RULES_CI_WRITE`, `MIMIR_*`, and the
   runbook's step 1 and "Delivering rules" section. `solo7-local` is kept read-only for **7 days** after step
   4(v) as an archive of the last tick-health history, then deleted by Brian (decision 9).

- **Point of no return: step 4(ii).** Before it, nothing has changed for any user, and rolling back is
  pausing or reverting. From it on there is **no ruler to roll back to for `dev`**, because its series stop
  reaching the legacy tenant at the rebuild; the way back is `is_paused = true`, then fix forward by
  `git revert` and the same pipeline, with the rules endpoint as the check. Brian chooses when 4(ii)
  happens, and can hold it until step 3 and `solo7dev`'s paused rules (the **defined** check) are verified. This is the migration-and-rollback
  statement CLAUDE.md §6 asks of a dependency.

### 8. Import

**Decision: recreate, not import.** The hand-made email contact point and the `severity = page` policy are
one email address and one matcher, with no history worth keeping; importing them puts a hand-chosen name and
default settings into state and then diffs against them forever. The sequence is: Terraform creates its
contact points and its policy tree; the policy tree *replaces* the stack's root policy (the provider's
`grafana_notification_policy` is a singleton that owns the whole tree, so a hand-made child policy is
overwritten at the first apply, which is the intended outcome). The email contact point is **not** recreated anywhere: "the account email" was only ever the placeholder, and
the hand-made one lives in the legacy stack, which is retired (decision 9), so in the new stacks there is
nothing to import or delete, only to create.

### 9. Stacks and environments

**Decision.**

| Stack | Stack name / slug | Environments (`namespace` label) | Telemetry sender |
|---|---|---|---|
| `solo7local` | `solo7local` | `andara-compose` (the compose stack), `andara-local` (the kind platform), `andara-ci` (CI's kind cluster) | compose Alloy (`AW-INF-048`); CI's kind job; Brian's local kind |
| `solo7dev` | `solo7dev` | `andara-dev`; the future staging environment (`andara-staging`) when it exists | the rebuilt cluster's `k8s-monitoring` |
| `solo7prod` | `solo7prod` | `andara-prod` | the rebuilt cluster's `k8s-monitoring`, when prod is installed |

- **Endpoints** are committed, because they are not secrets: each stack's `terraform.tfvars` carries
  `grafana_url`, `prom_url`, `prom_user`, `loki_url`, `loki_user`, `tempo_url`, `tempo_user` and `otlp_url`
  (`AW-INF-046` fills them from the stack's "Details" page after Brian creates it, and a test fails on an empty
  one). The scripts read the same names as variables: `GRAFANA_<STACK>_PROM_URL`, `_PROM_USER`, `_LOKI_URL`,
  `_LOKI_USER`, `_TEMPO_URL`, `_TEMPO_USER`, `_OTLP_URL`, `_URL` (for the rules endpoint), generated into
  `.local/box.env` by `make bootstrap` from the tfvars, and set as repository variables for CI. The unsuffixed
  `GRAFANA_CLOUD_*` names are retired with `alerts_sync.py`; `make observe-check ENV=<env>` selects the set by
  decision 12.
- **Compose's `namespace` is `andara-compose`.** `andara-local` stays the kind platform's (`AW-INF-006`).
  Compose has no kube-state-metrics or `namespace` label of its own: Alloy adds `namespace="andara-compose"`
  to everything it ships. The rules' existing guard (`count(up{namespace!=""}) > 0` on the `absent()` lines)
  is retained, so a compose run never fires an absence for `andara-dev`.
- **`solo7-local`, the existing hyphenated stack, is retired.** It holds `dev` and `prod` series, hand-made
  routing and ruler rules; the three new stacks are created beside it, `dev`'s telemetry moves at Brian's
  rebuild (nothing is migrated: `dev`'s World is discarded by the rebuild), and `solo7-local` is deleted by
  Brian 7 days after decision 7 step 4(v), as step 6 says. Its
  data is not carried over: series are the cluster's last 14 days of tick health, and `dev` is rebuilt.
- **Write credentials by place:** CI holds `PLAN` for all three and `APPLY` per environment (decision 4); the
  dev box holds `solo7local` apply only.
- **Retention and cost of `solo7local`:** compose and CI send continuously whenever they run. The stack is on
  Grafana Cloud's free tier by default (14-day retention, active-series limit); the Alloy config in
  `AW-INF-048` drops everything but the series the server and the drills use, and `make down` stops the
  sender. Grafana Cloud's own usage emails are the "is the free tier enough" signal (no repo job reads
  usage), and nothing here depends on it staying free.

### 10. Notification routing per stack

**Decision.**

| Stack | Default policy routes to | Contact points | Notes |
|---|---|---|---|
| `solo7local` | **none**: the default receiver is an empty contact point `blackhole` (no integrations) | `blackhole` | Rules evaluate and their state is readable (decision 2); `RecoveryStateMismatch` fires in `solo7local` and notifies no one. |
| `solo7dev` | Slack `#andaras-world-dev` for every severity | `slack-dev` (Slack, webhook `SLACK_WEBHOOK_DEV`) | `severity = page` does **not** page on dev: a dev outage is information, not an emergency. |
| `solo7prod` | root receiver `slack-prod`; two children both matching `severity = page`: the first → `irm-prod` with `continue = true`, the second → `slack-prod` | `slack-prod` (webhook `SLACK_WEBHOOK_PROD`; in the `solo7prod` root), `irm-prod` (Grafana IRM integration; in the `solo7prod-irm` root, which applies first) | One page = one IRM alert group and one Slack post. Non-page severities fall to the root: Slack only. |

- **Why a separate `blackhole` and not "no policy":** with no default contact point, Grafana falls back to its
  built-in email receiver. An explicit empty receiver is the only way to guarantee nothing is sent.
- **IRM, Terraform-managed (`solo7prod` only):** the Alertmanager-type integration (`irm-prod`), one escalation
  chain `page` with a single step "notify on-call from schedule `primary`", and the schedule `primary` whose
  one shift is Brian's IRM user (the user is a lookup by username, not created). **How a second person is
  added:** add the person's IRM username to a `locals.oncall_users` list in the module and a second step to
  the escalation chain (a variable `escalation_users`); the plan shows the change; the person sets their own
  phone and push notifications in IRM. No other file changes.
- `solo7local` and `solo7dev` have no IRM resources. `grafana_oncall_*` is configured only in the
  `solo7prod-irm` root (the provider block takes an IRM token only there). The policy tree in the `solo7prod`
  root names `irm-prod` as a string, so applying it before the IRM root exists fails loudly at apply, which is
  why prod applies the IRM root first.
- **What a developer without a credential gets from `make up`:** a working local stack, and a warning:
  Alloy does not start without `GRAFANA_SOLO7LOCAL_*` write credentials (`GRAFANA_SOLO7LOCAL_TELEMETRY_TOKEN`, decision 4, distinct from the Terraform credentials), the
  server's OTLP export has no destination, and every target that reads Grafana Cloud exits `3` (the existing
  convention: credentials missing, not failure). `make check` needs no credentials and is unchanged. §8's
  "against a real backend" is then carried by CI and by `dev`, which have credentials; that is stated in the
  story's §8 record, as CLAUDE.md §8's last paragraph already allows.

### 11. Dashboards

**Decision.** The source of truth is the existing JSON, moved to `deploy/grafana/dashboards/tick-health.json`
(`AW-INF-049` moves it from `deploy/compose/grafana/dashboards/` **and adds** the `ds` datasource variable and
the `namespace` variable, which the source does not have today, rewriting its panels' `datasource` and
queries to use them; the `terraform test` fails if either variable is absent from the source). Terraform deploys it to each stack with
`grafana_dashboard` (`folder` = the `Andara` folder, `overwrite = true`). `config_json` is **rendered**, not
the file as is: the module does `jsondecode(file(...))`, sets the `ds` variable's `current` and `query` for the stack by `merge`, and `jsonencode`s the result; a
`terraform test` asserts the rendered model of each stack names that stack's datasource UID and no other.
Per stack:

- **Datasource:** the dashboard's `datasource` is a variable `${ds}` of type `datasource` filtered to
  Prometheus; the render sets its `current` to the stack's `grafanacloud-<stack>-prom` UID (read as data, decision
  9), so one source JSON serves every stack and no UID is committed.
- **`namespace` variable:** `label_values(up{job="andara-server"}, namespace)`, so it lists the values
  present in that stack: `andara-compose`/`andara-local`/`andara-ci` in `solo7local`, `andara-dev` in
  `solo7dev`, `andara-prod` in `solo7prod`. It is `multi = false`, default to the first value, and every panel
  query carries `namespace="$namespace"`.
- Deleted by hand in the UI is drift, found by the daily `drift` job. The compose Grafana's
  provisioning files go with `AW-INF-048`.

### 12. Multi-stack structure and `ENV`

**Options.** (a) one root, a provider alias per stack; (b) a thin **root per stack** calling a shared module.
(a) plans and locks all three stacks at once, needs all three credential sets in one job (the PR plan job
would hold all three, and so would a dev-box `solo7local` apply), and puts one state's blast radius across
prod. (b) is four states (the three stacks and the IRM root), four plans, and one stack's credential set per job (decision 5's matrix).

**Decision: (b).**

```
deploy/terraform/grafana/
  modules/stack/            # rules, contact points, policy, mute timings, dashboard; variables below
  stacks/solo7local/        # main.tf, backend.tf (key grafana/solo7local.tfstate), terraform.tfvars
  stacks/solo7dev/
  stacks/solo7prod/         # no IRM resources, no IRM token; refers to the contact point irm-prod by name
  stacks/solo7prod-irm/     # Grafana and IRM providers: integration, schedule, chain, contact point irm-prod
  modules/irm/
  tests/                    # terraform test files, mock provider
  .terraform-version
```

`terraform.tfvars` per stack: `stack = "solo7dev"`, `environments = ["andara-dev"]`, `slack = true`,
`page_contact_point = null` (`"irm-prod"` for `solo7prod`: the name the policy tree routes `severity = page` to, as a string), and the endpoints of decision 9. The provider is `grafana/grafana`, pinned `~> 4.7` (**[verify in 046]**
the latest minor that has `keep_firing_for` and the IRM resources), with `.terraform.lock.hcl` committed.

**Rendering a rule set that names environments.** The source file is also read unrendered (decision 1), so the
environment-specific lines are marked **in the file**, as PromQL comments, which Prometheus ignores:

```
// CONTRACT SKETCH — not an implementation
or absent(up{job="andara-server", namespace="andara-dev"})    # env:andara-dev
or absent(up{job="andara-server", namespace="andara-prod"})   # env:andara-prod
```

The module drops every line marked `# env:<ns>` whose `<ns>` is not in the stack's `environments`, and keeps
unmarked lines. Each marked line must be an independent `or` operand: `AW-INF-047` reshapes
`AndaraServerUnavailable` so the group starts with a never-true term (`vector(0) < 0`) and every marked line
is `or …`, so any subset parses. So `solo7dev` renders only the `andara-dev` absence, `solo7prod` only
`andara-prod`, and `solo7local` neither (its guard `count(up{namespace!=""}) > 0` is also retained, and
compose is silent as before). **The check:** `make tf-test` renders every rule for every stack and parses it
(`promtool check rules` on the rendered output); a stack whose rendered rule names an environment outside its
list fails the test, and `solo7prod` must contain `andara-prod`'s absence line. The rule semantics (thresholds,
`for`) are not touched. This is an edit to `alerts.yaml`, SRE's file, by `AW-INF-047`, and the alert set and
thresholds stay as they are.

**How `ENV` selects a stack.** One mapping, in `scripts/grafana_stack.py` (`AW-INF-046`), read by the
Makefile and every script:

| `ENV` (or `ANDARA_ENV`/the namespace) | stack |
|---|---|
| `compose`, `local`, `ci` | `solo7local` |
| `dev`, `staging` | `solo7dev` |
| `prod` | `solo7prod` |

`observe-check`, `observe-unavailable`, `env-recover`, and the `stack-*` drills resolve ENV through it, then
read `GRAFANA_<STACK>_*` (uppercase stack name). A command whose ENV maps to a stack with no credentials set
exits `3` and names the missing variable. `make alerts-sync` / `alerts-diff` are removed with decision 7's last
step (so their ignored-`ENV` caveat goes away).

### 13. Platform contract for the cluster repo

**Options.** (a) this ADR states the contract and the cluster repo conforms; (b) the cluster repo's
`k8s-monitoring` values are the source and this repo reads them; (c) the contract is only enforced by a check.
(b) inverts ownership: the cluster is Brian's, in another repo, and this repo's rules are what break when it
drifts.

**Decision: (a), enforced by (c).** The tables below are the contract, and a mechanical check
(`make observe-check ENV=<env>`'s keep-list mode) confirms it after each rebuild. What the reinstalled
`k8s-monitoring` must send, and the order:

**Per environment, send to that environment's stack** (`dev` → `solo7dev`; `prod` → `solo7prod`):

| Signal | Destination | Credential |
|---|---|---|
| metrics | `prom_url` and `prom_user` from `deploy/terraform/grafana/stacks/<stack>/terraform.tfvars` (decision 9) | the Cloud access policy `andara-<stack>-telemetry` (decision 4: `metrics:write logs:write traces:write`, nothing else), one token, installed as the release's secret in the cluster repo |
| logs | `loki_url` and `loki_user` from the same file | same token |
| traces | `otlp_url` (and `tempo_url`) from the same file | same token |

**Labels.** Every series, log line and span carries `namespace = <the pod's namespace>` (`andara-dev`,
`andara-prod`) and no other environment label: the rules and the dashboard key on `namespace`.
`cluster` may be added freely; no rule reads it. `job="andara-server"` and `job="andara-projector-state"`
are produced by the chart's scrape annotations and must not be relabelled.

**The keep-list: series that must reach Grafana Cloud and not be dropped** (all for `namespace="andara-dev"`
and `"andara-prod"`; `container="server"` where it applies):

Two kinds of series. **Always present** ones exist on a clean rebuild and are checked before the un-pause.
**Fault-only** ones (marked ‡) are absent on a clean rebuild until the event they describe, so a clean rebuild has none: the
`--keep-list` mode does not require them, it checks that the cluster repo's metric allow-list **names** them,
and the first drill that produces the fault (`make env-recover ENV=dev`, rerun on the rebuilt cluster,
step 3 below) must then see them (the hash gauge at `1` after the fixed recovery, the exit-code series after the kill), or the drill is inconclusive (exit 1, as its AC-2 already is).

| Series | Used by | Check after the rebuild (each always-present row returns ≥ 1 series) |
|---|---|---|
| `up` for `job="andara-server"` | `AndaraServerUnavailable` | `up{job="andara-server", namespace="andara-dev"}` |
| `up` for `job="andara-projector-state"` | `StateProjectorDown` | `up{job="andara-projector-state", namespace="andara-dev"}` (a separate query: the server's series must not stand in for it) |
| `kube_pod_container_status_last_terminated_exitcode` ‡ | `RecoveryStateMismatch` (cluster clause) | `…{namespace="andara-dev", container="server"}` |
| `kube_pod_container_status_ready` | same | `…{namespace="andara-dev", container="server"}` |
| `kube_pod_container_status_restarts_total` | `AndaraServerCrashLooping` | `…{namespace="andara-dev", container="server"}` |
| `kube_deployment_spec_replicas` | `StateProjectorDown` (`AW-INF-025`) | `…{namespace="andara-dev", deployment="andara-projector-state"}` |
| `certmanager_certificate_expiration_timestamp_seconds` | `CertificateExpiringSoon` | any series; its `exported_namespace` carries the Certificate's namespace |
| `traefik_router_requests_total` | `IngressErrorRateHigh` | any series with `router=~"andara-.*"` (the rule derives `namespace` from the router name) |
| the server's `andara_*` series (`andara_ticks_total`, `andara_simulation_lag_seconds`, `andara_snapshot_age_seconds`, `andara_recovery_state_hash_match` ‡ (absent until a recovery sets it; on the cluster only `1` is ever scraped, a refused recovery is never Ready, `alerts.yaml`'s own comment), `andara_session_egress_drops_total`, `andara_stream_subscribers`, `andara_content_pending_seconds`, `andara_state_*`) | the remaining rules and the dashboard | `andara_ticks_total{namespace="andara-dev"}` |

(Authoritative list: every metric name appearing in `alerts.yaml` and the dashboard; `AW-INF-046` adds
`make observe-check ENV=<env>` a `--keep-list` mode that extracts them from both files and queries each,
so this table cannot go stale. The fault-only set is a constant in `scripts/observe_keep_list.py`, with a
test that fails when a metric the rules name is neither in the always-present set nor in that constant.
The cluster repo's allow-list is read from the file or URL in `K8S_MONITORING_VALUES`, which Brian sets;
unset, the allow-list half of the mode exits `3` naming the variable, and decision 7 step 4(v) does not
proceed on a run that skipped it. So the allow-list-names check, like the always-present queries, **gates the
un-pause**; the window between the un-pause and the drill that first produces the fault is accepted.)

**Order.**
1. Before the rebuild: apply `AW-INF-046` to `solo7dev` and `solo7prod` (the stacks exist, hand-made;
   contact points, policy, folder). Brian creates the `andara-<stack>-telemetry` access policy and token by
   hand (decision 4's bootstrap step) and the tfvars endpoints are filled, so the cluster repo can read them
   from this repo without asking. `AW-INF-047` is applied **paused** (decision 7 step 2).
2. Brian rebuilds the cluster and installs `k8s-monitoring` with the table above (decision 7 step 4(ii) to (iii):
   the legacy ruler namespace is deleted first).
3. After: `make observe-check ENV=dev` and its `--keep-list` mode pass against `solo7dev`; then decision 7
   step 4(v) un-pauses the rules; then `make observe-unavailable ENV=dev` (step 4(vi)). `AW-INF-034`'s drill is rerun
   on the rebuilt cluster, since the rebuild discards `dev`'s World.
4. `deploy/kind/config.yaml` stays CI's single-node shape; the cluster repo's multi-node kind config is its own.

### 14. The local environment: compose, kind, or both

**Options.** (A) keep compose; Alloy ships to `solo7local` (`AW-INF-048` as written). (B) retire compose; local
is kind running the chart. (C) keep only `min` in compose, and make kind the place for everything that needs
the platform.

The argument in one paragraph. What compose gets wrong about `dev` is real: Redpanda for Strimzi Kafka,
versitygw for the object store, no Traefik, no cert-manager, no probes. But three things already cover those
gaps and none of them is the developer's laptop: **CI's kind job** (`kind.yaml`) installs Traefik and
cert-manager the way the box has them and runs the chart's probes, edge and certificates on every change to
them; **`dev`** is the release gate, with `make env-recover` and `make observe-unavailable` as the cluster
versions of the M2 gate and the unavailable alert; and ADR-0002 §7 already decided that a local Kafka-API stand-in
is the right trade for the fast loop. What kind-locally would add is the *chart* on the developer's machine
— and the resource complaint that started this direction (Strimzi, the object store, `k8s-monitoring`) is
the price, plus a bootstrap path that must create a cluster that Brian's box cannot let the repo own. The cost
of B and C is rewriting the `stack-*` drivers and three compose-level fault injections (`SIGKILL`, broker
bounce, `stack-boundary-lost`) against pods, for a gain the CI kind job and `dev` already supply.

**Decision: A, narrowed.** Compose stays the local environment. Its observability services (Prometheus, Tempo,
Loki, Grafana, and the OTLP collector as configured) are removed; **Alloy** replaces the collector and ships to
`solo7local`. It does not become a second deployment description: its `full` profile stays Redpanda, Redis,
Postgres, the object store (`minio`, a versitygw image), Alloy and the server.

- **`make up`** means what CLAUDE.md §9 says: server + datastores + (credentialed) observability, now
  with Grafana Cloud as the observability. `make up PROFILE=min` is unchanged (server, Redpanda, Redis; no
  Alloy, no telemetry destination).
- **`make bootstrap && make up && make check`** remains the whole onboarding path, and needs no Grafana Cloud
  credential, no cluster and no kind.
- **The four `stack-*` targets and the M2 gate drill run in compose.**
  All four are re-pointed at `solo7local` as `AW-INF-048` has it (series, log lines and spans carry
  `namespace="andara-compose"`, polled to a deadline, exit `3` without credentials), because "verified against
  a real backend" is Brian's direction and is checked there; `stack-recover-mismatch` reads the rules
  endpoint of `solo7local` (decision 2). `stack-recover` (the M2 gate, RTO 120 s) and `stack-boundary-lost`
  keep their compose drivers. The **same gate is also run on the cluster** by `env-recover`, which is the release
  evidence; the compose run is the fast loop and not a substitute.
- **CI** runs what it runs today: `make check` and the kind job. The `stack` workflow's drills keep running; a
  job that has the `solo7local` telemetry and `GRAFANA_SOLO7LOCAL_ALERTS_READ_TOKEN` secrets also checks the rules endpoint, and one
  that does not (a fork) skips those assertions and says so (exit `3` locally, a notice in CI).
- **`AW-INF-048` stays as written**, with these lines changed (the comment on the story says so): its title
  ("writing telemetry to stdout") is wrong against its own body, which ships to `solo7local`; the removed
  services are five (the collector, Prometheus, Tempo, Loki, Grafana); the endpoint variable names are
  decision 9's; the telemetry token is `GRAFANA_SOLO7LOCAL_TELEMETRY_TOKEN`; rule state comes from
  `AW-INF-047`'s reader. The kind-as-local debate does not create a replacement story. `full` also keeps
  Postgres and the object store (the `minio` service, a versitygw image).
- **This is Brian's call.** The alternative worth its name is C: if the dev-loop pain is the chart and the
  probes rather than the sim, the change is a new `make kind-up` that runs the chart in a kind cluster the
  repo creates (`deploy/kind/config.yaml`), leaving compose for `min`. It is a different story set
  (`kind-up`, the four drills against pods), and nothing in decisions 1 to 13 depends on A over C; only
  `AW-INF-048` and the `stack-*` rows above would change.

## Consequences

- **The `ALERTS`-series idiom is gone.** Anything that wants alert history must record it itself
  (decision 2). A rule's firing is no longer a thing one can `rate()`; there is no `ALERTS_FOR_STATE` either.
  That is the cost of Grafana-managed rules, and we accept it for the routing, dashboards and IRM we gain.
- **`alerts.yaml` is now rendered per stack** by Terraform, and the render has its own tests. A mistake in the
  renderer is a mistake in what pages. The mitigation is `make tf-test` plus a live drill per stack.
- **State holds secrets** (webhooks; the IRM URL in its own root's state). Read access to state is a secret
  read, and the webhooks are shared with the pull-request plan job, which runs a pull request's Terraform; the
  IRM state is not (decision 3). The prod plan artifact is a state-equivalent secret: it is encrypted, lives a
  day and is deleted after apply, and its key is in the prod environments alone. The job's defence is the policy script
  (decision 5), an allow-list over blocks, functions and the backend; a provider vulnerability, or a construct
  the allow-list wrongly admits, would pass it. Apply tokens and the IRM token are never reachable from it.
  The residual: a leaked webhook posts to a channel; a false page needs the IRM URL, which only the two prod
  environments and Brian can read.
- **Three stacks, four states, and ~25 credentials** (decision 4) to rotate at least every 90 days.
  `tf-credentials-check` makes that a notice and not a surprise, and it is still Brian's chore.
- **Compose has no local rule evaluator.** A rule edit is verified by the expression tests and by loading in
  `solo7local`, not by a Prometheus on the laptop. A developer with no Grafana Cloud credential gets no
  rule-fire check from `make up`; CI and `dev` do.
- **Terraform is a new tool in `make bootstrap`** and a new licence question (BUSL); OpenTofu is a drop-in at
  this scope and the `required_version` is the only coupling.
- **A drift in the Grafana UI is overwritten at the next apply**, as the ruler path already did, but now it
  also overwrites the *routing*. The runbook must say so in its first line.
- **Foreclosed:** a single shared stack for all environments; hand-edited contact points as the routing source;
  and, for now, kind as the local development environment.
- **CLAUDE.md §8** needs one wording change, which is Brian's: replace "the integration suite exercising the
  story's own code against the local stack's backends (Redpanda, Tempo)" with "…against Redpanda locally and
  the `solo7local` Grafana Cloud stack for telemetry". `docs/specs/testing/live-assertions.md` is architecture's
  and is amended in the merge of this ADR's follow-up (rule 1's examples name `ALERTS` and a local Prometheus).

## Revisit when

- A Grafana-managed rule's firing differs from its PromQL's on a drill (decision 1's equivalence test fails
  once on real data): move the source to HCL (Option B) for that rule.
- `keep_firing_for` or `is_paused` is dropped or changes meaning in the provider: the 15-minute
  `RecoveryStateMismatch` bridge and the cutover both depend on them.
- Two different people need to page: the single-person IRM schedule becomes a rota (decision 10 already
  says how).
- A chart, probe or edge bug reaches `dev` that compose hid, **twice**: reopen decision 14 for C.
- `solo7local` exceeds the free tier's active-series limit twice in a month.
- A fourth stack (staging apart from dev, or a customer environment): root-per-stack still scales, but the
  credential count (decision 4) does not; reopen it then.
- The state bucket is breached or its webhook secrets leak once: move the contact points' secrets out of
  state (write-only attributes, when the provider supports them) before anything else.
