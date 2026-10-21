# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""scripts/cluster_secrets.py (AW-INF-052 AC-6, AC-7, AC-8): atomic export, `absent` keeps the earlier
copy, restore drops the old cluster's fields and keeps cert-manager's annotations."""
import base64
import os
import shutil
import subprocess
import sys
import tempfile
import types
import unittest
from unittest import mock

import yaml

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
sys.path.insert(0, os.path.join(REPO, "scripts"))
import cluster_secrets  # noqa: E402

CM_ANNOTATIONS = {
    "cert-manager.io/issuer-name": "andara-selfsigned",
    "cert-manager.io/issuer-kind": "ClusterIssuer",
    "cert-manager.io/certificate-name": "andara-ca",
}


def self_signed_pem():
    """A throwaway certificate, or None without openssl."""
    if shutil.which("openssl") is None:
        return None
    d = tempfile.mkdtemp()
    try:
        subprocess.run(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "1",
                        "-subj", "/CN=andara-ca", "-keyout", os.path.join(d, "k"), "-out", os.path.join(d, "c")],
                       check=True, capture_output=True)
        with open(os.path.join(d, "c"), "rb") as f:
            return f.read()
    finally:
        shutil.rmtree(d)


def secret(ns, name, pem=b"not a certificate", annotations=None):
    return {
        "apiVersion": "v1", "kind": "Secret", "type": "kubernetes.io/tls",
        "metadata": {
            "name": name, "namespace": ns, "resourceVersion": "4242", "uid": "u-1",
            "creationTimestamp": "2026-10-01T00:00:00Z", "managedFields": [{"manager": "cert-manager"}],
            "ownerReferences": [{"kind": "Certificate", "name": name}],
            "annotations": dict(annotations or {}, **{cluster_secrets.LAST_APPLIED: "{}"}),
            "labels": {"controller.cert-manager.io/fao": "true"},
        },
        "data": {"tls.crt": base64.b64encode(pem).decode(), "tls.key": base64.b64encode(b"KEY").decode()},
    }


class Cluster:
    """What kubectl sees: which Secrets and namespaces exist, and every call made."""

    def __init__(self, secrets=(), namespaces=("cert-manager", "andara-dev"), fail_reads=False):
        self.secrets = {(s["metadata"]["namespace"], s["metadata"]["name"]): s for s in secrets}
        self.namespaces, self.fail_reads, self.calls, self.applied = set(namespaces), fail_reads, [], []

    def kubectl(self, context, *args, stdin=None):
        self.calls.append((context,) + args)
        ok = lambda out="": types.SimpleNamespace(returncode=0, stdout=out, stderr="")
        err = lambda e: types.SimpleNamespace(returncode=1, stdout="", stderr=e)
        if args[:2] == ("get", "namespace"):
            return ok("namespace/" + args[2]) if args[2] in self.namespaces else \
                err('Error from server (NotFound): namespaces "%s" not found' % args[2])
        if args[0] == "-n" and args[2:4] == ("get", "secret"):
            if self.fail_reads:
                return err("Error from server (Forbidden): secrets is forbidden")
            s = self.secrets.get((args[1], args[4]))
            return ok(yaml.safe_dump(s)) if s else \
                err('Error from server (NotFound): secrets "%s" not found' % args[4])
        if args[:2] == ("apply", "-f"):
            obj = yaml.safe_load(stdin)
            self.applied.append(obj)
            self.secrets[(obj["metadata"]["namespace"], obj["metadata"]["name"])] = obj
            return ok("secret/%s configured" % obj["metadata"]["name"])
        return err("unexpected kubectl %s" % (args,))


class Base(unittest.TestCase):

    def setUp(self):
        self.dir = tempfile.mkdtemp()
        self.tls = os.path.join(self.dir, "tls")
        self.lines = []

    def tearDown(self):
        os.chmod(self.dir, 0o700)
        if os.path.isdir(self.tls):
            os.chmod(self.tls, 0o700)
        shutil.rmtree(self.dir)

    def use(self, cluster):
        for name, fake in (("kubectl", cluster.kubectl), ("require_context", lambda context: None)):
            p = mock.patch.object(cluster_secrets, name, fake)
            p.start()
            self.addCleanup(p.stop)
        return cluster

    def export(self):
        return cluster_secrets.export(tls_dir=self.tls, say=self.lines.append)

    def restore(self):
        return cluster_secrets.restore(tls_dir=self.tls, say=self.lines.append)


class Export(Base):

    def test_both_secrets_are_written_mode_0600_with_the_ca_serial(self):
        pem = self_signed_pem()
        self.use(Cluster([secret("cert-manager", "andara-ca-tls", pem or b"x", CM_ANNOTATIONS),
                          secret("andara-dev", "andara-edge-tls")]))
        result = self.export()
        self.assertEqual(result, {"cert-manager/andara-ca-tls": "written", "andara-dev/andara-edge-tls": "written"})
        for name in ("andara-ca-tls", "andara-edge-tls"):
            path = os.path.join(self.tls, name + ".yaml")
            self.assertEqual(os.stat(path).st_mode & 0o777, 0o600)
        self.assertEqual(os.stat(self.tls).st_mode & 0o777, 0o700)
        ca = [line for line in self.lines if line.startswith("cert-manager/andara-ca-tls written")][0]
        self.assertIn("mode=600", ca)
        if pem:
            self.assertRegex(ca, r"serial=[0-9A-F]+ notBefore=\w+")
        self.assertNotIn("KEY", "\n".join(self.lines))
        self.assertEqual(sorted(os.listdir(self.tls)), ["andara-ca-tls.yaml", "andara-edge-tls.yaml"],
                         "no temporary file is left behind")

    def test_an_absent_secret_keeps_the_earlier_export_byte_identical(self):
        os.makedirs(self.tls)
        earlier = os.path.join(self.tls, "andara-edge-tls.yaml")
        with open(earlier, "wb") as f:
            f.write(b"earlier export\n")
        self.use(Cluster([secret("cert-manager", "andara-ca-tls")]))
        result = self.export()
        self.assertEqual(result["andara-dev/andara-edge-tls"], "absent")
        with open(earlier, "rb") as f:
            self.assertEqual(f.read(), b"earlier export\n")
        self.assertTrue(any(line.startswith("andara-dev/andara-edge-tls absent (earlier export kept")
                            for line in self.lines), self.lines)

    def test_a_failed_write_fails_and_leaves_the_earlier_file(self):
        os.makedirs(self.tls)
        earlier = os.path.join(self.tls, "andara-ca-tls.yaml")
        with open(earlier, "w") as f:
            f.write("earlier\n")
        os.chmod(self.tls, 0o500)
        if os.access(self.tls, os.W_OK):
            self.skipTest("running as a user a read-only directory doesn't stop")
        self.use(Cluster([secret("cert-manager", "andara-ca-tls")]))
        with self.assertRaises(cluster_secrets.Failed) as e:
            self.export()
        self.assertIn("write cert-manager/andara-ca-tls", str(e.exception))
        with open(earlier) as f:
            self.assertEqual(f.read(), "earlier\n")

    def test_a_read_that_is_not_notfound_fails_rather_than_counting_as_absent(self):
        self.use(Cluster(fail_reads=True))
        with self.assertRaises(cluster_secrets.Failed) as e:
            self.export()
        self.assertIn("only NotFound counts as absent", str(e.exception))

    def test_a_not_found_that_is_not_the_api_servers_fails(self):
        """A missing credential plugin says "not found" too; only the API server's NotFound is absent."""
        c = self.use(Cluster())
        plugin = types.SimpleNamespace(returncode=1, stdout="", stderr=(
            "Unable to connect to the server: getting credentials: exec: executable kubelogin not found"))
        with mock.patch.object(cluster_secrets, "kubectl", lambda *a, **k: plugin), \
                self.assertRaises(cluster_secrets.Failed):
            self.export()
        self.assertEqual(c.calls, [])

    def test_the_context_is_kind_andara(self):
        c = self.use(Cluster())
        self.export()
        self.assertTrue(c.calls and all(call[0] == "kind-andara" for call in c.calls))


class Restore(Base):

    def exported(self, *secrets):
        os.makedirs(self.tls, exist_ok=True)
        for s in secrets:
            with open(os.path.join(self.tls, s["metadata"]["name"] + ".yaml"), "w") as f:
                yaml.safe_dump(s, f)

    def test_restore_keeps_the_annotations_and_drops_the_old_clusters_fields(self):
        self.exported(secret("cert-manager", "andara-ca-tls", annotations=CM_ANNOTATIONS),
                      secret("andara-dev", "andara-edge-tls"))
        c = self.use(Cluster())
        self.assertEqual(self.restore(), 2)
        ca = c.applied[0]
        self.assertEqual(ca["metadata"]["name"], "andara-ca-tls", "the CA root is applied first")
        self.assertEqual(ca["metadata"]["annotations"], CM_ANNOTATIONS)
        for field in ("resourceVersion", "uid", "creationTimestamp", "managedFields", "ownerReferences"):
            self.assertNotIn(field, ca["metadata"])
        self.assertEqual(ca["metadata"]["labels"], {"controller.cert-manager.io/fao": "true"})
        self.assertEqual(ca["data"]["tls.key"], base64.b64encode(b"KEY").decode())
        self.assertEqual([line.split()[0] for line in self.lines],
                         ["cert-manager/andara-ca-tls", "andara-dev/andara-edge-tls"])

    def test_restore_of_one_namespace_never_touches_another(self):
        """platform-up restores the CA root before andara-dev exists (AW-INF-054)."""
        self.exported(secret("cert-manager", "andara-ca-tls", annotations=CM_ANNOTATIONS),
                      secret("andara-dev", "andara-edge-tls"))
        c = self.use(Cluster(namespaces=("cert-manager",)))
        n = cluster_secrets.restore(tls_dir=self.tls, say=self.lines.append, namespaces=("cert-manager",))
        self.assertEqual(n, 1)
        self.assertEqual([s["metadata"]["name"] for s in c.applied], ["andara-ca-tls"])
        self.assertFalse([call for call in c.calls if "andara-dev" in call], c.calls)

    def test_nothing_exported_is_nothing_to_restore(self):
        c = self.use(Cluster())
        self.assertEqual(self.restore(), 0)
        self.assertIn("nothing to restore", self.lines[0])
        self.assertEqual(c.calls, [])

    def test_a_missing_namespace_fails_naming_it_before_anything_is_applied(self):
        self.exported(secret("cert-manager", "andara-ca-tls"), secret("andara-dev", "andara-edge-tls"))
        c = self.use(Cluster(namespaces=("cert-manager",)))
        with self.assertRaises(cluster_secrets.Failed) as e:
            self.restore()
        self.assertIn("target namespace andara-dev is missing", str(e.exception))
        self.assertEqual(c.applied, [])


BIN_KUBECTL = os.path.join(REPO, "bin", "kubectl")


@unittest.skipUnless(os.access(BIN_KUBECTL, os.X_OK), "bin/kubectl not installed (make bootstrap)")
class MissingContext(Base):
    """ADR-0013 decision 1: a script that finds the context missing exits 3 naming it. Real bin/kubectl
    against a throwaway kubeconfig, because kubectl words a missing context differently per command."""

    def kubeconfig(self, contexts):
        path = os.path.join(self.dir, "kubeconfig")
        cfg = {"apiVersion": "v1", "kind": "Config", "clusters": [], "users": [], "contexts": []}
        for name in contexts:
            # A server nothing listens on: connection refused, at once.
            cfg["clusters"].append({"name": name, "cluster": {"server": "https://127.0.0.1:1"}})
            cfg["users"].append({"name": name, "user": {"token": "t"}})
            cfg["contexts"].append({"name": name, "context": {"cluster": name, "user": name}})
        with open(path, "w") as f:
            yaml.safe_dump(cfg, f)
        p = mock.patch.dict(os.environ, {"KUBECONFIG": path, "KUBECTL": BIN_KUBECTL})
        p.start()
        self.addCleanup(p.stop)

    def exported(self):
        os.makedirs(self.tls, exist_ok=True)
        with open(os.path.join(self.tls, "andara-ca-tls.yaml"), "w") as f:
            yaml.safe_dump(secret("cert-manager", "andara-ca-tls"), f)

    def test_export_and_restore_exit_3_naming_the_context(self):
        self.exported()
        self.kubeconfig(["kind-solo7"])
        for step in (self.export, self.restore):
            with self.subTest(step=step.__name__), self.assertRaises(cluster_secrets.Missing) as e:
                step()
            self.assertIn("the kubeconfig has no context kind-andara", str(e.exception))

    def test_main_exits_3_through_real_kubectl(self):
        self.exported()
        self.kubeconfig([])
        with mock.patch.object(cluster_secrets, "TLS_DIR", self.tls):
            self.assertEqual(cluster_secrets.main(["export"]), 3)
            self.assertEqual(cluster_secrets.main(["restore"]), 3)

    def test_an_unreachable_cluster_is_a_failure_not_a_missing_context(self):
        self.exported()
        self.kubeconfig(["kind-andara"])
        for step in (self.export, self.restore):
            with self.subTest(step=step.__name__), self.assertRaises(cluster_secrets.Failed):
                step()


class Main(unittest.TestCase):

    def test_usage_is_exit_2(self):
        self.assertEqual(cluster_secrets.main([]), 2)
        self.assertEqual(cluster_secrets.main(["backup"]), 2)

    def test_a_missing_kubectl_is_exit_3(self):
        with mock.patch.dict(os.environ, {"KUBECTL": "/nonexistent/kubectl"}):
            self.assertEqual(cluster_secrets.main(["export"]), 3)

    def test_a_failure_is_exit_1(self):
        with mock.patch.object(cluster_secrets, "export", side_effect=cluster_secrets.Failed("x")):
            self.assertEqual(cluster_secrets.main(["export"]), 1)


if __name__ == "__main__":
    unittest.main()
