# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""`make env-destroy` (AW-INF-021 AC-7): refusals, idempotence, and the Application before the namespace."""
import json
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


def run(cluster, topics_step=None):
    with mock.patch.object(env_destroy, "kubectl", cluster.kubectl), \
            mock.patch.object(env_destroy.time, "sleep", lambda n: None), \
            mock.patch.object(env_destroy, "delete_topics",
                              topics_step or (lambda env: cluster.calls.append(("topics", env)))):
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
        self.assertFalse([x for x in c.calls if "delete" in x or "patch" in x or "topics" in x], c.calls)

    def test_the_environments_topics_go_after_the_namespace(self):
        c = Cluster()
        self.assertEqual(run(c), 0)
        self.assertLess(c.index("delete", "namespace"), c.index("topics"))
        self.assertIn(("topics", "dev"), c.calls)


class SharedTopics(unittest.TestCase):
    """AW-INF-055: env-destroy starts the environment from nothing on the shared broker too, and
    never names the other environment's topics."""

    def broker(self, live, refuse_group=None):
        calls = []

        def run(args):
            calls.append(list(args))
            if args[:2] == ["topic", "list"]:
                return types.SimpleNamespace(returncode=0, stderr="",
                                             stdout=json.dumps([{"name": n, "partitions": 1} for n in live]))
            if args[:2] == ["group", "delete"]:
                if args[2] == refuse_group:
                    return types.SimpleNamespace(returncode=1, stdout="", stderr="GROUP_AUTHORIZATION_FAILED")
                return types.SimpleNamespace(returncode=1, stdout="", stderr="GROUP_ID_NOT_FOUND: not found")
            return types.SimpleNamespace(returncode=0, stdout="", stderr="")
        return run, calls

    def test_only_its_own_prefixed_topics_and_groups(self):
        live = ["dev.andara.commands.v1", "dev.andara.audit.v1", "local.andara.commands.v1", "__consumer_offsets"]
        run, calls = self.broker(live)
        with mock.patch("sys.stdout"):
            env_destroy.delete_topics("dev", run=run)
        deleted = [c[2] for c in calls if c[:2] == ["topic", "delete"]]
        self.assertEqual(sorted(deleted), ["dev.andara.audit.v1", "dev.andara.commands.v1"])
        groups = [c[2] for c in calls if c[:2] == ["group", "delete"]]
        self.assertIn("andara-sim-dev", groups)
        self.assertTrue(all(g.endswith("-dev") for g in groups), groups)
        self.assertFalse([c for c in calls if any("local" in str(a) for a in c)], calls)

    def test_a_refused_group_delete_fails_naming_it(self):
        run, _ = self.broker([], refuse_group="andara-sim-dev")
        with self.assertRaises(env_destroy.StepFailed) as e:
            env_destroy.delete_topics("dev", run=run)
        self.assertIn("andara-sim-dev", str(e.exception))

    def test_an_environment_without_a_prefix_is_refused(self):
        run, calls = self.broker(["andara.commands.v1"])
        with self.assertRaises(env_destroy.StepFailed):
            env_destroy.delete_topics("prod", run=run)
        self.assertEqual(calls, [])


if __name__ == "__main__":
    unittest.main()
