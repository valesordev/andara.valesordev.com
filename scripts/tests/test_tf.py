# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""`make tf-plan`, `tf-apply`, `tf-unlock` (AW-INF-046): STACK, credentials, box.env and the lock, with Terraform stubbed."""
import contextlib
import io
import json
import os
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
    "SLACK_WEBHOOK_DEV": "https://hooks.slack.invalid/dev",
}

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
        self.write("# box\nexport GRAFANA_SOLO7DEV_READ_TOKEN='from-file'\nSLACK_WEBHOOK_DEV=\"https://w\"\nnot a line\n")
        fake = FakeTerraform()
        rc, _, _ = main(["plan", "--stack", "solo7dev"], {"SLACK_WEBHOOK_DEV": "https://env"}, fake)
        self.assertEqual(rc, 0)
        self.assertEqual(fake.envs[-1]["GRAFANA_AUTH"], "from-file")
        self.assertEqual(fake.envs[-1]["TF_VAR_slack_webhook"], "https://env")

    def test_worktree_falls_back_to_the_main_checkout(self):
        with open(tf.MAIN_BOX_ENV, "w") as f:
            f.write("GRAFANA_SOLO7DEV_READ_TOKEN=from-main\nSLACK_WEBHOOK_DEV=https://w\n")
        os.chmod(tf.MAIN_BOX_ENV, 0o600)
        fake = FakeTerraform()
        rc, _, _ = main(["plan", "--stack", "solo7dev"], {}, fake)
        self.assertEqual(rc, 0)
        self.assertEqual(fake.envs[-1]["GRAFANA_AUTH"], "from-main")
        # The worktree's own file wins over the main checkout's.
        self.write("GRAFANA_SOLO7DEV_READ_TOKEN=from-worktree\nSLACK_WEBHOOK_DEV=https://w\n")
        fake = FakeTerraform()
        main(["plan", "--stack", "solo7dev"], {}, fake)
        self.assertEqual(fake.envs[-1]["GRAFANA_AUTH"], "from-worktree")

    def test_andara_box_env_names_the_file(self):
        other = os.path.join(self.dir.name, "elsewhere.env")
        with open(other, "w") as f:
            f.write("GRAFANA_SOLO7DEV_READ_TOKEN=from-other\nSLACK_WEBHOOK_DEV=https://w\n")
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


if __name__ == "__main__":
    unittest.main()
