# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""`make projector-stop/-start/-rebuild` refusals and the rebuild Job (AW-INF-025)."""
import json
import os
import subprocess
import sys
import types
import unittest
from unittest import mock

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
sys.path.insert(0, os.path.join(REPO, "scripts"))
import projector  # noqa: E402

DEPLOYMENT = {
    "spec": {
        "selector": {"matchLabels": {"app": "andara-projector-state"}},
        "template": {
            "metadata": {
                "labels": {"app": "andara-projector-state", "app.kubernetes.io/name": "andara"},
                "annotations": {"checksum/config": "abc", "prometheus.io/scrape": "true",
                                "k8s.grafana.com/job": "andara-projector-state"},
            },
            "spec": {"containers": [{
                "name": "projector",
                "command": ["/usr/local/bin/andara-projector", "state"],
                "readinessProbe": {"httpGet": {"path": "/readyz"}},
                "livenessProbe": {"httpGet": {"path": "/livez"}},
                "envFrom": [{"configMapRef": {"name": "andara-config"}},
                            {"secretRef": {"name": "andara-snapshot-s3"}}],
            }]},
        },
    },
}


def make(*args):
    env = dict(os.environ, PATH="/usr/bin:/bin")
    return subprocess.run(["make", "-s", "-C", REPO, *args], capture_output=True, text=True, env=env)


class Usage(unittest.TestCase):
    """AC-7's usage half: exit 2 before anything touches the cluster (no kubectl on PATH)."""

    def test_a_missing_env_is_usage(self):
        for target in ("projector-stop", "projector-start", "projector-rebuild"):
            r = make(target)
            self.assertEqual(r.returncode, 2, target)
            self.assertIn("ENV is missing", r.stderr)

    def test_local_is_usage(self):
        r = make("projector-stop", "ENV=local")
        self.assertEqual(r.returncode, 2)
        self.assertIn("ENV=local", r.stderr)


def fake_kubectl(objects, calls):
    """A kubectl that answers `get <kind> <name> -o json` from `objects` and records the rest."""
    def kubectl(ns, *args, check=True, input=None):
        calls.append(args)
        if args[0] == "get" and args[-2:] == ("-o", "json"):
            key = (args[1], args[2])
            if key in objects:
                return types.SimpleNamespace(returncode=0, stdout=json.dumps(objects[key]), stderr="")
            return types.SimpleNamespace(returncode=1, stdout="",
                                         stderr='Error from server (NotFound): %s "%s" not found' % key)
        return types.SimpleNamespace(returncode=0, stdout="", stderr="")
    return kubectl


class Preconditions(unittest.TestCase):

    def run_cmd(self, fn, objects):
        calls = []
        with mock.patch.object(projector, "kubectl", fake_kubectl(objects, calls)):
            with self.assertRaises(projector.Failed) as e:
                fn()
        return str(e.exception), [c for c in calls if c[0] in ("scale", "create", "delete")]

    def test_a_disabled_projector_is_refused_naming_the_environment(self):
        for fn in (lambda: projector.stop("dev", "andara-dev", None),
                   lambda: projector.start("dev", "andara-dev"),
                   lambda: projector.rebuild("dev", "andara-dev", None)):
            msg, changed = self.run_cmd(fn, {})
            self.assertIn("projectors.state.enabled is false in andara-dev", msg)
            self.assertEqual(changed, [])

    def test_a_running_rebuild_job_is_refused_before_scaling(self):
        running = {"status": {"active": 1}}
        msg, changed = self.run_cmd(lambda: projector.rebuild("dev", "andara-dev", None), {
            ("deployment", projector.DEPLOYMENT): DEPLOYMENT,
            ("job", projector.JOB): running,
        })
        self.assertIn("Job %s is still running in andara-dev" % projector.JOB, msg)
        self.assertEqual(changed, [])


class RebuildJob(unittest.TestCase):

    def setUp(self):
        self.job = projector.job_doc(DEPLOYMENT, 1800)
        self.pod = self.job["spec"]["template"]

    def test_the_job_runs_rebuild_once(self):
        spec = self.job["spec"]
        self.assertEqual(self.job["metadata"]["name"], "andara-projector-state-rebuild")
        self.assertEqual((spec["backoffLimit"], spec["ttlSecondsAfterFinished"], spec["activeDeadlineSeconds"]),
                         (0, 3600, 1800))
        c = self.pod["spec"]["containers"][0]
        self.assertEqual(c["command"], ["/usr/local/bin/andara-projector", "state", "--rebuild"])
        self.assertEqual(self.pod["spec"]["restartPolicy"], "Never")
        self.assertEqual(c["envFrom"], DEPLOYMENT["spec"]["template"]["spec"]["containers"][0]["envFrom"])

    def test_the_job_is_not_scraped_and_not_the_deployments_pod(self):
        ann = self.pod["metadata"]["annotations"]
        self.assertFalse([k for k in ann if k.startswith(("prometheus.io/", "k8s.grafana.com/"))], ann)
        labels = self.pod["metadata"]["labels"]
        self.assertNotEqual(labels["app"], "andara-projector-state")
        self.assertEqual(labels["app.kubernetes.io/name"], "andara")

    def test_the_deployment_is_not_modified(self):
        self.assertEqual(DEPLOYMENT["spec"]["template"]["spec"]["containers"][0]["command"],
                         ["/usr/local/bin/andara-projector", "state"])


class CaughtUp(unittest.TestCase):

    def test_the_caught_up_line_gives_its_tick(self):
        logs = "\n".join([
            '{"level":"INFO","msg":"consumer group wiped for a rebuild","group":"g"}',
            "not json",
            '{"level":"INFO","msg":"state projector caught up","tick":4242}',
        ])
        self.assertEqual(projector.caught_up_tick(logs), 4242)

    def test_no_caught_up_line_is_none(self):
        self.assertIsNone(projector.caught_up_tick('{"msg":"state projector started","round_tick":10}'))


if __name__ == "__main__":
    unittest.main()
