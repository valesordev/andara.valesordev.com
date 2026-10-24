# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""make cluster-up | cluster-down | cluster-rebuild (AW-INF-052): the cluster's shape, the port check,
the CONFIRM guard, export before delete, prerequisites, `make clean` keeping .local/tls/, kind-load's
cluster from ANDARA_KUBE_CONTEXT, and every other context refused (AW-INF-053). Nothing here creates a cluster; the integration ACs (1, 2, 5) are the
operator's run on the box."""
import json
import os
import shutil
import socket
import stat
import subprocess
import sys
import tempfile
import types
import unittest
from unittest import mock

import yaml

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
sys.path.insert(0, os.path.join(REPO, "scripts"))
import cluster  # noqa: E402
import cluster_secrets  # noqa: E402
import kube  # noqa: E402
# The fixture kubeconfig, with ANDARA_KUBE_CONTEXT unset, whatever the shell running the tests has
# (AW-INF-053): these tests expect the default context, kind-andara.
_ENV = mock.patch.dict(os.environ, dict({k: v for k, v in os.environ.items() if k != "ANDARA_KUBE_CONTEXT"},
                                        KUBECONFIG=os.path.join(REPO, "scripts", "tests", "fixtures", "kubeconfig")),
                       clear=True)


def setUpModule():
    _ENV.start()


def tearDownModule():
    _ENV.stop()


def load(path):
    with open(os.path.join(REPO, path)) as f:
        return yaml.safe_load(f)


class Shape(unittest.TestCase):
    """deploy/kind/cluster.yaml and deploy/platform/versions.yaml, validated in `make check`."""

    def test_cluster_yaml(self):
        c = load("deploy/kind/cluster.yaml")
        self.assertEqual((c["kind"], c["apiVersion"], c["name"]), ("Cluster", "kind.x-k8s.io/v1alpha4", "andara"))
        self.assertEqual(set(c), {"kind", "apiVersion", "name", "nodes"})
        roles = [n["role"] for n in c["nodes"]]
        self.assertEqual(roles, ["control-plane", "worker", "worker", "worker"])
        cp = c["nodes"][0]
        self.assertIn('node-labels: "ingress-ready=true"', cp["kubeadmConfigPatches"][0])
        self.assertEqual(
            [(m["containerPort"], m["hostPort"], m["listenAddress"], m["protocol"]) for m in cp["extraPortMappings"]],
            [(80, 8080, "127.0.0.1", "TCP"), (443, 8443, "127.0.0.1", "TCP")])
        self.assertEqual([p for _, p in cluster.PORTS], [8080, 8443], "the port check and the mapping agree")
        for node in c["nodes"]:
            self.assertNotIn("image", node, "the node image is pinned in deploy/platform/versions.yaml")
            self.assertTrue(set(node) <= {"role", "kubeadmConfigPatches", "extraPortMappings"}, node)

    def test_node_image_is_pinned_by_tag_and_digest(self):
        self.assertRegex(cluster.node_image(), r"^kindest/node:v1\.\d+\.\d+@sha256:[0-9a-f]{64}$")

    def test_a_node_image_without_a_digest_is_refused(self):
        d = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, d)
        path = os.path.join(d, "versions.yaml")
        with open(path, "w") as f:
            f.write("kind:\n  nodeImage: kindest/node:v1.36.4\n")
        with mock.patch.object(cluster, "VERSIONS", path), self.assertRaises(cluster.Failed):
            cluster.node_image()

    def test_kubectl_is_within_one_minor_of_the_node_image(self):
        with open(os.path.join(REPO, "scripts", "bootstrap.sh")) as f:
            pins = dict(line.split("=", 1) for line in f if line.startswith(("KIND_VERSION=", "KUBECTL_VERSION=")))
        kubectl_minor = int(pins["KUBECTL_VERSION"].strip().strip('"').split(".")[1])
        node_minor = int(cluster.node_image().split(":v1.")[1].split(".")[0])
        self.assertLessEqual(abs(kubectl_minor - node_minor), 1)
        self.assertEqual(pins["KIND_VERSION"].strip().strip('"'), "v0.33.0",
                         "versions.yaml's node image is from kind v0.33.0's release notes; change them together")

    def test_ci_config_is_its_own_cluster(self):
        c = load("deploy/kind/config.yaml")
        self.assertNotIn("name", c)
        self.assertEqual([m["hostPort"] for m in c["nodes"][0]["extraPortMappings"]], [80, 443])


KUBECONFIG = {
    "apiVersion": "v1", "kind": "Config", "current-context": "kind-andara",
    "clusters": [{"name": "kind-andara", "cluster": {"server": "https://127.0.0.1:41234",
                                                      "certificate-authority-data": "Q0E="}}],
    "users": [{"name": "kind-andara", "user": {"client-certificate-data": "Q0VSVA==", "client-key-data": "S0VZ"}}],
    "contexts": [{"name": "kind-andara", "context": {"cluster": "kind-andara", "user": "kind-andara"}}],
}


COMPLETE = {
    "clusters.kind-andara.server": "https://127.0.0.1:41234",
    "clusters.kind-andara.certificate-authority-data": "Q0E=",
    "users.kind-andara.client-certificate-data": "Q0VSVA==",
    "users.kind-andara.client-key-data": "S0VZ",
    "contexts.kind-andara.cluster": "kind-andara",
    "contexts.kind-andara.user": "kind-andara",
}


class Kind:
    """bin/kind and bin/kubectl as cluster.run sees them: which clusters exist, the user's kubeconfig
    (its current-context and entries), and every call."""

    def __init__(self, clusters=("solo7",), current="kind-solo7", fail_create=False, context=None, ready=True):
        self.clusters, self.current, self.calls = list(clusters), current, []
        self.entries, self.fail_create, self.ready = {}, fail_create, ready
        # The user's kubeconfig entry for kind-andara: by default complete exactly when the cluster
        # exists; "partial" is a first run that died after the first `config set` of the context.
        if context == "partial":
            self.entries["contexts.kind-andara.cluster"] = "kind-andara"
        elif context if context is not None else "andara" in self.clusters:
            self.entries.update(COMPLETE)

    def run(self, *cmd, check=True):
        tool, args = os.path.basename(cmd[0]), cmd[1:]
        self.calls.append((tool,) + args)
        out = ""
        if tool == "kind" and args[:2] == ("get", "clusters"):
            out = "\n".join(self.clusters) + "\n"
        elif tool == "kind" and args[:2] == ("create", "cluster"):
            if "--kubeconfig" in args:
                with open(args[args.index("--kubeconfig") + 1], "w") as f:
                    yaml.safe_dump(KUBECONFIG, f)
            else:
                self.current = "kind-andara"  # what kind does to the default kubeconfig
            if self.fail_create:
                raise cluster.Failed("kind create failed: node never became Ready")
            self.clusters.append(args[args.index("--name") + 1])
        elif tool == "kind" and args[:2] == ("delete", "cluster"):
            self.clusters.remove(args[args.index("--name") + 1])
        elif tool == "kind" and args[:2] == ("get", "kubeconfig"):
            out = yaml.safe_dump(KUBECONFIG)
        elif tool == "kubectl" and args[:2] == ("config", "view"):
            cfg = {"clusters": [], "users": [], "contexts": []}
            for kind in cfg:
                fields = {k.split(".", 2)[2]: v for k, v in self.entries.items() if k.startswith(kind + ".kind-andara.")}
                if fields:
                    cfg[kind].append({"name": "kind-andara", kind[:-1]: fields})
            out = json.dumps(cfg)
        elif tool == "kubectl" and args[:2] == ("config", "use-context"):
            self.current = args[2]
        elif tool == "kubectl" and args[:2] == ("config", "set"):
            self.entries[args[2]] = args[3]
        elif tool == "kubectl" and "wait" in args and not self.ready:
            # As cluster.run does: raise only when the caller checks, so ignoring the wait's exit shows.
            err = "timed out waiting for the condition on nodes/andara-worker2"
            if check:
                raise cluster.Failed("kubectl --context kind-andara failed: " + err)
            return types.SimpleNamespace(returncode=1, stdout="", stderr=err)
        elif tool == "kubectl" and "nodes" in args:
            out = "andara-control-plane Ready control-plane 1m v1.36.4\n" + \
                  "".join("andara-worker%s %s <none> 1m v1.36.4\n" % (s, "Ready" if self.ready or s != "2" else "NotReady")
                          for s in ("", "2", "3"))
        return types.SimpleNamespace(returncode=0, stdout=out, stderr="")

    def made(self, *words):
        return [c for c in self.calls if all(w in c for w in words)]


class Base(unittest.TestCase):

    def setUp(self):
        self.out = []
        self.run_ = cluster.Run()
        self.run_.say = self.out.append

    def fake(self, kind):
        for target, value in ((cluster, "run"), (cluster, "prerequisites")):
            p = mock.patch.object(target, value, kind.run if value == "run" else lambda: ("bin/kind", "bin/kubectl"))
            p.start()
            self.addCleanup(p.stop)
        return kind

    def free_ports(self):
        return [("ANDARA_HTTP_PORT", free_port()), ("ANDARA_GRPC_PORT", free_port())]


def free_port():
    s = socket.socket()
    s.bind(("127.0.0.1", 0))
    port = s.getsockname()[1]
    s.close()
    return port


class Up(Base):

    def test_creates_andara_with_the_pinned_image_and_leaves_the_current_context(self):
        k = self.fake(Kind())
        cluster.up(self.run_, self.free_ports())
        create = k.made("kind", "create")[0]
        self.assertEqual(create[create.index("--name") + 1], "andara")
        self.assertEqual(create[create.index("--config") + 1], cluster.CONFIG)
        self.assertEqual(create[create.index("--image") + 1], cluster.node_image())
        self.assertEqual(k.current, "kind-solo7", "the current-context never moves, not even during the create")
        self.assertEqual(k.made("use-context"), [])
        self.assertNotEqual(create[create.index("--kubeconfig") + 1], os.path.expanduser("~/.kube/config"))
        self.assertEqual(k.entries, {
            "clusters.kind-andara.server": "https://127.0.0.1:41234",
            "clusters.kind-andara.certificate-authority-data": "Q0E=",
            "users.kind-andara.client-certificate-data": "Q0VSVA==",
            "users.kind-andara.client-key-data": "S0VZ",
            "contexts.kind-andara.cluster": "kind-andara",
            "contexts.kind-andara.user": "kind-andara",
        })
        self.assertTrue(k.made("wait", "--for=condition=Ready", "nodes", "--all"), "every node is waited for")
        self.assertIn("nodes 4/4 Ready", self.out)
        self.assertEqual(self.out[-1], "ready context=kind-andara")

    def test_a_failed_create_leaves_the_kubeconfig_alone(self):
        k = self.fake(Kind(fail_create=True))
        with self.assertRaises(cluster.Failed):
            cluster.up(self.run_, self.free_ports())
        self.assertEqual(k.current, "kind-solo7")
        self.assertEqual(k.entries, {})

    def test_a_second_run_changes_nothing(self):
        k = self.fake(Kind(clusters=("solo7", "andara")))
        cluster.up(self.run_, self.free_ports())
        self.assertEqual(k.made("create"), [])
        self.assertEqual(k.made("config", "set"), [])
        self.assertIn("andara exists; nothing to do", self.out)
        self.assertTrue(k.made("wait", "--for=condition=Ready", "nodes", "--all"), "a rerun still checks the nodes")
        self.assertIn("nodes 4/4 Ready", self.out)

    def test_a_rerun_with_a_node_not_ready_fails(self):
        """A first run that failed in the wait, or a cluster whose nodes went NotReady: not `ready`."""
        self.fake(Kind(clusters=("solo7", "andara"), ready=False))
        with self.assertRaises(cluster.Failed):
            cluster.up(self.run_, self.free_ports())
        self.assertNotIn("ready context=kind-andara", self.out)

    def test_a_cordoned_node_is_ready(self):
        """kubectl shows a cordoned node as `Ready,SchedulingDisabled`; its Ready condition is still True."""
        k = self.fake(Kind(clusters=("solo7", "andara")))
        k_run = k.run

        def run(*cmd, check=True):
            p = k_run(*cmd, check=check)
            if "get" in cmd and "nodes" in cmd:
                p.stdout = p.stdout.replace("andara-worker2 Ready", "andara-worker2 Ready,SchedulingDisabled")
            return p

        cluster.run = run
        cluster.up(self.run_, self.free_ports())
        self.assertIn("nodes 4/4 Ready", self.out)

    def test_no_nodes_fails(self):
        k = self.fake(Kind(clusters=("solo7", "andara")))
        k_run = k.run

        def run(*cmd, check=True):
            p = k_run(*cmd, check=check)
            if "get" in cmd and "nodes" in cmd:
                p.stdout = ""
            return p

        cluster.run = run
        with self.assertRaises(cluster.Failed) as e:
            cluster.up(self.run_, self.free_ports())
        self.assertIn("no nodes", str(e.exception))

    def test_a_node_not_ready_in_the_count_fails(self):
        """A node that goes NotReady between the wait and the count still fails the run."""
        k = self.fake(Kind(clusters=("solo7", "andara")))
        k_run = k.run

        def run(*cmd, check=True):
            if "get" in cmd and "nodes" in cmd:
                k.ready = False
            return k_run(*cmd, check=check)

        cluster.run = run
        with self.assertRaises(cluster.Failed) as e:
            cluster.up(self.run_, self.free_ports())
        self.assertIn("3/4", str(e.exception))
        self.assertNotIn("ready context=kind-andara", self.out)

    def test_a_rerun_adds_a_missing_context_without_moving_current(self):
        """A first run that created the cluster and then failed left no kind-andara context."""
        k = self.fake(Kind(clusters=("solo7", "andara"), context=False))
        cluster.up(self.run_, self.free_ports())
        self.assertEqual(k.made("create"), [])
        self.assertEqual(k.entries["users.kind-andara.client-key-data"], "S0VZ")
        self.assertEqual(k.current, "kind-solo7")
        self.assertIn("andara exists; context kind-andara was missing or incomplete, merged; current-context unchanged",
                      self.out)

    def test_a_rerun_repairs_a_context_left_without_its_user(self):
        """A first run that died between `config set contexts.kind-andara.cluster` and `.user`."""
        k = self.fake(Kind(clusters=("solo7", "andara"), context="partial"))
        cluster.up(self.run_, self.free_ports())
        self.assertEqual(k.entries, COMPLETE)
        self.assertEqual(k.current, "kind-solo7")
        self.assertEqual(self.out[-1], "ready context=kind-andara")

    def test_an_incomplete_kind_kubeconfig_is_refused(self):
        d = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, d)
        path = os.path.join(d, "kubeconfig")
        with open(path, "w") as f:
            yaml.safe_dump(dict(KUBECONFIG, users=[]), f)
        k = self.fake(Kind())
        with self.assertRaises(cluster.Failed) as e:
            cluster.merge_kubeconfig("bin/kubectl", path)
        self.assertIn("['users']", str(e.exception))
        self.assertEqual(k.made("config", "set"), [])

    def test_a_port_in_time_wait_is_free(self):
        """After a rebuild's delete, nginx's closed keep-alives to 8080/8443 sit in TIME_WAIT on
        docker-proxy's side; that's not in use. docker-proxy is Go, which sets SO_REUSEADDR on its
        listeners, and Linux lets a bind reuse a TIME_WAIT port only when both sockets set it."""
        listener = socket.socket()
        listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        listener.bind(("127.0.0.1", 0))
        listener.listen(1)
        port = listener.getsockname()[1]
        client = socket.create_connection(("127.0.0.1", port))
        server, _ = listener.accept()
        server.close()  # the side that closes first holds TIME_WAIT on the port
        client.close()
        listener.close()
        self.assertFalse(cluster.port_in_use(port))

    def test_a_port_in_use_fails_before_kind_naming_the_variable(self):
        k = self.fake(Kind())
        holder = socket.socket()
        holder.bind(("127.0.0.1", 0))
        holder.listen(1)
        self.addCleanup(holder.close)
        ports = [("ANDARA_HTTP_PORT", free_port()), ("ANDARA_GRPC_PORT", holder.getsockname()[1])]
        with self.assertRaises(cluster.Failed) as e:
            cluster.up(self.run_, ports)
        self.assertIn("ANDARA_GRPC_PORT", str(e.exception))
        self.assertEqual(k.made("create"), [])


class Confirm(unittest.TestCase):
    """AC-4: a missing or wrong CONFIRM is exit 2 before anything runs."""

    def test_refusals(self):
        with mock.patch.object(cluster, "run", side_effect=AssertionError("ran something")), \
             mock.patch.object(cluster_secrets, "kubectl", side_effect=AssertionError("exported")):
            for argv in (["down"], ["down", "solo7"], ["down", "kind-andara"], ["rebuild"], ["rebuild", "Andara"]):
                with self.subTest(argv=argv):
                    self.assertEqual(cluster.main(argv), 2)

    def test_through_make(self):
        r = subprocess.run(["make", "-s", "-C", REPO, "cluster-down", "CONFIRM=solo7"], capture_output=True, text=True)
        self.assertNotEqual(r.returncode, 0)
        self.assertIn("CONFIRM=solo7 is not the cluster andara", r.stderr)


class Down(Base):

    def test_deletes_andara_and_no_other(self):
        k = self.fake(Kind(clusters=("solo7", "andara", "other")))
        cluster.down(self.run_, "andara")
        self.assertEqual(k.made("delete"), [("kind", "delete", "cluster", "--name", "andara")])
        self.assertEqual(k.clusters, ["solo7", "other"])

    def test_no_cluster_is_exit_0(self):
        k = self.fake(Kind())
        cluster.down(self.run_, "andara")
        self.assertEqual(k.made("delete"), [])
        self.assertIn("no cluster andara; nothing to delete context=kind-andara", self.out)


class Rebuild(Base):

    def setUp(self):
        super().setUp()
        # platform-up never runs for real here: this box has a kind-andara it would install into.
        self.platform_runs = []
        p = mock.patch.object(cluster, "run_platform", lambda r: self.platform_runs.append(r.id))
        p.start()
        self.addCleanup(p.stop)
        p = mock.patch.object(cluster, "run_shared", lambda r: None)
        p.start()
        self.addCleanup(p.stop)

    def test_exports_then_deletes_then_creates_then_platform_up_then_shared_up(self):
        k = self.fake(Kind(clusters=("solo7", "andara")))
        order = []
        k_run = k.run
        cluster.run_platform = lambda r: order.append("platform-up " + r.id)
        cluster.run_shared = lambda r: order.append("shared-up " + r.id)

        def run(*cmd, check=True):
            order.append(os.path.basename(cmd[0]) + " " + cmd[1])
            return k_run(*cmd, check=check)

        cluster.run = run

        def export(context, say):
            order.append("export " + context)
            return {"cert-manager/andara-ca-tls": "written", "andara-dev/andara-edge-tls": "absent"}

        cluster.rebuild(self.run_, "andara", self.free_ports(), export=export)
        steps = [s for s in order if s.split()[0] in ("export", "platform-up", "shared-up")
                 or s.split()[1] in ("delete", "create")]
        self.assertEqual(steps, ["export kind-andara", "kind delete", "kind create", "platform-up " + self.run_.id,
                                 "shared-up " + self.run_.id])
        self.assertFalse([line for line in self.out if "AW-INF-055" in line], "shared-up's Kafka is no longer pending")
        self.assertFalse([line for line in self.out if "AW-INF-058" in line], "shared-up's object store is no longer pending")
        self.assertIn("pending the collectors: AW-INF-065", [line.strip() for line in self.out])
        self.assertFalse([line for line in self.out if "AW-INF-054" in line], "platform-up is no longer pending")
        self.assertEqual(self.out[-1], "rebuilt context=kind-andara")

    def test_a_failed_export_deletes_nothing(self):
        k = self.fake(Kind(clusters=("solo7", "andara")))

        def export(context, say):
            raise cluster_secrets.Failed("write cert-manager/andara-ca-tls: read-only")

        with self.assertRaises(cluster.Failed) as e:
            cluster.rebuild(self.run_, "andara", self.free_ports(), export=export)
        self.assertIn("andara was not deleted", str(e.exception))
        self.assertEqual(k.made("delete"), [])
        self.assertIn("andara", k.clusters)
        self.assertEqual(self.platform_runs, [])

    def test_a_missing_context_is_exit_3_and_deletes_nothing(self):
        k = self.fake(Kind(clusters=("andara",)))

        def export(context, say):
            raise cluster_secrets.Missing("kubeconfig context kind-andara is missing")

        with self.assertRaises(cluster.Missing) as e:
            cluster.rebuild(self.run_, "andara", self.free_ports(), export=export)
        self.assertIn("kind-andara", str(e.exception))
        self.assertEqual(k.made("delete"), [])

    def test_an_export_that_skipped_a_secret_deletes_nothing(self):
        k = self.fake(Kind(clusters=("andara",)))
        with self.assertRaises(cluster.Failed):
            cluster.rebuild(self.run_, "andara", self.free_ports(),
                            export=lambda context, say: {"cert-manager/andara-ca-tls": "written"})
        self.assertEqual(k.made("delete"), [])

    def test_no_cluster_skips_the_export_and_creates(self):
        k = self.fake(Kind())
        cluster.rebuild(self.run_, "andara", self.free_ports(),
                        export=lambda **_: self.fail("exported with no cluster"))
        self.assertEqual(len(k.made("create")), 1)
        self.assertIn("no cluster andara; skipping the export and the delete", self.out)


class Prerequisites(unittest.TestCase):
    """AC-9: bin/kind or bin/kubectl missing names `make bootstrap`; Docker missing names Docker; exit 3."""

    def setUp(self):
        self.bin = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.bin)
        p = mock.patch.object(cluster, "BIN", self.bin)
        p.start()
        self.addCleanup(p.stop)

    def tool(self, name):
        path = os.path.join(self.bin, name)
        with open(path, "w") as f:
            f.write("#!/bin/sh\nexit 0\n")
        os.chmod(path, 0o755)

    def code(self, *argv):
        err = []
        with mock.patch("sys.stderr", types.SimpleNamespace(write=err.append, flush=lambda: None)):
            return cluster.main(list(argv)), "".join(err)

    def test_no_kind(self):
        code, err = self.code("up")
        self.assertEqual(code, 3)
        self.assertIn("kind not found; run `make bootstrap`", err)

    def test_no_kubectl(self):
        self.tool("kind")
        code, err = self.code("up")
        self.assertEqual(code, 3)
        self.assertIn("kubectl not found; run `make bootstrap`", err)

    def test_no_docker(self):
        self.tool("kind")
        self.tool("kubectl")
        with mock.patch.object(cluster.shutil, "which", return_value=None):
            code, err = self.code("up")
        self.assertEqual(code, 3)
        self.assertIn("Docker not found", err)


class Clean(unittest.TestCase):
    """AC-10: `make clean` keeps .local/tls/ and removes bin/, coverage.out and the rest of .local/."""

    def test_clean_keeps_tls(self):
        d = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, d)
        for path in ("bin/kind", ".local/data/x", ".local/box.env", ".local/tls/andara-ca-tls.yaml", "coverage.out"):
            os.makedirs(os.path.join(d, os.path.dirname(path)), exist_ok=True)
            with open(os.path.join(d, path), "w") as f:
                f.write(path)
        os.chmod(os.path.join(d, ".local/tls/andara-ca-tls.yaml"), 0o600)
        r = subprocess.run(["make", "-s", "-C", d, "-f", os.path.join(REPO, "Makefile"), "clean"],
                           capture_output=True, text=True)
        self.assertEqual(r.returncode, 0, r.stderr)
        left = sorted(os.path.relpath(os.path.join(root, f), d) for root, _, files in os.walk(d) for f in files)
        self.assertEqual(left, [".local/tls/andara-ca-tls.yaml"])
        self.assertEqual(stat.S_IMODE(os.stat(os.path.join(d, left[0])).st_mode), 0o600)
        self.assertFalse(os.path.exists(os.path.join(d, "bin")))


class KindLoad(unittest.TestCase):
    """AW-INF-053: kind-load's cluster is ANDARA_KUBE_CONTEXT without its kind- prefix, and a context
    that isn't a kind one exits 2 naming it and loads nothing (AC-9)."""

    def setUp(self):
        self.d = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.d)
        self.log = os.path.join(self.d, "calls")
        self.kind = os.path.join(self.d, "kind")
        with open(self.kind, "w") as f:
            f.write("#!/bin/sh\necho \"$*\" >> %s\nexit 0\n" % self.log)
        os.chmod(self.kind, 0o755)

    def make(self, context=None):
        env = {k: v for k, v in os.environ.items() if k != "ANDARA_KUBE_CONTEXT"}
        if context is not None:
            env["ANDARA_KUBE_CONTEXT"] = context
        return subprocess.run(["make", "-s", "-C", REPO, "kind-load", "KIND=" + self.kind, "IMAGE=andara-server",
                               "TAG=t"], capture_output=True, text=True, env=env)

    def calls(self):
        if not os.path.exists(self.log):
            return []
        with open(self.log) as f:
            return f.read().splitlines()

    def test_unset_is_andara(self):
        r = self.make()
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(self.calls(), ["load docker-image andara-server:t --name andara"])

    def test_kind_ci_is_ci(self):
        r = self.make("kind-ci")
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(self.calls(), ["load docker-image andara-server:t --name ci"])

    def test_a_context_that_is_not_kind_loads_nothing(self):
        r = self.make("gke_x")
        self.assertNotEqual(r.returncode, 0)
        self.assertIn("ANDARA_KUBE_CONTEXT=gke_x is not a kind context", r.stderr)
        self.assertEqual(self.calls(), [])
        with mock.patch.dict(os.environ, {"ANDARA_KUBE_CONTEXT": "gke_x"}):
            self.assertEqual(kube.main(["kind-name"]), 2)


class OtherContext(unittest.TestCase):
    """AW-INF-053 AC-9: cluster-up, -down and -rebuild act on andara only, so another context is exit 2
    before anything runs, and never reinterpreted."""

    def test_refused_before_anything_runs(self):
        with mock.patch.dict(os.environ, {"ANDARA_KUBE_CONTEXT": "kind-ci"}), \
                mock.patch.object(cluster, "run", side_effect=AssertionError("ran something")), \
                mock.patch.object(cluster, "prerequisites", side_effect=AssertionError("looked for tools")), \
                mock.patch.object(cluster_secrets, "kubectl", side_effect=AssertionError("exported")):
            for argv in (["up"], ["down", "andara"], ["rebuild", "andara"]):
                with self.subTest(argv=argv):
                    self.assertEqual(cluster.main(argv), 2)

    def test_kind_andara_set_explicitly_is_allowed(self):
        with mock.patch.dict(os.environ, {"ANDARA_KUBE_CONTEXT": "kind-andara"}):
            cluster.box_only()

    def test_through_make(self):
        env = dict(os.environ, ANDARA_KUBE_CONTEXT="kind-ci")
        r = subprocess.run(["make", "-s", "-C", REPO, "cluster-down", "CONFIRM=andara"],
                           capture_output=True, text=True, env=env)
        self.assertEqual(r.returncode, 2, r.stderr)
        self.assertIn("ANDARA_KUBE_CONTEXT=kind-ci", r.stderr)


class Ignored(unittest.TestCase):
    """AC-12: nothing under .local is tracked, and .local/tls/ is ignored."""

    def test_local_is_untracked_and_ignored(self):
        tracked = subprocess.run(["git", "-C", REPO, "ls-files", ".local"], capture_output=True, text=True)
        self.assertEqual(tracked.stdout, "")
        self.assertEqual(subprocess.run(["git", "-C", REPO, "check-ignore", "-q", ".local/tls/x"]).returncode, 0)



class RebuildPlatform(unittest.TestCase):
    """run_platform carries platform-up's failures over as the rebuild's own exit codes."""

    def run_with(self, exc):
        def up(r, profile, env=None):
            self.seen = (profile, env.get("ANDARA_KUBE_CONTEXT"))
            if exc:
                raise exc
        with mock.patch.object(cluster.platform_up, "up", up):
            cluster.run_platform(cluster.Run())

    def test_runs_the_box_profile_against_kind_andara(self):
        self.run_with(None)
        self.assertEqual(self.seen, ("box", "kind-andara"))

    def test_a_missing_credential_is_exit_3(self):
        with self.assertRaises(cluster.Missing) as e:
            self.run_with(cluster.platform_up.Missing("CLOUDFLARE_API_TOKEN is unset"))
        self.assertIn("platform-up: CLOUDFLARE_API_TOKEN is unset", str(e.exception))

    def test_a_failed_release_is_exit_1(self):
        with self.assertRaises(cluster.Failed):
            self.run_with(cluster.platform_up.Failed("traefik did not become Ready"))


if __name__ == "__main__":
    unittest.main()
