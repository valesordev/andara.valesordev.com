# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""make tf-workflow-check (AW-INF-068 AC-1, AC-9, AC-11): the real workflow is clean, and each rule fails on a
workflow that breaks it, naming the job and the rule."""
import copy
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
        self.w["jobs"]["plan"]["strategy"]["matrix"]["include"].append(
            {"stack": "solo7-irm", "key": "SOLO7", "webhook": "SLACK_WEBHOOK_PROD"})
        self.broken("plan", "pr-plan")

    # concurrency (AC-11)

    def test_apply_cancelling(self):
        self.w["jobs"]["apply"]["concurrency"]["cancel-in-progress"] = True
        self.broken("apply", "concurrency")

    def test_apply_prod_in_another_group(self):
        self.w["jobs"]["apply-prod"]["concurrency"]["group"] = "elsewhere"
        self.broken("plan-prod", "concurrency")

    def test_drift_prod_irm_cancelling(self):
        self.w["jobs"]["drift-prod-irm"]["concurrency"]["cancel-in-progress"] = True
        self.broken("drift-prod-irm", "concurrency")

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
