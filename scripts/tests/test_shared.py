# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""`make shared-check` and `make shared-down` (AW-INF-055 AC-3, AC-8, AC-11), over fake readings."""
import contextlib
import io
import json
import os
import subprocess
import sys
import types
import unittest

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
sys.path.insert(0, os.path.join(REPO, "scripts"))
import shared  # noqa: E402

GI = 1024 ** 3


def pvc(name, cap="20Gi"):
    return {"metadata": {"name": name}, "spec": {"resources": {"requests": {"storage": cap}}},
            "status": {"capacity": {"storage": cap}}}


def pod(name, claim, phase="Running", ready=True, labels=None):
    return {"metadata": {"name": name, "labels": labels or {}},
            "spec": {"volumes": [{"name": "data", "persistentVolumeClaim": {"claimName": claim}}],
                     "containers": [{"name": "kafka", "volumeMounts": [{"name": "data", "mountPath": "/var/lib/kafka/data"}]}]},
            "status": {"phase": phase, "containerStatuses": [{"ready": ready}]}}


def log_dirs(*parts):
    """kafka-log-dirs output: a prose line, then the JSON."""
    brokers = {}
    for broker, partition, size in parts:
        brokers.setdefault(broker, []).append({"partition": partition, "size": size})
    doc = {"brokers": [{"broker": b, "logDirs": [{"logDir": "/var/lib/kafka/data/kafka-log%d" % b, "partitions": ps}]}
                       for b, ps in brokers.items()]}
    return "Querying brokers for log directories information\nReceived log directory information from brokers 0,1,2\n" + json.dumps(doc)


class FakeCluster:
    """kubectl as shared.check uses it: get pvc, get pods, exec du, exec kafka-log-dirs."""

    def __init__(self, used, dirs, pvcs=None, pods=None):
        self.used = used            # {pod: bytes}
        self.dirs = dirs
        self.pvcs = pvcs if pvcs is not None else [pvc("data-andara-log-broker-%d" % i) for i in range(3)]
        self.pods = pods if pods is not None else [
            pod("andara-log-broker-%d" % i, "data-andara-log-broker-%d" % i) for i in range(3)]
        self.calls = []

    def __call__(self, cmd, capture_output=True, text=True):
        self.calls.append(cmd)
        args = cmd[cmd.index("-n") + 2:]
        ok = lambda out: types.SimpleNamespace(returncode=0, stdout=out, stderr="")  # noqa: E731
        if args[:2] == ["get", "pvc"]:
            return ok(json.dumps({"items": self.pvcs}))
        if args[:2] == ["get", "pods"]:
            return ok(json.dumps({"items": self.pods}))
        if args[0] == "exec" and "du" in args:
            return ok("%d\t/var/lib/kafka/data\n" % (self.used[args[1]] // 1024))
        if args[0] == "exec" and args[-1] == "--describe":
            return ok(self.dirs)
        raise AssertionError("unexpected kubectl %r" % (args,))


class Units(unittest.TestCase):
    def test_quantities(self):
        self.assertEqual(shared.quantity("20Gi"), 20 * GI)
        self.assertEqual(shared.quantity("500M"), 500 * 1000 ** 2)
        self.assertEqual(shared.quantity("1024"), 1024)


class Check(unittest.TestCase):
    def run_check(self, cluster):
        out, err = io.StringIO(), io.StringIO()
        code = 0
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            try:
                shared.check(ns="andara-shared", run=cluster)
            except shared.Failed as e:
                print("shared: %s" % e, file=sys.stderr)
                code = e.code
        return code, out.getvalue(), err.getvalue()

    DIRS = log_dirs((0, "dev.andara.commands.v1-0", 3000), (0, "local.andara.commands.v1-0", 500),
                    (1, "dev.andara.commands.v1-1", 1000), (1, "__consumer_offsets-3", 70),
                    (2, "andara.test.foo-0", 9), (2, "it42.andara.commands.v1-0", 5))

    def test_under_the_threshold_prints_every_pvc_and_exits_0(self):
        c = FakeCluster({"andara-log-broker-0": 2 * GI, "andara-log-broker-1": 16 * GI,
                         "andara-log-broker-2": 0}, self.DIRS)
        code, out, err = self.run_check(c)
        self.assertEqual(code, 0, err)
        for i, pct in ((0, "10.0%"), (1, "80.0%"), (2, "0.0%")):
            self.assertIn("pvc data-andara-log-broker-%d" % i, out)
            self.assertIn(pct, out)
        self.assertNotIn("OVER", out)

    def test_over_the_threshold_names_the_pvc_and_exits_1(self):
        c = FakeCluster({"andara-log-broker-0": 2 * GI, "andara-log-broker-1": int(16.5 * GI),
                         "andara-log-broker-2": 0}, self.DIRS)
        code, out, err = self.run_check(c)
        self.assertEqual(code, 1)
        self.assertIn("pvc data-andara-log-broker-1 16.5 GiB of 20.0 GiB (82.5%) OVER 80%", out)
        self.assertIn("over 80%: data-andara-log-broker-1 (82.5%)", err)
        # The prefixes are still reported, so the operator sees which environment is filling it.
        self.assertIn("broker 1 dev.=1000 B local.=0 B", out)

    def test_bytes_are_attributed_to_each_prefix_per_broker(self):
        c = FakeCluster({p["metadata"]["name"]: 0 for p in FakeCluster({}, "").pods}, self.DIRS)
        _, out, _ = self.run_check(c)
        self.assertIn("broker 0 dev.=2.9 KiB local.=500 B", out)
        self.assertIn("broker 2 dev.=0 B local.=0 B", out)

    def test_topics_outside_both_prefixes_are_listed_on_their_own_lines(self):
        c = FakeCluster({p["metadata"]["name"]: 0 for p in FakeCluster({}, "").pods}, self.DIRS)
        code, out, _ = self.run_check(c)
        self.assertEqual(code, 0)
        for t in ("__consumer_offsets", "andara.test.foo", "it42.andara.commands.v1"):
            self.assertIn("other %s " % t, out)

    def test_an_unprefixed_andara_topic_fails(self):
        dirs = log_dirs((0, "andara.commands.v1-0", 10))
        c = FakeCluster({p["metadata"]["name"]: 0 for p in FakeCluster({}, "").pods}, dirs)
        code, out, err = self.run_check(c)
        self.assertEqual(code, 1)
        self.assertIn("stray andara.commands.v1", out)
        self.assertIn("unprefixed topics on the shared broker: andara.commands.v1", err)

    def test_an_unmounted_pvc_is_unmeasured_not_zero(self):
        pods = [pod("andara-log-broker-0", "data-andara-log-broker-0"),
                pod("andara-log-broker-1", "data-andara-log-broker-1", phase="Pending", ready=False)]
        c = FakeCluster({"andara-log-broker-0": GI}, self.DIRS, pvcs=[pvc("data-andara-log-broker-0"),
                                                                      pvc("data-andara-log-broker-1")], pods=pods)
        code, out, _ = self.run_check(c)
        self.assertEqual(code, 0)
        self.assertIn("pvc data-andara-log-broker-1 unmeasured", out)

    def test_a_broker_that_cannot_be_measured_is_named_and_the_rest_still_report(self):
        # A full disk crash-loops the broker: its pod stays Running and exec into it fails.
        c = FakeCluster({"andara-log-broker-0": 2 * GI, "andara-log-broker-2": GI}, self.DIRS)
        c.pods[1]["status"]["containerStatuses"] = [{"ready": False}]
        real = c.__call__

        def run(cmd, capture_output=True, text=True):
            if "andara-log-broker-1" in cmd and "du" in cmd:
                return types.SimpleNamespace(returncode=1, stdout="",
                                             stderr='error: container not found ("kafka")')
            return real(cmd)

        code, out, err = self.run_check(run)
        self.assertEqual(code, 1)
        self.assertIn("pvc data-andara-log-broker-1 unmeasured: measuring it in andara-log-broker-1 failed", out)
        self.assertIn("pvc data-andara-log-broker-0 2.0 GiB of 20.0 GiB (10.0%)", out)
        self.assertIn("pvc data-andara-log-broker-2", out)
        self.assertIn("broker 0 dev.=", out)
        self.assertIn("not measured, so not known to be under 80%: data-andara-log-broker-1", err)

    def test_no_ready_broker_fails(self):
        pods = [pod("andara-log-broker-0", "data-andara-log-broker-0", ready=False)]
        for p in pods:
            p["metadata"]["labels"] = {"strimzi.io/cluster": "andara-log"}
        c = FakeCluster({"andara-log-broker-0": GI}, self.DIRS, pvcs=[pvc("data-andara-log-broker-0")], pods=pods)
        code, _, err = self.run_check(c)
        self.assertEqual(code, 1)
        self.assertIn("no Ready broker", err)


class ListenerPeers(unittest.TestCase):
    """Codex on #618: shared-check's kafka-log-dirs runs in a broker pod and asks every broker, so
    the listener's NetworkPolicy must admit the broker pool shared-check picks from. It must
    admit only that pool in andara-shared, alongside the application peers."""

    def peers(self):
        import yaml
        with open(os.path.join(REPO, "deploy", "k8s", "kafka", "kafka.yaml")) as f:
            kafka = next(d for d in yaml.safe_load_all(f) if d and d["kind"] == "Kafka")
        listener = next(li for li in kafka["spec"]["kafka"]["listeners"] if li["port"] == 9092)
        return listener["networkPolicyPeers"]

    def test_the_broker_pool_shared_check_uses_is_admitted(self):
        want = dict(kv.split("=") for kv in ("strimzi.io/cluster=%s" % shared.KAFKA, "strimzi.io/pool-name=broker"))
        brokers = [p for p in self.peers() if "namespaceSelector" not in p
                   and p.get("podSelector", {}).get("matchLabels") == want]
        self.assertEqual(len(brokers), 1, self.peers())

    def test_application_pods_still_need_a_labelled_namespace(self):
        apps = [p for p in self.peers() if p.get("podSelector", {}).get("matchLabels") == {"app.kubernetes.io/name": "andara"}]
        self.assertEqual(len(apps), 1)
        self.assertEqual(apps[0]["namespaceSelector"]["matchExpressions"][0]["values"], ["local", "dev"])


class Down(unittest.TestCase):
    def make(self, *args):
        # PATH without kubectl: a refusal that reached the cluster would fail differently.
        env = dict(os.environ, PATH="/usr/bin:/bin")
        return subprocess.run(["make", "-s", "-C", REPO, "shared-down", *args],
                              capture_output=True, text=True, env=env)

    def test_without_confirm_it_exits_2_and_removes_nothing(self):
        for args in ((), ("CONFIRM=andara-dev",), ("CONFIRM=andara",)):
            with self.subTest(args=args):
                r = self.make(*args)
                self.assertEqual(r.returncode, 2, r.stderr)
                self.assertIn("nothing was removed", r.stderr)

    def test_confirmed_it_deletes_the_kafka_then_its_pvcs_and_never_the_namespace(self):
        calls = []

        def run(cmd, capture_output=True, text=True):
            calls.append(cmd[cmd.index("-n") + 2:])
            return types.SimpleNamespace(returncode=0, stdout="", stderr="")

        # The fixture kubeconfig (scripts-test) holds kind-andara, so kube.require passes offline.
        with contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
            shared.down("andara-shared", ns="andara-shared", run=run)
        self.assertEqual([c[:2] for c in calls], [["delete", "-f"], ["delete", "pvc"]])
        self.assertIn("strimzi.io/cluster=andara-log", calls[1])
        self.assertFalse([c for c in calls if "namespace" in c])


if __name__ == "__main__":
    unittest.main()
