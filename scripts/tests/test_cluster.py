# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""make cluster-up | cluster-down | cluster-rebuild (AW-INF-052): the cluster's shape, the port check,
the CONFIRM guard, export before delete, prerequisites, `make clean` keeping .local/tls/, and
KIND_CLUSTER refusing to guess. Nothing here creates a cluster; the integration ACs (1, 2, 5) are the
operator's run on the box."""
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


class Kind:
    """bin/kind and bin/kubectl as cluster.run sees them: which clusters exist, and every call."""

    def __init__(self, clusters=("solo7",), current="kind-solo7"):
        self.clusters, self.current, self.calls = list(clusters), current, []

    def run(self, *cmd, check=True):
        tool, args = os.path.basename(cmd[0]), cmd[1:]
        self.calls.append((tool,) + args)
        out = ""
        if tool == "kind" and args[:2] == ("get", "clusters"):
            out = "\n".join(self.clusters) + "\n"
        elif tool == "kind" and args[:2] == ("create", "cluster"):
            self.clusters.append(args[args.index("--name") + 1])
            self.current = "kind-andara"
        elif tool == "kind" and args[:2] == ("delete", "cluster"):
            self.clusters.remove(args[args.index("--name") + 1])
        elif tool == "kubectl" and args[:2] == ("config", "current-context"):
            out = self.current
        elif tool == "kubectl" and args[:2] == ("config", "use-context"):
            self.current = args[2]
        elif tool == "kubectl" and "nodes" in args:
            out = "andara-control-plane Ready control-plane 1m v1.36.4\n" + \
                  "".join("andara-worker%s Ready <none> 1m v1.36.4\n" % s for s in ("", "2", "3"))
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
        self.assertEqual(k.current, "kind-solo7", "kind's switch to kind-andara is undone")
        self.assertIn("nodes 4/4 Ready", self.out)
        self.assertEqual(self.out[-1], "ready context=kind-andara")

    def test_a_second_run_changes_nothing(self):
        k = self.fake(Kind(clusters=("solo7", "andara")))
        cluster.up(self.run_, self.free_ports())
        self.assertEqual(k.made("create"), [])
        self.assertIn("andara exists; nothing to do", self.out)

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
        self.assertIn("no cluster andara; nothing to delete", self.out)


class Rebuild(Base):

    def test_exports_then_deletes_then_creates(self):
        k = self.fake(Kind(clusters=("solo7", "andara")))
        order = []
        k_run = k.run

        def run(*cmd, check=True):
            order.append(os.path.basename(cmd[0]) + " " + cmd[1])
            return k_run(*cmd, check=check)

        cluster.run = run

        def export(context, say):
            order.append("export " + context)
            return {"cert-manager/andara-ca-tls": "written", "andara-dev/andara-edge-tls": "absent"}

        cluster.rebuild(self.run_, "andara", self.free_ports(), export=export)
        steps = [s for s in order if s.split()[0] in ("export",) or s.split()[1] in ("delete", "create")]
        self.assertEqual(steps, ["export kind-andara", "kind delete", "kind create"])
        self.assertIn("pending the collectors: AW-INF-065", [line.strip() for line in self.out])
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


class KindCluster(unittest.TestCase):
    """AC-11: with KIND_CLUSTER unset and two clusters, kind-load and kind-platform exit 2 and load nothing."""

    def setUp(self):
        self.d = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.d)
        self.log = os.path.join(self.d, "calls")
        self.kind = os.path.join(self.d, "kind")

    def stub(self, *clusters):
        with open(self.kind, "w") as f:
            f.write("#!/bin/sh\necho \"$*\" >> %s\n[ \"$1 $2\" = \"get clusters\" ] && printf '%s'\nexit 0\n"
                    % (self.log, "".join(c + "\\n" for c in clusters)))
        os.chmod(self.kind, 0o755)

    def make(self, target, *args):
        return subprocess.run(["make", "-s", "-C", REPO, target, "KIND=" + self.kind, *args],
                              capture_output=True, text=True)

    def calls(self):
        if not os.path.exists(self.log):
            return []
        with open(self.log) as f:
            return f.read().splitlines()

    def test_two_clusters_and_no_name_loads_nothing(self):
        self.stub("solo7", "andara")
        for target in ("kind-load", "kind-platform"):
            with self.subTest(target=target):
                r = self.make(target)
                self.assertNotEqual(r.returncode, 0)
                self.assertIn("there are 2 kind clusters (solo7, andara)", r.stderr)
        self.assertEqual([c for c in self.calls() if c != "get clusters"], [])
        self.assertEqual(cluster.main(["kind-cluster", "", self.kind]), 2)

    def test_a_named_cluster_is_used(self):
        self.stub("solo7", "andara")
        r = self.make("kind-load", "KIND_CLUSTER=solo7", "IMAGE=andara-server", "TAG=t")
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertIn("load docker-image andara-server:t --name solo7", self.calls())

    def test_one_cluster_is_used_unnamed(self):
        self.stub("solo7")
        r = self.make("kind-load", "IMAGE=andara-server", "TAG=t")
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertIn("load docker-image andara-server:t --name solo7", self.calls())


class Ignored(unittest.TestCase):
    """AC-12: nothing under .local is tracked, and .local/tls/ is ignored."""

    def test_local_is_untracked_and_ignored(self):
        tracked = subprocess.run(["git", "-C", REPO, "ls-files", ".local"], capture_output=True, text=True)
        self.assertEqual(tracked.stdout, "")
        self.assertEqual(subprocess.run(["git", "-C", REPO, "check-ignore", "-q", ".local/tls/x"]).returncode, 0)


if __name__ == "__main__":
    unittest.main()
