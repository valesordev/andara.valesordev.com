# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""make tf-workflow-check (AW-INF-068 AC-1, AC-9, AC-11): the real workflow is clean, and each rule fails on a
workflow that breaks it, naming the job and the rule."""
import copy
import json
import os
import sys
import unittest

import yaml

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
sys.path.insert(0, os.path.join(REPO, "scripts"))
import tf_workflow_check as wc  # noqa: E402


def load():
    with open(wc.WORKFLOW) as f:
        return yaml.safe_load(f)


def step(job, name):
    return next(s for s in job["steps"] if s.get("name", "").startswith(name) or s.get("id") == name)


def set_legs(job, on, off):
    """Write a job's matrix in the gated form, with these legs on and off."""
    job["strategy"]["matrix"]["include"] = "${{ fromJSON(%s && '%s' || '%s') }}" % (
        wc.GATE, json.dumps(on, separators=(",", ":")), json.dumps(off, separators=(",", ":")))


class Workflow(unittest.TestCase):

    def setUp(self):
        self.w = load()

    def broken(self, job, rule):
        problems = wc.check(self.w)
        hits = [p for p in problems if p[0] == job and p[1] == rule]
        self.assertTrue(hits, "expected %s/%s; got %s" % (job, rule, problems))

    def test_the_real_workflow_is_clean(self):
        self.assertEqual(wc.check(self.w), [])

    def test_the_jobs_the_contract_names(self):
        self.assertEqual(set(self.w["jobs"]), {"plan", "apply", "plan-prod", "apply-prod", "drift", "drift-prod-irm"})
        self.assertEqual(self.w["jobs"]["apply"]["environment"], "andara-main")
        self.assertEqual(self.w["jobs"]["plan-prod"]["environment"], "andara-prod-plan")
        self.assertEqual(self.w["jobs"]["plan-prod"]["needs"], "apply")
        self.assertEqual(self.w["jobs"]["apply-prod"]["environment"], "andara-prod-apply")
        self.assertEqual(self.w["jobs"]["drift-prod-irm"]["environment"], "andara-prod-plan")

    # token-as-variable

    def test_a_token_as_tf_var(self):
        step(self.w["jobs"]["apply"], "apply solo7dev")["env"]["TF_VAR_grafana_auth"] = \
            "${{ secrets.GRAFANA_SOLO7DEV_APPLY_TOKEN }}"
        self.broken("apply", "token-as-variable")

    def test_a_token_as_var_flag(self):
        step(self.w["jobs"]["apply"], "apply solo7dev")["run"] += ' -var "auth=$GRAFANA_SOLO7DEV_APPLY_TOKEN"'
        self.broken("apply", "token-as-variable")

    # prod-only

    def test_the_plan_key_in_a_pull_request_job(self):
        step(self.w["jobs"]["plan"], "plan")["env"]["TF_PLAN_KEY_PROD"] = "${{ secrets.TF_PLAN_KEY_PROD }}"
        self.broken("plan", "prod-only")

    def test_the_irm_token_in_the_scheduled_drift_job(self):
        step(self.w["jobs"]["drift"], "drift")["env"]["GRAFANA_SOLO7_IRM_TOKEN"] = "${{ secrets.GRAFANA_SOLO7_IRM_TOKEN }}"
        self.broken("drift", "prod-only")

    def test_the_prod_plan_account_outside_prod(self):
        auth = next(s for s in self.w["jobs"]["drift"]["steps"] if "auth" in str(s.get("uses", "")))
        auth["with"]["service_account"] = "${{ vars.TFSTATE_PRODPLAN_SA }}"
        self.broken("drift", "prod-only")

    def test_an_irm_apply_account_in_apply(self):
        auth = next(s for s in self.w["jobs"]["apply"]["steps"] if "auth" in str(s.get("uses", "")))
        auth["with"]["service_account"] = "${{ vars.TFSTATE_SOLO7_IRM_APPLY_SA }}"
        self.broken("apply", "prod-only")

    # apply-only

    def test_an_apply_token_in_plan_prod(self):
        step(self.w["jobs"]["plan-prod"], "plan solo7-irm")["env"]["GRAFANA_SOLO7_APPLY_TOKEN"] = \
            "${{ secrets.GRAFANA_SOLO7_APPLY_TOKEN }}"
        self.broken("plan-prod", "apply-only")

    # sinks

    def test_terraform_called_directly(self):
        self.w["jobs"]["plan-prod"]["steps"].append({"name": "show", "run": "terraform show plan"})
        self.broken("plan-prod", "sinks")

    def test_show_json_to_the_summary(self):
        self.w["jobs"]["apply"]["steps"].append(
            {"name": "leak", "run": 'python3 scripts/x.py show -json >> "$GITHUB_STEP_SUMMARY"'})
        self.broken("apply", "sinks")

    def test_a_decrypted_plan_with_openssl(self):
        self.w["jobs"]["apply-prod"]["steps"].append(
            {"name": "decrypt", "run": "openssl enc -d -in p.enc > plan"})
        self.broken("apply-prod", "sinks")

    def test_a_secret_in_a_script(self):
        self.w["jobs"]["apply"]["steps"].append(
            {"name": "echo", "run": 'echo "${{ secrets.SLACK_WEBHOOK_DEV }}"'})
        self.broken("apply", "sinks")

    def test_an_unencrypted_artifact(self):
        up = next(s for s in self.w["jobs"]["plan-prod"]["steps"] if "upload-artifact" in str(s.get("uses", "")))
        up["with"]["path"] = "${{ runner.temp }}/prod/solo7.tfplan"
        self.broken("plan-prod", "sinks")

    def test_the_whole_secrets_context(self):
        step(self.w["jobs"]["plan"], "plan")["env"]["ALL"] = "${{ toJSON(secrets) }}"
        self.broken("plan", "secrets-context")

    def test_a_prod_secret_in_workflow_env(self):
        self.w["env"] = {"TF_PLAN_KEY_PROD": "${{ secrets.TF_PLAN_KEY_PROD }}"}
        self.broken("<workflow>", "prod-only")

    def test_a_token_as_tf_var_in_job_env(self):
        self.w["jobs"]["apply"]["env"] = {"TF_VAR_auth": "${{ secrets.GRAFANA_SOLO7DEV_APPLY_TOKEN }}"}
        self.broken("apply", "token-as-variable")

    def test_the_plan_job_checking_out_the_head(self):
        checkout = next(s for s in self.w["jobs"]["plan"]["steps"] if "checkout" in str(s.get("uses", "")))
        checkout["with"]["ref"] = "${{ github.event.pull_request.head.sha }}"
        self.broken("plan", "pr-plan")

    def test_a_failure_report_that_needs_the_checkout(self):
        """A step that reports a failure may be reporting a failed checkout: it can't run repository files."""
        fallback = step(self.w["jobs"]["drift"], "a failed drift job")
        fallback["run"] = 'python3 scripts/tf_ci.py drift-issue --stack x --reason y'
        self.broken("drift", "failure-report")

    def test_a_fallback_that_trusts_the_step_outcome(self):
        """A drift step can fail without reporting (an exception that isn't tf.Exit); only its own
        `reported` output may stop the fallback."""
        for job in ("drift", "drift-prod-irm"):
            with self.subTest(job=job):
                self.w = load()
                step(self.w["jobs"][job], "a failed drift job")["if"] = "failure() && steps.drift.outcome != 'failure'"
                self.broken(job, "failure-report")

    def test_drift_reachable_by_dispatch(self):
        self.w["jobs"]["drift"]["if"] = "github.event_name == 'schedule' || github.event_name == 'workflow_dispatch'"
        self.broken("drift", "drift-auth")

    # pr-plan (AC-1)

    def test_the_plan_job_with_an_apply_token(self):
        step(self.w["jobs"]["plan"], "plan")["env"]["X"] = "${{ secrets.GRAFANA_SOLO7_APPLY_TOKEN }}"
        self.broken("plan", "pr-plan")

    def test_the_plan_job_with_the_other_stacks_token(self):
        step(self.w["jobs"]["plan"], "plan")["env"]["X"] = "${{ secrets.GRAFANA_SOLO7_READ_TOKEN }}"
        self.broken("plan", "pr-plan")

    def test_the_plan_job_with_an_environment(self):
        self.w["jobs"]["plan"]["environment"] = "andara-main"
        self.broken("plan", "pr-plan")

    def test_the_plan_job_without_the_fork_guard(self):
        self.w["jobs"]["plan"]["if"] = "github.event_name == 'pull_request_target'"
        self.broken("plan", "pr-plan")

    def test_the_plan_job_planning_the_irm_root(self):
        irm = {"stack": "solo7-irm", "key": "SOLO7", "webhook": "SLACK_WEBHOOK_PROD"}
        set_legs(self.w["jobs"]["plan"], wc.PR_MATRIX + [irm], wc.DEV_MATRIX)
        self.broken("plan", "pr-plan")

    # prod-gate (the 2026-10-19 scope change: no prod environment yet)

    def test_the_real_matrices_hold_solo7_only_behind_the_gate(self):
        for job in ("plan", "drift"):
            with self.subTest(job=job):
                self.assertEqual(wc.matrix_legs(self.w["jobs"][job]), (wc.PR_MATRIX, wc.DEV_MATRIX))

    def test_a_prod_job_without_the_gate(self):
        ungated = {"plan-prod": "github.event_name == 'push'",
                   "apply-prod": "needs.plan-prod.outputs.changes == 'true'",
                   "drift-prod-irm": "github.event_name == 'schedule'"}
        for job, cond in ungated.items():
            with self.subTest(job=job):
                self.w = load()
                self.w["jobs"][job]["if"] = cond
                self.broken(job, "prod-gate")

    def test_a_gate_that_precedence_can_bypass(self):
        """`gate && a || b` runs on b alone; `gate && (a) || (b)` likewise. Only `gate && (<one group>)` holds."""
        for cond in ("vars.TF_PROD_ENABLED == 'true' || github.event_name == 'push'",
                     "vars.TF_PROD_ENABLED == 'true' && github.event_name == 'push' || github.event_name == 'workflow_dispatch'",
                     "vars.TF_PROD_ENABLED == 'true' && (github.event_name == 'push') || (github.event_name == 'workflow_dispatch')",
                     "vars.TF_PROD_ENABLED != 'true' && (github.event_name == 'push')",
                     "vars.TF_PROD_ENABLED && (github.event_name == 'push')",
                     # parens inside string literals must not balance the group
                     "vars.TF_PROD_ENABLED == 'true' && (github.ref == '(' ) || always() || ( github.ref == ')' )",
                     "vars.TF_PROD_ENABLED == 'true' && (github.ref == 'it''s (' ) || always() || ( github.ref == ')' )"):
            with self.subTest(cond=cond):
                self.w = load()
                self.w["jobs"]["plan-prod"]["if"] = cond
                self.broken("plan-prod", "prod-gate")

    def test_a_job_moved_into_a_prod_environment_without_the_gate(self):
        self.w["jobs"]["drift"]["environment"] = "andara-prod-plan"
        self.broken("drift", "prod-gate")

    def test_a_literal_matrix_with_solo7(self):
        """The pre-gate shape: a solo7 leg that exists whatever the variable says."""
        for job in ("plan", "drift"):
            with self.subTest(job=job):
                self.w = load()
                self.w["jobs"][job]["strategy"]["matrix"]["include"] = copy.deepcopy(wc.PR_MATRIX)
                self.broken(job, "prod-gate")

    def test_solo7_in_the_off_legs(self):
        for job in ("plan", "drift"):
            with self.subTest(job=job):
                self.w = load()
                set_legs(self.w["jobs"][job], wc.PR_MATRIX, wc.PR_MATRIX)
                self.broken(job, "prod-gate")

    def test_the_legs_swapped(self):
        """solo7 present when the gate is off and absent when it is on."""
        set_legs(self.w["jobs"]["drift"], wc.DEV_MATRIX, wc.PR_MATRIX)
        self.broken("drift", "prod-gate")

    def test_a_matrix_built_some_other_way(self):
        self.w["jobs"]["drift"]["strategy"]["matrix"]["include"] = "${{ fromJSON(vars.TF_MATRIX) }}"
        self.broken("drift", "prod-gate")

    def test_drift_losing_its_dev_leg(self):
        set_legs(self.w["jobs"]["drift"], wc.PR_MATRIX[1:], [])
        self.broken("drift", "prod-gate")

    def test_a_gated_condition_with_parens_in_a_literal(self):
        """A literal '(' or ')' inside the gated group is just text."""
        self.w["jobs"]["plan-prod"]["if"] = "vars.TF_PROD_ENABLED == 'true' && (github.ref != 'refs/heads/x)')"
        self.assertEqual(wc.check(self.w), [])

    def test_a_matrix_axis_beside_the_gated_include(self):
        """A `stack: [solo7]` axis makes its own leg whatever include says."""
        for job in ("plan", "drift"):
            with self.subTest(job=job):
                self.w = load()
                self.w["jobs"][job]["strategy"]["matrix"]["stack"] = ["solo7"]
                self.broken(job, "prod-gate")

    def test_a_whole_matrix_expression(self):
        self.w["jobs"]["drift"]["strategy"]["matrix"] = "${{ fromJSON(vars.TF_MATRIX) }}"
        self.broken("drift", "prod-gate")

    def test_another_matrix_job_built_some_other_way(self):
        job = copy.deepcopy(self.w["jobs"]["drift"])
        job["strategy"]["matrix"]["include"] = "${{ fromJSON(vars.TF_MATRIX) }}"
        self.w["jobs"]["drift-copy"] = job
        self.broken("drift-copy", "prod-gate")

    def test_drift_prod_irm_is_read_behind_its_gate(self):
        """The gated schedule condition passes drift-auth; read raw, it would not."""
        self.w["jobs"]["drift-prod-irm"]["if"] = "vars.TF_PROD_ENABLED == 'true' && (github.event_name == 'schedule')"
        self.assertFalse([p for p in wc.check(self.w) if p[1] == "drift-auth"])

    def test_drift_prod_irm_reachable_by_dispatch_behind_the_gate(self):
        self.w["jobs"]["drift-prod-irm"]["if"] = (
            "vars.TF_PROD_ENABLED == 'true' && (github.event_name == 'schedule' || github.event_name == 'workflow_dispatch')")
        self.broken("drift-prod-irm", "drift-auth")

    # concurrency (AC-11)

    def test_apply_cancelling(self):
        self.w["jobs"]["apply"]["concurrency"]["cancel-in-progress"] = True
        self.broken("apply", "concurrency")

    def test_apply_prod_in_another_group(self):
        self.w["jobs"]["apply-prod"]["concurrency"]["group"] = "elsewhere"
        self.broken("plan-prod", "concurrency")

    def test_drift_in_an_apply_group(self):
        """In an apply's group, a drift leg could replace a pending apply; tf_ci.py checks for an overlapping apply instead."""
        self.w["jobs"]["drift"]["concurrency"] = {
            "group": "${{ matrix.stack == 'solo7' && 'terraform-prod' || 'terraform-apply' }}", "cancel-in-progress": False}
        self.broken("drift", "concurrency")

    def test_drift_without_actions_read(self):
        """The overlap check lists the workflow's runs, which needs actions: read, in both drift jobs."""
        for job in ("drift", "drift-prod-irm"):
            with self.subTest(job=job):
                self.w = load()
                self.w["jobs"][job]["permissions"].pop("actions", None)
                self.broken(job, "concurrency")

    def test_drift_prod_irm_cancelling(self):
        self.w["jobs"]["drift-prod-irm"]["concurrency"]["cancel-in-progress"] = True
        self.broken("drift-prod-irm", "concurrency")

    def test_every_credential_the_workflow_reads_is_in_tf_ci_environments_table(self):
        """make tf-ci-environments checks the names in tf_ci.py; they must be the ones the jobs read."""
        import tf_ci
        on, _ = wc.matrix_legs(self.w["jobs"]["plan"])
        keys = [m["key"] for m in on]
        hooks = [m["webhook"] for m in on]
        for name, job in self.w["jobs"].items():
            env = wc.environment(job)
            allowed = set(tf_ci.REPO_SECRETS) | set(tf_ci.ENVIRONMENTS.get(env, {}).get("secrets", ()))
            for kind, ref in wc.refs(job):
                if ref.startswith("format("):
                    names = [ref.split("'")[1].replace("{0}", k) for k in keys]
                elif ref == "matrix.webhook":
                    names = hooks
                else:
                    names = [ref]
                for n in names:
                    with self.subTest(job=name, ref=n):
                        if kind == "vars":
                            self.assertIn(n, tf_ci.REPO_VARS + (tf_ci.GATE_VAR,))
                        else:
                            self.assertIn(n, allowed, "%s reads %s, which tf-ci-environments does not "
                                                      "expect in %s" % (name, n, env or "the repository"))

    def test_main_reports_job_and_rule(self):
        w = copy.deepcopy(self.w)
        w["jobs"]["plan"]["environment"] = "andara-main"
        path = os.path.join(os.environ.get("TMPDIR", "/tmp"), "tf-workflow-check-%d.yaml" % os.getpid())
        with open(path, "w") as f:
            yaml.safe_dump(w, f)
        try:
            self.assertEqual(wc.main([path]), 1)
            self.assertEqual(wc.main([wc.WORKFLOW]), 0)
            self.assertEqual(wc.main(["/nonexistent.yaml"]), 2)
        finally:
            os.remove(path)


if __name__ == "__main__":
    unittest.main()
