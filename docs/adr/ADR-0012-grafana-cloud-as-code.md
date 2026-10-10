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

The constraint that makes this non-obvious is that **four things now disagree about the same rule file**:
`alerts.yaml` is read by the compose Prometheus (until `AW-INF-048`), the chart's ConfigMap, the helm tests,
and `observe_check.py` / `observe_unavailable.py`; Grafana-managed rules **do not write the `ALERTS` series**
that `env_recover.py` (AC-2, AC-7), `observe_unavailable.py` and `stack_recover_mismatch.sh` assert on, so the
M2 gate on `dev` breaks silently unless something replaces that read; and `AndaraServerUnavailable` names
`andara-dev` and `andara-prod` in its `absent()` lines, so one rule set across three stacks would fire in
`solo7dev` for a prod that is not there.

Checked before deciding (the repo's own rule for infra picks): no accepted ADR covers Terraform, a state
backend or Grafana. ADR-0002 §7 ("Redpanda locally, Kafka on `dev` and `prod`") is the precedent for decision
14: a local stand-in is acceptable where the contract it exercises is the same.

Facts about the provider and Grafana this ADR leans on, from the provider's published documentation
(2026-10-10). Items marked **[verify in 046/047]** are what the first story to touch them must prove before
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
- The S3 backend locks with a `.tflock` file beside the state when `use_lockfile = true` (Terraform ≥ 1.10).

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
- Rule group `andara` in folder `Andara`, one `grafana_rule_group` per `groups[]` entry of `alerts.yaml`,
  `interval` from the group.
- **How `make up`'s §8 check stays honest:** the compose Prometheus's local evaluation of `alerts.yaml` ends
  with `AW-INF-048`. What replaces it is the rule loaded in `solo7local` by the same Terraform root, and
  `stack-recover-mismatch` observing it fire there (decision 2). The unit-level coverage of each expression
  (`promtool test rules` over the file, run by `make check` through the helm tests) is unchanged, because it
  tests the expression, which is the thing both engines run.
- **The equivalence test** belongs to `AW-INF-047`: for each of the 12 rules, a rendered-pipeline check that
  the `expr` text in the Terraform plan is byte-identical to the file's (after the per-stack rendering of
  decision 12), and one live drill per stack that makes a rule fire (`observe-unavailable`, decision 2). A
  pipeline that changes what an expression means fails the drill, not review.

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
  (`Authorization: Bearer <stack service-account token>`). Select by `data.groups[].name == "andara"`,
  `rules[].name`, and the `namespace` label in `rules[].alerts[].labels`.
  - **Loaded:** the rule is present, `health == "ok"`, and `lastEvaluation` is within twice its group's
    interval. `terraform plan` showing no diff is the other half: the definition is what the file says.
  - **State for `namespace="andara-dev"`:** `alerts[]` filtered to `labels.namespace == "andara-dev"`, and
    `state` in `firing` (the Prometheus-API word for Grafana's "Alerting", and for "Recovering" while
    `keep_firing_for` runs, which is what AC-7's "the alert outlives the process" reads).
- **Windows are observed, not reconstructed.** `env_recover.py` AC-2/AC-7 and `observe_unavailable.py` today
  assert over a range query of `ALERTS`. The rules endpoint answers "now". The scripts therefore poll it at
  ≤ 10 s from the moment they cause the fault, record every `(time, state)` they see, and assert over their
  own record. This is `docs/specs/testing/live-assertions.md` rule 1 (poll to a deadline), applied to a
  subject that has no history API worth trusting. `ALERT_SETTLE` (130 s, "two of the ruler's 1 m evaluations")
  becomes the group's interval times two plus the poll interval, read from the group rather than hard-coded.
- **Credential:** yes, a new one per stack, a **service account token with role `Viewer`**
  (`andara-alerts-read`; decision 4). The ruler read key (`ANDARA_ALERTS_CI_READ`, `rules:read`) does not
  authorize Grafana's own API. `GRAFANA_CLOUD_READ_TOKEN` (`metrics:read`) is unchanged and still serves the
  series assertions.
- **"Not already firing" preflight** (`stack_recover_mismatch.sh` today asserts `ALERTS` has no series before
  it starts): the same call, "no `alerts[]` for the rule in `solo7local`", and the 15 m `keep_firing_for`
  wait-out message stays.
- `alerts_sync.py`, `mimirtool` and the ruler read key are retired by decision 7's last step. `rule_loaded`
  in `env_recover.py` is rewritten against the rules endpoint; `observe_unavailable.py` keeps
  reading the rule's `expr` from `alerts.yaml` (`rule_expr()`); the file is still the source.

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
blocked. Keys: `grafana/solo7local.tfstate`, `grafana/solo7dev.tfstate`, `grafana/solo7prod.tfstate`.
`use_lockfile = true`; no DynamoDB table. Terraform `>= 1.10, < 2` (`required_version`), pinned by
`.terraform-version` and installed by `make bootstrap`.

- **Who may read the state:** the state holds the Slack webhook URL(s) and the IRM integration URL, because
  the provider stores contact-point settings and integration URLs in it. So state read is a **secret read**,
  and it is granted to exactly two principals: the apply role (read/write on its own key) and the plan role
  (read on all three keys, no write, no delete, and it does not take the lock: `plan -lock=false`). The plan
  role is assumed by the pull-request plan job (decision 5), which is why that job's input is policed below.
  **Terraform never creates the credentials CI uses** (decision 4), so state holds no token that can write to
  Grafana. What a leaked webhook buys is a post to a Slack channel, or a false page to Brian; rotating each is
  a documented step of the runbook.
- **Access is by OIDC federation** from GitHub Actions to the bucket's cloud account (no static cloud key in
  GitHub): a trust policy keyed on the repository and, for the apply role, on `environment:andara-main`.
  The dev box uses Brian's own cloud identity.
- State backups: versioning is the recovery path; `terraform import` from the live stack is the second.

### 4. Credentials

**Options.** (a) one org-wide Cloud access policy with a broad token for everything; (b) a Cloud access
policy and stack service account **per stack, per job**; (c) the same as (b) but created by Terraform.
(a) is simplest and gives the PR plan job a write credential, which breaks the repo's existing property.
(c) makes the credential that creates credentials the thing in the state.

**Decision: (b), created by hand once, rotated by hand, named and stored as below.** Terraform manages
what is *inside* a stack and never the credentials it authenticates with.

The provider needs two kinds of authentication (**[verify in 046: current provider docs]**): the **Cloud API**
(access policies, stack-level resources the Cloud API owns) and the **stack** (alert rules, contact points,
policies, folders, dashboards, via a stack service account token); and, for IRM, an **IRM/OnCall access token**.
To keep the surface small, `AW-INF-046` is built with the stack's own endpoint and a stack service account
for everything it can, and uses the Cloud API credential only for what only it can do (listing the stack's
endpoints and data-source UIDs as data, read-only).

| Credential (GitHub name) | Kind and scope | Stored | Read by |
|---|---|---|---|
| `GRAFANA_<STACK>_PLAN_TOKEN` (×3) | stack service account `andara-tf-plan`, role **Viewer**; reads alerting, folders, dashboards, contact points, policies | repository secret | the `terraform-plan` job on a pull request |
| `GRAFANA_<STACK>_APPLY_TOKEN` (×3) | stack service account `andara-tf-apply`, role **Admin** on that stack only (the provider's alerting provisioning needs it) | secret of the `andara-main` environment (and `andara-prod-apply`, below, for prod) | the `terraform-apply` job on `main` |
| `GRAFANA_<STACK>_ALERTS_READ_TOKEN` (×3) | stack service account `andara-alerts-read`, role **Viewer**; decision 2 | repository secret | the drills (`env-recover`, `observe-unavailable`, `stack-recover-mismatch`) |
| `GRAFANA_CLOUD_READ_TOKEN` (unchanged) | Cloud access policy, `metrics:read logs:read traces:read` | repository secret / `.local/box.env` | `make observe-check`, the drills |
| `GRAFANA_SOLO7PROD_IRM_TOKEN` | IRM access token; only `solo7prod` has IRM resources | secret of `andara-prod-apply` | the prod apply |
| `SLACK_WEBHOOK_DEV`, `SLACK_WEBHOOK_PROD` | Slack incoming webhook, one per channel | secrets of `andara-main` / `andara-prod-apply` (Terraform variables `TF_VAR_slack_webhook`) | the applies |
| `TFSTATE_*` | not a secret: OIDC role ARNs for plan and apply (decision 3) | repository variables | the jobs |
| the dev box write credential | `GRAFANA_SOLO7LOCAL_APPLY_TOKEN`, **one per stack**, only `solo7local` by default | `.local/box.env` (gitignored, mode 0600) | `make tf-apply STACK=solo7local` by Brian |

- **The existing property is kept.** A pull-request job holds only the three `*_PLAN_TOKEN`s and the
  read-only state role, and never an apply token. `main` applies from the `andara-main` environment
  (`solo7local`, `solo7dev`) and the `andara-prod-apply` environment (`solo7prod`, decision 5); an apply
  token is readable only by a job that declares its environment, so a branch's copy of the workflow cannot
  read it.
- **Bootstrap token:** the first `andara-tf-apply` service-account token for each stack is created by Brian
  in the Grafana UI (stack → Administration → Service accounts) and pasted into the environment; this is the
  chicken-and-egg step, and it is the only manual credential step that cannot be removed. `docs/runbooks/
  grafana-credentials.md` (`AW-INF-046`) lists the click path, the name, the role, and the expiry.
- **Rotation:** service-account tokens carry a 90-day expiry; the rotation is a runbook step
  (create the new token, set the secret, run the `terraform-plan` job, revoke the old). A token within 14 days
  of expiry is surfaced by `make tf-credentials-check`, a scheduled job that calls each stack's API with each
  token and reports days left; its failure is a GitHub issue, not a page.
- The dev-box credential is the first credential that leaves CI; the rule is that the box holds only
  `solo7local`'s apply token, and `dev` and `prod` apply from CI only.

### 5. Delivery

**Options.** (a) plan on pull request, apply on `main`, all three stacks in one job; (b) the same, one job
per stack in a fixed order; (c) apply from the dev box only.
(a) fails a later stack's plan with an earlier stack's change. (c) has no review gate.

**Decision: (b).**

- **Pull request:** `terraform-plan` workflow, triggered by `pull_request_target` on changes under
  `deploy/terraform/**`, `alerts.yaml`, and the dashboard source; same-repository pull requests only. It runs
  the **base branch's** scripts and reads the pull request's Terraform as **data**, with the read-only
  credentials. Because `terraform plan` executes provider and data-source code, a branch can otherwise run
  anything with a read token and a state-read role, so a base-branch script first **polices** the pull
  request's `deploy/terraform/**` and refuses to plan if: any `provider` or `required_providers` source is not
  `grafana/grafana`; any `data "external"`, `provisioner`, `local-exec` or `module` with a remote `source`
  appears; or `.terraform.lock.hcl` changes the `grafana/grafana` hashes without the version pin changing.
  The policy check is `scripts/tf_policy.py` (`AW-INF-046`), tested with a fixture per refusal. The plan for
  each stack is posted in the job summary. Fork pull requests, and pull requests aimed at another branch, are
  skipped, as `alerts` does today.
- **Merge to `main`:** a `terraform-apply` workflow, one job per stack in the order **`solo7local` →
  `solo7dev` → `solo7prod`**, each `needs:` the one before it. Each job: `terraform init`, `plan -out`,
  `apply` of that plan, then `plan -detailed-exitcode` which must exit 0 (a second plan changes nothing, as
  `alerts-sync` does today). A failed or non-converging job stops the chain, so a change that breaks on
  `solo7local` never reaches `solo7dev` or `solo7prod`.
- **`solo7prod` waits for approval:** its job declares the environment `andara-prod-apply`, with Brian as a
  required reviewer. The prod plan is already in the earlier summary; the approval is Brian reading it.
- **What a failed apply leaves behind:** Terraform's state is written after each resource, so a failed apply
  leaves the resources it finished applied and the rest not, and the state says which. Every resource here is
  idempotent and independently valid (a rule group, a contact point), so a partial apply is a stack in a
  between state, not a broken one. The job fails red; the next push re-runs `plan` which shows exactly the
  remainder. There is no automatic rollback: rolling back is reverting the commit, which the same pipeline
  applies. The one ordering hazard, a notification policy that points at a contact point not yet created, is
  removed by Terraform's dependency graph (the policy references the contact point).
- **How an apply failure and drift are surfaced:** an apply failure is a red workflow, which notifies Brian
  through GitHub. **Drift** (someone edited a rule or a policy in the UI) is surfaced by a scheduled
  `terraform-drift` workflow, daily, running `plan -detailed-exitcode` per stack with the read-only
  credentials: exit 2 opens or updates one GitHub issue labelled `drift:<stack>` containing the plan. It is
  never an alert in Grafana, because the thing that has failed is the thing that delivers alerts.
- **Targets:** `make tf-fmt-check tf-validate tf-test` (in `make check`, no credentials needed, mock
  provider), `make tf-plan STACK=<stack>`, `make tf-apply STACK=<stack>`, `make tf-drift STACK=<stack>`.

### 6. Scope of "all Grafana Cloud pieces"

**Decision: in Terraform, in each stack:** the folder `Andara`; the `andara` rule groups (Grafana-managed);
contact points; the notification policy tree; mute timings; the `tick-health` dashboard (and its folder
permissions where they differ from default); for `solo7prod`, the IRM integration, schedule and escalation
chain (decision 10).

**Outside Terraform, by hand, and why:**

| Piece | Why it stays manual |
|---|---|
| The three stacks themselves | Creating a stack needs the org-level Cloud credential, which is exactly the broad credential decision 4 refuses to put in CI; created once. |
| The service accounts and tokens decision 4 lists | Chicken and egg; Terraform cannot authenticate with a token it is about to create. |
| The Slack workspace, the two channels, the webhooks | Slack's, not Grafana's. |
| IRM's on-call *people* and their notification preferences (phone, push) | Personal data, entered by the person. The *schedule* referencing them is Terraform's. |
| The `k8s-monitoring` release and its telemetry token | Brian's cluster repo (decision 13). |
| Data sources | Grafana Cloud provisions each stack's `grafanacloud-<stack>-prom|logs|traces` data sources; Terraform reads their UIDs and does not manage them. |
| Billing, org membership, SSO | Not observability. |

### 7. Cutover (ruler → Grafana-managed, no double page, no gap)

**Options.** (a) big-bang: delete from the ruler, create in Grafana; (b) create Grafana-managed rules
**paused**, verify, then switch; (c) both live with routing to hide one.
(a) leaves a window with neither. (c) pages twice or relies on a mute hiding a real page.

**Decision: (b), per stack, in this order.** No step leaves a rule both loaded in the ruler and live in
Grafana-managed, or in neither.

1. `AW-INF-046` is applied for the stack: folder, contact points, notification policy, mute timings. Nothing
   routes anywhere yet; the data-source-managed ruler is still the only evaluator.
2. `AW-INF-047` applies the rule groups **with `is_paused = true`** on every rule. A paused Grafana-managed
   rule neither evaluates nor notifies. The ruler remains the only live copy. `terraform plan` is clean.
3. Equivalence check on the paused set: `make tf-plan` shows each rule's `expr` equal to the file's, and
   `observe-unavailable` / `env-recover` **dry-run** (they assert only that the rule is loaded and healthy
   through the rules endpoint, which still answers for a paused rule's definition) pass.
4. **The switch, one commit and one workflow run.** "Live" means evaluating: a paused rule is loaded but not
   live. Precondition, checked by the job and failing it if not met: no `andara` alert is `pending`, `firing`
   or recovering in the stack, in the ruler or in Grafana. Then, in order: the rules are un-paused
   (`is_paused = false`), and the ruler's `andara` namespace is deleted as the job's last step with the
   existing write credential. Un-pause comes first because a missing evaluator is worse than a doubled one.
   The two evaluators' alerts do not share label sets (Grafana-managed alerts add `grafana_folder`), so
   Alertmanager will not deduplicate them: a symptom that begins inside the one-run window between the two
   steps notifies twice, once per evaluator. That is the accepted worst case (a duplicate, never a silence),
   and the precondition removes it for every symptom already present. If the delete fails the job fails
   red and the next run retries it.
5. Prove it: the drill (`observe-unavailable ENV=dev` on `solo7dev`) fires and clears through the rules
   endpoint; `mimirtool rules print` returns no `andara` namespace.
6. Remove the ruler path: `alerts_sync.py` and `make alerts-sync alerts-diff`, the `alerts` workflow, the
   ruler credentials (`ANDARA_ALERTS_CI_READ`, `ANDARA_RULES_CI_WRITE`, `MIMIR_*`), and the runbook's step 1
   and "Delivering rules" section. The compose Prometheus stops reading the file with `AW-INF-048`.

- **Order across stacks:** `solo7local`, then `solo7dev`, then `solo7prod`, each a separate merge to `main`;
  the next stack's step 4 does not start until the previous stack's step 5 passed.
- **`solo7-local` (the existing hyphenated stack):** see decision 9.
- **Rollback to the ruler:** until step 6, rolling back is `is_paused = true` plus
  `alerts_sync.py sync` (both still in the repo). After step 6 it is `git revert` of step 6's commit and the
  same two commands. This is the migration-and-rollback path CLAUDE.md §6 asks of a dependency.

### 8. Import

**Decision: recreate, not import.** The hand-made email contact point and the `severity = page` policy are
one email address and one matcher, with no history worth keeping; importing them puts a hand-chosen name and
default settings into state and then diffs against them forever. The sequence is: Terraform creates its
contact points and its policy tree; the policy tree *replaces* the stack's root policy (the provider's
`grafana_notification_policy` is a singleton that owns the whole tree, so a hand-made child policy is
overwritten at the first apply, which is the intended outcome); the hand-made contact point is then deleted by
hand. The email contact point is **not** recreated anywhere except as a `solo7prod` fallback receiver on the
IRM route (decision 10), because "the account email" was only ever the placeholder.

### 9. Stacks and environments

**Decision.**

| Stack | Stack name / slug | Environments (`namespace` label) | Telemetry sender |
|---|---|---|---|
| `solo7local` | `solo7local` | `andara-compose` (the compose stack), `andara-local` (the kind platform), `andara-ci` (CI's kind cluster) | compose Alloy (`AW-INF-048`); CI's kind job; Brian's local kind |
| `solo7dev` | `solo7dev` | `andara-dev`; the future staging environment (`andara-staging`) when it exists | the rebuilt cluster's `k8s-monitoring` |
| `solo7prod` | `solo7prod` | `andara-prod` | the rebuilt cluster's `k8s-monitoring`, when prod is installed |

- **Endpoints** are not hard-coded: each stack's metrics, logs, traces and Grafana URLs are read from the
  Cloud API as data in the module (`grafana_cloud_stack`), and written to the runbook by `AW-INF-046` as a
  table after the first apply. The repository variables `GRAFANA_CLOUD_PROM_URL` / `_PROM_USER` /
  `_LOKI_*` / `_TEMPO_*` become per-stack (`GRAFANA_SOLO7DEV_PROM_URL`, …); `make observe-check ENV=<env>`
  selects the set by decision 12.
- **Compose's `namespace` is `andara-compose`.** `andara-local` stays the kind platform's (`AW-INF-006`).
  Compose has no kube-state-metrics or `namespace` label of its own: Alloy adds `namespace="andara-compose"`
  to everything it ships. The rules' existing guard (`count(up{namespace!=""}) > 0` on the `absent()` lines)
  is retained, so a compose run never fires an absence for `andara-dev`.
- **`solo7-local`, the existing hyphenated stack, is retired.** It holds `dev` and `prod` series, hand-made
  routing and ruler rules; the three new stacks are created beside it, `dev`'s telemetry moves at Brian's
  rebuild (nothing is migrated: `dev`'s World is discarded by the rebuild), and `solo7-local` is deleted by
  Brian after decision 7 has completed for `solo7dev` and step 6's rollback window (7 days) has passed. Its
  data is not carried over: series are the cluster's last 14 days of tick health, and `dev` is rebuilt.
- **Write credentials by place:** CI holds `PLAN` for all three and `APPLY` per environment (decision 4); the
  dev box holds `solo7local` apply only.
- **Retention and cost of `solo7local`:** compose and CI send continuously whenever they run. The stack is on
  Grafana Cloud's free tier by default (14-day retention, active-series limit); the Alloy config in
  `AW-INF-048` drops everything but the series the server and the drills use, and `make down` stops the
  sender. A scheduled `tf-credentials-check` also reports `solo7local`'s active series against the limit; this
  is the "is the free tier enough" signal, and nothing here depends on it staying free.

### 10. Notification routing per stack

**Decision.**

| Stack | Default policy routes to | Contact points | Notes |
|---|---|---|---|
| `solo7local` | **none**: the default receiver is an empty contact point `blackhole` (no integrations) | `blackhole` | Rules evaluate and their state is readable (decision 2); `RecoveryStateMismatch` fires in `solo7local` and notifies no one. |
| `solo7dev` | Slack `#andaras-world-dev` for every severity | `slack-dev` (Slack, webhook `SLACK_WEBHOOK_DEV`) | `severity = page` does **not** page on dev: a dev outage is information, not an emergency. |
| `solo7prod` | child policy `severity = page` → `irm-prod` **and** `slack-prod`; default → `slack-prod` | `slack-prod` (webhook `SLACK_WEBHOOK_PROD`), `irm-prod` (Grafana IRM integration), `email-brian` as a last-resort fallback on the IRM route | |

- **Why a separate `blackhole` and not "no policy":** with no default contact point, Grafana falls back to its
  built-in email receiver. An explicit empty receiver is the only way to guarantee nothing is sent.
- **IRM, Terraform-managed (`solo7prod` only):** the Alertmanager-type integration (`irm-prod`), one escalation
  chain `page` with a single step "notify on-call from schedule `primary`", and the schedule `primary` whose
  one shift is Brian's IRM user (the user is a lookup by username, not created). **How a second person is
  added:** add the person's IRM username to a `locals.oncall_users` list in the module and a second step to
  the escalation chain (a variable `escalation_users`); the plan shows the change; the person sets their own
  phone and push notifications in IRM. No other file changes.
- `solo7local` and `solo7dev` have no IRM resources. `grafana_oncall_*` is configured only in the
  `solo7prod` root (the provider block takes an IRM token only there).
- **What a developer without a credential gets from `make up`:** a working local stack, and a warning:
  Alloy does not start without `GRAFANA_SOLO7LOCAL_*` write credentials (a `solo7local` *telemetry* token, a
  new `metrics:write logs:write traces:write` access policy, distinct from the Terraform credentials), the
  server's OTLP export has no destination, and every target that reads Grafana Cloud exits `3` (the existing
  convention: credentials missing, not failure). `make check` needs no credentials and is unchanged. §8's
  "against a real backend" is then carried by CI and by `dev`, which have credentials; that is stated in the
  story's §8 record, as CLAUDE.md §8's last paragraph already allows.

### 11. Dashboards

**Decision.** The source of truth is the existing JSON, moved to `deploy/grafana/dashboards/tick-health.json`
(`AW-INF-049` moves it from `deploy/compose/grafana/dashboards/`). Terraform deploys it to each stack with
`grafana_dashboard` (`config_json = file(...)`, `folder` = the `Andara` folder, `overwrite = true`). Per stack:

- **Datasource:** the dashboard's `datasource` is a variable `${ds}` of type `datasource` filtered to
  Prometheus; the module sets its default to the stack's `grafanacloud-<stack>-prom` UID (read as data, decision
  9), so one JSON serves every stack and no UID is committed.
- **`namespace` variable:** `label_values(up{job="andara-server"}, namespace)`, so it lists the values
  present in that stack: `andara-compose`/`andara-local`/`andara-ci` in `solo7local`, `andara-dev` in
  `solo7dev`, `andara-prod` in `solo7prod`. It is `multi = false`, default to the first value, and every panel
  query carries `namespace="$namespace"`.
- Deleted by hand in the UI is drift, found by the daily `terraform-drift` job. The compose Grafana's
  provisioning files go with `AW-INF-048`.

### 12. Multi-stack structure and `ENV`

**Options.** (a) one root, a provider alias per stack; (b) a thin **root per stack** calling a shared module.
(a) plans and locks all three stacks at once, needs all three credential sets on every run (the PR plan job
would hold all three, and so would a dev-box `solo7local` apply), and puts one state's blast radius across
prod. (b) is three states, three plans, and exactly one credential set per job.

**Decision: (b).**

```
deploy/terraform/grafana/
  modules/stack/            # rules, contact points, policy, mute timings, dashboard; variables below
  stacks/solo7local/        # main.tf, backend.tf (key grafana/solo7local.tfstate), terraform.tfvars
  stacks/solo7dev/
  stacks/solo7prod/         # also configures the IRM provider and calls modules/irm
  modules/irm/
  tests/                    # terraform test files, mock provider
  .terraform-version
```

`terraform.tfvars` per stack: `stack = "solo7dev"`, `environments = ["andara-dev"]`, `slack = true`,
`irm = false`, `receivers = {...}`. The provider is `grafana/grafana`, pinned `~> 4.7` (**[verify in 046]**
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
| metrics | the stack's Prometheus remote-write URL and user (`GRAFANA_<STACK>_PROM_URL` / `_USER`, shown by `terraform output` / the runbook table) | a Cloud access policy `andara-<stack>-telemetry` with scopes `metrics:write logs:write traces:write`, one token, installed as the release's secret |
| logs | the stack's Loki URL and user | same token |
| traces | the stack's OTLP endpoint | same token |

**Labels.** Every series, log line and span carries `namespace = <the pod's namespace>` (`andara-dev`,
`andara-prod`) and no other environment label: the rules and the dashboard key on `namespace`.
`cluster` may be added freely; no rule reads it. `job="andara-server"` and `job="andara-projector-state"`
are produced by the chart's scrape annotations and must not be relabelled.

**The keep-list: series that must reach Grafana Cloud and not be dropped** (all for `namespace="andara-dev"`
and `"andara-prod"`; `container="server"` where it applies):

| Series | Used by | Check after the rebuild (each returns ≥ 1 series) |
|---|---|---|
| `up{job=~"andara-server|andara-projector-state"}` | `AndaraServerUnavailable`, `StateProjectorDown` | `up{job="andara-server", namespace="andara-dev"}` |
| `kube_pod_container_status_last_terminated_exitcode` | `RecoveryStateMismatch` (cluster clause) | `…{namespace="andara-dev", container="server"}` |
| `kube_pod_container_status_ready` | same | `…{namespace="andara-dev", container="server"}` |
| `kube_pod_container_status_restarts_total` | `AndaraServerCrashLooping` | `…{namespace="andara-dev", container="server"}` |
| `kube_deployment_spec_replicas` | `StateProjectorDown` (`AW-INF-025`) | `…{namespace="andara-dev", deployment="andara-projector-state"}` |
| `certmanager_certificate_expiration_timestamp_seconds` | `CertificateExpiringSoon` | any series; its `exported_namespace` carries the Certificate's namespace |
| `traefik_router_requests_total` | `IngressErrorRateHigh` | any series with `router=~"andara-dev-andara.*"` |
| the server's `andara_*` series (`andara_ticks_total`, `andara_simulation_lag_seconds`, `andara_snapshot_age_seconds`, `andara_recovery_state_hash_match`, `andara_session_egress_drops_total`, `andara_stream_subscribers`, `andara_content_pending_seconds`, `andara_state_*`) | the remaining rules and the dashboard | `andara_ticks_total{namespace="andara-dev"}` |

(Authoritative list: every metric name appearing in `alerts.yaml` and the dashboard; `AW-INF-046` adds
`make observe-check ENV=<env>` a `--keep-list` mode that extracts them from both files and queries each,
so this table cannot go stale.)

**Order.**
1. Before the rebuild: apply `AW-INF-046` to `solo7dev` and `solo7prod` (the stacks exist, hand-made;
   contact points, policy, folder), and create the telemetry access policy and token (decision 4's by-hand
   step) for each. `AW-INF-047` is applied **paused** (decision 7 step 2) so nothing pages from an empty stack.
2. Brian rebuilds the cluster and installs `k8s-monitoring` with the table above.
3. After: `make observe-check ENV=dev` and its `--keep-list` mode pass against `solo7dev`; then decision 7
   step 4 un-pauses the rules; then `make observe-unavailable ENV=dev` (step 5). `AW-INF-034`'s drill is rerun
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
of B and C is rewriting four `stack-*` drivers and three compose-level fault injections (`SIGKILL`, broker
bounce, `stack-boundary-lost`) against pods, for a gain the CI kind job and `dev` already supply.

**Decision: A, narrowed.** Compose stays the local environment. Its observability services (Prometheus, Tempo,
Loki, Grafana, and the OTLP collector as configured) are removed; **Alloy** replaces the collector and ships to
`solo7local`. It does not become a second deployment description: its `full` profile stays Redpanda, Redis,
Postgres, the object store, Alloy and the server.

- **`make up`** means what CLAUDE.md §9 says: server + datastores + (credentialed) observability, now
  with Grafana Cloud as the observability. `make up PROFILE=min` is unchanged (server, Redpanda, Redis; no
  Alloy, no telemetry destination).
- **`make bootstrap && make up && make check`** remains the whole onboarding path, and needs no Grafana Cloud
  credential, no cluster and no kind.
- **The four `stack-*` targets and the M2 gate drill run in compose.**
  `stack-smoke` and `stack-projector-check` assert on the **server's own `/metrics`** and logs (loopback,
  8080) where they only need to see that the process counted something, and read Grafana Cloud only where the
  story's contract says a Cloud series is the evidence; `stack-recover-mismatch` reads the rules endpoint of
  `solo7local` (decision 2); `stack-recover` (the M2 gate, RTO 120 s) and `stack-boundary-lost` keep their
  compose drivers. The **same gate is also run on the cluster** by `env-recover`, which is the release
  evidence; the compose run is the fast loop and not a substitute.
- **CI** runs what it runs today: `make check` and the kind job. The `stack` workflow's drills keep running; a
  job that has the `solo7local` telemetry and `ALERTS_READ` secrets also checks the rules endpoint, and one
  that does not (a fork) skips those assertions and says so (exit `3` locally, a notice in CI).
- **`AW-INF-048` stays**, as the narrowed story: remove the four services, add Alloy with the
  `namespace="andara-compose"` relabel and the keep-list, make the credentials optional (decision 10).
  The kind-as-local debate does not create a replacement story.
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
- **State holds secrets** (webhooks, an IRM URL). Read access to state is a secret read, and it is shared with
  the pull-request plan job, which runs a pull request's Terraform. The job's defence is the policy script
  (decision 5), which is a deny-list; a provider vulnerability, or a clever HCL construct it does not know,
  would pass it. The residual: a leaked webhook posts to a channel or sends a false page to Brian.
- **Three stacks, three states, three credential sets** per kind of job: 7+ secrets to rotate every 90 days.
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
