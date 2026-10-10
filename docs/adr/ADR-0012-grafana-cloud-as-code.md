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
(step 1). All of it lives in the one stack that exists today. Its name is `solo7` (Brian's `gcx` context; `gcx config view`: server `https://solo7.grafana.net`); the
story text calls it `solo7-local`.
It holds `dev`'s series, the ruler namespace `andara`, and a hand-made email contact point. Read through
`gcx` on 2026-10-10 it also shows `AndaraServerUnavailable{namespace="andara-prod"}` **firing**, because prod
is not installed (the runbook's "silence it until prod exists").

Brian's direction (2026-10-09 and 2026-10-10), all of which this ADR takes as given:

- Alerts move from the ruler to **Grafana-managed rules**, and every Grafana Cloud piece goes under Terraform.
- **Grafana Cloud is the one observability backend at every stage.** The compose stack's Prometheus, Tempo,
  Loki and Grafana are removed (`AW-INF-048`); "verified against a real backend" (CLAUDE.md §8) is checked
  there.
- **Two stacks:** `solo7` for **prod**, and `solo7dev` for **everything non-prod**. Every series, log line and
  span carries an **`environment` label** whose value is `local`, `dev`, `staging` or `prod`. `solo7dev` posts
  to Slack `#andaras-world-dev`; `solo7` posts to `#andaras-world` and pages through Grafana IRM, whose
  schedule is Brian alone.
- **Tests and drills stop calling Grafana's HTTP APIs directly** and use the **`gcx`** command line tool
  instead (decision 2).
- Brian creates the stacks by hand. **This repository takes over the kind cluster and the `k8s-monitoring`
  install** (2026-10-10): the cluster's creation and rebuild, and the Helm release that ships its telemetry,
  are `make` targets here, not a separate cluster repo. A follow-up story builds them and updates the ADRs that
  assumed the split; this ADR changes only where it named the cluster repo (decisions 3, 4, 6, 7, 13, 14).

The constraint that makes this non-obvious is that **three things disagree about the same rule file**:
`alerts.yaml` is read by the compose Prometheus (until `AW-INF-048`), the chart's ConfigMap, the helm tests,
and `observe_unavailable.py` (which evaluates one rule's `expr` against the data source, `rule_expr()`);
Grafana-managed rules **do not write the `ALERTS` series** that `env_recover.py` (AC-2, AC-7) and
`stack_recover_mismatch.sh` assert on, so the M2 gate on `dev` breaks silently unless something replaces that
read (`observe_unavailable.py` never read `ALERTS`, and so asserts nothing about rule state today: decision 2
gives it a state read); and `AndaraServerUnavailable` names `andara-dev` and `andara-prod` in its `absent()`
lines, so one rule set evaluated in both stacks would fire in `solo7dev` for a prod that is not there. The rules
also key on `namespace` today and carry no `environment`, which the new label and the per-environment routing
in `solo7dev` (decision 10) both need.

Checked before deciding (the repo's own rule for infra picks): no accepted ADR covers Terraform, a state
backend or Grafana. ADR-0002 §7 ("Redpanda locally, Kafka on `dev` and `prod`") is the precedent for decision
14: a local stand-in is acceptable where the contract it exercises is the same.

Facts this ADR leans on, from the providers' published documentation and from `gcx` v0.2.11 run against the
`solo7` context (read-only). Items marked **[verify in 046/047]** are what the first story to touch them must
prove before its criterion that depends on them is written as passing:

- `grafana_rule_group`'s `rule` block takes `for`, `keep_firing_for`, `no_data_state` and `exec_err_state`.
  `RecoveryStateMismatch` needs `keep_firing_for: 15m`. Durations must use `s|m|h|d`, never `y`.
- A Grafana-managed rule has a query/expression pipeline and a condition, where the ruler's rule has a single
  PromQL expression. Whether the 12 expressions survive the move label-for-label is decision 1's test.
- `gcx alert rules list` returns, per rule group, each rule's `uid`, `name`, `state` (`inactive`, `pending`,
  `firing`), `health`, `isPaused`, `lastEvaluation`, `labels` and its current `alerts[]`; `gcx alert instances
  list` returns each instance's `ruleName`, `ruleUid`, `labels`, `state`, `activeAt` and `value`, and with
  `--datasource grafanacloud-prom` reads the data-source-managed (ruler) alerts. Whether "Recovering" (during
  `keep_firing_for`) reads as `firing` **[verify in 047]**.
- The stack's data sources have the fixed UIDs `grafanacloud-prom`, `grafanacloud-logs` and `grafanacloud-traces`
  (seen in `solo7`) **[verify in solo7dev]**.
- IRM resources are in the same Terraform provider (`grafana_oncall_*`) with a separate `oncall_access_token`
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
  Prometheus data source (UID `grafanacloud-prom`); expression `B` is `Math` `$A * 0 + 1`; condition `C` is
  `Threshold` `B > 0`. That keeps Prometheus's semantics, "a rule fires for each series its expression
  returns", and keeps the series' labels (so `environment` and `namespace` ride through), where a plain
  threshold on `A` would not fire for `up == 0`, which returns a series whose value is `0`.
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
- **Rules carry the `environment` label.** Every aggregation in `alerts.yaml` becomes `by (environment,
  namespace)` and every `on(...)` join `on (environment, namespace)`, so each alert instance carries both
  labels. `environment` is the routing and rendering key (decisions 10 and 12); `namespace` stays as the source
  within an environment, because `local` has three (`andara-compose`, `andara-local`, `andara-ci`) and the
  drills must not see each other's series. Series that have no `environment` of their own (cert-manager,
  Traefik) get one derived in the rule the way `namespace` is derived today: one `label_replace` per
  environment of decision 9's table, applied **before** the aggregation (in `IngressErrorRateHigh`, inside both
  the numerator and the denominator of the ratio, ahead of `sum by`). An `absent()` copies onto its result only
  the labels of its equality matchers, so each marked absence line keeps **both** matchers, `environment` and
  `namespace`, and the instance carries both. The helm tests gain a `promtool test rules` case per rule
  asserting that `environment` and `namespace` are on the alert.
  This is an edit to `alerts.yaml`, SRE's file, by `AW-INF-047`; the alert set, thresholds and `for` are not
  touched.
- **How `make up`'s §8 check stays honest:** the compose Prometheus's local evaluation of `alerts.yaml` ends
  with `AW-INF-048`. What replaces it is the rule live in `solo7dev` by the same Terraform root, and
  `stack-recover-mismatch` observing it fire there for `environment="local"` (decision 2). The unit-level
  coverage of each expression (`promtool test rules` over the file, run by `make check` through the helm
  tests) is unchanged, because it tests the expression, which is the thing both engines run.
- **The equivalence test** belongs to `AW-INF-047`: for each of the 12 rules, a rendered-pipeline check that
  the `expr` text in the Terraform plan is byte-identical to the file's (after the per-stack rendering of
  decision 12), and a live drill that makes a rule fire and **asserts its state through `gcx`**
  (decision 2): `stack-recover-mismatch` for `environment="local"` (048's, once compose Alloy ships) and
  `observe-unavailable` for `environment="dev"` (047's). A pipeline that changes what an expression means fails
  a drill, not review. `solo7` has no drill: prod's server cannot be taken away to prove a rule, so its rules
  are covered by the same module, the rendered-expression test, the "defined" and "live" checks of
  decision 2, and the first healthy evaluation after un-pause.

### 2. Reading rule state, and testing telemetry: `gcx`

**Options.** (a) the `gcx` command line tool (Grafana's own, `github.com/grafana/gcx`); (b) the Grafana HTTP
APIs called from our scripts (`/api/prometheus/grafana/api/v1/rules`, `/api/v1/query`, Loki's and Tempo's
endpoints); (c) a recording rule that re-exports state as a series so the existing `ALERTS{…}` queries keep
working; (d) Grafana's alert state history (Loki-backed).
(b) is what the scripts do today and it makes every script own authentication, URLs, pagination and response
shapes for four APIs, each with a per-signal credential. (c) makes the rule set carry its own test harness, writes
a series that is not what pages, and silently diverges the day a rule is edited and its twin is not. (d) is
retrospective, eventually consistent and a second store. (a) is one tool, one credential per stack, and the same
commands an operator types by hand; its cost is a pre-1.0 tool (v0.2.11) whose output may change.

**Decision: (a).** From this ADR on, **no script, `make` target or workflow calls Grafana's query or alerting
HTTP APIs directly**; they call `gcx`. Terraform's provider remains the only writer to Grafana, and its traffic
is not a test.

- **Install.** `make bootstrap` runs `install_pinned gcx github.com/grafana/gcx/cmd/gcx v0.2.11` (its existing helper, which installs into
  the repo's `bin/` and checks the module version with `go version -m`, as it does for the other Go tools;
  `gcx --version` prints `0.2.11`, without the `v`, so it is not the check). The pin moves only
  by an edit to one line, in a change that re-records the fixtures below.
- **One wrapper.** Every call is made by `scripts/gcx.py` (`AW-INF-046`), which runs `bin/gcx --config
  "$GCX_CONFIG" --context <context> … -o json`, maps a non-zero exit to exit `1` with `gcx`'s message, and
  parses the JSON. The scripts never parse `gcx` output themselves. Because the tool is pre-1.0 the wrapper is
  tested against recorded outputs (`scripts/tests/fixtures/gcx/`), re-recorded on a version bump, so a changed
  field fails a unit test rather than a drill.
- **Config and context.** `make gcx-config` writes `$GCX_CONFIG` (default `.local/gcx.yaml`, mode 0600,
  gitignored; in CI under `$RUNNER_TEMP`) with one context per stack, `solo7dev` and `solo7`: `grafana.server` =
  the stack's `grafana_url` from its `terraform.tfvars` (decision 9) and `grafana.token` =
  `GRAFANA_<STACK>_READ_TOKEN` (decision 4); a stack whose token variable is unset is left out. The repo's
  scripts always pass `--config` and `--context`, so Brian's own `~/.config/gcx/config.yaml` and its `solo7`
  context (which holds a Cloud token) are never read or written. `ENV` selects the context (decision 12).
  A script exits `3` naming `GRAFANA_<STACK>_READ_TOKEN` before it calls `gcx` when the token is missing.
- **What replaces what.** Data source UIDs are `grafanacloud-prom`, `grafanacloud-logs`, `grafanacloud-traces`
  (passed with `-d`).

| Question the script asks | `gcx` command (always `--context <stack> -o json`) |
|---|---|
| Is the rule **defined** (also true for a paused rule)? | `alert rules list --limit 0 --group <group>` → the rule by `name` / `uid`, its `isPaused` as planned. `terraform plan` showing no diff is the other half. |
| Is the rule **live** (evaluating)? | the same → `health == "ok"`, `isPaused == false`, and `lastEvaluation` within twice the group's `interval`. A paused rule is defined and never live; a check that needs a live rule fails on a paused one. |
| What is the rule's state for `environment="dev"`? | `alert instances list --name '^RecoveryStateMismatch$' --json ruleName,labels,state,activeAt` → filter `labels.environment == "dev"` (and `labels.namespace`) in the wrapper; the wrapper maps the instance `state` to one vocabulary: `Alerting` (Grafana-managed) and `firing` (ruler) are **firing**; `Pending` is pending; `Normal` and `Normal (NoData)` are **not** firing. `Recovering`, during `keep_firing_for`, is **[verify in 047]**: the drills must treat it as firing for AC-7's "the alert outlives the process" only if `gcx` reports it. |
| Is anything firing or pending for a rule before a drill starts (`stack_recover_mismatch.sh`'s "not already firing")? | `alert instances list --name '^<rule>$' --state firing` and `--state pending` → none for the environment; the 15 m `keep_firing_for` wait-out message stays. |
| Did metrics arrive (`observe-check`)? | `metrics query -d grafanacloud-prom 'up{job="andara-server",environment="dev"}' --since 5m` (a series is the answer); `metrics series -d grafanacloud-prom --since 5m '{__name__=~"andara_.*"}'` (a selector is required) for the keep-list. |
| Did logs arrive? | `logs query -d grafanacloud-logs '{…environment="dev"…}' --since 5m --limit 5` |
| Did traces arrive; does a `trace_id` resolve? | `traces query -d grafanacloud-traces '{ … }' --since 5m`; `traces get -d grafanacloud-traces <trace_id>` |
| Did the page reach IRM (`AW-INF-046` AC9)? | `irm oncall alert-groups list` against `solo7` |
| What does the legacy ruler still have (cutover only)? | `alert instances list --datasource grafanacloud-prom` against `solo7`, until decision 7 step 6 |

- **Empty is not an error, and not a pass.** `gcx` prints `null` (and `{"ruleName":null,…}` with `--json`) for an
  empty `alert instances list`, a bare list without `--json` and `{"items":[…]}` with it, and `result: []` with
  `status: success` for a query that matches nothing; a mistyped `--name` regex looks exactly like "nothing
  firing". The wrapper normalises all of these to `[]`, and every "none" assertion (not already firing; nothing
  pending before the ruler is deleted) first confirms the rule exists, **per backend**: for Grafana-managed rules with `alert rules list --group`; for the legacy ruler, which `alert rules list` cannot see, by requiring a known rule (`AndaraServerUnavailable`) to appear in the unfiltered `alert instances list --datasource grafanacloud-prom` (or a `mimirtool rules list`) before it accepts "none". An empty
  metric, log or trace result fails every "arrived" check. The fixtures record each shape.
- **Windows are observed, not reconstructed.** `env_recover.py` AC-2/AC-7 and `stack_recover_mismatch.sh`
  today assert over `ALERTS` (a range query in the first, instant in the second). `gcx` answers "now". The
  scripts therefore poll it at ≤ 10 s from the moment they cause the fault, record every `(time, state)` they
  see, and assert over their own record. This is `docs/specs/testing/live-assertions.md` rule 1 (poll to a
  deadline), applied to a subject that has no history API worth trusting. `ALERT_SETTLE` (130 s, "two of the
  ruler's 1 m evaluations") becomes the group's `interval` times two plus the poll interval, read from the group.
- `rule_loaded` in `env_recover.py` is rewritten to the **live** check. `observe_unavailable.py` keeps
  evaluating the rule's `expr` from `alerts.yaml` (`rule_expr()`), now through `gcx metrics query`, and **gains**
  a step: the rule reaches `firing` for `environment="dev"`, then clears. It stays `ENV=dev` only (it exits `3`
  for any other, because it scales a server to zero).
- **The rule is enforced:** `make gcx-only-check` (in `make check`, `AW-INF-046`) fails if any script,
  `Makefile` or workflow contains a Grafana query or alerting API path (`/api/prom`, `/api/v1/query`,
  `/api/prometheus/`, `/loki/api`, a `*.grafana.net` URL outside `terraform.tfvars` and docs) or an
  `Authorization: Bearer` header for Grafana. The one exception is `alerts_sync.py` and its `mimirtool` calls,
  which are the ruler path that decision 7 step 6 deletes; the check's exception list names them and empties
  with that step.

### 3. State backend and locking

**Options.** (a) **S3-compatible bucket** with `use_lockfile`, one key per root; (b) HCP Terraform
(state only, local execution); (c) state in the git repo (encrypted, e.g. SOPS); (d) the cluster's object
store.
(a) is one dependency Brian already understands, versioned, and lock-capable without a database; it needs a
bucket and an identity that GitHub Actions can assume. (b) is the least to operate and has workspace-level
locking and access control, at the price of a vendor account and a pricing model that has changed more than
once. (c) has no lock, so two applies race, and the history of secrets-in-git is permanent. (d) is rejected
outright: the cluster is rebuilt from this repo (decision 13), so the state of the thing that observes it
cannot live in it, and CI cannot reach it either.

**Decision: (a).** A private bucket `andara-tfstate` (the bucket's account and region are Brian's choice at
creation; the contract is the name and the keys), versioning on, server-side encryption on, public access
blocked. Keys: `grafana/solo7dev.tfstate`, `grafana/solo7.tfstate`, and `grafana/solo7-irm.tfstate` (the IRM
root, decision 12). `use_lockfile = true`; no DynamoDB table. Terraform `>= 1.11, < 2` (`required_version`),
pinned by `.terraform-version` and installed by `make bootstrap`.

- **Who may read the state:** the state holds the Slack webhook URL(s) and, in the IRM root, the IRM
  integration URL, because the provider stores contact-point settings and integration URLs in it. So state
  read is a **secret read**. There are **three roots, three states** (`solo7dev`, `solo7`, `solo7-irm`;
  decision 12), and a role is bound to named roots, each matched exactly (`s3:prefix` = `grafana/<root>.tfstate*`;
  `solo7` does not match `solo7dev` or `solo7-irm`, whose names differ at the character after `solo7`). The roles:
  - an **apply role per root**: get, put and delete on the root's key **and its lock object `<key>.tflock`**,
    and `ListBucket` for that prefix (which `use_lockfile` needs); trust bound to the environment the root
    applies from: `andara-main` for `solo7dev`, `andara-prod-apply` for `solo7` and `solo7-irm`;
  - a **PR/drift plan role for `solo7dev` and `solo7` only**: read on that root's key, the same `ListBucket`,
    no write, no delete, no lock (`plan -lock=false`); trust bound to the claims below (`pull_request_target` and `schedule` events of the base branch's workflow). **It does not exist for `solo7-irm`**: no pull-request job, and no scheduled job outside
    `andara-prod-plan`, can read the IRM state;
  - a **`plan-prod` role** (read on `solo7` and `solo7-irm`, with `ListBucket` for those two prefixes, no write)
    bound to `environment:andara-prod-plan`, which only `main` can deploy to. Its jobs (`plan-prod`,
    `drift-prod-irm`) run `plan -lock=false`, since a plan would otherwise take the lock object, which needs a
    write; the shared concurrency group serialises them against `apply-prod`;
  - Brian's own cloud identity, the bucket's owner, which can read every key (the dev box's day-to-day
    credential is limited to `solo7dev`'s key).
  The PR plan job is policed because it runs a pull request's Terraform (decision 5). A fixture in
  `scripts/tests` asserts the workflow's pull-request jobs, and scheduled jobs outside `andara-prod-plan`,
  reference no `SOLO7_IRM` role, no `TFSTATE_PRODPLAN_ROLE`, no IRM token and no `TF_PLAN_KEY_PROD`. The trust
  conditions pin claims that cannot be set by a pull request's branch. The PR/drift plan role requires
  `workflow_ref` to be the full path `valesordev/andara.valesordev.com/.github/workflows/terraform.yaml@refs/heads/main`
  (the base branch's workflow, which is what `pull_request_target` and the schedule both run), `event_name` to be
  `pull_request_target` or `schedule`, and the `sub` not to be an environment subject (these jobs reference no
  `environment:`, because a job that does gets the `...:environment:<name>` subject instead). The
  environment-bound roles (`andara-main`, `andara-prod-plan`, `andara-prod-apply`) require
  `sub = repo:valesordev/andara.valesordev.com:environment:<name>` **and** the same `workflow_ref`; all three
  environments are deployable from `main` only (`andara-main`'s deployment branch rule says so, as the existing
  environment does today), and decision 5's `apply` job declares `environment: andara-main`. **The claim values are expected, not yet
  observed**: GitHub's OIDC reference does not list `pull_request_target`, so `AW-INF-046` first runs a debug
  step that prints the decoded claims of each job (`pull_request_target`, `schedule`, each environment) and
  writes the trust policies from what it sees; a positive condition that no job can satisfy fails closed, so a wrong guess there is a failed plan, not an
  exposure. The PR/drift role's `sub` condition is a negation, so it fails open: `workflow_ref` and
  `event_name` are the gating conditions and `sub` is defence in depth, replaced by a positive match once 046
  has observed the real values. If the repository uses immutable subject claims, the immutable forms replace
  the `sub` strings.
  **Terraform never creates the credentials CI uses** (decision 4), so state holds no token that can write to
  Grafana. What a leaked webhook buys is a post to a Slack channel; what a leaked IRM integration URL buys is
  a false page to Brian, which is why that URL lives only in the IRM root's state; rotating each is
  a documented step of the runbook.
- **Access is by OIDC federation** from GitHub Actions to the bucket's cloud account (no static cloud key in
  GitHub): a trust policy keyed on the repository plus the claims in the role list above. The dev box uses Brian's own cloud identity.
- State backups: versioning is the recovery path; `terraform import` from the live stack is the second.

### 4. Credentials

**Options.** (a) one org-wide Cloud access policy with a broad token for everything; (b) a stack service
account per stack and purpose, plus Cloud access policies for telemetry; (c) the same as (b) but created by
Terraform.
(a) is simplest and gives the PR plan job a write credential, which breaks the repo's existing property.
(c) makes the credential that creates credentials the thing in the state.

**Decision: (b), created by hand once, rotated by hand, named and stored as below.** Terraform manages
what is *inside* a stack and never the credentials it authenticates with.

**There is no Cloud-API credential in the repo.** Everything Terraform manages is reached through the stack's own
endpoint with a stack service account (alerting, folders, dashboards, contact points, policies), plus an IRM
token for `solo7`. `gcx` reads through the same kind of stack token (decision 2): one **read** token per stack
serves Terraform's plan, the drift job and every `gcx` call, so the Cloud access policies that used to carry
`metrics:read` / `logs:read` / `traces:read` for the scripts are gone. The stacks' endpoints are not secrets and
are **committed** in each stack's `terraform.tfvars` (decision 9). The org-level credential that could create
stacks and access policies stays with Brian (decision 6). The property the repo keeps is stated precisely:
**a pull-request job never holds a credential that can change Grafana's configuration or routing** (apply
tokens, the IRM token). It may hold read credentials, and the low-harm secrets (the telemetry token and the
Slack webhooks), which state also holds.

`<STACK>` is `SOLO7DEV` or `SOLO7`.

| Credential | Kind and scope | Stored | Read by |
|---|---|---|---|
| `GRAFANA_<STACK>_READ_TOKEN` (×2) | stack service account `andara-read`, role **Viewer** **[verify in 046: Viewer must read rule groups, contact points, policies and dashboards through the provider and run the `gcx` commands of decision 2, including datasource queries and `irm oncall` for `solo7`; if not, use a custom RBAC role `andara-reader` with read-only alerting, dashboard and datasource-query permissions, never a write role]** | repository secret; `.local/box.env` | `gcx` (every drill, `observe-check`, the `stack` workflow), and the `plan` (pull request), `drift`, `plan-prod` and `drift-prod-irm` jobs |
| `GRAFANA_<STACK>_APPLY_TOKEN` (×2) | stack service account `andara-tf-apply`, role **Admin** on that stack only | secret of `andara-main` (`solo7dev`) or `andara-prod-apply` (`solo7`; also used by the `solo7-irm` root for its contact point) | `apply` jobs on `main` |
| `GRAFANA_<STACK>_TELEMETRY_TOKEN` (×2) | Cloud access policy `andara-<stack>-telemetry`: `metrics:write logs:write traces:write` and nothing else | `solo7dev`: `.local/box.env` and a repository secret for CI's kind and `stack` jobs, and the `k8s-monitoring` secret that `make monitoring-install` creates from `.local/box.env` for `dev` and `staging`. `solo7`: `.local/box.env` and that secret only, never a repository secret | compose Alloy; CI kind/`stack` jobs; `k8s-monitoring` |
| `GRAFANA_SOLO7_IRM_TOKEN` | IRM access token | secrets of `andara-prod-plan` and `andara-prod-apply` (both reachable from `main` only) | the `plan-prod`, `drift-prod-irm` and `apply-prod` jobs, for the IRM root only |
| `SLACK_WEBHOOK_DEV`, `SLACK_WEBHOOK_PROD` (`TF_VAR_slack_webhook`) | Slack incoming webhook, one per channel | repository secrets | `plan`, `drift` and `apply` jobs (low-harm: posts to a channel) |
| `TFSTATE_<ROOT>_PLAN_ROLE` (`<ROOT>` = `SOLO7DEV`, `SOLO7`), `TFSTATE_<ROOT>_APPLY_ROLE` (also `SOLO7_IRM`), `TFSTATE_PRODPLAN_ROLE` | not secrets: OIDC role ARNs (decision 3) | repository variables | the jobs |
| `TF_PLAN_KEY_PROD` | symmetric key that encrypts prod plan files before upload (decision 5) | secrets of `andara-prod-plan` and `andara-prod-apply`, and a copy in `.local/box.env` (gitignored, mode 0600), because GitHub secrets cannot be read back | `plan-prod`, `apply-prod`; `make tf-plan-show`, by Brian |
| the dev box's write credential | `GRAFANA_SOLO7DEV_APPLY_TOKEN` | `.local/box.env` (gitignored, mode 0600) | `make tf-apply STACK=solo7dev`, by Brian |

- **A telemetry token on a pull-request job is accepted.** It is write-only for series, logs and spans in
  `solo7dev`; a pull request that holds it could add series labelled `environment="dev"` and cause a Slack post in
  `#andaras-world-dev`, never a page (decision 10). It is not a configuration credential. `solo7`'s telemetry
  token is not in this repo at all.
- **Provider credentials are never Terraform variables.** They reach the providers only through the
  providers' environment variables (`GRAFANA_AUTH`, the IRM token's variable), never `TF_VAR_*` or `-var`, so
  no plan file or state holds a token; `scripts/tf_policy.py` and a workflow fixture refuse a token passed
  any other way. The Slack webhooks are the exception, deliberately: `TF_VAR_slack_webhook`.
- **IRM is outside the PR `plan` and `drift` jobs, by being its own root.** Its token can write, so
  everything that needs it is in `stacks/solo7-irm/` with its own state (decision 12): the IRM integration,
  schedule and escalation chain, **and the `irm-prod` contact point**, whose URL comes from the integration.
  The `solo7` root refers to that contact point by its name as a string, with no Terraform dependency, so
  planning it (a pull request, the drift job) never traverses into IRM and never needs the token. The IRM root
  uses the Grafana provider (for the contact point; `solo7`'s read and apply tokens) and the IRM provider (the
  IRM token), and is planned only by jobs in `andara-prod-plan` or `andara-prod-apply` (decision 5).
- **Bootstrap tokens** (the chicken-and-egg step): Brian creates, per stack and by hand, the two service
  accounts above (stack → Administration → Service accounts), the telemetry access policies
  (`grafana.com` → Access policies) and `solo7`'s IRM token (IRM → Settings → API), and pastes the values into the
  places above. They are the only manual credential step that cannot be removed.
  `docs/runbooks/grafana-credentials.md` (`AW-INF-046`) gives each one's click path, name, role and expiry.
  (This changes `AW-INF-046`'s scope: Terraform does not create the Cloud access policies or tokens.)
- **Rotation:** every token carries an expiry of at most 90 days, recorded as a date in
  `deploy/terraform/grafana/credentials.yaml` (names and dates, never values). `make tf-credentials-check`,
  a scheduled job that reads that file (the IRM token included), opens a GitHub issue when any is within 14 days.
  It calls no API, so it needs no credential and never waits on an environment approval.
- The dev-box credential is the first credential that leaves CI; the box holds `solo7dev`'s apply
  token and the plan key (to read, not to apply); `solo7` applies from CI only.

### 5. Delivery

**Options.** (a) plan on pull request, apply on `main`, both stacks in one job; (b) the same, one job
per stack in a fixed order; (c) apply from the dev box only.
(a) fails a later stack's plan with an earlier stack's change. (c) has no review gate.

**Decision: (b).**

- **Pull request:** the `plan` job of `.github/workflows/terraform.yaml` (046's single workflow, jobs `plan`,
  `apply`, `drift`, and for prod `plan-prod`, `apply-prod`, `drift-prod-irm`), triggered by
  `pull_request_target` on changes under `deploy/terraform/**`, `alerts.yaml` and the dashboard source;
  same-repository pull requests only. It is a **matrix of two jobs, one per stack**, each with only that stack's
  `READ_TOKEN`, plan role and webhook. It runs the **base branch's** scripts and reads the pull request's
  Terraform as **data**. Because `terraform plan` executes provider code and evaluates every HCL function, a
  branch could otherwise read the runner's environment into a plan output, so a base-branch script
  (`scripts/tf_policy.py`, `AW-INF-046`) first checks the pull request's `deploy/terraform/**` against an
  **allow-list** and refuses to plan on anything outside it:
  - blocks and providers: only `grafana/grafana` (and `hashicorp/terraform`'s built-in `terraform_data`
    without provisioners), `resource` of type `grafana_*` or `terraform_data` only, `data "grafana_*"`, `variable`, `locals`, `output`, `module` with a
    local `source` under `deploy/terraform/grafana/modules/`;
  - functions: the only file function allowed is `file()` whose argument is a string literal or the single template `"${path.module}/<literal>"` or
    `"${path.root}/<literal>"` (these hold directory paths, not secrets; no other computed path), normalised and
    resolved as a real path, and that real path is inside `deploy/terraform/grafana/`, `deploy/helm/andara/files/alerts.yaml` or
    `deploy/grafana/dashboards/`; the tfvars are read natively and need no `file()`. Positive fixtures are the
    real call sites (`alerts.yaml`, `tick-health.json`); `fileexists`, `templatefile`, `filebase64`, `fileset` and `abspath` are refused. **Symlinks
    are refused**: the script rejects any git entry of mode 120000 and any symlink on disk in the pull
    request's files it extracts, before Terraform runs, because a link under an allowed directory to
    `/proc/self/environ` would otherwise put the runner's environment into a plan (fixtures: a symlink to `/proc/self/environ`, a `..` path that resolves outside the roots, a computed path). No `nonsensitive`, no `terraform_remote_state`, no `external`, no provisioner of any kind; every
    `variable` and `output` block is **byte-identical to the base branch's** (so `slack_webhook` and every
    other secret variable keep `sensitive = true`, and no output can be added to print one; a change to
    either (and the PR that first creates `deploy/terraform/grafana/`, which has no base to compare to) goes
    through the **separate-merge path**: the job refuses to plan, prints the diff, and the maintainer
    merges that change on its own; the plan that then runs on `main` (and, for `solo7`, the one the
    `andara-prod-apply` reviewer reads) is the first to include it. The rule covers the `variable` and `output`
    blocks under `modules/` as well, so a story that needs a new variable lands it as its own small change);
  - the `backend` block and every `provider` block are byte-identical to the base branch's; each stack's
    `terraform.tfvars` endpoint values are checked against the stack they belong to: every URL is split into
    host and path and both are checked. The **host** must equal one of the stack's own hosts as the base
    branch's tfvars records it, matched whole with a `$` anchor (fixtures: `https://x.grafana.net.evil.com`, the
    `user@host` form, a port, a query or fragment). `grafana_url` must have an **empty path**. The sender URLs
    (`prom_url`, `loki_url`, `tempo_url`, `otlp_url`) carry the path Grafana gives for them (for example
    `/api/prom/push` for metrics), so each must equal the base branch's value or match a per-kind path constant
    in `scripts/tf_policy.py` **[verify in 046 against each stack's Details page]**. Every `*_user` must match
    `^[0-9]+$`. The endpoint `variable` blocks have **no `default`**, and
    a change to any `variable` default, or to an endpoint value, makes the job **refuse to plan** and print
    the diff, until the maintainer merges the change to those values separately from other changes (so the
    read token never goes to a host the base branch did not name); no
    `*.auto.tfvars` or other variable file besides `terraform.tfvars`; the job sets no `TF_CLI_ARGS*` and
    passes no `-var`/`-var-file` the base script does not; `required_providers` equal to the base branch's, `terraform init -lockfile=readonly` (so no other provider is
    downloaded), and `.terraform.lock.hcl` changes the `grafana/grafana` hashes only together with the version
    pin (fixtures: `resource "local_file"`, `data "external"`).

  A fixture per refusal lives in `scripts/tests`. The plan is posted in the job summary with sensitive values
  redacted by Terraform. Fork pull requests, and pull requests aimed at another branch, are skipped, as
  `alerts` does today. The residual is stated in Consequences.
- **Merge to `main`:** an `apply` job of the same workflow for `solo7dev` first (`terraform init`, `plan -out`,
  `apply` of that plan, then `plan -detailed-exitcode` which must exit 0: a second plan changes nothing, as
  `alerts-sync` does today). `plan-prod` `needs:` it, so a change that breaks on `solo7dev` never reaches
  `solo7`. Prod is the two jobs below.
- **`solo7` waits for approval, after a complete plan exists.** A job that references an environment with
  required reviewers does not start until it is approved, so the plan cannot be inside it. Prod is therefore
  two jobs: `plan-prod` (environment `andara-prod-plan`: no reviewers, deployable from `main` only, holds the
  read token and the IRM token) plans **both** prod roots, `solo7-irm` first, with `-out`, uploads
  the plan files, **encrypted with `TF_PLAN_KEY_PROD`** (a saved plan embeds state, config and variable
  values, so the webhook and the IRM integration URL are in it), as a one-day artifact and posts the
  plan text for the `solo7` root in the job summary, redacted by Terraform. **For the `solo7-irm` root no
  sink ever carries attribute values: the job summary, the Actions log, the `drift:solo7-irm` issue and
  `apply-prod`'s second-plan check all get only resource addresses and actions** (`terraform plan -out=f
  >/dev/null`, then `terraform show -json f | jq '.resource_changes[] | {address, actions: .change.actions}'`;
  the second-plan check prints nothing but its exit code; a fixture fails if an IRM URL string appears in a log,
  summary or issue body), because the provider
  does not mark the IRM integration's `link` or the `irm-prod` contact point's `url` sensitive, and a
  replacement plan would print a live, usable URL; the contact point also takes it through `sensitive()` in
  the module. Brian reads the full IRM plan with `make tf-plan-show RUN=<id> STACK=solo7-irm|solo7` (it downloads the run's
  encrypted artifact with his `gh` login, which needs artifact read, and decrypts it with `TF_PLAN_KEY_PROD`
  from `.local/box.env`, into a pipe, never a file, and `terraform show` needs only `init -backend=false` and
  the provider pin, no state-bucket role), and that is what he approves. Fields that pass through `sensitive()`
  (the IRM URL) show only as changed or unchanged, so `plan-prod` prints the sha256 of the plaintext plan file in the summary (and `apply-prod` prints it again before applying)
  and `tf-plan-show` prints it too, tying what he read to what `apply-prod` applies; then `apply-prod`
  (environment `andara-prod-apply`, Brian the required reviewer) starts only when he approves, downloads
  those plan files and applies exactly them, in the order `solo7-irm`, `solo7`, and finishes with
  the empty-second-plan check of both. What Brian approves is therefore the complete plan, IRM included.
  Anyone who can read the repository's artifacts sees only ciphertext; the key is in the two prod
  environments and on Brian's box alone. `apply-prod` deletes the artifact when it ends (`if: always()`), and it **refuses a
  superseded plan**: the plan records the commit it was made from, and `apply-prod` fails unless that is the
  current head of `main`; if a later merge moved the head, the approved plan is discarded, `plan-prod` is
  re-run on the new head, and Brian approves again. `plan-prod` and `apply-prod` share a `concurrency` group,
  so two merges queue rather than race, and approving the older run first applies nothing.
- **What a failed apply leaves behind:** Terraform's state is written after each resource, so a failed apply
  leaves the resources it finished applied and the rest not, and the state says which. Every resource here is
  idempotent and independently valid (a rule group, a contact point), so a partial apply is a stack in a
  between state, not a broken one. The job fails red; the next push re-runs `plan` which shows exactly the
  remainder. There is no automatic rollback: rolling back is reverting the commit, which the same pipeline
  applies. The one ordering hazard, a notification policy that points at a contact point not yet created, is
  removed by Terraform's dependency graph (the policy references the contact point).
- **How an apply failure and drift are surfaced:** an apply failure is a red workflow, which notifies Brian
  through GitHub. **Drift** (someone edited a rule or a policy in the UI) is surfaced by a scheduled
  `drift` job of the same workflow, daily, running `plan -detailed-exitcode` per stack with the read-only
  credentials (the IRM root excluded, decision 4): exit 2 opens or updates one GitHub issue labelled
  `drift:<stack>` containing the plan. A third job, `drift-prod-irm`, runs the same daily check for the IRM root, opening its issue with addresses and actions only (above),
  in the `andara-prod-plan` environment (no reviewers, `main` only) and opens `drift:solo7-irm`. It is
  never an alert in Grafana, because the thing that has failed is the thing that delivers alerts.
- **`STACK` values** for `tf-plan`, `tf-apply` and `tf-drift`: `solo7dev`, `solo7`, `solo7-irm` (the last takes
  `solo7`'s endpoints in its own `terraform.tfvars`).
- **Targets:** `make tf-plan-show RUN=<id> STACK=<solo7|solo7-irm>` (above), `make tf-fmt-check tf-validate tf-test` (in `make check`, no credentials needed, mock
  provider), `make tf-plan STACK=<stack>`, `make tf-apply STACK=<stack>`, `make tf-drift STACK=<stack>`.

### 6. Scope of "all Grafana Cloud pieces"

**Decision: in Terraform, in each stack:** the folder `Andara`; the rule groups of `alerts.yaml` (Grafana-managed,
decision 1); contact points; the notification policy tree; mute timings; the `tick-health` dashboard (and its
folder permissions where they differ from default); for `solo7`, the IRM integration, schedule and escalation
chain (decision 10).

**Outside Terraform, by hand, and why:**

| Piece | Why it stays manual |
|---|---|
| The two stacks themselves | Creating a stack needs the org-level Cloud credential, which is exactly the broad credential decision 4 refuses to put in CI; created once (`solo7` exists; `solo7dev` is Brian's to create). |
| The service accounts, Cloud access policies and tokens decision 4 lists | Chicken and egg (Terraform cannot authenticate with a token it is about to create), and creating them needs the org-level Cloud credential. |
| The Slack workspace, the two channels, the webhooks | Slack's, not Grafana's. |
| IRM's on-call *people* and their notification preferences (phone, push) | Personal data, entered by the person. The *schedule* referencing them is Terraform's. |
| The `k8s-monitoring` release and the cluster | Helm and `kind`, not Terraform: a Terraform root that could reach the cluster would put cluster credentials beside state and tokens. They are `make` targets in this repo run from Brian's box (decision 13). The telemetry token is created by hand (decision 4) and read from `.local/box.env`. |
| Data sources | Grafana Cloud provisions each stack's `grafanacloud-prom`, `-logs` and `-traces` data sources; Terraform and `gcx` use their UIDs and do not manage them. |
| Other things already in `solo7` (Synthetic Monitoring checks and alerts, usage alerts, Faro, k6) | Not Andara's rules; the policy tree Terraform now owns must keep routing them (decision 8). |
| Billing, org membership, SSO | Not observability. |

### 7. Cutover (the ruler in `solo7` → Grafana-managed rules in `solo7dev` and `solo7`)

The ruler namespace `andara` exists in **`solo7`**, the stack that becomes prod. It evaluates `dev`'s series,
because `dev` ships to `solo7` today; it evaluates nothing for `prod`, which is not installed, and the absence
rule is firing for it (Context). `solo7dev` is new and has never had ruler rules. So the move is: `dev`'s
telemetry and its rules go from `solo7` to `solo7dev` together at the rebuild (`make cluster-rebuild`, decision 13), and `solo7` keeps its
Grafana-managed rules paused until `prod` is installed. A rule must not be live in both places. It may be live
in neither only during the planned outage, which runs from the deletion of the ruler namespace (step 4(ii)) to
`dev`'s first series arriving in `solo7dev` (step 4(iii)): the environment is being rebuilt for most of it, and
nobody is playing in a fresh World.

**Options.** (a) delete from the ruler, then create in Grafana; (b) create the Grafana-managed rules
**paused**, then move each environment's telemetry and its rules together; (c) both live, with routing hiding
one.
(a) leaves the environment unwatched for as long as the apply takes. (c) pages twice, or relies on a mute that
can hide a real page.

**Decision: (b).** Definitions: "live" means evaluating (a paused rule is defined, not live).

0. Before anything is applied to `solo7`: the prod-absence alert is firing and its severity is `page`
   (`gcx alert instances list --datasource grafanacloud-prom --state firing`). Brian silences it as the
   runbook says; otherwise the new IRM route pages him for a prod that does not exist.
1. `AW-INF-046` is applied to `solo7dev` and to `solo7` (the IRM root first, through `plan-prod` and the
   approved `apply-prod`): folder, contact points, notification policy, mute timings. The ruler's alerts in
   `solo7` carry no `environment` label, so from this step they reach the new root receiver `slack-prod`, and a
   `severity = page` one reaches IRM. Until step 4(ii), a `dev` outage pages Brian through IRM, as it already
   emailed him before; that is accepted.
2. `AW-INF-047` applies the rule groups to both stacks with `is_paused = true` on every rule. In `solo7` the
   ruler remains the only live evaluator of `dev`'s series. `terraform plan` is clean and the **defined**
   check (decision 2) passes for every rule in each stack. `required_environments` (decision 12) is `[]` in
   both stacks.
3. `solo7dev`, for `local`: when compose Alloy ships (`AW-INF-048`), merge `is_paused = false` for `solo7dev`.
   The ruler never evaluated `local` and `dev` is not in `solo7dev` yet, so nothing is doubled, and with
   `required_environments = []` no absence rule fires for a `dev` that has not arrived. `local` alerts route
   to `blackhole` (decision 10). Run `stack-recover-mismatch`, which asserts through `gcx` that the rule fires
   for `environment="local"` and notifies nobody. (Ordering with `AW-INF-048`: `AW-INF-047` supplies the rules
   and the `gcx` reads, `AW-INF-048` carries the firing assertion, so 047's own criterion for `local` is
   **defined**.)
4. `dev`, as one ordered run, starting at the outage: (i) the precondition, checked by the script, which
   refuses to continue if unmet: no `severity = page` alert for `namespace="andara-dev"` is pending or firing in
   `solo7`'s ruler (`gcx alert instances list --datasource grafanacloud-prom`). A lower-severity alert that is
   firing (today `ContentLoadFailing`, a ticket) does not block: the rebuild discards that World, and the
   script prints it so the omission is visible; (ii) Brian deletes the ruler namespace `andara`
   from `solo7` with `make alerts-delete` (a target `AW-INF-047` adds to `alerts_sync.py`, using the existing
   ruler write key, which Brian exports by hand as `MIMIR_API_KEY_WRITE`; it is not in `.local/box.env`; it is
   run by hand, once, and the key is given to no CI job beyond what `alerts` holds today); step 0's silence is lifted only after `alert instances list --datasource grafanacloud-prom` no longer lists the prod-absence alert, so a lingering instance cannot page; (iii) the rebuild
   completes and `k8s-monitoring` ships `dev` to `solo7dev` with `environment="dev"`; (iv) `make observe-check
   ENV=dev --keep-list` (decision 13) passes; (v) a merge sets `required_environments = ["dev"]` in
   `solo7dev`'s tfvars, which adds the `dev` absence line to `AndaraServerUnavailable`; (vi) `make
   observe-unavailable ENV=dev` fires and clears the alert through `gcx`. The rules were live since step 3, so
   `dev`'s series are covered the moment they arrive; (v) only adds the absence rule, which would otherwise
   have fired for a `dev` that had not shipped yet.
5. `prod`, when it is installed: ship its telemetry to `solo7` with `environment="prod"`, pass `observe-check
   ENV=prod --keep-list`, then one merge sets `required_environments = ["prod"]` and `is_paused = false` for
   `solo7` (which reaches prod through `plan-prod` and Brian's approval of `apply-prod`). Until then `solo7`'s
   rules are paused, and with the ruler gone nothing fires for the missing `prod`.
6. Remove the ruler path in one change after step 4(vi): `alerts_sync.py`, `make alerts-sync alerts-diff
   alerts-delete`, the `alerts` workflow, `ANDARA_ALERTS_CI_READ`, `ANDARA_RULES_CI_WRITE`, `MIMIR_*`,
   `mimirtool` in `make bootstrap`, the `gcx-only-check` exception, and the runbook's step 1 and "Delivering
   rules" section. `solo7` is not retired, and `dev`'s old series in it are not carried over or deleted: they
   age out with the stack's retention, and no rule matches a series without `environment="prod"`.

- **Point of no return: step 4(ii).** Before it, nothing has changed for any user, and rolling back is
  pausing or reverting. From it on there is **no ruler to roll back to for `dev`**, because its series stop
  reaching `solo7` at the rebuild; the way back is `is_paused = true`, then fix forward by
  `git revert` and the same pipeline, with `gcx` as the check. Brian chooses when 4(ii) happens, and can hold
  it until step 3 and the **defined** check on `solo7dev`'s rules are done. This is the migration-and-rollback
  statement CLAUDE.md §6 asks of a dependency.

### 8. Import

**Decision: recreate, not import.** The hand-made email contact point and the `severity = page` policy in `solo7`
are one email address and one matcher, with no history worth keeping; importing them puts a hand-chosen name and
default settings into state and then diffs against them forever. The sequence is: Terraform creates its
contact points and its policy tree; the policy tree *replaces* the stack's root policy (the provider's
`grafana_notification_policy` is a singleton that owns the whole tree, so a hand-made child policy is
overwritten at the first apply, which is the intended outcome); the hand-made email contact point is then
deleted by hand, since "the account email" was only ever the placeholder. **Other things in `solo7` already
route through that tree** (Synthetic Monitoring, usage and Faro alerts, seen in `gcx alert rules list`):
`AW-INF-046` lists them before the first apply and the new tree leaves them on the root receiver (`slack-prod`),
so replacing the tree does not silence them. `solo7dev` is new: nothing to import or delete there.

### 9. Stacks and environments

**Decision.** Two stacks; four `environment` values.

| Stack | Slug / `gcx` context | `environment` values it holds | Telemetry senders |
|---|---|---|---|
| `solo7dev` | `solo7dev` | `local`, `dev`, `staging` (when it exists) | compose Alloy (`AW-INF-048`), CI's kind job and Brian's local kind (`local`); the rebuilt cluster's `k8s-monitoring` (`dev`, `staging`) |
| `solo7` | `solo7` | `prod` | the rebuilt cluster's `k8s-monitoring`, when prod is installed |

- **The `environment` label** is on every series, log line and span, with exactly the values `local`, `dev`,
  `staging`, `prod`. `namespace` stays as the source within an environment. The mapping, and the only one:

| `namespace` / source | `environment` |
|---|---|
| `andara-compose` (compose), `andara-local` (the kind platform), `andara-ci` (CI's kind cluster) | `local` |
| `andara-dev` | `dev` |
| `andara-staging` | `staging` |
| `andara-prod` | `prod` |

  The sender applies it: compose Alloy sets `environment="local"` and `namespace="andara-compose"` on all three
  signals; `k8s-monitoring` derives `environment` from the namespace (this table) for every series, log and span
  of an `andara-*` namespace, including kube-state-metrics' (decision 13). A telemetry-bearing sender that sets a
  value outside the four is a defect `observe-check --keep-list` reports (`gcx metrics labels -d grafanacloud-prom
  --label environment`). `local` has three sources, so the drills select on `namespace` as well as `environment`.
- **Endpoints** are committed, because they are not secrets: each stack's `terraform.tfvars` carries
  `grafana_url` (what `gcx` and Terraform use) and, for the senders, `prom_url`, `prom_user`, `loki_url`,
  `loki_user`, `tempo_url`, `tempo_user` and `otlp_url`, each a full URL including the path the sender needs. `AW-INF-046` fills them from the stack's "Details" page
  after Brian creates it, and a test fails on an empty one. The unsuffixed `GRAFANA_CLOUD_*` names are retired
  with `alerts_sync.py`, and no script reads a Prometheus, Loki or Tempo URL: `gcx` reaches the data sources
  through `grafana_url`.
- **`solo7` is the stack that exists** (Context), now prod; `solo7dev` is new. There is no stack to retire and
  no archive step.
- **Write credentials by place:** CI holds `READ` for both stacks and `APPLY` per environment (decision 4); the
  dev box holds `solo7dev` apply, plus the prod plan key for reading plans (never applying).
- **Retention and cost of `solo7dev`:** compose, CI, `dev` and `staging` send to it. The stack is on Grafana
  Cloud's free tier by default (14-day retention, active-series limit); the Alloy config in `AW-INF-048` drops
  everything but the series the server and the drills use, and `make down` stops the sender. Grafana Cloud's
  own usage emails are the "is the free tier enough" signal (no repo job reads usage), and nothing here depends
  on it staying free.

### 10. Notification routing per stack

**Decision.** `solo7dev` holds three environments, so it routes on the `environment` label.

| Stack | Policy tree | Contact points | Notes |
|---|---|---|---|
| `solo7dev` | root receiver `slack-dev`; one child matching `environment = local` → `blackhole` (no `continue`) | `slack-dev` (Slack `#andaras-world-dev`, webhook `SLACK_WEBHOOK_DEV`), `blackhole` (no integrations) | `local` notifies no one; `dev` and `staging` post to Slack. `severity = page` does **not** page here: a non-prod outage is information, not an emergency. An alert with no `environment` falls to the root and posts to Slack, so a rule that loses the label is loud, not silent. |
| `solo7` | root receiver `slack-prod`; two children both matching `severity = page`: the first → `irm-prod` with `continue = true`, the second → `slack-prod` | `slack-prod` (webhook `SLACK_WEBHOOK_PROD`; in the `solo7` root), `irm-prod` (Grafana IRM integration; in the `solo7-irm` root, which applies first) | One page = one IRM alert group and one Slack post. Non-page severities fall to the root: Slack only. |

- **Why a separate `blackhole` and not "no policy":** with no default contact point, Grafana falls back to its
  built-in email receiver. An explicit empty receiver is the only way to guarantee nothing is sent.
- **`local` still evaluates and is observable:** its rules fire and `gcx alert instances list` shows them
  (decision 2); `stack-recover-mismatch` needs `RecoveryStateMismatch` to fire for `environment="local"`. The
  route only decides who is told.
- **IRM, Terraform-managed (`solo7` only):** the Alertmanager-type integration (`irm-prod`), one escalation
  chain `page` with a single step "notify on-call from schedule `primary`", and the schedule `primary` whose
  one shift is Brian's IRM user (the user is a lookup by username, not created). **How a second person is
  added:** add the person's IRM username to a `locals.oncall_users` list in the module and a second step to
  the escalation chain (a variable `escalation_users`); the plan shows the change; the person sets their own
  phone and push notifications in IRM. No other file changes.
- `solo7dev` has no IRM resources. `grafana_oncall_*` is configured only in the `solo7-irm` root (the provider
  block takes an IRM token only there). The policy tree in the `solo7` root names `irm-prod` as a string, so
  applying it before the IRM root exists fails loudly at apply, which is why prod applies the IRM root first.
- **What a developer without a credential gets from `make up`:** a working local stack, and a warning:
  Alloy does not start without `GRAFANA_SOLO7DEV_TELEMETRY_TOKEN` (decision 4, distinct from the Terraform
  credentials), the server's OTLP export has no destination, and every target that reads Grafana Cloud exits
  `3` (the existing convention: credentials missing, not failure). `make check` needs no credentials and is
  unchanged. §8's "against a real backend" is then carried by CI and by `dev`, which have credentials; that is
  stated in the story's §8 record, as CLAUDE.md §8's last paragraph already allows.

### 11. Dashboards

**Decision.** The source of truth is the existing JSON, moved to `deploy/grafana/dashboards/tick-health.json`
(`AW-INF-049` moves it from `deploy/compose/grafana/dashboards/` **and adds** the `ds` datasource variable and the
`environment` and `namespace` variables, which the source does not have today, rewriting its panels'
`datasource` and queries to use them). Terraform deploys it to each stack with `grafana_dashboard`
(`config_json = file(...)`, `folder` = the `Andara` folder, `overwrite = true`). The file is the same in both
stacks, because the data source UIDs are the same (Facts): no per-stack rendering.

- **Datasource:** `${ds}` is a variable of type `datasource` filtered to Prometheus, default
  `grafanacloud-prom`.
- **`environment` variable:** `label_values(up{job="andara-server"}, environment)`, so it lists the values
  present in that stack: `local`, `dev` and `staging` in `solo7dev`, `prod` in `solo7`. `multi = false`, default
  the first. **`namespace`** is a dependent variable, `label_values(up{job="andara-server",
  environment="$environment"}, namespace)`, so `local`'s three sources can be told apart. Every panel query
  carries both.
- A `terraform test` fails if the source lacks any of the three variables. Deleted by hand in the UI is drift,
  found by the daily `drift` job. The compose Grafana's provisioning files go with `AW-INF-048`.

### 12. Multi-stack structure and `ENV`

**Options.** (a) one root, a provider alias per stack; (b) a thin **root per stack** calling a shared module.
(a) plans and locks both stacks at once, needs both credential sets in one job (the PR plan job would hold
both, and so would a dev-box `solo7dev` apply), and puts one state's blast radius across prod. (b) is three
states (two stacks and the IRM root), three plans, and one stack's credential set per job (decision 5's matrix).

**Decision: (b).**

```
deploy/terraform/grafana/
  modules/stack/            # rules, contact points, policy, mute timings, dashboard; variables below
  stacks/solo7dev/          # main.tf, backend.tf (key grafana/solo7dev.tfstate), terraform.tfvars
  stacks/solo7/             # no IRM resources, no IRM token; refers to the contact point irm-prod by name
  stacks/solo7-irm/         # Grafana and IRM providers: integration, schedule, chain, contact point irm-prod
  modules/irm/
  tests/                    # terraform test files, mock provider
  .terraform-version
```

`terraform.tfvars` per stack: `stack = "solo7dev"`, `environments = ["local", "dev", "staging"]`,
`required_environments = []` (`["dev"]` after decision 7 step 4(v); `solo7`: `environments = ["prod"]`,
`required_environments = []` until step 5), `slack = true`, `page_contact_point = null` (`"irm-prod"` for `solo7`:
the name the policy tree routes `severity = page` to, as a string), and the endpoints of decision 9. The
provider is `grafana/grafana`, pinned `~> 4.7` (**[verify in 046]** the latest minor that has `keep_firing_for`
and the IRM resources), with `.terraform.lock.hcl` committed.

**Rendering a rule set that names environments.** The source file is also read unrendered (decision 1), so the
lines that must exist per environment are marked **in the file**, as PromQL comments, which Prometheus ignores:

```
// CONTRACT SKETCH — not an implementation
or absent(up{job="andara-server", environment="dev", namespace="andara-dev"})    # env:dev
or absent(up{job="andara-server", environment="prod", namespace="andara-prod"})  # env:prod
```

The module drops every line marked `# env:<environment>` whose value is not in the stack's
**`required_environments`**, and keeps unmarked lines. Each marked line must be an independent `or` operand:
`AW-INF-047` reshapes `AndaraServerUnavailable` so the group starts with a never-true term (`vector(0) < 0`) and
every marked line is `or …`, so any subset parses. So `solo7dev` renders the `dev` absence once `dev` is
required (and `staging`'s when it exists), `solo7` renders `prod`'s once prod is, and neither renders the other's.
`local` is never required. The rules' existing guard (`and on() count(up{environment!=""}) > 0`) is retained.
**The check:** `make tf-test` renders every rule for every stack and parses it (`promtool check rules` on the
rendered output); a stack whose rendered rule names an environment outside its `required_environments` fails the
test. The rule semantics (thresholds, `for`) are not touched.

**How `ENV` selects a stack.** One mapping, in `scripts/grafana_stack.py` (`AW-INF-046`), read by the
Makefile and every script; `ENV` is the `environment` label value:

| `ENV` | stack / `gcx` context | selectors the drills add |
|---|---|---|
| `local` | `solo7dev` | `environment="local"`, and `namespace="andara-compose"` for the compose drills |
| `dev`, `staging` | `solo7dev` | `environment="dev"` / `"staging"` |
| `prod` | `solo7` | `environment="prod"` |

`observe-check`, `observe-unavailable`, `env-recover`, and the `stack-*` drills resolve ENV through it, then
call `gcx` with that context (decision 2). A command whose ENV maps to a stack with no read token set exits `3`
and names `GRAFANA_<STACK>_READ_TOKEN`. `make alerts-sync` / `alerts-diff` are removed with decision 7's last
step (so their ignored-`ENV` caveat goes away).

### 13. The cluster and `k8s-monitoring` live in this repo

Until 2026-10-10 the cluster and its `k8s-monitoring` release belonged to a separate repo, and this decision was
a contract this repo wrote for it. Now one repo owns both the rules and the thing that feeds them.

**Options.** (a) a contract table in this ADR, enforced by a check, with the values still in another repo;
(b) the values and the cluster definition live here, so the contract is a file and a test; (c) Terraform manages
the Helm release.
(a) keeps the ownership inversion that made the rules break when the cluster drifted. (c) puts cluster
credentials into Terraform (decision 6).

**Decision: (b).** `k8s-monitoring`'s values are a file in this repo, rendered with the stack endpoints from
`deploy/terraform/grafana/stacks/<stack>/terraform.tfvars` (decision 9), so an endpoint is written once. The
tables below are what that file must satisfy, and `make check` fails when it does not (the allow-list half of
the keep-list mode, below), where before an unset variable skipped it. Targets (names are the follow-up
story's to confirm; the contract is that each exists and is idempotent): `make cluster-up` creates the
cluster from the box's kind config; `make monitoring-install ENV=<env>` installs `k8s-monitoring` into it with
the secret from `.local/box.env`; `make cluster-rebuild` is both, preceded by a destroy that Brian confirms.
`make kind-platform` (Traefik, cert-manager) joins the same path. What the installed `k8s-monitoring` must
send, and the order:

**Per environment, send to that environment's stack** (`dev` and `staging` → `solo7dev`; `prod` → `solo7`):

| Signal | Destination | Credential |
|---|---|---|
| metrics | `prom_url` and `prom_user` from `deploy/terraform/grafana/stacks/<stack>/terraform.tfvars` (decision 9) | the Cloud access policy `andara-<stack>-telemetry` (decision 4: `metrics:write logs:write traces:write`, nothing else), one token, installed as the release's secret by `make monitoring-install` |
| logs | `loki_url` and `loki_user` from the same file | same token |
| traces | `otlp_url` (and `tempo_url`) from the same file | same token |

**Labels.** Every series, log line and span carries `environment` (`dev` for `andara-dev`, `staging` for
`andara-staging`, `prod` for `andara-prod`; decision 9's table) **and** `namespace = <the pod's namespace>`: the rules, the dashboard and the
routing key on both. That includes kube-state-metrics' series, which take `environment` from the pod's
namespace through the same mapping. `cluster` may be added freely; no rule reads it. `job="andara-server"` and
`job="andara-projector-state"` are produced by the chart's scrape annotations and must not be relabelled. Series
from outside an `andara-*` namespace (Traefik, cert-manager) carry no `environment`; the rules derive it, as
they derive `namespace` today (decision 1).

**The keep-list: series that must reach Grafana Cloud and not be dropped** (for `environment` `dev` and
`prod`; `container="server"` where it applies):

Two kinds of series. **Always present** ones exist on a clean rebuild and are checked before step 4(v). **Fault-only**
ones (marked ‡) are absent on a clean rebuild until the event they describe, so a clean rebuild has none: the
`--keep-list` mode does not require them, it checks that the values file's metric allow-list **names** them,
and the first drill that produces the fault (`make env-recover ENV=dev`, rerun on the rebuilt cluster,
step 3 below) must then see them (the hash gauge at `1` after the fixed recovery, the exit-code series after the
kill), or the drill is inconclusive (exit 1, as its AC-2 already is).

| Series | Used by | Check after the rebuild, by `gcx metrics query -d grafanacloud-prom` (each always-present row returns ≥ 1 series) |
|---|---|---|
| `up` for `job="andara-server"` | `AndaraServerUnavailable` | `up{job="andara-server", environment="dev"}` |
| `up` for `job="andara-projector-state"` | `StateProjectorDown` | `up{job="andara-projector-state", environment="dev"}` (a separate query: the server's series must not stand in for it) |
| `kube_pod_container_status_last_terminated_exitcode` ‡ | `RecoveryStateMismatch` (cluster clause) | `…{environment="dev", container="server"}` |
| `kube_pod_container_status_ready` | same | `…{environment="dev", container="server"}` |
| `kube_pod_container_status_restarts_total` | `AndaraServerCrashLooping` | `…{environment="dev", container="server"}` |
| `kube_deployment_spec_replicas` | `StateProjectorDown` (`AW-INF-025`) | `…{environment="dev", deployment="andara-projector-state"}` |
| `certmanager_certificate_expiration_timestamp_seconds` | `CertificateExpiringSoon` | `{exported_namespace="andara-dev", name=~"andara-.*"}` (the Certificate's namespace; the check is per environment, so another workload's certificate cannot satisfy it) |
| `traefik_router_requests_total` | `IngressErrorRateHigh` | `{router=~"andara-dev-andara.*"}` (Traefik names an Ingress router `<namespace>-<ingress>-<host>…`, and the rules derive `namespace` and `environment` from that name; the check uses the selected environment's prefix, so `andara-ci` or `andara-staging` routers cannot satisfy it) |
| the server's `andara_*` series (`andara_ticks_total`, `andara_simulation_lag_seconds`, `andara_snapshot_age_seconds`, `andara_recovery_state_hash_match` ‡ (absent until a recovery sets it; on the cluster only `1` is ever scraped, a refused recovery is never Ready, `alerts.yaml`'s own comment), `andara_session_egress_drops_total`, `andara_stream_subscribers`, `andara_content_pending_seconds`, `andara_state_*`) | the remaining rules and the dashboard | `andara_ticks_total{environment="dev"}` |

(Authoritative list: every metric name appearing in `alerts.yaml` and the dashboard; `AW-INF-046` adds
`make observe-check ENV=<env>` a `--keep-list` mode that extracts them from both files and queries each through
`gcx`, so this table cannot go stale. The fault-only set is a constant in `scripts/observe_keep_list.py`, with
a test that fails when a metric the rules name is neither in the always-present set nor in that constant.
The allow-list is read from the in-repo values file; `K8S_MONITORING_VALUES` is gone, and the allow-list half
runs in `make check`, not only against a live stack. The always-present queries, run by `observe-check
--keep-list`, **gate adding `dev` to `required_environments`**; the window until the drill that first produces
the fault is accepted.)

**Order.**
1. Before the rebuild: apply `AW-INF-046` to `solo7dev` and `solo7` (`solo7dev`, once Brian creates it;
   `solo7` exists; contact points, policy, folder). Brian creates the `andara-<stack>-telemetry` access policy and
   token by hand (decision 4's bootstrap step), puts it in `.local/box.env`, and fills the tfvars endpoints,
   which `monitoring-install` reads. `AW-INF-047` is applied **paused** (decision 7 step 2).
2. Brian runs `make cluster-rebuild` (cluster, platform, `k8s-monitoring` with the table above) (decision 7 step 4(ii) to (iii):
   the ruler namespace is deleted first).
3. After: `make observe-check ENV=dev --keep-list` passes against `solo7dev`; then decision 7 step 4(v) adds `dev`
   to `required_environments`; then `make observe-unavailable ENV=dev` (step 4(vi)). `AW-INF-034`'s drill is
   rerun on the rebuilt cluster, since the rebuild discards `dev`'s World.
4. `deploy/kind/config.yaml` stays CI's single-node shape. The box's cluster shape (nodes, port mappings)
   moves into this repo as its own file beside it, so CI's cluster and the box's cannot silently diverge:
   a test asserts that what the chart and `kind-platform` need (the `ingress-ready` label, `:80`/`:443`) is in both.

### 14. The local environment: compose, kind, or both

**Options.** (A) keep compose; Alloy ships to `solo7dev` as `environment="local"` (`AW-INF-048` as written).
(B) retire compose; local is kind running the chart. (C) keep only `min` in compose, and make kind the place
for everything that needs the platform.

The argument in one paragraph. What compose gets wrong about `dev` is real: Redpanda for Strimzi Kafka,
versitygw for the object store, no Traefik, no cert-manager, no probes. But three things already cover those
gaps and none of them is the developer's laptop: **CI's kind job** (`kind.yaml`) installs Traefik and
cert-manager the way the box has them and runs the chart's probes, edge and certificates on every change to
them; **`dev`** is the release gate, with `make env-recover` and `make observe-unavailable` as the cluster
versions of the M2 gate and the unavailable alert; and ADR-0002 §7 already decided that a local Kafka-API stand-in
is the right trade for the fast loop. What kind-locally would add is the *chart* on the developer's machine
— and the resource complaint that started this direction (Strimzi, the object store, `k8s-monitoring`) is
the price. The bootstrap path is no longer a blocker: this repo now creates the cluster (decision 13), so
`make cluster-up` exists whether or not local uses it. The cost of B and C is rewriting the `stack-*` drivers and three compose-level fault injections (`SIGKILL`, broker
bounce, `stack-boundary-lost`) against pods, for a gain the CI kind job and `dev` already supply.

**Decision: A, narrowed.** Compose stays the local environment. Its observability services (Prometheus, Tempo,
Loki, Grafana, and the OTLP collector as configured) are removed; **Alloy** replaces the collector and ships to
`solo7dev` with `environment="local"` and `namespace="andara-compose"`. It does not become a second deployment
description: its `full` profile stays Redpanda, Redis, Postgres, the object store (`minio`, a versitygw image),
Alloy and the server.

- **`make up`** means what CLAUDE.md §9 says: server + datastores + (credentialed) observability, now
  with Grafana Cloud as the observability. `make up PROFILE=min` is unchanged (server, Redpanda, Redis; no
  Alloy, no telemetry destination).
- **`make bootstrap && make up && make check`** remains the whole onboarding path, and needs no Grafana Cloud
  credential, no cluster and no kind. (`make bootstrap` installs `gcx`, decision 2.)
- **The four `stack-*` targets and the M2 gate drill run in compose.**
  All four are re-pointed at `solo7dev` through `gcx` as `AW-INF-048` has it (series, log lines and spans carry
  `environment="local"` and `namespace="andara-compose"`, polled to a deadline, exit `3` without
  credentials), because "verified against a real backend" is Brian's direction and is checked there;
  `stack-recover-mismatch` reads rule state with `gcx alert instances list` (decision 2). `stack-recover` (the M2
  gate, RTO 120 s) and `stack-boundary-lost` keep their compose drivers. The **same gate is also run on the
  cluster** by `env-recover`, which is the release evidence; the compose run is the fast loop and not a
  substitute.
- **CI** runs what it runs today: `make check` and the kind job. The `stack` workflow's drills keep running; a
  job that has the `solo7dev` telemetry and `GRAFANA_SOLO7DEV_READ_TOKEN` secrets also checks Grafana Cloud through
  `gcx`, and one that does not (a fork) skips those assertions and says so (exit `3` locally, a notice in CI).
- **`AW-INF-048` stays**, with these lines changed (the comment on the story says so): its title ("writing
  telemetry to stdout") is wrong against its own body, which ships to `solo7dev`; the removed services are five
  (the collector, Prometheus, Tempo, Loki, Grafana); the label is `environment="local"` plus
  `namespace="andara-compose"`, not `namespace` alone; the stack is `solo7dev`; the endpoint variables are
  decision 9's and the telemetry token is `GRAFANA_SOLO7DEV_TELEMETRY_TOKEN`; the re-pointed targets read through
  `gcx` (decision 2) with `GRAFANA_SOLO7DEV_READ_TOKEN`; rule state comes from `AW-INF-047`'s reads. The
  kind-as-local debate does not create a replacement story.
- **This is Brian's call, and it is cheaper to reverse than it was.** The alternative worth its name is C: if
  the dev-loop pain is the chart and the probes rather than the sim, the change is to run `make cluster-up` and
  `monitoring-install ENV=local` (decision 13) on the laptop, leaving compose for `min`. What remains is the
  four drills against pods (`stack-*` rewritten) and a `local` entry in the namespace table pointing at the
  kind cluster; the cluster and the install already exist. Nothing in decisions 1 to 13 depends on A over C; only
  `AW-INF-048` and the `stack-*` rows above would change.

## Consequences

- **The `ALERTS`-series idiom is gone.** Anything that wants alert history must record it itself
  (decision 2). A rule's firing is no longer a thing one can `rate()`; there is no `ALERTS_FOR_STATE` either.
  That is the cost of Grafana-managed rules, and we accept it for the routing, dashboards and IRM we gain.
- **We depend on a pre-1.0 tool.** `gcx` v0.2.11 can change its output between versions. The single wrapper,
  the recorded fixtures and the version pin (decision 2) turn that into a failing unit test at a bump, not a
  failing drill. If `gcx` is abandoned, the cost is rewriting one wrapper, because no script parses Grafana's
  APIs itself.
- **One `environment` label, one more thing every sender must get right.** `local` has three sources, so
  `namespace` stays in the rules and the drills; an unlabelled series from a new sender is invisible to the
  environment rules and routes to Slack, and `observe-check --keep-list` is what catches it.
- **`solo7dev` mixes `local`, `dev` and `staging`.** A local run can fire a rule in the same Grafana as `dev`'s;
  the `environment = local` route to `blackhole` is the only thing that keeps it quiet, and a mistake in that one
  matcher is a Slack post, not a page.
- **`alerts.yaml` is now rendered per stack** by Terraform, and the render has its own tests. A mistake in the
  renderer is a mistake in what pages. The mitigation is `make tf-test` plus a live drill per environment.
- **State holds secrets** (webhooks; the IRM URL in its own root's state). Read access to state is a secret
  read, and the webhooks are shared with the pull-request plan job, which runs a pull request's Terraform; the
  IRM state is not (decision 3). The prod plan artifact is a state-equivalent secret: it is encrypted, lives a
  day and is deleted after apply, and its key is in the prod environments and on Brian's box alone. The job's defence is the policy
  script (decision 5), an allow-list over blocks, functions and the backend; a provider vulnerability, or a
  construct the allow-list wrongly admits, would pass it. Apply tokens and the IRM token are never reachable
  from it. The residual: a leaked webhook posts to a channel; a false page needs the IRM URL, which only the two
  prod environments and Brian can read.
- **Two stacks, three states, and ~15 credentials** (decision 4) to rotate at least every 90 days, fewer than
  the drafts because `gcx` and Terraform's plan share one read token per stack. `tf-credentials-check` makes
  that a notice and not a surprise, and it is still Brian's chore.
- **Compose has no local rule evaluator.** A rule edit is verified by the expression tests and by loading in
  `solo7dev`, not by a Prometheus on the laptop. A developer with no Grafana Cloud credential gets no
  rule-fire check from `make up`; CI and `dev` do.
- **Terraform is a new tool in `make bootstrap`** and a new licence question (BUSL); OpenTofu is a drop-in at
  this scope and the `required_version` is the only coupling.
- **A drift in the Grafana UI is overwritten at the next apply**, as the ruler path already did, but now it
  also overwrites the *routing*. The runbook must say so in its first line.
- **Foreclosed:** a single shared stack for prod and non-prod; hand-edited contact points as the routing
  source; direct Grafana API calls from our scripts; a cluster or `k8s-monitoring` definition outside this
  repo; and, for now, kind as the *developer's* local environment (the box's cluster is `dev`/`staging`/`prod`).
- **This repo now creates and can destroy the box's cluster.** `kind_platform.sh` today leaves existing
  releases alone because they serve six other projects; if the cluster is still shared, `cluster-rebuild`
  destroys their workloads too. [ASSUMPTION: the cluster is Andara's alone after this move, or its other
  tenants are Brian's to re-install; the follow-up story states which, and `cluster-rebuild`'s confirmation
  names the tenants it will lose.]
- **The telemetry token lives on the box.** `.local/box.env` now holds `solo7`'s telemetry token as well as
  `solo7dev`'s (the cluster repo held it before), so the box is the one place that can ship prod telemetry;
  a lost box means rotating it (`credentials.yaml`).
- **CLAUDE.md §8** needs one wording change, which is Brian's: replace "the integration suite exercising the
  story's own code against the local stack's backends (Redpanda, Tempo)" with "…against Redpanda locally and
  the `solo7dev` Grafana Cloud stack for telemetry, read through `gcx`". `docs/specs/testing/live-assertions.md`
  is architecture's and is amended in the merge of this ADR's follow-up (rule 1's examples name `ALERTS` and a
  local Prometheus).

## Revisit when

- A Grafana-managed rule's firing differs from its PromQL's on a drill (decision 1's equivalence test fails
  once on real data): move the source to HCL (Option B) for that rule.
- `keep_firing_for` or `is_paused` is dropped or changes meaning in the provider: the 15-minute
  `RecoveryStateMismatch` bridge and the cutover both depend on them.
- `gcx` changes a command or output we use twice across version bumps, or reaches 1.0 with a different shape.
- Two different people need to page: the single-person IRM schedule becomes a rota (decision 10 already
  says how).
- A chart, probe or edge bug reaches `dev` that compose hid, **twice**: reopen decision 14 for C. With the
  cluster in this repo that is now a configuration change plus the drills, not a new subsystem.
- The cluster hosts workloads that are not Andara's, or a second cluster appears: decision 13's rebuild and
  token placement assume one cluster, one owner.
- `solo7dev` exceeds the free tier's active-series limit twice in a month, or a `local` run notifies a person
  once: split `local` back into its own stack.
- A third stack (a customer environment): root-per-stack still scales, but the credential count (decision 4)
  does not; reopen it then.
- The state bucket is breached or its webhook secrets leak once: move the contact points' secrets out of
  state (write-only attributes, when the provider supports them) before anything else.
