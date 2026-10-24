# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""`make shared-check` and `make shared-down` (AW-INF-055 AC-3, AC-8, AC-11), over fake readings."""
import contextlib
import io
import json
import os
import shutil
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

    def test_the_peers_are_exactly_the_apps_the_toolbox_and_the_broker_pool(self):
        # The whole list: any extra peer, a widened selector or an operator change fails it.
        self.assertEqual(self.peers(), [
            {"namespaceSelector": {"matchExpressions": [
                {"key": "andara.valesor/env", "operator": "In", "values": ["local", "dev"]}]},
             "podSelector": {"matchLabels": {"app.kubernetes.io/name": "andara"}}},
            {"podSelector": {"matchLabels": {"app.kubernetes.io/name": "andara-kafka-tools"}}},
            # The pool shared-check picks its pod from (shared.broker_pod), in andara-shared only.
            {"podSelector": {"matchLabels": {"strimzi.io/cluster": shared.KAFKA, "strimzi.io/pool-name": "broker"}}},
            # The schema registry, which keeps its schemas in _schemas (AW-INF-057).
            {"podSelector": {"matchLabels": {"app.kubernetes.io/name": "andara-schema-registry"}}},
        ])


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

    def test_confirmed_it_deletes_the_kafka_its_pvcs_and_the_object_store_never_the_namespace(self):
        calls = []

        def run(cmd, capture_output=True, text=True):
            calls.append(cmd[cmd.index("-n") + 2:])
            return types.SimpleNamespace(returncode=0, stdout="", stderr="")

        # The fixture kubeconfig (scripts-test) holds kind-andara, so kube.require passes offline.
        with contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
            shared.down("andara-shared", ns="andara-shared", run=run)
        self.assertEqual([c[:2] for c in calls], [["delete", "-f"], ["delete", "-f"], ["delete", "pvc"],
                                                  ["delete", "-f"], ["delete", "secret"]])
        self.assertEqual(calls[0][2], shared.REGISTRY_MANIFESTS)  # the registry before its broker
        self.assertEqual(calls[1][2], shared.KAFKA_MANIFESTS)
        self.assertIn("strimzi.io/cluster=andara-log", calls[2])
        self.assertEqual(calls[3][2], shared.OBJECTSTORE_MANIFEST)
        self.assertIn("andara-objectstore-root", calls[4])
        self.assertFalse([c for c in calls if "namespace" in c])


class FakeNet:
    """kubectl as shared.netcheck uses it. verdicts maps (namespace, pod) to a list of probe outputs,
    one per exec, the last repeated; a namespace in scratch_like stands for the unlabelled one. With
    no override, each probe answers as shared.NET_WANTS says its listener admits it, so a correctly
    declared policy passes for every listener."""

    BLOCKED = "probe-ran\nresolved\nanchored\nblocked\n"
    CONNECTED = "probe-ran\nresolved\nanchored\nconnected\n"
    PORTS = {"9092": "kafka", "9000": "objectstore", "8081": "registry"}

    def __init__(self, verdicts=None, labels=None, fail=(), objectstore=False, registry=False):
        self.labels = labels if labels is not None else {"andara-local": "local", "andara-dev": "dev"}
        self.verdicts = verdicts or {}
        self.fail = set(fail)       # of "create", "apply", "wait"
        self.objectstore = objectstore  # whether andara-shared has the gateway's Service (AW-INF-058)
        self.registry = registry    # whether it has the schema registry's (AW-INF-057)
        self.calls, self.applied, self.execs, self.ready = [], [], {}, set()
        self.hosts = []             # (namespace, pod, host:port) per exec

    def __call__(self, cmd, capture_output=True, text=True, input=None):
        args = cmd[cmd.index("--context") + 2:]
        ns = None
        if args[:1] == ["-n"]:
            ns, args = args[1], args[2:]
        self.calls.append((ns, args))
        ok = lambda out="": types.SimpleNamespace(returncode=0, stdout=out, stderr="")  # noqa: E731
        if args[:2] == ["get", "namespace"]:
            if args[2] not in self.labels:
                return types.SimpleNamespace(returncode=1, stdout="", stderr="NotFound")
            lab = {"andara.valesor/env": self.labels[args[2]]} if self.labels[args[2]] else {}
            return ok(json.dumps({"metadata": {"name": args[2], "labels": lab}}))
        no = types.SimpleNamespace(returncode=1, stdout="", stderr="boom")
        if args[:3] == ["get", "service", "andara-objectstore"]:
            return ok("service/andara-objectstore") if self.objectstore else \
                types.SimpleNamespace(returncode=1, stdout="", stderr="NotFound")
        if args[:3] == ["get", "service", "andara-schema-registry"]:
            return ok("service/andara-schema-registry") if self.registry else \
                types.SimpleNamespace(returncode=1, stdout="", stderr="NotFound")
        if args[:2] == ["apply", "-f"]:
            if "apply" in self.fail:
                return no
            self.applied.append(json.loads(input))
            return ok()
        if args[0] == "create" and "create" in self.fail:
            return no
        if args[0] == "wait":
            if "wait" in self.fail:
                return no
            self.ready.add((ns, args[2].split("/", 1)[1]))
            return ok()
        if args[:2] == ["get", "pod"]:
            return ok(json.dumps({"status": {"phase": "Pending", "containerStatuses": [
                {"state": {"waiting": {"reason": "ImagePullBackOff"}}}]}}))
        if args[0] in ("create", "delete"):
            return ok()
        if args[0] == "exec":
            pod = args[1]
            if (ns, pod) not in self.ready:
                raise AssertionError("exec in %s/%s before its Ready wait" % (ns, pod))
            key = (ns, pod) if (ns, pod) in self.verdicts else ("scratch", pod) if ns.startswith("andara-netcheck-") else (ns, pod)
            seq = self.verdicts.get(key) or [self.CONNECTED if shared.NET_WANTS[self.PORTS[args[-1]]][
                self.case(ns, pod)] == "connected" else self.BLOCKED]
            n = self.execs.get((ns, pod), 0)
            self.execs[(ns, pod)] = n + 1
            # exec <pod> -c probe -- bash -c SCRIPT <host> <anchor> <port>
            self.hosts.append((ns, pod, "%s:%s" % (args[-3], args[-1])))
            return ok(seq[min(n, len(seq) - 1)])
        raise AssertionError("unexpected kubectl %r" % (args,))

    @staticmethod
    def case(ns, pod):
        """The probe's key in shared.NET_WANTS."""
        if ns.startswith("andara-netcheck-"):
            return "scratch"
        if pod.endswith("-other"):
            return "other"
        if pod.endswith("-tools"):
            return "tools"
        return ns.replace("andara-", "")


@unittest.skipUnless(shutil.which("getent") and shutil.which("timeout") and shutil.which("bash"),
                     "the probe script needs getent, timeout and bash, as the toolbox image has")
class ProbeScript(unittest.TestCase):
    """NET_SCRIPT itself, run locally by bash in place of `kubectl exec`: the anchoring is the
    shell's, so a canned output can't test it (pre-review round 2)."""

    @staticmethod
    def local(cmd, capture_output=True, text=True):
        return subprocess.run(cmd[cmd.index("--") + 1:], capture_output=True, text=True)

    def setUp(self):
        import socket
        self.listener = socket.socket()
        self.listener.bind(("127.0.0.1", 0))
        self.listener.listen(8)
        self.addCleanup(self.listener.close)
        self.open = str(self.listener.getsockname()[1])
        closed = socket.socket()
        closed.bind(("127.0.0.1", 0))
        self.closed = str(closed.getsockname()[1])
        closed.close()

    def probe(self, host, anchor_port, port):
        return shared.probe_once("ns", "pod", host, run=self.local, anchor="127.0.0.1/" + anchor_port, port=port)

    def test_connected(self):
        self.assertEqual(self.probe("127.0.0.1", self.open, self.open), "connected")

    def test_blocked_when_it_resolved_and_reached_the_anchor(self):
        self.assertEqual(self.probe("127.0.0.1", self.open, self.closed), "blocked")

    def test_unanchored_when_the_anchor_is_unreachable(self):
        self.assertEqual(self.probe("127.0.0.1", self.closed, self.closed), "unanchored")

    def test_unanchored_when_the_host_doesnt_resolve(self):
        self.assertEqual(self.probe("no-such-host.invalid", self.open, self.closed), "unanchored")


class NetCheck(unittest.TestCase):
    """make shared-netcheck (AW-INF-055 AC-5), over a fake cluster."""

    def run_net(self, net, **kw):
        t = [0.0]

        def sleep(s):
            t[0] += s
        out, err = io.StringIO(), io.StringIO()
        code = 0
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            try:
                shared.netcheck(ns="andara-shared", run=net, sleep=sleep, clock=lambda: t[0], suffix="abc123",
                                image="probe:img", **kw)
            except shared.Failed as e:
                code, msg = 1, str(e)
                err.write(msg)
        return code, out.getvalue(), err.getvalue()

    def cleanup(self, net):
        return sorted((ns or "", a[1], a[2]) for ns, a in net.calls if a[0] == "delete")

    def test_admitted_and_refused_as_declared_exits_0_and_cleans_up(self):
        net = FakeNet()
        code, out, err = self.run_net(net)
        self.assertEqual(code, 0, err)
        self.assertIn("netcheck: ok", out)
        self.assertEqual(self.cleanup(net), [
            ("", "namespace", "andara-netcheck-abc123"),
            ("andara-dev", "pod", "andara-netcheck-abc123"),
            ("andara-dev", "pod", "andara-netcheck-abc123-other"),
            ("andara-local", "pod", "andara-netcheck-abc123"),
            ("andara-shared", "pod", "andara-netcheck-abc123-tools")])
        # every probe ran against the shared bootstrap's FQDN
        probes = {tuple(a[-3:]) for _, a in net.calls if a[0] == "exec"}
        self.assertEqual(probes, {("andara-log-kafka-bootstrap.andara-shared.svc",
                                   "kube-dns.kube-system.svc.cluster.local/53", "9092")})

    def test_without_the_object_store_it_says_its_policy_is_not_checked(self):
        net = FakeNet()
        code, out, err = self.run_net(net)
        self.assertEqual(code, 0, err)
        self.assertIn("no object store in andara-shared; its policy is not checked", out)
        self.assertEqual({h for _, _, h in net.hosts}, {"andara-log-kafka-bootstrap.andara-shared.svc:9092"})

    def test_with_the_object_store_every_case_is_probed_on_both_listeners(self):
        net = FakeNet(objectstore=True)
        code, out, err = self.run_net(net)
        self.assertEqual(code, 0, err)
        hosts = {h for _, _, h in net.hosts}
        self.assertEqual(hosts, {"andara-log-kafka-bootstrap.andara-shared.svc:9092",
                                 "andara-objectstore.andara-shared.svc:9000"})
        for ns, pod in {(n, p) for n, p, _ in net.hosts}:
            self.assertEqual({h for n, p, h in net.hosts if (n, p) == (ns, pod)}, hosts, (ns, pod))
        self.assertIn("each listener admits only its declared peers: the Kafka listener (kafka.yaml), "
                      "the object store (objectstore/shared.yaml)", out)

    def test_a_connection_to_the_object_store_from_outside_fails_naming_it(self):
        # The unlabelled namespace's probe: refused by the listener (first three execs), connected
        # to the gateway (the rest).
        net = FakeNet({("scratch", "andara-netcheck-abc123"): [FakeNet.BLOCKED] * 3 + ["probe-ran\nconnected\n"]},
                      objectstore=True)
        code, out, err = self.run_net(net)
        self.assertEqual(code, 1)
        self.assertIn("FAIL the object store (objectstore/shared.yaml), an andara pod in an unlabelled namespace: "
                      "connected (want blocked)", out)
        self.assertNotIn("FAIL the Kafka listener", out)

    def test_the_probes_match_the_listener_peer_and_nothing_of_the_chart(self):
        net = FakeNet()
        self.run_net(net)
        by = {(m["metadata"]["namespace"], m["metadata"]["name"]): m for m in net.applied}
        self.assertEqual(len(by), 5)
        for (ns, name), m in by.items():
            labels = m["metadata"]["labels"]
            self.assertNotIn("app", labels, name)  # the chart's selectors carry app: andara
            self.assertEqual(m["spec"]["containers"][0]["image"], "probe:img")
            want = None if name.endswith("-other") else "andara-kafka-tools" if name.endswith("-tools") else "andara"
            self.assertEqual(labels.get("app.kubernetes.io/name"), want, (ns, name))
        self.assertIn(("andara-netcheck-abc123", "andara-netcheck-abc123"), by)
        # The toolbox probe is in andara-shared, where the registry's and Kafka's toolbox peer is.
        self.assertIn(("andara-shared", "andara-netcheck-abc123-tools"), by)

    def test_the_registry_admits_only_the_toolbox(self):
        """AW-INF-057 AC-1: the andara pods that reach Kafka are refused by the registry."""
        net = FakeNet(objectstore=True, registry=True)
        code, out, err = self.run_net(net)
        self.assertEqual(code, 0, err)
        for what in ("an andara pod in andara-local: blocked", "an andara pod in andara-dev: blocked",
                     "an andara pod in an unlabelled namespace: blocked",
                     "a pod not labelled andara in andara-dev: blocked",
                     "a toolbox pod in andara-shared: connected"):
            self.assertIn("ok the schema registry (schema-registry/registry.yaml), %s" % what, out)

    def test_an_andara_pod_reaching_the_registry_fails(self):
        # andara-dev's andara probe: admitted by Kafka (5 execs worth) and the object store, and the
        # registry lets it in too: a failure naming the registry.
        net = FakeNet({("andara-dev", "andara-netcheck-abc123"): [FakeNet.CONNECTED]}, objectstore=True, registry=True)
        code, out, err = self.run_net(net)
        self.assertEqual(code, 1)
        self.assertIn("FAIL the schema registry (schema-registry/registry.yaml), an andara pod in andara-dev: "
                      "connected (want blocked)", out)

    def test_a_connection_from_the_unlabelled_namespace_fails_naming_it_and_still_cleans_up(self):
        net = FakeNet({("scratch", "andara-netcheck-abc123"): ["probe-ran\nconnected\n"]})
        code, out, err = self.run_net(net)
        self.assertEqual(code, 1)
        self.assertIn("FAIL the Kafka listener (kafka.yaml), an andara pod in an unlabelled namespace: connected (want blocked)", out)
        self.assertIn("is not as its manifest declares: the Kafka listener (kafka.yaml): an andara pod in an unlabelled namespace", err)
        self.assertNotIn("not proven", err)
        self.assertEqual(len(self.cleanup(net)), 5)

    def test_one_connection_in_the_refusal_attempts_fails(self):
        # A connection outranks an unanchored attempt: one connection proves the policy failed.
        for other in (FakeNet.BLOCKED, "probe-ran\nblocked\n"):
            net = FakeNet({("andara-dev", "andara-netcheck-abc123-other"):
                           [other, "probe-ran\nresolved\nanchored\nconnected\n", other]})
            code, out, err = self.run_net(net)
            self.assertEqual(code, 1)
            self.assertIn("FAIL the Kafka listener (kafka.yaml), a pod not labelled andara in andara-dev: connected (want blocked)", out)
            self.assertIn("is not as its manifest declares: the Kafka listener (kafka.yaml): a pod not labelled andara in andara-dev", err)
            self.assertNotIn("not proven", err)
            self.assertEqual(net.execs[("andara-dev", "andara-netcheck-abc123-other")], 3)

    def test_an_admitted_probe_is_polled_to_its_deadline(self):
        # blocked twice, then connected: the policy lagged the pod's readiness
        net = FakeNet({("andara-local", "andara-netcheck-abc123"):
                       ["probe-ran\nblocked\n", "probe-ran\nblocked\n", "probe-ran\nconnected\n"]})
        code, out, err = self.run_net(net)
        self.assertEqual(code, 0, err)
        net = FakeNet({("andara-dev", "andara-netcheck-abc123"): [FakeNet.BLOCKED]})
        code, out, _ = self.run_net(net, deadline=10)
        self.assertEqual(code, 1)
        self.assertIn("FAIL the Kafka listener (kafka.yaml), an andara pod in andara-dev: blocked (want connected)", out)
        self.assertGreater(net.execs[("andara-dev", "andara-netcheck-abc123")], 1)

    def test_an_admitted_probe_that_never_connects_fails_ac5_even_unanchored(self):
        # The anchor backs an absence, not a presence: an admitted pod that can't connect fails.
        net = FakeNet({("andara-local", "andara-netcheck-abc123"): ["probe-ran\nblocked\n"]})
        code, out, err = self.run_net(net, deadline=10)
        self.assertEqual(code, 1)
        self.assertIn("FAIL the Kafka listener (kafka.yaml), an andara pod in andara-local: unanchored (want connected)", out)
        self.assertIn("is not as its manifest declares: the Kafka listener (kafka.yaml): an andara pod in andara-local", err)
        self.assertNotIn("not proven", err)

    def test_a_probe_that_never_ran_is_not_a_refusal(self):
        net = FakeNet({("scratch", "andara-netcheck-abc123"): [""]})
        code, _, err = self.run_net(net)
        self.assertEqual(code, 1)
        self.assertIn("gave no verdict", err)
        self.assertEqual(len(self.cleanup(net)), 5)

    def test_an_unlabelled_or_missing_environment_namespace_refuses_before_creating_anything(self):
        for labels in ({"andara-local": "local", "andara-dev": ""}, {"andara-local": "local"}):
            net = FakeNet(labels=labels)
            code, _, err = self.run_net(net)
            self.assertEqual(code, 1, labels)
            self.assertIn("andara-dev is missing or isn't labelled andara.valesor/env=dev; run `make shared-up`", err)
            self.assertFalse([a for _, a in net.calls if a[0] in ("create", "apply", "delete")])

    def test_a_refusal_from_a_probe_that_couldnt_resolve_or_reach_the_anchor_fails(self):
        # Reviewer on the netcheck PR: broken DNS or egress in the probe pod reads as blocked with
        # no policy at all (live-assertions rule 3).
        for out in ("probe-ran\nanchored\nblocked\n", "probe-ran\nresolved\nblocked\n"):
            net = FakeNet({("scratch", "andara-netcheck-abc123"): [out]})
            code, stdout, _ = self.run_net(net)
            self.assertEqual(code, 1, out)
            self.assertIn("FAIL the Kafka listener (kafka.yaml), an andara pod in an unlabelled namespace: unanchored (want blocked)", stdout)

    def test_one_unanchored_attempt_among_the_refusals_fails_the_case(self):
        net = FakeNet({("scratch", "andara-netcheck-abc123"): ["probe-ran\nblocked\n", FakeNet.BLOCKED]})
        code, out, err = self.run_net(net)
        self.assertEqual(code, 1)
        self.assertIn("FAIL the Kafka listener (kafka.yaml), an andara pod in an unlabelled namespace: unanchored (want blocked)", out)
        self.assertIn("its DNS or egress, not the policy", err)
        self.assertNotIn("is not as its manifest declares", err)

    def test_a_probe_that_isnt_ready_names_itself_and_why_and_still_cleans_up(self):
        net = FakeNet(fail={"wait"})
        code, _, err = self.run_net(net)
        self.assertEqual(code, 1)
        self.assertIn("probe andara-local/andara-netcheck-abc123 not Ready within 120s (ImagePullBackOff)", err)
        self.assertEqual(len(self.cleanup(net)), 5)
        self.assertFalse([a for _, a in net.calls if a[0] == "exec"])

    def test_a_failed_create_or_apply_fails_and_cleans_up_what_it_made(self):
        net = FakeNet(fail={"create"})
        code, _, err = self.run_net(net)
        self.assertEqual(code, 1)
        self.assertNotIn(("", "namespace", "andara-netcheck-abc123"), self.cleanup(net))
        net = FakeNet(fail={"apply"})
        code, _, err = self.run_net(net)
        self.assertEqual(code, 1)
        self.assertIn("creating probe andara-local/andara-netcheck-abc123: boom", err)
        self.assertIn(("", "namespace", "andara-netcheck-abc123"), self.cleanup(net))
        self.assertFalse([a for _, a in net.calls if a[0] in ("wait", "exec")])

    def test_a_namespace_labelled_for_the_other_environment_refuses(self):
        net = FakeNet(labels={"andara-local": "local", "andara-dev": "local"})
        code, _, err = self.run_net(net)
        self.assertEqual(code, 1)
        self.assertIn("andara-dev is missing or isn't labelled andara.valesor/env=dev", err)

    def test_a_verdict_without_the_marker_is_no_verdict(self):
        net = FakeNet({("scratch", "andara-netcheck-abc123"): ["resolved\nanchored\nblocked\n"]})
        code, _, err = self.run_net(net)
        self.assertEqual(code, 1)
        self.assertIn("gave no verdict", err)

    def test_the_probe_image_is_the_toolbox_s(self):
        self.assertRegex(shared.tools_image(), r"^docker\.redpanda\.com/redpandadata/redpanda:v\d")


if __name__ == "__main__":
    unittest.main()
