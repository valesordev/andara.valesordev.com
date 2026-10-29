# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""The edge's port is ANDARA_EDGE_PORT, default 443 (the public name through nginx), in every script that
dials it; 8443 is the override for the kind edge (AW-INF-060 AC-4, ADR-0013 amendment 24)."""
import io
import os
import re
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
sys.path.insert(0, os.path.join(REPO, "scripts"))
import values_get  # noqa: E402

SCRIPTS = ("scripts/helm_install.sh", "scripts/env_recover.py", "scripts/stream_soak.sh")
BARE = re.compile(r"(?<![0-9])443(?![0-9])")
DEFAULTS = {".sh": re.compile(r'"\$\{ANDARA_EDGE_PORT:-443\}"'),
            ".py": re.compile(r'environ\.get\("ANDARA_EDGE_PORT"\) or "443"')}


class EdgePort(unittest.TestCase):

    def test_443_appears_only_as_the_default(self):
        for rel in SCRIPTS:
            with self.subTest(script=rel):
                with open(os.path.join(REPO, rel)) as f:
                    hits = [line.strip() for line in f if BARE.search(line)]
                self.assertEqual(len(hits), 1, "%s: 443 outside its one default: %s" % (rel, hits))
                self.assertRegex(hits[0], DEFAULTS[os.path.splitext(rel)[1]])

    def test_env_recover_dials_the_override(self):
        import env_recover
        for environ, want in (({}, "local.andara.valesordev.com:443"),
                              ({"ANDARA_EDGE_PORT": "8443"}, "local.andara.valesordev.com:8443")):
            with self.subTest(environ=environ):
                run = env_recover.Run("local", "andara-local", environ)
                try:
                    # No ANDARA_BOOTSTRAP_OPERATOR: it stops after choosing the address, before any dial.
                    with self.assertRaises(env_recover.Failed):
                        run.setup_cli()
                finally:
                    subprocess.run(["rm", "-rf", run.work])
                self.assertEqual(run.address, want)

    def test_every_dial_site_uses_port(self):
        for rel, site, want in (("scripts/helm_install.sh", r"--server-address (\S+)", "$HOST:$PORT"),
                                ("scripts/helm_install.sh", r'edge_hint\.sh" (.+)$', '"$HOST" "$PORT"'),
                                ("scripts/stream_soak.sh", r"-connect (\S+)", '"$RESOLVE:$PORT"'),
                                ("scripts/stream_soak.sh", r"ANDARA_SOAK_ADDR=(\S+)", '"$HOST:$PORT"')):
            with self.subTest(script=rel, site=site):
                with open(os.path.join(REPO, rel)) as f:
                    found = re.findall(site, f.read(), re.M)
                self.assertTrue(found, "%s: no %s" % (rel, site))
                self.assertEqual(set(found), {want}, "%s: a dial site without $PORT: %s" % (rel, found))


class Hint(unittest.TestCase):
    """helm-install's --resolve hint (scripts/edge_hint.sh), on a stub getent: it never fails the
    install, and names the --resolve dial only where the DNS dial can't connect (ADR-0013 amendment 24)."""

    def hint(self, answer, override):
        with tempfile.TemporaryDirectory() as d:
            p = os.path.join(d, "getent")
            with open(p, "w") as f:
                # getent exits 2 for a name it can't resolve.
                f.write("#!/bin/sh\n" + ("printf '%s STREAM x\\n'\n" % answer if answer else "exit 2\n"))
            os.chmod(p, 0o755)
            environ = {k: v for k, v in os.environ.items() if k != "ANDARA_EDGE_PORT"}
            environ["PATH"] = d + os.pathsep + os.environ["PATH"]
            if override:
                environ["ANDARA_EDGE_PORT"] = override
            r = subprocess.run(["bash", "-euo", "pipefail", "scripts/edge_hint.sh", "x.example", override or "443"],
                               cwd=REPO, capture_output=True, text=True, env=environ)
        self.assertEqual(r.returncode, 0, r.stderr)
        return r.stdout

    def test_each_case(self):
        for answer, override, want in (("", None, "does not resolve here"),           # CI's andara.local
                                       ("", "8443", "does not resolve here"),
                                       ("100.79.240.98", None, ""),                   # the box, through nginx
                                       ("100.79.240.98", "8443", "does not resolve to 127.0.0.1"),
                                       ("100.79.240.98", "9443", "does not resolve to 127.0.0.1"),
                                       ("127.0.0.1", None, ""),
                                       ("127.0.0.1", "8443", "")):
            with self.subTest(answer=answer, override=override):
                out = self.hint(answer, override)
                if want:
                    self.assertIn(want, out)
                    self.assertIn("--resolve x.example:%s:127.0.0.1" % (override or "443"), out)
                else:
                    self.assertEqual(out, "")


class Profile(unittest.TestCase):
    """`make helm-install ENV=local PROFILE=ci` is the only overlay; any other PROFILE exits 2 (AW-INF-060 AC-10)."""

    def run_install(self, env, profile):
        """helm_install.sh on stub kubectl and helm that log and fail, against the fixture kubeconfig:
        a refusal that regressed would otherwise install into the ambient cluster."""
        with tempfile.TemporaryDirectory() as d:
            log = os.path.join(d, "calls")
            for tool in ("kubectl", "helm"):
                p = os.path.join(d, tool)
                with open(p, "w") as f:
                    f.write('#!/bin/sh\nprintf "%%s\\n" "%s $*" >> "%s"\nexit 1\n' % (tool, log))
                os.chmod(p, 0o755)
            environ = {k: v for k, v in os.environ.items() if k != "ANDARA_KUBE_CONTEXT"}
            environ.update(PATH=d + os.pathsep + os.environ["PATH"],
                           KUBECONFIG=os.path.join(REPO, "scripts", "tests", "fixtures", "kubeconfig"))
            r = subprocess.run(["bash", "scripts/helm_install.sh", env, "andara-server", "dev", "", "", profile],
                               cwd=REPO, capture_output=True, text=True, env=environ)
            made = open(log).read().splitlines() if os.path.exists(log) else []
        # The refusal comes before any helm or kubectl call.
        self.assertEqual(made, [], r.stderr)
        return r

    def test_an_unknown_profile_exits_2(self):
        for profile in ("full", "min", "box", "../ci"):
            with self.subTest(profile=profile):
                r = self.run_install("local", profile)
                self.assertEqual(r.returncode, 2, r.stderr)
                self.assertIn("PROFILE=%s: must be ci" % profile, r.stderr)

    def test_ci_on_another_env_exits_2(self):
        r = self.run_install("dev", "ci")
        self.assertEqual(r.returncode, 2, r.stderr)
        self.assertIn("overlay on local", r.stderr)

    def recipe(self, *args):
        r = subprocess.run(["make", "-n", "-s", "-C", REPO, "helm-install", "ENV=local", *args],
                           capture_output=True, text=True, env={k: v for k, v in os.environ.items() if k != "PROFILE"})
        self.assertEqual(r.returncode, 0, r.stderr)
        return " ".join(r.stdout.replace("\\\n", " ").split())

    def test_the_target_passes_a_command_line_profile_only(self):
        self.assertRegex(self.recipe("PROFILE=ci"), r'helm_install\.sh "local" .* "ci"$')
        # The compose default `full` is not a helm profile.
        self.assertRegex(self.recipe(), r'helm_install\.sh "local" .* ""$')


class ValuesGet(unittest.TestCase):
    """helm_install.sh reads the merged values, as helm installs them (AW-INF-060, values/ci.yaml)."""

    def test_the_overlay_wins_and_merges(self):
        local = os.path.join(REPO, "deploy", "helm", "values", "local.yaml")
        ci = os.path.join(REPO, "deploy", "helm", "values", "ci.yaml")
        self.assertEqual(values_get.get("host", [local]), "local.andara.valesordev.com")
        self.assertEqual(values_get.get("host", [local, ci]), "andara.local")
        self.assertEqual(values_get.get("server.sim.source", [local]), "")
        self.assertEqual(values_get.get("server.sim.source", [local, ci]), "memory")
        # Merged, not replaced: ci.yaml's server block leaves local's topic prefix.
        self.assertEqual(values_get.get("server.kafka.topic_prefix", [local, ci]), "local")

    def test_the_command_line(self):
        local = os.path.join(REPO, "deploy", "helm", "values", "local.yaml")
        ci = os.path.join(REPO, "deploy", "helm", "values", "ci.yaml")
        with mock.patch("sys.stdout", new_callable=io.StringIO) as out:
            self.assertEqual(values_get.main(["host", "--values", local, "--values", ci]), 0)
        self.assertEqual(out.getvalue(), "andara.local\n")
        with mock.patch("sys.stderr", new_callable=io.StringIO):
            for bad in ([], ["host"], ["host", "--values"], ["host", local], ["host", "--values", local, ci]):
                self.assertEqual(values_get.main(bad), 2, bad)

    def test_a_null_deletes(self):
        self.assertEqual(values_get.merge({"a": {"b": 1, "c": 2}}, {"a": {"b": None}}), {"a": {"c": 2}})


if __name__ == "__main__":
    unittest.main()
