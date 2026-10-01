# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""`make world-reset` refuses before touching the cluster (AW-INF-021, AC-10)."""
import json
import os
import subprocess
import sys
import types
import unittest
from unittest import mock

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


class FailsClosed(unittest.TestCase):
    """Preconditions stop the reset before anything on the cluster changes (Codex on #165)."""

    def fake(self, argo_err=None, store=""):
        calls = []

        def kubectl(ns, *args, check=True):
            calls.append(args)
            if args[:2] == ("get", "application"):
                if argo_err:
                    p = types.SimpleNamespace(returncode=1, stdout="", stderr=argo_err)
                else:
                    p = types.SimpleNamespace(returncode=0, stderr="",
                                              stdout='{"spec": {"syncPolicy": {"automated": {}}}}')
            elif args[:2] == ("get", "configmap"):
                p = types.SimpleNamespace(returncode=0, stdout=store, stderr="")
            else:
                p = types.SimpleNamespace(returncode=0, stdout="", stderr="")
            if check and p.returncode != 0:
                raise world_reset.StepFailed(p.stderr)
            return p
        return calls, kubectl

    def run_reset(self, **kw):
        calls, kubectl = self.fake(**kw)
        with mock.patch.object(world_reset, "kubectl", kubectl), \
                mock.patch.object(world_reset.topics, "rpk_runner", lambda env: None):
            with self.assertRaises(world_reset.StepFailed) as e:
                world_reset.reset("dev", "andara-dev")
        changed = [c for c in calls if c[0] in ("patch", "scale", "delete")]
        return str(e.exception), changed

    def test_an_unreadable_application_stops_before_any_change(self):
        msg, changed = self.run_reset(argo_err='Error from server (Forbidden): applications is forbidden')
        self.assertIn("only NotFound counts as none", msg)
        self.assertEqual(changed, [])

    def test_an_unknown_snapshot_store_stops_before_any_change(self):
        msg, changed = self.run_reset(store="gcs")
        self.assertIn("snapshot.store=gcs", msg)
        self.assertEqual(changed, [])

    def test_a_missing_application_is_not_an_error(self):
        _, kubectl = self.fake(argo_err='Error from server (NotFound): applications "andara-dev" not found')
        with mock.patch.object(world_reset, "kubectl", kubectl):
            self.assertEqual(world_reset.argo_policy("andara-dev"), (False, None))


class WaitUp(unittest.TestCase):
    """After scaling back up: Ready, or started and waiting for content (AW-SRV-042), counts."""

    def pod(self, ready=False, started=False):
        return json.dumps({"status": {
            "conditions": [{"type": "Ready", "status": "True" if ready else "False"}],
            "containerStatuses": [{"name": "server", "started": started,
                                   "state": {"running": {"startedAt": "2026-10-01T00:00:00Z"}}}]}})

    def wait(self, pods, logs="", deadline=30):
        seq = list(pods)
        calls = []

        def kubectl(ns, *args, check=True):
            calls.append(args)
            if args[:2] == ("get", "pod"):
                return types.SimpleNamespace(returncode=0, stdout=seq.pop(0) if len(seq) > 1 else seq[0], stderr="")
            if args[:1] == ("logs",):
                return types.SimpleNamespace(returncode=0, stdout=logs, stderr="")
            return types.SimpleNamespace(returncode=0, stdout="", stderr="")
        t = [0.0]
        with mock.patch.object(world_reset, "kubectl", kubectl):
            got = world_reset.wait_up("andara-dev", deadline=deadline,
                                      sleep=lambda n: t.__setitem__(0, t[0] + n), clock=lambda: t[0])
        return got, calls

    def test_ready_is_ready(self):
        got, _ = self.wait([self.pod(), self.pod(started=True), self.pod(ready=True, started=True)])
        self.assertEqual(got, "ready")

    def test_started_and_waiting_for_content_counts(self):
        line = '{"level":"WARN","msg":"waiting for content: no Zones in effect; publish and activate a pack"}'
        got, calls = self.wait([self.pod(started=True)], logs=line)
        self.assertEqual(got, "waiting")
        logs = [c for c in calls if c[:1] == ("logs",)][0]
        self.assertIn("--since-time=2026-10-01T00:00:00Z", logs)

    def test_started_unready_without_the_wait_line_keeps_waiting_then_fails(self):
        with self.assertRaises(world_reset.StepFailed) as e:
            self.wait([self.pod(started=True)], logs="recovering from the log", deadline=20)
        self.assertIn("neither Ready nor waiting for content within 20s", str(e.exception))

    def test_not_started_fails_at_the_deadline(self):
        with self.assertRaises(world_reset.StepFailed):
            self.wait([self.pod()], deadline=10)


class Scope(unittest.TestCase):

    def test_it_resets_the_world_and_never_content_or_audit(self):
        self.assertEqual(sorted(world_reset.RESET_TOPICS), sorted(
            ["andara.commands.v1", "andara.events.v1", "andara.state.v1", "andara.accounts.v1"]))
        for t in world_reset.RESET_TOPICS:
            self.assertFalse(t.startswith("andara.content.") or t == "andara.audit.v1", t)


if __name__ == "__main__":
    unittest.main()
