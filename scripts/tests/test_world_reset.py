# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""`make world-reset` refuses before touching the cluster (AW-INF-021, AC-10)."""
import os
import subprocess
import sys
import unittest

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
sys.path.insert(0, os.path.join(REPO, "scripts"))
import world_reset  # noqa: E402


def make(*args):
    # PATH without kubectl: a refusal that reached the cluster would fail differently.
    env = dict(os.environ, PATH="/usr/bin:/bin")
    return subprocess.run(["make", "-s", "-C", REPO, "world-reset", *args],
                          capture_output=True, text=True, env=env)


class Refusals(unittest.TestCase):

    def assertRefused(self, r, text):
        self.assertEqual(r.returncode, 2, r.stderr)
        self.assertIn(text, r.stderr)

    def test_prod_is_refused(self):
        self.assertRefused(make("ENV=prod", "CONFIRM=andara-prod"), "world-reset: prod is never reset")

    def test_local_is_refused(self):
        self.assertRefused(make("ENV=local", "CONFIRM=andara-local"), "make down VOLUMES=1")

    def test_a_missing_confirm_is_refused(self):
        self.assertRefused(make("ENV=dev"), "CONFIRM is missing; it must be the namespace, andara-dev")

    def test_a_confirm_that_is_not_the_namespace_is_refused(self):
        self.assertRefused(make("ENV=dev", "CONFIRM=dev"), "CONFIRM=dev is not the namespace andara-dev")

    def test_no_env_is_usage(self):
        self.assertRefused(make("CONFIRM=andara-dev"), "usage: make world-reset")


class Scope(unittest.TestCase):

    def test_it_resets_the_world_and_never_content_or_audit(self):
        self.assertEqual(sorted(world_reset.RESET_TOPICS), sorted(
            ["andara.commands.v1", "andara.events.v1", "andara.state.v1", "andara.accounts.v1"]))
        for t in world_reset.RESET_TOPICS:
            self.assertFalse(t.startswith("andara.content.") or t == "andara.audit.v1", t)


if __name__ == "__main__":
    unittest.main()
