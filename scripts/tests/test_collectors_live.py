# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""make collectors-check and collectors-probe (AW-INF-065) against fakes: each assertion fails on the
state that breaks it, and the probe's absence is anchored on the control's arrival."""
import io
import json
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


class NotReady(unittest.TestCase):
    """A collector that isn't Ready prints why: its pods' states, their events and their last log lines
    (lifecycle run 40387333385: receiver 0/1 with nothing to read)."""

    def test_the_pods_states_events_and_logs_are_printed(self):
        stuck = pod("observability", "k8s-monitoring-receiver-abc-1")
        stuck["status"] = {"phase": "Running",
                           "conditions": [{"type": "Ready", "status": "False", "reason": "ContainersNotReady"}],
                           "containerStatuses": [{"name": "alloy", "ready": False, "restartCount": 4,
                                                  "state": {"waiting": {"reason": "CrashLoopBackOff"}}}]}
        other = pod("observability", "k8s-monitoring-metrics-abc-1")

        calls = []

        def kubectl(env, *args):
            calls.append(args)
            if args[2:4] == ("get", "events"):
                return mock.Mock(returncode=0, stdout="Warning BackOff restarting failed container\n", stderr="")
            if args[2] == "logs":
                return mock.Mock(returncode=1, stdout="", stderr="Error from server (Forbidden): pods/log")
            return mock.Mock(returncode=1, stdout="", stderr="unexpected")
        out = io.StringIO()
        with mock.patch.object(live, "kjson", lambda env, *a: {"items": [stuck, other]}), \
                mock.patch.object(live, "kubectl", kubectl), redirect_stdout(out):
            live.diagnose(live.Run(), {}, "receiver")
        text = out.getvalue()
        self.assertIn("k8s-monitoring-receiver-abc-1: phase Running, Ready=False (ContainersNotReady)", text)
        self.assertIn("alloy: waiting CrashLoopBackOff, 4 restart(s)", text)
        self.assertIn("Warning BackOff restarting failed container", text)
        self.assertIn("Error from server (Forbidden): pods/log", text, "a failed read prints its stderr")
        self.assertIn(("-n", "observability", "logs", "k8s-monitoring-receiver-abc-1", "--all-containers", "--tail=20"),
                      calls)
        self.assertNotIn("metrics-abc-1", text)

    RS_EVENT = {"involvedObject": {"kind": "ReplicaSet", "name": "k8s-monitoring-receiver-85579d4f9f"},
                "type": "Warning", "reason": "FailedCreate", "message": "exceeded quota"}

    def no_pods(self, pods_doc, alloy=True, events=True):
        def kjson(env, *args):
            if args[2:4] == ("get", "pods"):
                return pods_doc
            if args[2:5] == ("get", "alloy", "k8s-monitoring-receiver") and alloy:
                return {"status": {"conditions": [{"type": "Deployed", "status": "False", "reason": "InstallFailed",
                                                   "message": "quota exceeded"}]}}
            if args[2:4] == ("get", "events") and events:
                return {"items": [self.RS_EVENT,
                                  {"involvedObject": {"kind": "Deployment", "name": "k8s-monitoring-receiver"},
                                   "type": "Normal", "reason": "ScalingReplicaSet", "message": "scaled up to 1"},
                                  {"involvedObject": {"kind": "Pod", "name": "k8s-monitoring-receiverx-1"},
                                   "type": "Normal", "reason": "Pulled", "message": "unrelated"}]}
            return None
        out = io.StringIO()
        with mock.patch.object(live, "kjson", kjson), redirect_stdout(out):
            live.diagnose(live.Run(), {}, "receiver")
        return out.getvalue()

    def test_no_pods_prints_the_collectors_own_conditions_and_its_replicasets_events(self):
        """The operator never made the Deployment, or the ReplicaSet can't make pods: the reason is on the
        Alloy resource and on the ReplicaSet (FailedCreate), not on a pod."""
        text = self.no_pods({"items": []})
        self.assertIn("receiver: no pods", text)
        self.assertIn("alloy k8s-monitoring-receiver: Deployed=False (InstallFailed: quota exceeded)", text)
        self.assertIn("ReplicaSet/k8s-monitoring-receiver-85579d4f9f: Warning FailedCreate: exceeded quota", text)
        self.assertIn("Deployment/k8s-monitoring-receiver: Normal ScalingReplicaSet: scaled up to 1", text,
                      "the workload's own events (a DaemonSet's FailedCreate) match by exact name")
        self.assertNotIn("unrelated", text, "a name that only shares the prefix, with no '-', is another object")

    def test_unreadable_alloy_resource_and_events_say_so(self):
        text = self.no_pods({"items": []}, alloy=False, events=False)
        self.assertIn("alloy k8s-monitoring-receiver: could not be read", text)
        self.assertIn("k8s-monitoring-receiver: events could not be read", text)

    def test_an_unreadable_pod_list_says_so(self):
        self.assertIn("receiver: no pods (the pod list could not be read)", self.no_pods(None))


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
    """AC-3: one active collector, counted among the running fleet pods' own. A replaced pod's collector
    stays active in Fleet for up to ~3h when it couldn't unregister (a revoked token), so it isn't counted."""

    PODS = ["k8s-monitoring-fleet-abc-1"]

    def run_with(self, items, pods=None):
        """items: one Fleet listing, or a list of listings returned in turn (the last one repeats)."""
        listings = items if items and isinstance(items[0], list) else [items]
        calls, clock = [], [0.0]

        def run(*a, **k):
            calls.append(1)
            return {"items": listings[min(len(calls), len(listings)) - 1]}
        r = live.Run()
        with mock.patch.object(live.gcx, "run", run), redirect_stdout(io.StringIO()):
            live.fleet_inventory(r, {}, lambda: self.PODS if pods is None else pods,
                                 sleep=lambda s: clock.__setitem__(0, clock[0] + s), now=lambda: clock[0])
        r.calls = len(calls)
        return r

    def test_the_inventory_is_polled_until_the_new_pod_registers(self):
        """A restarted fleet pod is Ready before its remotecfg registers (live-assertions.md rule 2)."""
        r = self.run_with([[], [self.collector("k8s-monitoring-fleet-abc-1")]])
        self.assertEqual(r.failures, [])
        self.assertEqual(r.calls, 2)

    def test_a_collector_that_never_registers_fails_at_the_deadline(self):
        r = self.run_with([])
        self.assertTrue([f for f in r.failures if "0 active collector(s)" in f], r.failures)
        self.assertGreater(r.calls, 1)

    def test_unlistable_fleet_pods_fail(self):
        r2 = live.Run()
        with mock.patch.object(live.gcx, "run", lambda *a, **k: {"items": []}), redirect_stdout(io.StringIO()):
            live.fleet_inventory(r2, {}, lambda: None, sleep=lambda s: None, now=iter(range(0, 10000, 100)).__next__)
        self.assertTrue([f for f in r2.failures if "fleet's pods could not be listed" in f], r2.failures)

    def collector(self, pod, inactive=False, **attrs):
        name = "k8s-monitoring-andara-observability-" + pod
        spec = {"local_attributes": dict({"cluster": "andara", "stack": "solo7dev", "workloadName": "fleet",
                                          "collector.ID": name}, **attrs)}
        if inactive:
            spec["marked_inactive_at"] = "2026-10-25T15:53:17Z"
        return {"spec": spec}

    def test_one_active_collector_passes_and_inactive_ones_are_ignored(self):
        r = self.run_with([self.collector("k8s-monitoring-fleet-old-0", inactive=True),
                           self.collector("k8s-monitoring-fleet-abc-1")])
        self.assertEqual(r.failures, [])

    def test_a_gone_pods_collector_still_marked_active_is_not_counted(self):
        r = self.run_with([self.collector("k8s-monitoring-fleet-old-0"), self.collector("k8s-monitoring-fleet-abc-1")])
        self.assertEqual(r.failures, [])

    def test_two_running_pods_each_registered_fail(self):
        pods = ["k8s-monitoring-fleet-abc-1", "k8s-monitoring-fleet-abc-2"]
        r = self.run_with([self.collector(p) for p in pods], pods)
        self.assertTrue([f for f in r.failures if "2 active collector(s)" in f], r.failures)

    def test_a_running_pod_with_no_collector_fails(self):
        r = self.run_with([self.collector("k8s-monitoring-fleet-old-0")])
        self.assertTrue([f for f in r.failures if "0 active collector(s)" in f], r.failures)

    def test_a_pod_name_that_is_only_a_prefix_does_not_match(self):
        r = self.run_with([self.collector("k8s-monitoring-fleet-abc-12")], ["k8s-monitoring-fleet-abc-1"])
        self.assertTrue([f for f in r.failures if "0 active collector(s)" in f], r.failures)

    def test_an_environment_attribute_fails(self):
        r = self.run_with([self.collector("k8s-monitoring-fleet-abc-1", environment="dev")])
        self.assertTrue([f for f in r.failures if "environment=dev" in f], r.failures)


class FleetPods(unittest.TestCase):

    def fleet(self, name, ns="observability", phase="Running", labels=None, deleting=False):
        p = pod(ns, name, {"app.kubernetes.io/instance": "k8s-monitoring-fleet"} if labels is None else labels)
        p["status"] = {"phase": phase}
        if deleting:
            p["metadata"]["deletionTimestamp"] = "2026-10-26T18:33:45Z"
        return p

    def test_only_running_fleet_pods_in_observability_that_are_not_terminating(self):
        doc = {"items": [self.fleet("k8s-monitoring-fleet-abc-1"),
                         self.fleet("k8s-monitoring-fleet-pending", phase="Pending"),
                         self.fleet("k8s-monitoring-fleet-done", phase="Succeeded"),
                         self.fleet("k8s-monitoring-fleet-elsewhere", ns="andara-dev"),
                         self.fleet("k8s-monitoring-fleet-unlabelled", labels={}),
                         self.fleet("k8s-monitoring-fleet-old", deleting=True)]}
        self.assertEqual(live.fleet_pods(doc), ["k8s-monitoring-fleet-abc-1"])


class Check(unittest.TestCase):
    """check() end to end against fakes: what it hands pod_checks and fleet_inventory."""

    def run_check(self, pods_doc, ready=lambda kind, name: True):
        def kjson(env, *args):
            if args[:3] == ("get", "pods", "-A") or args[2:4] == ("get", "pods"):
                return pods_doc
            if args[:2] == ("get", "crd"):
                return {"metadata": {"creationTimestamp": "2026-10-25T15:49:09Z"}}
            if args[2:4] == ("get", "alloy"):
                return {"items": [{"metadata": {"creationTimestamp": "2026-10-25T15:49:16Z"}}]}
            if args[2:4] == ("get", "secret"):
                return {"data": {k: "eA==" for k in live.SECRET_KEYS["box"]}}
            return None
        helm_rows = json.dumps([{"name": live.RELEASE, "chart": "k8s-monitoring-4.5.3", "status": "deployed"}])
        seen, out = {}, io.StringIO()
        r = live.Run()
        with mock.patch.object(live, "kjson", kjson), \
                mock.patch.object(live.kube, "require", lambda **k: None), \
                mock.patch.object(live.kube, "helm", lambda *a, **k: ["helm"]), \
                mock.patch.object(live, "tool", lambda *a: "/bin/true"), \
                mock.patch.object(live.subprocess, "run", lambda *a, **k: mock.Mock(stdout=helm_rows)), \
                mock.patch.object(live, "wait_ready", lambda env, kind, name, **k: (True, 1, 1) if ready(kind, name)
                                  else (False, 0, 1)), \
                mock.patch.object(live, "fleet_inventory", lambda r, env, list_pods, **k: seen.setdefault("pods", list_pods())), \
                redirect_stdout(out):
            live.check(r, "box", {})
        r.out = out.getvalue()
        return r, seen

    def test_a_collector_that_is_not_ready_is_diagnosed_and_the_rest_still_run(self):
        seen = []
        with mock.patch.object(live, "diagnose", lambda r, env, name: seen.append(name)):
            r, _ = self.run_check({"items": []}, ready=lambda kind, name: name != "k8s-monitoring-receiver")
        self.assertEqual(seen, ["receiver"])
        self.assertTrue([f for f in r.failures if "k8s-monitoring-receiver Ready 0/1" in f], r.failures)
        self.assertTrue([o for o in r.out.splitlines() if "daemonset k8s-monitoring-logs Ready" in o], r.out)

    def test_an_unreadable_pod_list_fails_and_skips_the_inventory(self):
        r, seen = self.run_check(None)
        self.assertTrue([f for f in r.failures if "pods in all namespaces could not be listed" in f], r.failures)
        self.assertNotIn("pods", seen)

    def test_the_inventory_gets_the_running_fleet_pods(self):
        fleet = pod("observability", "k8s-monitoring-fleet-abc-1", {"app.kubernetes.io/instance": "k8s-monitoring-fleet"})
        fleet["status"] = {"phase": "Running"}
        other = pod("observability", "k8s-monitoring-metrics-x", {"app.kubernetes.io/instance": "k8s-monitoring-metrics"})
        r, seen = self.run_check({"items": [fleet, other]})
        self.assertEqual(r.failures, [])
        self.assertEqual(seen["pods"], ["k8s-monitoring-fleet-abc-1"])


class Usage(unittest.TestCase):

    def test_an_unknown_command_is_exit_2(self):
        with redirect_stdout(io.StringIO()), mock.patch("sys.stderr", io.StringIO()):
            self.assertEqual(live.main(["nope"]), 2)


if __name__ == "__main__":
    unittest.main()
