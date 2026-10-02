# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""`make env-destroy` (AW-INF-021 AC-7): refusals, idempotence, and the Application before the namespace."""
import os
import subprocess
import sys
import types
import unittest
from unittest import mock

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
sys.path.insert(0, os.path.join(REPO, "scripts"))
import env_destroy  # noqa: E402


def make(*args):
    # PATH without kubectl: a refusal that reached the cluster would fail differently.
    return subprocess.run(["make", "-s", "-C", REPO, "env-destroy", *args],
                          capture_output=True, text=True, env=dict(os.environ, PATH="/usr/bin:/bin"))


class Refusals(unittest.TestCase):

    def assertRefused(self, r, text):
        self.assertEqual(r.returncode, 2, r.stderr)
        self.assertIn(text, r.stderr)

    def test_prod_is_refused(self):
        self.assertRefused(make("ENV=prod", "CONFIRM=andara-prod"), "env-destroy: prod is never destroyed")

    def test_local_is_refused(self):
        self.assertRefused(make("ENV=local", "CONFIRM=andara-local"), "make down VOLUMES=1")

    def test_a_missing_confirm_is_refused(self):
        self.assertRefused(make("ENV=dev"), "CONFIRM is missing; it must be the namespace, andara-dev")

    def test_a_wrong_confirm_is_refused(self):
        self.assertRefused(make("ENV=dev", "CONFIRM=dev"), "CONFIRM=dev is not the namespace andara-dev")

    def test_no_env_is_usage(self):
        self.assertRefused(make(), "usage: make env-destroy")


class Cluster:
    """What kubectl sees: whether the Application and namespace exist, and every call."""

    def __init__(self, app="present", ns_gets_until_gone=2):
        self.app, self.ns_left, self.calls = app, ns_gets_until_gone, []

    def kubectl(self, *args, check=True):
        self.calls.append(args)
        rc, err = 0, ""
        if "application" in args and "get" in args:
            if self.app == "absent":
                rc, err = 1, 'Error from server (NotFound): applications "andara-dev" not found'
            elif self.app == "forbidden":
                rc, err = 1, "Error from server (Forbidden): applications is forbidden"
        if args[:2] == ("get", "namespace"):
            if self.ns_left <= 0:
                rc, err = 1, 'Error from server (NotFound): namespaces "andara-dev" not found'
            self.ns_left -= 1
        if check and rc:
            raise env_destroy.StepFailed(err)
        return types.SimpleNamespace(returncode=rc, stdout="{}", stderr=err)

    def index(self, *words):
        for i, c in enumerate(self.calls):
            if all(w in c for w in words):
                return i
        return -1


def run(cluster):
    with mock.patch.object(env_destroy, "kubectl", cluster.kubectl), \
            mock.patch.object(env_destroy.time, "sleep", lambda n: None):
        return env_destroy.main(["dev", "andara-dev"])


class Destroy(unittest.TestCase):

    def test_the_application_goes_before_the_namespace(self):
        c = Cluster()
        self.assertEqual(run(c), 0)
        app, ns = c.index("delete", "application"), c.index("delete", "namespace")
        self.assertNotEqual(app, -1)
        self.assertNotEqual(ns, -1)
        self.assertLess(app, ns)

    def test_a_missing_namespace_is_exit_zero_and_deletes_nothing(self):
        c = Cluster(app="absent", ns_gets_until_gone=0)
        self.assertEqual(run(c), 0)
        self.assertEqual(c.index("delete", "namespace"), -1)

    def test_an_unreadable_application_stops_before_any_delete(self):
        c = Cluster(app="forbidden")
        self.assertEqual(run(c), 1)
        self.assertFalse([x for x in c.calls if "delete" in x or "patch" in x], c.calls)


if __name__ == "__main__":
    unittest.main()
