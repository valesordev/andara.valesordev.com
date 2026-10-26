# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""make collectors-check and collectors-probe (AW-INF-065) against fakes: each assertion fails on the
state that breaks it, and the probe's absence is anchored on the control's arrival."""
import io
import os
import sys
import unittest
from contextlib import redirect_stdout
from unittest import mock

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
sys.path.insert(0, os.path.join(REPO, "scripts"))
import collectors_live as live  # noqa: E402


def pod(ns, name, labels=None, volumes=None, env=None, env_from=None):
    return {"metadata": {"namespace": ns, "name": name, "labels": labels or {}},
            "spec": {"volumes": volumes or [], "containers": [{"name": "c", "env": env or [], "envFrom": env_from or []}]}}


class SecretReaders(unittest.TestCase):
    """AC-13."""

    def test_every_way_a_pod_reads_a_secret_is_seen(self):
        name = "grafana-telemetry"
        self.assertTrue(live.references_secret(pod("x", "a", volumes=[{"secret": {"secretName": name}}]), name))
        self.assertTrue(live.references_secret(
            pod("x", "a", volumes=[{"projected": {"sources": [{"secret": {"name": name}}]}}]), name))
        self.assertTrue(live.references_secret(
            pod("x", "a", env=[{"name": "T", "valueFrom": {"secretKeyRef": {"name": name, "key": "token"}}}]), name))
        self.assertTrue(live.references_secret(pod("x", "a", env_from=[{"secretRef": {"name": name}}]), name))
        self.assertFalse(live.references_secret(pod("x", "a", env=[{"name": "T", "value": name}]), name))

    def test_only_the_releases_pods_count_as_inside(self):
        self.assertTrue(live.in_release(pod("observability", "a", {"app.kubernetes.io/instance": "k8s-monitoring-fleet"})))
        self.assertFalse(live.in_release(pod("andara-dev", "a", {"app.kubernetes.io/instance": "k8s-monitoring-fleet"})))
        self.assertFalse(live.in_release(pod("observability", "a", {"app.kubernetes.io/instance": "debug"})))


class PodInventory(unittest.TestCase):
    """PR #657: a pod list that can't be read fails the checks that read it, never passes them empty."""

    def run_with(self, doc):
        r, out = live.Run(), io.StringIO()
        with redirect_stdout(out):
            live.pod_checks(r, doc)
        r.out = out.getvalue()
        return r

    def test_an_unreadable_inventory_fails(self):
        r = self.run_with(None)
        self.assertTrue([f for f in r.failures if "pods in all namespaces could not be listed" in f], r.failures)
        self.assertNotIn("ok   ", r.out)

    def test_a_listed_empty_inventory_passes(self):
        self.assertEqual(self.run_with({"items": []}).failures, [])

    def test_a_node_exporter_and_an_outside_reader_fail(self):
        r = self.run_with({"items": [pod("observability", "k8s-monitoring-node-exporter-x"),
                                     pod("andara-dev", "a", volumes=[{"secret": {"secretName": "grafana-telemetry"}}])]})
        self.assertTrue([f for f in r.failures if "node-exporter" in f], r.failures)
        self.assertTrue([f for f in r.failures if "outside the release: andara-dev/a" in f], r.failures)


class Readiness(unittest.TestCase):

    def test_a_daemonset_and_a_deployment_are_read_by_their_own_fields(self):
        self.assertEqual(live.ready({"kind": "DaemonSet", "status": {"desiredNumberScheduled": 4, "numberReady": 4}}),
                         (True, 4, 4))
        self.assertEqual(live.ready({"kind": "Deployment", "spec": {"replicas": 1}, "status": {}}), (False, 0, 1))
        self.assertEqual(live.ready(None), (False, 0, 1))

    def test_readiness_is_polled_until_it_holds(self):
        docs = iter([None, {"kind": "Deployment", "spec": {"replicas": 1}, "status": {}},
                     {"kind": "Deployment", "spec": {"replicas": 1}, "status": {"readyReplicas": 1}}])
        clock = [0.0]
        with mock.patch.object(live, "kjson", lambda *a: next(docs)):
            ok, _, _ = live.wait_ready({}, "deployment", "x", sleep=lambda s: clock.__setitem__(0, clock[0] + s),
                                       now=lambda: clock[0])
        self.assertTrue(ok)
        self.assertEqual(clock[0], 2 * live.POLL)

    def test_readiness_that_never_holds_fails_at_the_deadline(self):
        clock = [0.0]
        with mock.patch.object(live, "kjson", lambda *a: None):
            ok, _, _ = live.wait_ready({}, "daemonset", "x", timeout=30,
                                       sleep=lambda s: clock.__setitem__(0, clock[0] + s), now=lambda: clock[0])
        self.assertFalse(ok)
        self.assertGreaterEqual(clock[0], 30)


class FakeGcx:
    GcxError = live.gcx.GcxError

    def __init__(self, control_after=0, control_stamp=None, unkept=False, labels=None):
        self.polls = 0
        self.control_after = control_after
        self.control_stamp = control_stamp
        self.unkept = unkept
        self.labels = labels or {"environment": "unknown", "cluster": "andara"}
        self.series_calls = []

    def metrics_query(self, ctx, expr, env=None, since=None):
        if expr.startswith("timestamp("):
            self.polls += 1
            if self.polls <= self.control_after:
                return []
            return [{"metric": {}, "value": [0, str(self.control_stamp)]}]
        return [{"metric": dict(self.labels, instance=live.PROBE_INSTANCE)}]

    def metrics_series(self, ctx, selector, since=None, env=None):
        self.series_calls.append((self.polls, selector))
        return [{"__name__": live.UNKEPT}] if self.unkept else []


class Probe(unittest.TestCase):
    """AC-7's second half."""

    def setUp(self):
        self.clock = [1000.0]
        for name, value in (("tool", lambda n, v: "/bin/true"), ("push", lambda *a: None), ("free_port", lambda: 1)):
            p = mock.patch.object(live, name, value)
            p.start()
            self.addCleanup(p.stop)
        p = mock.patch.object(live.subprocess, "Popen")
        p.start()
        self.addCleanup(p.stop)
        p = mock.patch.object(live.kube, "require", lambda env=None, ctx=None: "kind-andara")
        p.start()
        self.addCleanup(p.stop)
        p = mock.patch.object(live.socket, "create_connection", mock.MagicMock())
        p.start()
        self.addCleanup(p.stop)

    def now(self):
        return self.clock[0]

    def sleep(self, s):
        self.clock[0] += s

    def probe(self, q):
        r = live.Run()
        with redirect_stdout(io.StringIO()):
            live.probe(r, {"ANDARA_KUBE_CONTEXT": "kind-andara"}, q=q, now=self.now, sleep=self.sleep)
        return r

    def test_a_kept_control_and_a_dropped_probe_pass(self):
        q = FakeGcx(control_after=3, control_stamp=1001)
        self.assertEqual(self.probe(q).failures, [])

    def test_absence_is_read_only_after_the_control_arrived(self):
        """Rule 3: the anchor is the control's own sample, newer than the push."""
        q = FakeGcx(control_after=3, control_stamp=1001)
        self.probe(q)
        self.assertEqual([p for p, _ in q.series_calls], [4])

    def test_an_unkept_series_that_arrived_fails(self):
        r = self.probe(FakeGcx(control_stamp=1001, unkept=True))
        self.assertTrue([f for f in r.failures if "kafka_probe_unkept is absent" in f], r.failures)

    def test_a_control_that_never_arrives_fails_and_asserts_no_absence(self):
        q = FakeGcx(control_after=10 ** 6, control_stamp=1001)
        r = self.probe(q)
        self.assertTrue([f for f in r.failures if "arrived in solo7dev" in f], r.failures)
        self.assertEqual(q.series_calls, [])

    def test_a_control_sample_from_before_the_push_is_not_the_anchor(self):
        """A sample older than the push (minus skew) is a stale series, not this push's."""
        r = self.probe(FakeGcx(control_stamp=900))
        self.assertTrue([f for f in r.failures if "arrived in solo7dev" in f], r.failures)

    def test_an_unstamped_control_fails(self):
        r = self.probe(FakeGcx(control_stamp=1001, labels={"cluster": "andara"}))
        self.assertTrue([f for f in r.failures if "environment=None" in f], r.failures)


class Inventory(unittest.TestCase):
    """AC-3."""

    def run_with(self, items):
        r = live.Run()
        with mock.patch.object(live.gcx, "run", lambda *a, **k: {"items": items}), redirect_stdout(io.StringIO()):
            live.fleet_inventory(r, {})
        return r

    def collector(self, name, inactive=False, **attrs):
        spec = {"local_attributes": dict({"cluster": "andara", "stack": "solo7dev", "workloadName": "fleet",
                                          "collector.ID": name}, **attrs)}
        if inactive:
            spec["marked_inactive_at"] = "2026-10-25T15:53:17Z"
        return {"spec": spec}

    def test_one_active_collector_passes_and_inactive_ones_are_ignored(self):
        r = self.run_with([self.collector("old", inactive=True), self.collector("new")])
        self.assertEqual(r.failures, [])

    def test_two_active_collectors_fail(self):
        r = self.run_with([self.collector("a"), self.collector("b")])
        self.assertTrue([f for f in r.failures if "2 active collector(s)" in f])

    def test_an_environment_attribute_fails(self):
        r = self.run_with([self.collector("a", environment="dev")])
        self.assertTrue([f for f in r.failures if "environment=dev" in f])


class Usage(unittest.TestCase):

    def test_an_unknown_command_is_exit_2(self):
        with redirect_stdout(io.StringIO()), mock.patch("sys.stderr", io.StringIO()):
            self.assertEqual(live.main(["nope"]), 2)


if __name__ == "__main__":
    unittest.main()
