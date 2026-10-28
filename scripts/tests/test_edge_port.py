# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""The edge's port is ANDARA_EDGE_PORT, default 8443, in every script that dials it (AW-INF-060 AC-4)."""
import io
import os
import re
import subprocess
import sys
import unittest
from unittest import mock

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
sys.path.insert(0, os.path.join(REPO, "scripts"))
import values_get  # noqa: E402

SCRIPTS = ("scripts/helm_install.sh", "scripts/env_recover.py", "scripts/stream_soak.sh")
BARE = re.compile(r"(?<![0-9])443(?![0-9])")
DEFAULTS = {".sh": re.compile(r'"\$\{ANDARA_EDGE_PORT:-8443\}"'),
            ".py": re.compile(r'environ\.get\("ANDARA_EDGE_PORT"\) or "8443"')}


class EdgePort(unittest.TestCase):

    def test_no_script_hard_codes_443(self):
        for rel in SCRIPTS:
            with self.subTest(script=rel):
                with open(os.path.join(REPO, rel)) as f:
                    hits = [n for n, line in enumerate(f, 1) if BARE.search(line)]
                self.assertEqual(hits, [], "%s has a bare 443 on line(s) %s" % (rel, hits))

    def test_each_reads_andara_edge_port_defaulting_to_8443(self):
        for rel in SCRIPTS:
            with self.subTest(script=rel):
                with open(os.path.join(REPO, rel)) as f:
                    text = f.read()
                self.assertRegex(text, DEFAULTS[os.path.splitext(rel)[1]])


class Profile(unittest.TestCase):
    """`make helm-install ENV=local PROFILE=ci` is the only overlay; any other PROFILE exits 2 (AW-INF-060 AC-10)."""

    def run_install(self, env, profile):
        # The refusal comes before any helm or kubectl call, so no cluster is reached.
        return subprocess.run(["bash", "scripts/helm_install.sh", env, "andara-server", "dev", "", "", profile],
                              cwd=REPO, capture_output=True, text=True)

    def test_an_unknown_profile_exits_2(self):
        for profile in ("full", "min", "box", "../ci"):
            with self.subTest(profile=profile):
                r = self.run_install("local", profile)
                self.assertEqual(r.returncode, 2, r.stderr)
                self.assertIn("PROFILE=%s: must be ci" % profile, r.stderr)

    def test_ci_on_another_env_exits_2(self):
        r = self.run_install("dev", "ci")
        self.assertEqual(r.returncode, 2, r.stderr)
        self.assertIn("overlay on local", r.stderr)

    def recipe(self, *args):
        r = subprocess.run(["make", "-n", "-s", "-C", REPO, "helm-install", "ENV=local", *args],
                           capture_output=True, text=True, env={k: v for k, v in os.environ.items() if k != "PROFILE"})
        self.assertEqual(r.returncode, 0, r.stderr)
        return " ".join(r.stdout.replace("\\\n", " ").split())

    def test_the_target_passes_a_command_line_profile_only(self):
        self.assertRegex(self.recipe("PROFILE=ci"), r'helm_install\.sh "local" .* "ci"$')
        # The compose default `full` is not a helm profile.
        self.assertRegex(self.recipe(), r'helm_install\.sh "local" .* ""$')


class ValuesGet(unittest.TestCase):
    """helm_install.sh reads the merged values, as helm installs them (AW-INF-060, values/ci.yaml)."""

    def test_the_overlay_wins_and_merges(self):
        local = os.path.join(REPO, "deploy", "helm", "values", "local.yaml")
        ci = os.path.join(REPO, "deploy", "helm", "values", "ci.yaml")
        self.assertEqual(values_get.get("host", [local]), "local.andara.valesordev.com")
        self.assertEqual(values_get.get("host", [local, ci]), "andara.local")
        self.assertEqual(values_get.get("server.sim.source", [local]), "")
        self.assertEqual(values_get.get("server.sim.source", [local, ci]), "memory")
        # Merged, not replaced: ci.yaml's server block leaves local's topic prefix.
        self.assertEqual(values_get.get("server.kafka.topic_prefix", [local, ci]), "local")

    def test_the_command_line(self):
        local = os.path.join(REPO, "deploy", "helm", "values", "local.yaml")
        ci = os.path.join(REPO, "deploy", "helm", "values", "ci.yaml")
        with mock.patch("sys.stdout", new_callable=io.StringIO) as out:
            self.assertEqual(values_get.main(["host", "--values", local, "--values", ci]), 0)
        self.assertEqual(out.getvalue(), "andara.local\n")
        with mock.patch("sys.stderr", new_callable=io.StringIO):
            for bad in ([], ["host"], ["host", "--values"], ["host", local], ["host", "--values", local, ci]):
                self.assertEqual(values_get.main(bad), 2, bad)

    def test_a_null_deletes(self):
        self.assertEqual(values_get.merge({"a": {"b": 1, "c": 2}}, {"a": {"b": None}}), {"a": {"c": 2}})


if __name__ == "__main__":
    unittest.main()
