# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""`make tf-plan`, `tf-apply`, `tf-unlock` (AW-INF-046): STACK, credentials, box.env and the lock, with Terraform stubbed."""
import contextlib
import io
import json
import os
import re
import sys
import tempfile
import unittest
from unittest import mock

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
sys.path.insert(0, os.path.join(REPO, "scripts"))
import tf  # noqa: E402

DEV = {
    "GRAFANA_SOLO7DEV_READ_TOKEN": "read-token",
    "GRAFANA_SOLO7DEV_APPLY_TOKEN": "apply-token",
    "GRAFANA_SOLO7DEV_FLEET_READ_TOKEN": "fleet-read-token",
    "GRAFANA_SOLO7DEV_FLEET_APPLY_TOKEN": "fleet-apply-token",
    "SLACK_WEBHOOK_DEV": "https://hooks.slack.invalid/dev",
}
# solo7dev's committed fleet_user (stacks/solo7dev/terraform.tfvars).
FLEET_USER = tf.tfvar("solo7dev", "fleet_user")

# Terraform 1.16's message for a held gcs lock, colour codes and box-drawing included.
LOCK_OUTPUT = (
    "\x1b[31m╷\x1b[0m\n"
    "\x1b[31m│\x1b[0m \x1b[1m\x1b[31mError: \x1b[0m\x1b[1mError acquiring the state lock\x1b[0m\n"
    "\x1b[31m│\x1b[0m \n"
    "\x1b[31m│\x1b[0m Error message: writing \"gs://andara-tfstate/grafana/solo7dev/default.tflock\" failed: googleapi: Error 412\n"
    "\x1b[31m│\x1b[0m Lock Info:\n"
    "\x1b[31m│\x1b[0m   ID:        1760412345678901\n"
    "\x1b[31m│\x1b[0m   Path:      gs://andara-tfstate/grafana/solo7dev/default.tflock\n"
    "\x1b[31m│\x1b[0m   Operation: OperationTypeApply\n"
    "\x1b[31m│\x1b[0m   Who:       brian@devbox\n"
    "\x1b[31m│\x1b[0m   Version:   1.16.4\n"
    "\x1b[31m│\x1b[0m   Created:   2026-10-14 10:00:00.0 +0000 UTC\n"
)


class FakeTerraform:
    """Records each terraform call and answers from a script of (match, rc, output)."""

    def __init__(self, answers=()):
        self.calls = []
        self.envs = []
        self.answers = list(answers)

    def __call__(self, args, cwd, env, capture=False):
        self.calls.append(args)
        self.envs.append(env)
        for i, (match, rc, out) in enumerate(self.answers):
            if args[0] == match[0] and all(a in args for a in match[1:]):
                del self.answers[i]
                return rc, out
        return 0, ""


def main(argv, env, fake):
    err = io.StringIO()
    with mock.patch.object(tf, "run", fake), contextlib.redirect_stdout(io.StringIO()) as out, \
            contextlib.redirect_stderr(err):
        rc = tf.main(argv, environ=env)
    return rc, out.getvalue(), err.getvalue()


class NoBoxEnv(unittest.TestCase):
    def setUp(self):
        self.dir = tempfile.TemporaryDirectory()
        for name, value in (("BOX_ENV", "box.env"), ("MAIN_BOX_ENV", "main-box.env"),
                            ("TEST_ALERT_STATE", "tf-test-alert")):
            p = mock.patch.object(tf, name, os.path.join(self.dir.name, value))
            p.start()
            self.addCleanup(p.stop)
        self.addCleanup(self.dir.cleanup)


class StackTest(NoBoxEnv):
    """AC-4: STACK is required, and the dev box plans and applies solo7dev only."""

    def test_unset_or_unknown_exits_2_listing_the_roots(self):
        for stack in ("", "prod", "solo7dev "):
            fake = FakeTerraform()
            rc, _, err = main(["plan", "--stack", stack], dict(DEV), fake)
            self.assertEqual(rc, 2, stack)
            self.assertIn("solo7dev, solo7, solo7-irm", err)
            self.assertEqual(fake.calls, [])

    def test_prod_roots_refused_outside_github_actions(self):
        for stack in ("solo7", "solo7-irm"):
            for cmd in ("plan", "apply", "unlock"):
                fake = FakeTerraform()
                rc, _, err = main([cmd, "--stack", stack, "--lock-id", "1"], {"GITHUB_ACTIONS": "false"}, fake)
                self.assertEqual(rc, 2, (stack, cmd))
                self.assertIn("CI only", err)
                self.assertIn("make tf-plan-show", err)
                self.assertEqual(fake.calls, [], "no terraform call before the refusal")

    def test_prod_roots_allowed_in_github_actions(self):
        env = {"GITHUB_ACTIONS": "true", "GRAFANA_SOLO7_READ_TOKEN": "r", "SLACK_WEBHOOK_PROD": "w"}
        fake = FakeTerraform()
        rc, _, _ = main(["plan", "--stack", "solo7"], env, fake)
        self.assertEqual(rc, 0)
        self.assertEqual(fake.envs[-1]["GRAFANA_AUTH"], "r")
        # solo7-irm uses solo7's token and no webhook.
        fake = FakeTerraform()
        rc, _, _ = main(["plan", "--stack", "solo7-irm"], {"GITHUB_ACTIONS": "true", "GRAFANA_SOLO7_READ_TOKEN": "r"}, fake)
        self.assertEqual(rc, 0)
        self.assertNotIn("TF_VAR_slack_webhook", fake.envs[-1])


class PlanLockTest(NoBoxEnv):
    """A plan takes no lock: the plan accounts hold objectViewer only (ADR-0012 decision 3)."""

    def test_plan_runs_without_the_lock(self):
        fake = FakeTerraform()
        rc, _, _ = main(["plan", "--stack", "solo7dev"], dict(DEV), fake)
        self.assertEqual(rc, 0)
        plans = [c for c in fake.calls if c[0] == "plan"]
        self.assertEqual(len(plans), 1)
        self.assertIn("-lock=false", plans[0])

    def test_apply_still_locks(self):
        fake = FakeTerraform([(["show"], 0, plan_json(["create"]))])
        rc, _, _ = main(["apply", "--stack", "solo7dev"], dict(DEV), fake)
        self.assertEqual(rc, 0)
        self.assertTrue(fake.calls)
        self.assertFalse(any("-lock=false" in c for c in fake.calls))


class CredentialTest(NoBoxEnv):
    """AC-1: a missing credential exits 3 naming it, before any terraform call."""

    def test_plan_needs_read_token_and_webhook(self):
        for drop in ("GRAFANA_SOLO7DEV_READ_TOKEN", "SLACK_WEBHOOK_DEV"):
            env = dict(DEV)
            del env[drop]
            fake = FakeTerraform()
            rc, _, err = main(["plan", "--stack", "solo7dev"], env, fake)
            self.assertEqual(rc, 3)
            self.assertIn(drop, err)
            self.assertEqual(fake.calls, [])

    def test_apply_needs_apply_token(self):
        env = dict(DEV)
        del env["GRAFANA_SOLO7DEV_APPLY_TOKEN"]
        fake = FakeTerraform()
        rc, _, err = main(["apply", "--stack", "solo7dev"], env, fake)
        self.assertEqual(rc, 3)
        self.assertIn("GRAFANA_SOLO7DEV_APPLY_TOKEN", err)
        self.assertNotIn("READ_TOKEN", err)
        self.assertEqual(fake.calls, [])

    def test_fleet_token_exits_3_naming_it(self):
        """AW-INF-051: solo7dev's provider configures Fleet, so plan and apply need its Fleet token."""
        for cmd, drop, other in (("plan", "GRAFANA_SOLO7DEV_FLEET_READ_TOKEN", "FLEET_APPLY"),
                                 ("apply", "GRAFANA_SOLO7DEV_FLEET_APPLY_TOKEN", "FLEET_READ")):
            env = dict(DEV)
            del env[drop]
            fake = FakeTerraform()
            rc, _, err = main([cmd, "--stack", "solo7dev"], env, fake)
            self.assertEqual(rc, 3, cmd)
            self.assertIn(drop, err)
            self.assertNotIn(other, err)
            self.assertEqual(fake.calls, [], "no terraform call before the refusal")

    def test_fleet_auth_is_user_colon_token(self):
        for cmd, token in (("plan", "fleet-read-token"), ("apply", "fleet-apply-token")):
            fake = FakeTerraform([(["show"], 0, plan_json(["create"]))])
            rc, _, _ = main([cmd, "--stack", "solo7dev"], dict(DEV), fake)
            self.assertEqual(rc, 0, cmd)
            self.assertTrue(fake.envs)
            for tenv in fake.envs:
                self.assertEqual(tenv["GRAFANA_FLEET_MANAGEMENT_AUTH"], "%s:%s" % (FLEET_USER, token))

    def test_ambient_fleet_settings_do_not_reach_terraform(self):
        env = dict(DEV, GRAFANA_FLEET_MANAGEMENT_URL="https://elsewhere.invalid",
                   GRAFANA_FLEET_MANAGEMENT_AUTH="1:ambient")
        fake = FakeTerraform()
        rc, _, _ = main(["plan", "--stack", "solo7dev"], env, fake)
        self.assertEqual(rc, 0)
        for tenv in fake.envs:
            self.assertNotIn("GRAFANA_FLEET_MANAGEMENT_URL", tenv)
            self.assertEqual(tenv["GRAFANA_FLEET_MANAGEMENT_AUTH"], FLEET_USER + ":fleet-read-token")

    def test_roots_without_fleet_need_no_fleet_token(self):
        """solo7's Fleet provider is AW-INF-078's, and solo7-irm has none."""
        self.assertTrue(tf.has_fleet("solo7dev"))
        self.assertFalse(tf.has_fleet("solo7"))
        self.assertFalse(tf.has_fleet("solo7-irm"))
        fake = FakeTerraform()
        rc, _, _ = main(["plan", "--stack", "solo7"],
                        {"GITHUB_ACTIONS": "true", "GRAFANA_SOLO7_READ_TOKEN": "r", "SLACK_WEBHOOK_PROD": "w"}, fake)
        self.assertEqual(rc, 0)
        self.assertNotIn("GRAFANA_FLEET_MANAGEMENT_AUTH", fake.envs[-1])

    def test_has_fleet_reads_every_tf_file_and_not_comments(self):
        with tempfile.TemporaryDirectory() as root, mock.patch.object(tf, "root_dir", lambda stack: root):
            with open(os.path.join(root, "main.tf"), "w") as f:
                f.write('provider "grafana" {\n  url = var.grafana_url\n  # fleet_management_url = var.fleet_url\n}\n')
            self.assertFalse(tf.has_fleet("solo7dev"), "a commented line is not a Fleet provider")
            with open(os.path.join(root, "providers.tf"), "w") as f:
                f.write('provider "grafana" {\n  alias                = "fleet"\n  fleet_management_url = var.fleet_url\n}\n')
            self.assertTrue(tf.has_fleet("solo7dev"), "a Fleet provider in a second .tf file")

    def test_token_reaches_terraform_only_as_grafana_auth(self):
        env = dict(DEV, TF_VAR_grafana_auth="leak", TF_CLI_ARGS_plan="-var=x=y", GRAFANA_SOLO7_APPLY_TOKEN="other")
        fake = FakeTerraform()
        rc, _, _ = main(["plan", "--stack", "solo7dev"], env, fake)
        self.assertEqual(rc, 0)
        for tenv in fake.envs:
            self.assertEqual(tenv["GRAFANA_AUTH"], "read-token")
            self.assertEqual(tenv["TF_VAR_slack_webhook"], DEV["SLACK_WEBHOOK_DEV"])
            leaked = [k for k in tenv if k.endswith("_TOKEN") or k.startswith("TF_CLI_ARGS")
                      or (k.startswith("TF_VAR_") and k != "TF_VAR_slack_webhook")]
            self.assertEqual(leaked, [])
            self.assertNotIn("apply-token", tenv.values())
        for args in fake.calls:
            self.assertFalse(any(a.startswith("-var") for a in args), args)


class BoxEnvTest(NoBoxEnv):
    def write(self, text, mode=0o600):
        with open(tf.BOX_ENV, "w") as f:
            f.write(text)
        os.chmod(tf.BOX_ENV, mode)

    def test_loads_and_environment_wins(self):
        self.write("# box\nexport GRAFANA_SOLO7DEV_READ_TOKEN='from-file'\nSLACK_WEBHOOK_DEV=\"https://w\"\nGRAFANA_SOLO7DEV_FLEET_READ_TOKEN=f\nnot a line\n")
        fake = FakeTerraform()
        rc, _, _ = main(["plan", "--stack", "solo7dev"], {"SLACK_WEBHOOK_DEV": "https://env"}, fake)
        self.assertEqual(rc, 0)
        self.assertEqual(fake.envs[-1]["GRAFANA_AUTH"], "from-file")
        self.assertEqual(fake.envs[-1]["TF_VAR_slack_webhook"], "https://env")

    def test_worktree_falls_back_to_the_main_checkout(self):
        with open(tf.MAIN_BOX_ENV, "w") as f:
            f.write("GRAFANA_SOLO7DEV_READ_TOKEN=from-main\nSLACK_WEBHOOK_DEV=https://w\nGRAFANA_SOLO7DEV_FLEET_READ_TOKEN=f\n")
        os.chmod(tf.MAIN_BOX_ENV, 0o600)
        fake = FakeTerraform()
        rc, _, _ = main(["plan", "--stack", "solo7dev"], {}, fake)
        self.assertEqual(rc, 0)
        self.assertEqual(fake.envs[-1]["GRAFANA_AUTH"], "from-main")
        # The worktree's own file wins over the main checkout's.
        self.write("GRAFANA_SOLO7DEV_READ_TOKEN=from-worktree\nSLACK_WEBHOOK_DEV=https://w\nGRAFANA_SOLO7DEV_FLEET_READ_TOKEN=f\n")
        fake = FakeTerraform()
        main(["plan", "--stack", "solo7dev"], {}, fake)
        self.assertEqual(fake.envs[-1]["GRAFANA_AUTH"], "from-worktree")

    def test_andara_box_env_names_the_file(self):
        other = os.path.join(self.dir.name, "elsewhere.env")
        with open(other, "w") as f:
            f.write("GRAFANA_SOLO7DEV_READ_TOKEN=from-other\nSLACK_WEBHOOK_DEV=https://w\nGRAFANA_SOLO7DEV_FLEET_READ_TOKEN=f\n")
        os.chmod(other, 0o600)
        fake = FakeTerraform()
        rc, _, _ = main(["plan", "--stack", "solo7dev"], {"ANDARA_BOX_ENV": other}, fake)
        self.assertEqual(rc, 0)
        self.assertEqual(fake.envs[-1]["GRAFANA_AUTH"], "from-other")

    def test_group_or_world_readable_refused(self):
        for mode in (0o640, 0o604, 0o644):
            self.write("GRAFANA_SOLO7DEV_READ_TOKEN=x\n", mode)
            fake = FakeTerraform()
            rc, _, err = main(["plan", "--stack", "solo7dev"], dict(DEV), fake)
            self.assertEqual(rc, 1, oct(mode))
            self.assertIn("chmod 600", err)
            self.assertEqual(fake.calls, [])


def plan_json(*actions):
    return json.dumps({"resource_changes": [{"change": {"actions": a}} for a in actions]})


class ApplyTest(NoBoxEnv):
    def test_counts(self):
        self.assertEqual(tf.counts(json.loads(plan_json(["create"], ["update"], ["delete", "create"], ["no-op"], ["delete"]))),
                         (2, 1, 2))

    def test_nothing_to_do_prints_zeros_and_does_not_apply(self):
        """AC-5: a second apply prints 0 to add, 0 to change, 0 to destroy."""
        fake = FakeTerraform([(["show"], 0, plan_json(["no-op"]))])
        rc, out, _ = main(["apply", "--stack", "solo7dev"], dict(DEV), fake)
        self.assertEqual(rc, 0)
        self.assertIn("0 to add, 0 to change, 0 to destroy", out)
        self.assertNotIn("apply", [c[0] for c in fake.calls])

    def test_applies_the_saved_plan_then_requires_an_empty_second_plan(self):
        fake = FakeTerraform([(["show"], 0, plan_json(["create"], ["create"]))])
        rc, out, _ = main(["apply", "--stack", "solo7dev"], dict(DEV), fake)
        self.assertEqual(rc, 0)
        self.assertIn("2 to add, 0 to change, 0 to destroy", out)
        plan_out = [a for a in fake.calls[1] if a.startswith("-out=")][0][len("-out="):]
        self.assertEqual([c[0] for c in fake.calls], ["init", "plan", "show", "apply", "plan"])
        self.assertEqual(fake.calls[3][-1], plan_out, "apply applies exactly the saved plan")
        self.assertIn("-detailed-exitcode", fake.calls[4])
        self.assertFalse(os.path.exists(os.path.dirname(plan_out)), "the plan file (it holds the webhook) is removed")

    def test_second_plan_not_empty_fails(self):
        fake = FakeTerraform([(["show"], 0, plan_json(["create"])), (["plan", "-detailed-exitcode"], 2, "")])
        rc, _, err = main(["apply", "--stack", "solo7dev"], dict(DEV), fake)
        self.assertEqual(rc, 1)
        self.assertIn("did not converge", err)

    def test_provider_error_exits_1(self):
        fake = FakeTerraform([(["show"], 0, plan_json(["create"])), (["apply"], 1, "Error: 500 from Grafana")])
        rc, _, err = main(["apply", "--stack", "solo7dev"], dict(DEV), fake)
        self.assertEqual(rc, 1)
        self.assertIn("apply failed", err)


class LockTest(NoBoxEnv):
    """AC-8: a held lock exits 1 at once naming its ID and holder; tf-unlock needs LOCK_ID."""

    def test_lock_holder_parsed(self):
        self.assertEqual(tf.lock_holder(LOCK_OUTPUT), ("1760412345678901", "brian@devbox"))
        self.assertIsNone(tf.lock_holder("Error: something else\n  ID: 1\n"))

    def test_held_lock_on_apply(self):
        fake = FakeTerraform([(["plan"], 1, LOCK_OUTPUT)])
        rc, out, err = main(["apply", "--stack", "solo7dev"], dict(DEV), fake)
        self.assertEqual(rc, 1)
        self.assertIn("tf: lock held: id=1760412345678901 who=brian@devbox", out)
        self.assertIn("LOCK_ID=1760412345678901", err)
        self.assertNotIn("apply", [c[0] for c in fake.calls])
        self.assertIn("-lock-timeout=0s", fake.calls[1], "fails at once rather than waiting")

    def test_unlock_requires_lock_id(self):
        fake = FakeTerraform()
        rc, _, err = main(["unlock", "--stack", "solo7dev"], {}, fake)
        self.assertEqual(rc, 2)
        self.assertIn("LOCK_ID", err)
        self.assertEqual(fake.calls, [])

    def test_unlock_needs_no_grafana_credential(self):
        fake = FakeTerraform()
        rc, _, _ = main(["unlock", "--stack", "solo7dev", "--lock-id", "17"], {"GRAFANA_AUTH": "x"}, fake)
        self.assertEqual(rc, 0)
        self.assertEqual(fake.calls[-1], ["force-unlock", "-force", "17"])
        self.assertNotIn("GRAFANA_AUTH", fake.envs[-1])


class TestAlertTest(NoBoxEnv):
    """AC-7's method: one always-firing alert per environment, created and removed with the apply token."""

    def test_env_must_be_one_the_stack_routes(self):
        for env_name in ("", "prod", "ci"):
            fake = FakeTerraform()
            rc, _, err = main(["test-alert", "--stack", "solo7dev", "--env", env_name], dict(DEV), fake)
            self.assertEqual(rc, 2, env_name)
            self.assertIn("local, dev, staging", err)
            self.assertEqual(fake.calls, [])

    def test_needs_apply_token(self):
        fake = FakeTerraform()
        rc, _, err = main(["test-alert", "--stack", "solo7dev", "--env", "dev"], {}, fake)
        self.assertEqual(rc, 3)
        self.assertIn("GRAFANA_SOLO7DEV_APPLY_TOKEN", err)
        self.assertEqual(fake.calls, [])

    def test_prod_refused_outside_github_actions(self):
        fake = FakeTerraform()
        rc, _, _ = main(["test-alert", "--stack", "solo7", "--env", "prod"], {}, fake)
        self.assertEqual(rc, 2)
        self.assertEqual(fake.calls, [])

    def test_create_and_clean(self):
        for cmd, verb in (("test-alert", "apply"), ("test-alert-clean", "destroy")):
            fake = FakeTerraform()
            rc, _, _ = main([cmd, "--stack", "solo7dev", "--env", "local"], dict(DEV), fake)
            self.assertEqual(rc, 0)
            init, act = fake.calls
            self.assertIn("-backend-config=path=" + os.path.join(tf.TEST_ALERT_STATE, "solo7dev-local.tfstate"), init)
            self.assertEqual(act[0], verb)
            self.assertIn("grafana_url=https://solo7dev.grafana.net", act)
            self.assertIn("environment=local", act)
            self.assertEqual(fake.envs[-1]["GRAFANA_AUTH"], "apply-token")
            self.assertNotIn("TF_VAR_slack_webhook", fake.envs[-1])


class SharedFilesTest(unittest.TestCase):
    def test_committed_copies_are_identical(self):
        tf.check_shared()

    def test_a_drifted_copy_fails(self):
        real_open = open

        def fake_open(path, *a, **kw):
            if path == os.path.join(tf.root_dir("solo7-irm"), "endpoints.tf"):
                return io.StringIO("drifted")
            return real_open(path, *a, **kw)

        with mock.patch("builtins.open", fake_open), contextlib.redirect_stdout(io.StringIO()):
            with self.assertRaises(tf.Exit) as e:
                tf.check_shared()
        self.assertIn("endpoints.tf", e.exception.msg)
        self.assertIn("solo7-irm", e.exception.msg)


class DashboardBreaksTest(NoBoxEnv):
    """AW-INF-049 AC-2: a break the preconditions don't refuse fails tf-test; one they refuse passes."""

    def breaks(self, fake):
        with mock.patch.object(tf, "run", fake), contextlib.redirect_stdout(io.StringIO()):
            return tf.dashboard_breaks({})

    def test_a_break_terraform_accepts_fails(self):
        with self.assertRaises(tf.Exit) as e:
            self.breaks(FakeTerraform())
        self.assertEqual(e.exception.code, 1)
        self.assertIn("no ds variable: tf-test passed", e.exception.msg)

    def test_a_failure_for_another_reason_fails(self):
        fake = FakeTerraform([(["test"], 1, "Error: Unsupported argument\n")])
        with self.assertRaises(tf.Exit) as e:
            self.breaks(fake)
        self.assertIn("failed for another reason", e.exception.msg)

    def test_every_break_refused_with_its_message(self):
        # Terraform wraps the message behind its box drawing; the match reads through it.
        answers = [(["test"], 1, "\x1b[31m│\x1b[0m Error: %s\n\x1b[31m│\x1b[0m %s\n" % tuple(m.split(" ", 1)))
                   for _, _, m in tf.DASHBOARD_BREAKS]
        fake = FakeTerraform(answers)
        self.assertEqual(self.breaks(fake), 0)
        self.assertEqual([c[0] for c in fake.calls],
                         ["init"] + ["test"] * (len(tf.DASHBOARD_BREAKS) + len(tf.DASHBOARD_ACCEPTS)))

    def test_an_accept_terraform_refuses_fails(self):
        answers = [(["test"], 1, "Error: %s\n" % m) for _, _, m in tf.DASHBOARD_BREAKS]
        answers.append((["test"], 1, "Error: read a series without\n"))
        with self.assertRaises(tf.Exit) as e:
            self.breaks(FakeTerraform(answers))
        self.assertIn("with an offset: tf-test failed, want a pass", e.exception.msg)

    def test_the_breaks_cover_the_reviewed_bypasses(self):
        """A label matched as a substring of another, and a bare name without an underscore (pre-PR review)."""
        names = [w for w, _, _ in tf.DASHBOARD_BREAKS]
        self.assertIn("a label that only ends in environment", names)
        self.assertIn("a bare metric name without an underscore", names)

    def test_each_break_changes_the_committed_source(self):
        with open(tf.DASHBOARD) as f:
            source = f.read()
        for what, breaker in [(w, b) for w, b, _ in tf.DASHBOARD_BREAKS] + list(tf.DASHBOARD_ACCEPTS):
            d = json.loads(source)
            breaker(d)
            self.assertNotEqual(d, json.loads(source), what)


def planned_from_file(doc, required, render=tf.render_expr):
    """The rule groups a correct module plans for required, built from the file here."""
    def rule(r):
        return {"name": r["alert"], "condition": "C", "for": r.get("for", "0s"),
                "keep_firing_for": r.get("keep_firing_for"), "no_data_state": "OK", "exec_err_state": "Error",
                "is_paused": True, "labels": {k: str(v) for k, v in r.get("labels", {}).items()},
                "annotations": dict(r.get("annotations", {})),
                "data": [
                    {"ref_id": "A", "datasource_uid": "grafanacloud-prom",
                     "model": json.dumps({"refId": "A", "expr": render(r["expr"], required), "instant": True})},
                    {"ref_id": "B", "datasource_uid": "__expr__",
                     "model": json.dumps({"refId": "B", "type": "math", "expression": "$A * 0 + 1"})},
                    {"ref_id": "C", "datasource_uid": "__expr__",
                     "model": json.dumps({"refId": "C", "type": "threshold", "expression": "B",
                                          "conditions": [{"evaluator": {"type": "gt", "params": [0]}}]})}]}
    return {g["name"]: {"name": g["name"], "interval_seconds": 60, "rule": [rule(r) for r in g["rules"]]}
            for g in doc["groups"]}


class RulesRenderTest(unittest.TestCase):
    """AW-INF-047 AC-2, AC-3: the comparison fails on every way a module could render a rule wrongly."""

    def setUp(self):
        import yaml
        with open(tf.ALERTS) as f:
            self.doc = yaml.safe_load(f)

    def a_expr(self, groups, alert):
        r = next(r for g in groups.values() for r in g["rule"] if r["name"] == alert)
        return next(d for d in r["data"] if d["ref_id"] == "A")

    def set_expr(self, groups, alert, expr):
        d = self.a_expr(groups, alert)
        m = json.loads(d["model"])
        m["expr"] = expr
        d["model"] = json.dumps(m)

    def test_the_file_marks_dev_and_prod(self):
        self.assertEqual(tf.marked_environments(self.doc), ["dev", "prod"])

    def test_render_drops_only_the_lines_marked_outside_required(self):
        expr = "a\n  or b  # env:dev\n  or c  # env:prod\n"
        self.assertEqual(tf.render_expr(expr, []), "a\n")
        self.assertEqual(tf.render_expr(expr, ["dev"]), "a\n  or b  # env:dev\n")
        self.assertEqual(tf.render_expr(expr, ["dev", "prod"]), expr)

    def test_a_correct_plan_passes_for_every_required_set(self):
        for required in ([], ["dev"], ["prod"], ["dev", "prod"]):
            self.assertEqual(tf.rule_problems(planned_from_file(self.doc, required), required, self.doc), [], required)

    def test_a_dev_line_kept_when_dev_is_not_required_fails(self):
        groups = planned_from_file(self.doc, ["dev"])
        problems = tf.rule_problems(groups, [], self.doc)
        self.assertTrue(any("AndaraServerUnavailable: rendered expr differs" in p for p in problems), problems)
        self.assertTrue(any("names environment(s) dev outside" in p for p in problems), problems)

    def test_a_regex_matcher_naming_an_unrequired_environment_fails(self):
        groups = planned_from_file(self.doc, [])
        self.set_expr(groups, "SimulationLagging", 'andara_simulation_lag_seconds{environment=~"dev"} > 0.5')
        problems = tf.rule_problems(groups, [], self.doc)
        self.assertTrue(any("SimulationLagging: names environment(s) dev outside" in p for p in problems), problems)

    def test_a_line_dropped_that_is_required_fails(self):
        groups = planned_from_file(self.doc, [])
        problems = tf.rule_problems(groups, ["dev"], self.doc)
        self.assertTrue(any("AndaraServerUnavailable: rendered expr differs" in p for p in problems), problems)

    def test_a_one_byte_difference_fails(self):
        groups = planned_from_file(self.doc, [])
        expr = json.loads(self.a_expr(groups, "SimulationLagging")["model"])["expr"]
        self.set_expr(groups, "SimulationLagging", expr.replace("0.5", "0.6"))
        problems = tf.rule_problems(groups, [], self.doc)
        self.assertTrue(any("SimulationLagging: rendered expr differs" in p for p in problems), problems)

    def test_with_every_environment_required_the_expr_is_the_files(self):
        everything = ["dev", "prod"]
        # A renderer that drops a marker comment still renders the line, but isn't the file's text.
        def strip(expr, required):
            return re.sub(r"\s*# env:\w+", "", expr)
        problems = tf.rule_problems(planned_from_file(self.doc, everything, render=strip), everything, self.doc)
        self.assertTrue(any("byte for byte" in p for p in problems), problems)

    def test_a_missing_group_a_renamed_rule_and_a_wrong_duration_each_fail(self):
        groups = planned_from_file(self.doc, [])
        del groups["andara-edge"]
        groups["andara-tick"]["rule"][0]["name"] = "SimulationLaggin"
        groups["andara-recovery"]["rule"][0]["keep_firing_for"] = "5m"
        groups["andara-server"]["rule"][0]["for"] = "3m"
        problems = "\n".join(tf.rule_problems(groups, [], self.doc))
        self.assertIn("groups ", problems)
        self.assertIn("andara-tick: rules ['SimulationLaggin']", problems)
        self.assertIn("RecoveryStateMismatch: keep_firing_for '5m', want '15m'", problems)
        self.assertIn("AndaraServerUnavailable: for '3m', want '2m'", problems)

    def test_an_unpaused_rule_or_a_changed_pipeline_fails(self):
        groups = planned_from_file(self.doc, [])
        r = groups["andara-tick"]["rule"][0]
        r["is_paused"] = False
        r["data"][1]["model"] = json.dumps({"refId": "B", "type": "math", "expression": "$A"})
        r["no_data_state"] = "NoData"
        problems = "\n".join(tf.rule_problems(groups, [], self.doc))
        self.assertIn("is_paused False, want True", problems)
        self.assertIn("B is not Math", problems)
        self.assertIn("no_data_state 'NoData', want 'OK'", problems)

    def test_labels_and_annotations_are_compared(self):
        groups = planned_from_file(self.doc, [])
        groups["andara-tick"]["rule"][0]["labels"]["severity"] = "ticket"
        groups["andara-tick"]["rule"][0]["annotations"]["runbook"] = "x"
        problems = "\n".join(tf.rule_problems(groups, [], self.doc))
        self.assertIn("SimulationLagging: labels", problems)
        self.assertIn("SimulationLagging: annotations", problems)

    def test_composite_durations_read_back(self):
        self.assertEqual(tf.seconds("1h30m"), 5400)
        self.assertEqual(tf.seconds("0s"), 0)
        self.assertIsNone(tf.seconds(None))
        with self.assertRaises(ValueError):
            tf.seconds("2 m")

    @unittest.skipUnless(os.access(tf.PROMTOOL, os.X_OK), "bin/promtool absent; run `make bootstrap`")
    def test_promtool_parses_the_rendered_rules_and_refuses_a_broken_one(self):
        for required in ([], ["dev"], ["prod"], ["dev", "prod"]):
            self.assertEqual(tf.promtool_problems(planned_from_file(self.doc, required)), [], required)
        groups = planned_from_file(self.doc, [])
        self.set_expr(groups, "SimulationLagging", "max by (")
        self.assertTrue(tf.promtool_problems(groups))

    def test_the_test_plan_output_is_read_per_run(self):
        groups = planned_from_file(self.doc, [])
        lines = [json.dumps({"type": "test_run", "test_run": {"run": "r1", "progress": "complete", "status": "pass"}}),
                 json.dumps({"type": "test_plan", "@testrun": "r1", "test_plan": {"resource_changes": [
                     {"address": '%s["%s"]' % (tf.GROUP_ADDRESS, n), "change": {"after": g}} for n, g in groups.items()
                 ] + [{"address": "module.stack.grafana_folder.andara", "change": {"after": {"name": "x"}}}]}}),
                 "not json"]
        plans, status = tf.planned_rule_groups("\n".join(lines))
        self.assertEqual(status, {"r1": "pass"})
        self.assertEqual(sorted(plans["r1"]), sorted(groups))


if __name__ == "__main__":
    unittest.main()
