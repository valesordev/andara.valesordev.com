# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""`make content-seed` (AW-INF-021): its refusals, and its decisions against a fake andara-cli.

The fake records every invocation and answers each subcommand from a JSON scenario, so the
seed's choice (already active, publish, or activate an earlier run's version) is checked
without a server. The live run on dev is the story's verification record.
"""
import json
import os
import shutil
import stat
import subprocess
import tempfile
import textwrap
import unittest

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

FAKE = textwrap.dedent("""\
    #!/usr/bin/env python3
    import json, os, sys
    d = os.environ["FAKE_DIR"]
    args = [a for a in sys.argv[1:]]
    with open(os.path.join(d, "calls"), "a") as f:
        f.write(" ".join(args) + "\\n")
    sc = json.load(open(os.path.join(d, "scenario.json")))
    rest = list(args)
    if rest[:2] == ["--output", "json"]:
        rest = rest[2:]
    words = [a for a in rest if not a.startswith("-")]
    def out(o):
        print(json.dumps(o)); sys.exit(0)
    if words[:2] == ["auth", "login"]:
        sys.stdin.read(); sys.exit(0)
    if words[:2] == ["auth", "whoami"]:
        out({"account_id": sc.get("operator", "op1")})
    if words[:2] == ["content", "history"]:
        out({"pack": "town", "active_version": sc.get("active", 0), "versions": sc.get("versions", [])})
    if words[:2] == ["content", "publish"]:
        out({"pack": "town", "version": sc.get("publish_as", 1), "blobs_total": 6, "blobs_uploaded": 6})
    if words[:2] == ["content", "activate"]:
        sc["serving"] = int(words[3]); json.dump(sc, open(os.path.join(d, "scenario.json"), "w"))
        out({"pack": "town", "version": int(words[3])})
    if words[:2] == ["server", "info"]:
        out({"content": [{"pack": "andara.core", "version": 1}] +
             ([{"pack": "town", "version": sc["serving"]}] if sc.get("serving") else [])})
    sys.exit(9)
""")


class Seed(unittest.TestCase):

    def setUp(self):
        self.dir = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.dir, True)
        self.cli = os.path.join(self.dir, "andara-cli")
        with open(self.cli, "w") as f:
            f.write(FAKE)
        os.chmod(self.cli, os.stat(self.cli).st_mode | stat.S_IEXEC)

    def run_seed(self, scenario, env_name="dev", operator="operator:pw"):
        with open(os.path.join(self.dir, "scenario.json"), "w") as f:
            json.dump(scenario, f)
        env = dict(os.environ, FAKE_DIR=self.dir, ANDARA_CLI=self.cli, CONTENT_SEED_TIMEOUT="3s")
        env.pop("ANDARA_BOOTSTRAP_OPERATOR", None)
        if operator is not None:
            env["ANDARA_BOOTSTRAP_OPERATOR"] = operator
        args = ["make", "-s", "-C", REPO, "content-seed"] + (["ENV=" + env_name] if env_name is not None else [])
        return subprocess.run(args, capture_output=True, text=True, env=env)

    def calls(self):
        p = os.path.join(self.dir, "calls")
        if not os.path.exists(p):
            return []
        with open(p) as f:
            return f.read().splitlines()

    # Refusals, before any RPC.
    def test_no_env_is_usage(self):
        r = self.run_seed({}, env_name=None)
        self.assertIn("content-seed: ENV is required", r.stderr)
        self.assertEqual(self.calls(), [])

    def test_prod_is_refused(self):
        r = self.run_seed({}, env_name="prod")
        self.assertIn("content-seed: prod is not seeded with the fixture", r.stderr)
        self.assertEqual(self.calls(), [])

    def test_no_operator_credential_stops_before_any_rpc(self):  # AC-9
        r = self.run_seed({}, operator=None)
        self.assertNotEqual(r.returncode, 0)
        self.assertIn("content-seed: ANDARA_BOOTSTRAP_OPERATOR is not set", r.stderr)
        self.assertEqual(self.calls(), [])

    # Decisions.
    def test_an_active_town_publishes_nothing(self):  # AC-3
        r = self.run_seed({"active": 3, "versions": [{"version": 3, "author": "builder9"}]})
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertIn("content-seed: town@3 is already active; nothing published", r.stdout)
        self.assertFalse([c for c in self.calls() if "publish" in c or "activate" in c], self.calls())

    def test_an_empty_store_is_published_and_activated(self):
        r = self.run_seed({"publish_as": 1})
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertTrue(r.stdout.rstrip().endswith("content-seed: town@1 published and active"), r.stdout)
        publish = [c for c in self.calls() if c.split()[2:4] == ["content", "publish"]]
        activate = [c for c in self.calls() if c.split()[2:4] == ["content", "activate"]]
        self.assertEqual(len(publish), 1, self.calls())
        self.assertIn("--path content/fixtures/town", publish[0])
        self.assertEqual(len(activate), 1, self.calls())
        for flag in ("--override", "--yes", "--reason dev fixture"):
            self.assertIn(flag, activate[0])

    def test_an_earlier_runs_unactivated_version_is_activated_not_republished(self):
        r = self.run_seed({"operator": "op1", "versions": [{"version": 2, "author": "op1"}]})
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertIn("content-seed: town@2 published and active", r.stdout)
        self.assertFalse([c for c in self.calls() if "publish" in c.split()], self.calls())

    def test_a_builders_unactivated_version_is_not_activated_by_the_seed(self):
        r = self.run_seed({"operator": "op1", "publish_as": 3, "versions": [{"version": 2, "author": "builder9"}]})
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertIn("content-seed: town@3 published and active", r.stdout)

    def test_a_swap_that_never_shows_fails_naming_the_deadline(self):
        # server info never lists town: activate is recorded, but the fake forgets it.
        with open(self.cli) as f:
            fake = f.read().replace('sc["serving"] = int(words[3]); ', "")
        with open(self.cli, "w") as f:
            f.write(fake)
        r = self.run_seed({"publish_as": 1})
        self.assertEqual(r.returncode, 2)  # make's own status for a failed recipe
        self.assertIn("server info did not list town@1 within 3s", r.stderr)


if __name__ == "__main__":
    unittest.main()
