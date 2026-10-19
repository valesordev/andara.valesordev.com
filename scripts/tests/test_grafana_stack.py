# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""scripts/grafana_stack.py (AW-INF-075): the ENV → stack mapping (AC-5) and the missing read token (AC-4)."""
import os
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
sys.path.insert(0, os.path.join(REPO, "scripts"))
import gcx  # noqa: E402
import grafana_stack  # noqa: E402
import observe_check  # noqa: E402

NO_BOX = {"ANDARA_BOX_ENV": os.path.join(tempfile.gettempdir(), "andara-no-box.env")}


class Mapping(unittest.TestCase):

    def test_env_to_stack(self):
        self.assertEqual([grafana_stack.stack(e) for e in ("local", "dev", "staging", "prod")],
                         ["solo7dev", "solo7dev", "solo7dev", "solo7"])

    def test_selectors(self):
        self.assertEqual(grafana_stack.selectors("local"), {"environment": "local", "namespace": "andara-local"})
        self.assertEqual(grafana_stack.selectors("dev"), {"environment": "dev"})
        self.assertEqual(grafana_stack.selectors("staging"), {"environment": "staging"})
        self.assertEqual(grafana_stack.selectors("prod"), {"environment": "prod"})

    def test_other_values_exit_2(self):
        for env in ("", "ci", "Dev", "solo7", "unknown"):
            with self.subTest(env=env), self.assertRaises(grafana_stack.Exit) as e:
                grafana_stack.stack(env)
            self.assertEqual(e.exception.code, 2)

    def test_cli(self):
        run = lambda *a: subprocess.run([sys.executable, os.path.join(REPO, "scripts", "grafana_stack.py"), *a],  # noqa: E731
                                        capture_output=True, text=True)
        self.assertEqual(run("prod").stdout.strip(), "solo7")
        self.assertEqual(run("local").stdout.strip(), "solo7dev")
        self.assertEqual(run("nope").returncode, 2)
        self.assertEqual(run().returncode, 2)


class Token(unittest.TestCase):
    """AC-4: an unset read token exits 3 naming it, before gcx is called."""

    def test_require_names_the_stack_variable(self):
        # Only the other stack's token is set, so the message must name the right one.
        for env, var, other in (("dev", "GRAFANA_SOLO7DEV_READ_TOKEN", "GRAFANA_SOLO7_READ_TOKEN"),
                                ("local", "GRAFANA_SOLO7DEV_READ_TOKEN", "GRAFANA_SOLO7_READ_TOKEN"),
                                ("prod", "GRAFANA_SOLO7_READ_TOKEN", "GRAFANA_SOLO7DEV_READ_TOKEN")):
            with self.subTest(env=env), self.assertRaises(grafana_stack.Exit) as e:
                grafana_stack.require(env, dict(NO_BOX, **{other: "x"}))
            self.assertEqual(e.exception.code, 3)
            self.assertIn(var + " is unset", str(e.exception))

    def test_require_returns_stack_and_token(self):
        self.assertEqual(grafana_stack.require("prod", dict(NO_BOX, GRAFANA_SOLO7_READ_TOKEN=" t ")), ("solo7", "t"))

    def test_observe_check_exits_3_before_gcx(self):
        env = {k: v for k, v in os.environ.items() if not k.startswith("GRAFANA_")}
        env.update(NO_BOX)
        for target, var in (("dev", "GRAFANA_SOLO7DEV_READ_TOKEN"), ("prod", "GRAFANA_SOLO7_READ_TOKEN")):
            with self.subTest(env=target), mock.patch.dict(os.environ, env, clear=True), \
                    mock.patch.object(gcx, "run", side_effect=AssertionError("gcx called")), \
                    mock.patch.object(gcx, "write_config", side_effect=AssertionError("config written")):
                with self.assertRaises(grafana_stack.Exit) as e:
                    observe_check.main([target])
                self.assertEqual(e.exception.code, 3)
                self.assertIn(var, str(e.exception))

    def test_observe_check_script_exit_code(self):
        env = {k: v for k, v in os.environ.items() if not k.startswith("GRAFANA_")}
        env.update(NO_BOX, PATH="/usr/bin:/bin")
        p = subprocess.run([sys.executable, os.path.join(REPO, "scripts", "observe_check.py"), "dev"],
                           capture_output=True, text=True, env=env)
        self.assertEqual(p.returncode, 3, p.stdout + p.stderr)
        self.assertIn("GRAFANA_SOLO7DEV_READ_TOKEN is unset", p.stdout)
        p = subprocess.run([sys.executable, os.path.join(REPO, "scripts", "observe_check.py"), "nope"],
                           capture_output=True, text=True, env=env)
        self.assertEqual(p.returncode, 2)


if __name__ == "__main__":
    unittest.main()
