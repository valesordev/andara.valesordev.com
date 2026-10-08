# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""`make server-stop` and `make server-start` (AW-INF-037)."""
import contextlib
import copy
import io
import json
import os
import subprocess
import sys
import types
import unittest
from unittest import mock

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
sys.path.insert(0, os.path.join(REPO, "scripts"))
import server_ctl  # noqa: E402
import world_reset  # noqa: E402

AUTOMATED = {"prune": True, "selfHeal": True}
OPTIONS = ["RespectIgnoreDifferences=true"]
READY_POD = {"status": {"conditions": [{"type": "Ready", "status": "True"}]}}


def make(target, *args):
    # PATH without kubectl: a refusal that reached the cluster would fail differently.
    env = dict(os.environ, PATH="/usr/bin:/bin")
    return subprocess.run(["make", "-s", "-C", REPO, target, *args],
                          capture_output=True, text=True, env=env)


def ok(stdout=""):
    return types.SimpleNamespace(returncode=0, stdout=stdout, stderr="")


class Cluster:
    """A fake kubectl over one Argo CD Application and one StatefulSet pod."""

    def __init__(self, app=True, automated=AUTOMATED, annotations=None, pod=True, app_error=None,
                 pod_goes_on_scale=True):
        self.app = None
        if app:
            self.app = {"metadata": {"annotations": dict(annotations or {})},
                        "spec": {"syncPolicy": {"syncOptions": list(OPTIONS)}}}
            if automated is not None:
                self.app["spec"]["syncPolicy"]["automated"] = dict(automated)
        self.app_error = app_error
        self.pod = pod
        self.pod_goes_on_scale = pod_goes_on_scale
        self.replicas = 1
        self.calls = []

    def kubectl(self, ns, *args, check=True):
        self.calls.append((ns,) + args)
        if args[:2] == ("get", "application"):
            if self.app_error:
                return types.SimpleNamespace(returncode=1, stdout="", stderr=self.app_error)
            if self.app is None:
                return types.SimpleNamespace(returncode=1, stdout="",
                                             stderr='Error from server (NotFound): applications "%s" not found' % ns)
            return ok(json.dumps(self.app))
        if args[0] == "patch":
            self.patch(args[args.index("--type") + 1], json.loads(args[args.index("-p") + 1]))
            return ok()
        if args[0] == "scale":
            self.replicas = int(args[2].split("=")[1])
            if self.replicas == 0 and self.pod_goes_on_scale:
                self.pod = False
            elif self.replicas > 0:
                self.pod = True
            return ok()
        if args[:2] == ("get", "pod"):
            if not self.pod:
                return types.SimpleNamespace(returncode=1, stdout="", stderr="NotFound")
            return ok(json.dumps(READY_POD))
        return ok()

    def patch(self, kind, body):
        if kind == "json":
            for op in body:
                parts = op["path"].strip("/").split("/")
                node = self.app
                for p in parts[:-1]:
                    node = node[p]
                if op["op"] == "remove":
                    del node[parts[-1]]
                else:
                    node[parts[-1]] = op["value"]
            return

        def merge(dst, src):
            for k, v in src.items():
                if v is None:
                    dst.pop(k, None)
                elif isinstance(v, dict):
                    merge(dst.setdefault(k, {}), v)
                else:
                    dst[k] = v
        merge(self.app, body)

    @property
    def annotation(self):
        return self.app["metadata"]["annotations"].get(server_ctl.ANNOTATION)

    @property
    def automated(self):
        return self.app["spec"]["syncPolicy"].get("automated")

    def changes(self):
        return [c for c in self.calls if c[1] in ("patch", "scale")]


def run(cluster, cmd, env="dev"):
    out = io.StringIO()
    with mock.patch.object(world_reset, "kubectl", cluster.kubectl), \
            mock.patch.object(world_reset.time, "sleep", lambda s: None), \
            contextlib.redirect_stdout(out):
        (server_ctl.stop if cmd == "stop" else server_ctl.start)(env, "andara-" + env)
    return out.getvalue()


class Refusals(unittest.TestCase):

    def assertRefused(self, r, text):
        self.assertEqual(r.returncode, 2, r.stderr)
        self.assertIn(text, r.stderr)

    def test_no_env_says_what_it_expected(self):
        for target in ("server-stop", "server-start"):
            self.assertRefused(make(target), "ENV is missing: make %s ENV=<dev|prod>" % target)

    def test_an_unknown_env_says_what_it_expected(self):
        for target in ("server-stop", "server-start"):
            self.assertRefused(make(target, "ENV=staging"), "ENV=staging is unknown; expected dev or prod")

    def test_local_points_at_make_down(self):
        self.assertRefused(make("server-stop", "ENV=local"), "`make down`")


class Stop(unittest.TestCase):

    def test_it_suspends_sync_scales_to_zero_and_says_start_restores_it(self):
        c = Cluster()
        out = run(c, "stop")
        self.assertIsNone(c.automated)
        self.assertEqual(json.loads(c.annotation), AUTOMATED)
        self.assertEqual(c.replicas, 0)
        self.assertEqual(c.app["spec"]["syncPolicy"]["syncOptions"], OPTIONS)
        self.assertIn("server-stop: suspended automated sync on andara-dev", out)
        self.assertIn("server-stop: scaled statefulset/andara to 0", out)
        self.assertEqual(out.splitlines()[-1],
                         "server-stop: automated sync is suspended; make server-start restores it")

    def test_it_records_before_it_suspends(self):
        c = Cluster()
        run(c, "stop")
        patches = [c_ for c_ in c.calls if c_[1] == "patch"]
        self.assertIn(server_ctl.ANNOTATION, patches[0][-1])
        self.assertIn("remove", patches[1][-1])

    def test_it_suspends_before_it_scales(self):
        c = Cluster()
        run(c, "stop")
        remove = next(i for i, x in enumerate(c.calls) if x[1] == "patch" and "remove" in x[-1])
        scale = next(i for i, x in enumerate(c.calls) if x[1] == "scale")
        self.assertLess(remove, scale)

    def test_the_stop_bound_reaches_the_wait_and_the_message(self):
        c = Cluster()
        with mock.patch.dict(os.environ, {"SERVER_STOP_TIMEOUT": "45s"}):
            run(c, "stop")
        self.assertIn(("andara-dev", "wait", "--for=delete", "pod/andara-0", "--timeout=45s"), c.calls)
        c = Cluster(pod_goes_on_scale=False)
        with mock.patch.dict(os.environ, {"SERVER_STOP_TIMEOUT": "7s"}):
            with self.assertRaises(server_ctl.Failed) as e:
                run(c, "stop")
        self.assertIn("7s after scaling", str(e.exception))

    def test_a_failed_stop_after_the_suspend_says_the_sync_is_suspended(self):
        c = Cluster(pod_goes_on_scale=False)
        err = io.StringIO()
        with contextlib.redirect_stderr(err), self.assertRaises(server_ctl.Failed):
            run(c, "stop")
        self.assertEqual(err.getvalue().strip(), server_ctl.SUSPENDED_LINE)

    def test_a_failed_stop_with_nothing_suspended_does_not_say_so(self):
        c = Cluster(app=False, pod_goes_on_scale=False)
        err = io.StringIO()
        with contextlib.redirect_stderr(err), self.assertRaises(server_ctl.Failed):
            run(c, "stop")
        self.assertEqual(err.getvalue(), "")

    def test_a_second_stop_succeeds_and_keeps_the_first_record(self):
        c = Cluster()
        run(c, "stop")
        before = copy.deepcopy(c.app)
        out = run(c, "stop")
        self.assertEqual(c.app, before)
        self.assertEqual(json.loads(c.annotation), AUTOMATED)
        self.assertIn("already suspended", out)
        self.assertEqual(out.splitlines()[-1],
                         "server-stop: automated sync is suspended; make server-start restores it")
        run(c, "start")
        self.assertEqual(c.automated, AUTOMATED)

    def test_a_policy_restored_by_hand_keeps_the_recorded_one(self):
        c = Cluster(annotations={server_ctl.ANNOTATION: json.dumps(AUTOMATED)},
                    automated={"prune": False, "selfHeal": True})
        run(c, "stop")
        self.assertEqual(json.loads(c.annotation), AUTOMATED)
        self.assertIsNone(c.automated)

    def test_no_application_scales_without_suspending_and_says_so(self):
        c = Cluster(app=False)
        out = run(c, "stop", env="prod")
        self.assertEqual(c.replicas, 0)
        self.assertEqual([x for x in c.calls if x[1] == "patch"], [])
        self.assertIn("no Argo CD Application andara-prod; nothing to suspend", out)
        self.assertNotIn("is suspended", out)

    def test_an_application_without_automated_sync_is_left_alone(self):
        c = Cluster(automated=None)
        out = run(c, "stop")
        self.assertEqual([x for x in c.calls if x[1] == "patch"], [])
        self.assertIsNone(c.annotation)
        self.assertEqual(c.replicas, 0)
        self.assertNotIn("is suspended", out)

    def test_an_unreadable_application_stops_before_any_change(self):
        c = Cluster(app_error="Error from server (Forbidden): applications is forbidden")
        with self.assertRaises(server_ctl.Failed) as e:
            run(c, "stop")
        self.assertIn("only NotFound counts as none", str(e.exception))
        self.assertEqual(c.changes(), [])

    def test_a_pod_that_does_not_go_fails(self):
        c = Cluster(pod_goes_on_scale=False)
        with self.assertRaises(server_ctl.Failed) as e:
            run(c, "stop")
        self.assertIn("andara-0 still running", str(e.exception))


class Start(unittest.TestCase):

    def stopped(self, **kw):
        c = Cluster(**kw)
        run(c, "stop")
        return c

    def test_it_restores_the_policy_it_suspended_and_waits_for_ready(self):
        c = self.stopped()
        out = run(c, "start")
        self.assertEqual(c.automated, AUTOMATED)
        self.assertIsNone(c.annotation)
        self.assertEqual(c.app["spec"]["syncPolicy"]["syncOptions"], OPTIONS)
        self.assertEqual(c.replicas, 1)
        self.assertIn("server-start: restored automated sync on andara-dev", out)
        self.assertIn("andara-0 Ready", out)

    def test_the_start_bound_reaches_the_wait(self):
        c = self.stopped()
        with mock.patch.dict(os.environ, {"SERVER_START_TIMEOUT": "10m"}), \
                mock.patch.object(world_reset, "wait_up", return_value="ready") as w:
            run(c, "start")
        self.assertEqual(w.call_args.kwargs["deadline"], 600)

    def test_a_hand_set_policy_is_replaced_not_merged(self):
        c = self.stopped()
        c.app["spec"]["syncPolicy"]["automated"] = {"allowEmpty": True}
        run(c, "start")
        self.assertEqual(c.automated, AUTOMATED)

    def test_it_scales_to_the_charts_replica_count(self):
        c = self.stopped()
        with mock.patch.object(server_ctl, "chart_replicas", lambda env: 3):
            run(c, "start")
        self.assertEqual(c.replicas, 3)

    def test_it_restores_before_it_waits(self):
        c = self.stopped()
        run(c, "start")
        self.assertLess(max(i for i, x in enumerate(c.calls) if x[1] == "patch"),
                        max(i for i, x in enumerate(c.calls) if x[1:3] == ("get", "pod")))

    def test_on_a_running_server_with_nothing_recorded_it_changes_only_nothing(self):
        c = Cluster()
        before = copy.deepcopy(c.app)
        out = run(c, "start")
        self.assertEqual(c.app, before)
        self.assertEqual(c.replicas, 1)
        self.assertIn("nothing to restore", out)

    def test_no_application_scales_and_waits(self):
        c = Cluster(app=False, pod=False)
        out = run(c, "start", env="prod")
        self.assertEqual(c.replicas, 1)
        self.assertIn("no Argo CD Application andara-prod; nothing to restore", out)

    def test_a_corrupt_record_fails_and_is_kept(self):
        c = Cluster(annotations={server_ctl.ANNOTATION: "not json"}, automated=None)
        with self.assertRaises(server_ctl.Failed) as e:
            run(c, "start")
        self.assertIn(server_ctl.ANNOTATION, str(e.exception))
        self.assertEqual(c.annotation, "not json")


class ChartReplicas(unittest.TestCase):

    def test_it_reads_the_charts_value_for_each_environment(self):
        for env in ("dev", "prod"):
            self.assertEqual(server_ctl.chart_replicas(env), 1)

    def test_each_environments_override_is_read(self):
        import tempfile
        with tempfile.TemporaryDirectory() as d:
            os.makedirs(os.path.join(d, "andara"))
            os.makedirs(os.path.join(d, "values"))
            for rel, text in (("andara/values.yaml", "replicaCount: 1\n"),
                              ("values/dev.yaml", "replicaCount: 2\n"),
                              ("values/prod.yaml", "replicaCount: 3\n")):
                with open(os.path.join(d, rel), "w") as f:
                    f.write(text)
            self.assertEqual(server_ctl.chart_replicas("dev", d), 2)
            self.assertEqual(server_ctl.chart_replicas("prod", d), 3)

    def test_a_boolean_count_is_refused(self):
        with mock.patch("builtins.open", mock.mock_open(read_data="replicaCount: true\n")):
            with self.assertRaises(server_ctl.Failed):
                server_ctl.chart_replicas("dev")

    def test_a_zero_count_is_refused(self):
        with mock.patch("builtins.open", mock.mock_open(read_data="replicaCount: 0\n")):
            with self.assertRaises(server_ctl.Failed):
                server_ctl.chart_replicas("dev")


class Durations(unittest.TestCase):

    def test_a_malformed_duration_exits_2(self):
        r = make("server-stop", "ENV=dev", "SERVER_STOP_TIMEOUT=soon")
        self.assertEqual(r.returncode, 2, r.stderr)
        self.assertIn("is not a duration", r.stderr)

    def test_defaults_and_units(self):
        with mock.patch.dict(os.environ, {}, clear=False):
            os.environ.pop("SERVER_STOP_TIMEOUT", None)
            os.environ.pop("SERVER_START_TIMEOUT", None)
            self.assertEqual(server_ctl.seconds("SERVER_STOP_TIMEOUT"), 120)
            self.assertEqual(server_ctl.seconds("SERVER_START_TIMEOUT"), 300)
        with mock.patch.dict(os.environ, {"SERVER_START_TIMEOUT": "10m"}):
            self.assertEqual(server_ctl.seconds("SERVER_START_TIMEOUT"), 600)


if __name__ == "__main__":
    unittest.main()
