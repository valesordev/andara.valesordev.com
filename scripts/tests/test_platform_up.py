# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""AW-INF-054's unit half: scripts/platform_up.py against a fake cluster, so nothing here needs
Docker, kind or a chart download. The fake keeps Helm's release state, so a second run can show
`unchanged` and a changed pin an upgrade."""
import base64
import io
import json
import os
import shutil
import subprocess
import sys
import tempfile
import types
import unittest
from contextlib import redirect_stderr, redirect_stdout
from unittest import mock

import yaml

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
sys.path.insert(0, os.path.join(REPO, "scripts"))
import platform_up  # noqa: E402

TOKEN = "cf-test-token-not-real-0123456789"
NAMESPACE = {release: ns for release, ns in platform_up.RELEASES.values()}

# `docker network inspect kind` on a dual-stack network, IPv6 listed first, as the box has it.
INSPECT_BOX = json.dumps([{"Name": "kind", "IPAM": {"Config": [
    {"Subnet": "fc00:f853:ccd:e793::/64", "Gateway": "fc00:f853:ccd:e793::1"},
    {"Subnet": "172.19.0.0/16", "Gateway": "172.19.0.1"}]}}])
# A second machine, whose kind network docker numbered differently.
INSPECT_OTHER = json.dumps([{"Name": "kind", "IPAM": {"Config": [
    {"Subnet": "172.18.0.0/16", "Gateway": "172.18.0.1"}]}}])


class FakeCluster:
    """kubectl, helm and docker as platform_up sees them: releases, namespaces, issuers, and every call."""

    def __init__(self, inspect=INSPECT_BOX, issuers_ready=True, bindings=True):
        self.ca_secret = False   # cert-manager writes andara-ca-tls once andara-ca.yaml is applied
        self.ca_secret_err = ""  # a read error that isn't NotFound
        self.not_ready = set()   # names whose wait times out
        self.secret_err = ""     # stderr of a failed token Secret write
        self.context = "kind-andara"
        self.kubectl_path = "bin/kubectl"
        self.inspect = inspect
        self.releases = {}      # release -> {"chart", "status", "values"}
        self.namespaces = set()
        self.applied = []       # files applied, and "secret" for the token's
        self.secrets = {}       # (namespace, name) -> the applied Secret document
        self.issuers_ready = issuers_ready
        self.bindings = bindings
        self.calls = []
        self.stdins = []

    def ok(self, out=""):
        return types.SimpleNamespace(returncode=0, stdout=out, stderr="")

    def no(self, err=""):
        return types.SimpleNamespace(returncode=1, stdout="", stderr=err)

    def kubectl(self, *args, check=True, stdin=None):
        self.calls.append(("kubectl",) + args)
        if stdin is not None:
            self.stdins.append(stdin)
        if args[:2] == ("get", "nodes"):
            return self.ok("node/andara-control-plane\n")
        if args[-4:] == ("secret", "andara-ca-tls", "-o", "name"):
            if self.ca_secret_err:
                return self.no(self.ca_secret_err)
            return self.ok("secret/andara-ca-tls\n") if self.ca_secret else self.no(
                'Error from server (NotFound): secrets "andara-ca-tls" not found')
        if args[:2] == ("apply", "-f"):
            if args[2].endswith("andara-ca.yaml"):
                self.ca_secret = True
            first = args[2] not in self.applied
            self.applied.append(args[2])
            name = os.path.basename(args[2])
            return self.ok("clusterissuer.cert-manager.io/%s %s\n" % (name, "created" if first else "unchanged"))
        if args[:2] == ("apply", "--server-side"):
            if self.secret_err:
                return self.no(self.secret_err)
            doc = json.loads(stdin)
            self.secrets[(doc["metadata"]["namespace"], doc["metadata"]["name"])] = doc
            self.applied.append("secret")
            return self.ok("secret/%s serverside-applied\n" % doc["metadata"]["name"])
        if len(args) >= 6 and args[0] == "-n" and args[2:4] == ("get", "secret") and args[-2:] == ("-o", "json"):
            doc = self.secrets.get((args[1], args[4]))
            return self.ok(json.dumps(doc)) if doc else self.no('Error from server (NotFound): secrets "%s" not found' % args[4])
        if "wait" in args:
            target = [a for a in args if "/" in a and not a.startswith("--")][0].split("/", 1)[1]
            if not self.issuers_ready or target in self.not_ready:
                return self.no("timed out waiting for the condition")
            return self.ok()
        if args[:2] == ("get", "namespace"):
            return self.ok() if args[2] in self.namespaces else self.no("NotFound")
        if args[:2] == ("create", "namespace"):
            self.namespaces.add(args[2])
            return self.ok()
        if "rolebinding" in args:
            return self.ok() if self.bindings else self.no("NotFound")
        return self.ok()

    def helm(self, *args, check=True):
        self.calls.append(("helm",) + args)
        if args[0] == "list":
            ns = args[args.index("-n") + 1]
            rows = [{"name": n, "chart": r["chart"], "status": r["status"]}
                    for n, r in self.releases.items() if NAMESPACE[n] == ns]
            return self.ok(json.dumps(rows))
        if args[:2] == ("get", "values"):
            r = self.releases.get(args[2])
            return self.ok(json.dumps(r["values"])) if r else self.no("release: not found")
        if args[:2] == ("upgrade", "--install"):
            release, chart = args[2], args[3]
            version = args[args.index("--version") + 1]
            with open(args[args.index("--values") + 1]) as f:
                values = yaml.safe_load(f)
            self.releases[release] = {"chart": "%s-%s" % (chart, version), "status": "deployed", "values": values}
            return self.ok()
        return self.ok()

    def docker(self, *args, check=True):
        self.calls.append(("docker",) + args)
        return self.ok(self.inspect)

    def upgrades(self):
        return [c[3] for c in self.calls if c[:3] == ("helm", "upgrade", "--install")]

    def order(self):
        """The steps that change something, in order."""
        steps = []
        stdins = iter(self.stdins)
        for c in self.calls:
            if c[:3] == ("helm", "upgrade", "--install"):
                steps.append("helm " + c[3])
            elif c[:3] == ("kubectl", "apply", "-f"):
                steps.append("apply " + os.path.basename(c[3]))
            elif c[:3] == ("kubectl", "apply", "--server-side"):
                # The Cloudflare token's Secret is "secret"; any other is named.
                name = json.loads(next(stdins))["metadata"]["name"]
                steps.append("secret" if name == "cloudflare-api-token" else "secret " + name)
            elif c[:2] == ("kubectl", "create"):
                steps.append("namespace " + c[3])
            elif c[0] == "restore":
                steps.append("restore")
        return steps


class Base(unittest.TestCase):

    def setUp(self):
        self.fake = FakeCluster()
        self.restored = []
        for name, value in (("Cluster", lambda context, env=None: self.fake),
                            ("require_context", lambda c: None)):
            p = mock.patch.object(platform_up, name, value)
            p.start()
            self.addCleanup(p.stop)

    def restore(self, context, namespaces, say):
        self.fake.calls.append(("restore", context, tuple(namespaces)))
        self.restored.append(tuple(namespaces))
        say("cert-manager/andara-ca-tls restored serial=01 notBefore=Oct 18 2026")

    def env(self, **kw):
        # Never the real .local/box.env, which may hold a real token.
        return dict({"ANDARA_BOX_ENV": os.path.join(tempfile.gettempdir(), "no-such-box.env")}, **kw)

    def up(self, profile="box", **env):
        out, err = io.StringIO(), io.StringIO()
        r = platform_up.Run()
        with redirect_stdout(out), redirect_stderr(err):
            platform_up.up(r, profile, env=self.env(**env), restore=self.restore)
        return out.getvalue()


class Box(Base):

    def test_installs_in_dependency_order(self):
        """AC-1: cert-manager, the CA root restore, andara-ca, the issuers, Traefik, andara-shared,
        Strimzi, Argo CD, then the collectors' namespace, Secret and release (AW-INF-065)."""
        self.up(CLOUDFLARE_API_TOKEN=TOKEN)
        self.assertEqual(self.fake.order(), [
            "helm cert-manager", "restore", "apply andara-ca.yaml", "secret", "apply letsencrypt.yaml",
            "helm traefik", "namespace andara-shared", "helm strimzi", "helm argocd",
            "namespace observability", "secret grafana-telemetry", "helm k8s-monitoring"])

    def test_the_restore_is_the_ca_root_only_and_before_the_first_apply(self):
        """AC-4's order: andara-ca.yaml applied without its Secret mints a new root."""
        self.up()
        self.assertEqual(self.restored, [("cert-manager",)])
        order = self.fake.order()
        self.assertLess(order.index("helm cert-manager"), order.index("restore"))
        self.assertLess(order.index("restore"), order.index("apply andara-ca.yaml"))

    def test_every_release_is_at_its_pin(self):
        self.up()
        doc = platform_up.load_versions()
        for item in platform_up.CHARTS:
            release, _ = platform_up.RELEASES[item]
            _, chart, version = platform_up.chart(item, doc)
            self.assertEqual(self.fake.releases[release]["chart"], "%s-%s" % (chart, version))

    def test_a_second_run_changes_nothing_and_says_unchanged(self):
        """AC-1: no release revision moves, and every item prints unchanged."""
        self.up(CLOUDFLARE_API_TOKEN=TOKEN)
        before = len(self.fake.upgrades())
        out = self.up(CLOUDFLARE_API_TOKEN=TOKEN)
        self.assertEqual(len(self.fake.upgrades()), before)
        for item in platform_up.CHARTS:
            label = "k8s-monitoring" if item == "collectors" else item
            self.assertRegex(out, r"platform: %s \S+ unchanged run=" % label)
        self.assertIn("andara-ca unchanged", out)
        self.assertIn("letsencrypt, letsencrypt-staging unchanged", out)
        self.assertIn("namespace andara-shared unchanged", out)

    def test_a_release_off_its_pin_is_upgraded(self):
        """AC-2: reconcile, not skip."""
        self.up()
        self.fake.releases["traefik"]["chart"] = "traefik-41.3.0"
        out = self.up()
        self.assertEqual(self.fake.upgrades()[-1], "traefik")
        self.assertIn("installed (was traefik-41.3.0)", out)

    def test_a_release_with_other_values_is_upgraded(self):
        self.up()
        self.fake.releases["strimzi"]["values"] = {"watchNamespaces": ["andara-dev", "andara-prod"]}
        out = self.up()
        self.assertEqual(self.fake.upgrades()[-1], "strimzi")
        self.assertIn("strimzi 1.2.0 installed (values differ)", out)

    def test_a_null_in_the_values_file_does_not_count_as_a_change(self):
        """Helm may drop traefik's `rollingUpdate: null` from the stored values."""
        self.up()
        stored = json.loads(json.dumps(self.fake.releases["traefik"]["values"]))
        self.assertIn("rollingUpdate", stored["updateStrategy"])
        self.assertIsNone(stored["updateStrategy"]["rollingUpdate"])
        del stored["updateStrategy"]["rollingUpdate"]
        self.fake.releases["traefik"]["values"] = stored
        before = len(self.fake.upgrades())
        self.up()
        self.assertEqual(len(self.fake.upgrades()), before)

    def test_a_release_not_deployed_is_reinstalled(self):
        """AC-1/2: a failed release at the right version is not `unchanged`."""
        self.up()
        self.fake.releases["argocd"]["status"] = "failed"
        out = self.up()
        self.assertEqual(self.fake.upgrades()[-1], "argocd")
        self.assertIn("argocd 10.9.2 installed (status failed)", out)

    def test_a_present_ca_root_is_never_restored_over(self):
        """A renewed or rotated root on the cluster stays; restore is for a cluster without one."""
        self.up()
        self.assertEqual(self.restored, [("cert-manager",)])
        out = self.up()
        self.assertEqual(self.restored, [("cert-manager",)])
        self.assertIn("cert-manager/andara-ca-tls present; restore skipped", out)

    def test_an_unreadable_ca_secret_is_exit_1_not_a_restore(self):
        self.fake.ca_secret_err = "error: You must be logged in to the server (Unauthorized)"
        with self.assertRaises(platform_up.Failed) as e:
            self.up()
        self.assertIn("only NotFound means absent", str(e.exception))
        self.assertEqual(self.restored, [])
        self.assertNotIn("apply andara-ca.yaml", self.fake.order())

    def test_strimzi_watches_andara_shared_only(self):
        """AC-5."""
        self.up()
        self.assertEqual(self.fake.releases["strimzi"]["values"], {"watchNamespaces": ["andara-shared"]})

    def test_strimzi_without_its_bindings_is_reapplied(self):
        """A deleted and recreated andara-shared loses the chart's RoleBindings while the release matches."""
        self.up()
        self.fake.bindings = False
        out = self.up()
        self.assertEqual(self.fake.upgrades()[-1], "strimzi")
        self.assertIn("RoleBindings missing in andara-shared", out)

    def test_the_shared_namespace_exists_before_strimzi(self):
        self.up()
        order = self.fake.order()
        self.assertLess(order.index("namespace andara-shared"), order.index("helm strimzi"))

    def test_every_call_names_the_context_and_none_switches_it(self):
        self.up()
        self.assertFalse([c for c in self.fake.calls if "use-context" in c])

    def test_a_failed_issuer_is_exit_1(self):
        self.fake.issuers_ready = False
        with self.assertRaises(platform_up.Failed) as e:
            self.up()
        self.assertIn("andara-ca did not become Ready", str(e.exception))


class Edge(Base):

    def test_traefik_trusts_the_ipv4_gateway_only(self):
        """AC-9 on the box's dual-stack network: the IPv6 gateway is never trusted."""
        out = self.up()
        ports = self.fake.releases["traefik"]["values"]["ports"]
        self.assertEqual(ports["websecure"]["proxyProtocol"], {"trustedIPs": ["172.19.0.1/32"]})
        self.assertNotIn("proxyProtocol", ports["web"])
        self.assertIn("platform: edge trusts PROXY from 172.19.0.1/32", out)

    def test_another_machine_trusts_its_own_gateway(self):
        """AC-9: a second kind network, a second trust range."""
        self.fake.inspect = INSPECT_OTHER
        self.up()
        trusted = self.fake.releases["traefik"]["values"]["ports"]["websecure"]["proxyProtocol"]["trustedIPs"]
        self.assertEqual(trusted, ["172.18.0.1/32"])

    def test_a_moved_gateway_upgrades_traefik(self):
        self.up()
        self.fake.inspect = INSPECT_OTHER
        self.up()
        self.assertEqual(self.fake.upgrades()[-1], "traefik")

    def test_gateway_parsing(self):
        self.assertEqual(platform_up.gateway(INSPECT_BOX), "172.19.0.1/32")
        for bad in ("[]", json.dumps([{"IPAM": {"Config": [{"Gateway": "fc00::1"}]}}]), "not json"):
            with self.subTest(bad=bad), self.assertRaises(platform_up.Failed):
                platform_up.gateway(bad)

    def test_no_gateway_is_committed(self):
        """AC-9: the trust range is computed, never in a committed file."""
        with open(platform_up.TRAEFIK_VALUES) as f:
            values = yaml.safe_load(f)
        self.assertNotIn("proxyProtocol", values["ports"]["websecure"])
        self.assertNotIn("proxyProtocol", values["ports"]["web"])


class LetsEncrypt(Base):

    def test_without_the_token_both_issuers_are_skipped_and_it_exits_0(self):
        """AC-11."""
        out = self.up()
        self.assertNotIn("apply letsencrypt.yaml", self.fake.order())
        self.assertNotIn("secret", self.fake.order())
        self.assertIn("letsencrypt, letsencrypt-staging skipped: CLOUDFLARE_API_TOKEN is unset", out)
        self.assertRegex(out.splitlines()[-1], r"skipped: letsencrypt, letsencrypt-staging \(CLOUDFLARE_API_TOKEN unset\)")
        self.assertIn("apply andara-ca.yaml", self.fake.order())

    def test_required_and_unset_is_exit_3_before_anything_runs(self):
        """AC-11: REQUIRE_LETSENCRYPT=1."""
        with self.assertRaises(platform_up.Missing) as e:
            self.up(REQUIRE_LETSENCRYPT="1")
        self.assertEqual(e.exception.code, 3)
        self.assertEqual(self.fake.calls, [])

    def test_the_token_is_written_from_stdin_and_never_printed(self):
        """AC-3: not in argv, not in the output, and in the Secret only."""
        out = self.up(CLOUDFLARE_API_TOKEN=TOKEN)
        b64 = base64.b64encode(TOKEN.encode()).decode()
        for c in self.fake.calls:
            self.assertFalse([a for a in c if TOKEN in str(a) or b64 in str(a)], c)
        self.assertNotIn(TOKEN, out)
        self.assertNotIn(b64, out)
        secret = json.loads(self.fake.stdins[0])
        self.assertEqual((secret["metadata"]["namespace"], secret["metadata"]["name"]), ("cert-manager", "cloudflare-api-token"))
        self.assertEqual(base64.b64decode(secret["data"]["api-token"]).decode(), TOKEN)
        apply = [c for c in self.fake.calls if c[:3] == ("kubectl", "apply", "--server-side")][0]
        self.assertIn("-", apply)

    def test_the_token_comes_from_box_env(self):
        d = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, d)
        path = os.path.join(d, "box.env")
        with open(path, "w") as f:
            f.write("CLOUDFLARE_API_TOKEN=%s\n" % TOKEN)
        os.chmod(path, 0o600)
        out = io.StringIO()
        with redirect_stdout(out):
            platform_up.up(platform_up.Run(), "box", env={"ANDARA_BOX_ENV": path}, restore=self.restore)
        self.assertIn("secret", self.fake.order())

    def test_a_box_env_others_can_read_is_refused(self):
        d = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, d)
        path = os.path.join(d, "box.env")
        with open(path, "w") as f:
            f.write("CLOUDFLARE_API_TOKEN=%s\n" % TOKEN)
        os.chmod(path, 0o644)
        with self.assertRaises(platform_up.Failed) as e:
            platform_up.up(platform_up.Run(), "box", env={"ANDARA_BOX_ENV": path}, restore=self.restore)
        self.assertIn("readable by its owner only", str(e.exception))

    def test_a_failed_secret_write_never_echoes_the_token(self):
        b64 = base64.b64encode(TOKEN.encode()).decode()
        self.fake.secret_err = "admission webhook denied data.api-token=%s (%s)" % (b64, TOKEN)
        out = io.StringIO()
        r = platform_up.Run()
        with redirect_stdout(out), self.assertRaises(platform_up.Failed) as e:
            platform_up.up(r, "box", env=self.env(CLOUDFLARE_API_TOKEN=TOKEN), restore=self.restore)
        text = out.getvalue() + str(e.exception)
        self.assertIn("<redacted>", str(e.exception))
        self.assertNotIn(TOKEN, text)
        self.assertNotIn(b64, text)

    def test_an_issuer_that_never_becomes_ready_is_exit_1(self):
        """AC-3."""
        self.fake.not_ready = {"letsencrypt-staging"}
        with self.assertRaises(platform_up.Failed) as e:
            self.up(CLOUDFLARE_API_TOKEN=TOKEN)
        self.assertIn("clusterissuer letsencrypt-staging did not become Ready", str(e.exception))
        self.assertNotIn("helm traefik", self.fake.order())


class CI(Base):

    def test_ci_installs_cert_manager_andara_ca_traefik_and_the_collectors_only(self):
        """AC-12, and AW-INF-065: the collectors with values-ci.yaml."""
        out = self.up("ci", CLOUDFLARE_API_TOKEN=TOKEN)
        self.assertEqual(self.fake.order(), ["helm cert-manager", "apply andara-ca.yaml", "helm traefik",
                                             "namespace observability", "secret grafana-telemetry",
                                             "helm k8s-monitoring"])
        self.assertEqual(self.restored, [])
        self.assertIn("proxyProtocol", self.fake.releases["traefik"]["values"]["ports"]["websecure"])
        self.assertIn("(PROFILE=ci)", out.splitlines()[-1])

    def test_require_letsencrypt_does_not_apply_to_ci(self):
        self.up("ci", REQUIRE_LETSENCRYPT="1")
        self.assertIn("helm traefik", self.fake.order())


TELEMETRY = "telemetry-test-token-not-real-0123456789"


class Telemetry(Base):
    """AW-INF-065: the Secret grafana-telemetry and the k8s-monitoring release."""

    def secret(self):
        doc = self.fake.secrets[("observability", "grafana-telemetry")]
        return {k: base64.b64decode(v).decode() for k, v in doc["data"].items()}

    def test_the_secret_holds_the_token_and_the_usernames(self):
        self.up(GRAFANA_SOLO7DEV_TELEMETRY_TOKEN=TELEMETRY)
        self.assertEqual(self.secret(), {"token": TELEMETRY, "prom": "3652529", "loki": "1821929",
                                         "tempo": "1816230", "fleet": "1864153"})

    def test_ci_has_no_fleet_username(self):
        self.up("ci")
        self.assertNotIn("fleet", self.secret())

    def test_the_token_is_never_printed_or_in_argv(self):
        """AC-4."""
        out = self.up(GRAFANA_SOLO7DEV_TELEMETRY_TOKEN=TELEMETRY)
        b64 = base64.b64encode(TELEMETRY.encode()).decode()
        for c in self.fake.calls:
            self.assertFalse([a for a in c if TELEMETRY in str(a) or b64 in str(a)], c)
        self.assertNotIn(TELEMETRY, out)
        self.assertNotIn(b64, out)
        self.assertIn("secret observability/grafana-telemetry written from GRAFANA_SOLO7DEV_TELEMETRY_TOKEN", out)
        for values in (r["values"] for r in self.fake.releases.values()):
            self.assertNotIn(TELEMETRY, json.dumps(values))

    def test_a_failed_secret_write_never_echoes_the_token(self):
        b64 = base64.b64encode(TELEMETRY.encode()).decode()
        self.fake.secret_err = "denied data.token=%s (%s)" % (b64, TELEMETRY)
        out = io.StringIO()
        with redirect_stdout(out), self.assertRaises(platform_up.Failed) as e:
            platform_up.up(platform_up.Run(), "ci", env=self.env(GRAFANA_SOLO7DEV_TELEMETRY_TOKEN=TELEMETRY),
                           restore=self.restore)
        text = out.getvalue() + str(e.exception)
        self.assertNotIn(TELEMETRY, text)
        self.assertNotIn(b64, text)

    def test_unset_writes_an_empty_token_exits_0_and_the_last_line_says_so(self):
        """AC-2."""
        out = self.up()
        self.assertEqual(self.secret()["token"], "")
        self.assertIn("helm k8s-monitoring", self.fake.order())
        self.assertIn("written with an empty token (GRAFANA_SOLO7DEV_TELEMETRY_TOKEN unset)", out)
        last = out.splitlines()[-1]
        self.assertIn("the collectors ship nothing", last)
        self.assertIn("GRAFANA_SOLO7DEV_TELEMETRY_TOKEN", last)

    def test_a_second_run_reports_the_secret_and_release_unchanged(self):
        """AC-1."""
        self.up(GRAFANA_SOLO7DEV_TELEMETRY_TOKEN=TELEMETRY)
        before = len(self.fake.upgrades())
        out = self.up(GRAFANA_SOLO7DEV_TELEMETRY_TOKEN=TELEMETRY)
        self.assertEqual(len(self.fake.upgrades()), before)
        self.assertIn("platform: k8s-monitoring 4.5.3 unchanged run=", out)
        self.assertIn("secret observability/grafana-telemetry unchanged", out)
        self.assertIn("namespace observability unchanged", out)

    def test_a_rotated_token_rewrites_the_secret_and_leaves_the_release(self):
        self.up(GRAFANA_SOLO7DEV_TELEMETRY_TOKEN=TELEMETRY)
        before = len(self.fake.upgrades())
        out = self.up(GRAFANA_SOLO7DEV_TELEMETRY_TOKEN=TELEMETRY + "-rotated")
        self.assertIn("secret observability/grafana-telemetry written", out)
        self.assertEqual(len(self.fake.upgrades()), before)

    def test_the_release_gets_the_profile_values(self):
        self.up("ci")
        values = self.fake.releases["k8s-monitoring"]["values"]
        self.assertEqual(values["cluster"]["name"], "ci")
        self.assertNotIn("fleet", values["collectors"])
        self.assertEqual(self.fake.releases["k8s-monitoring"]["chart"], "k8s-monitoring-4.5.3")


class Usage(unittest.TestCase):

    def test_an_unknown_profile_is_exit_2(self):
        with mock.patch.object(platform_up, "Cluster", lambda c: self.fail("touched a cluster")):
            with self.assertRaises(platform_up.Usage):
                platform_up.up(platform_up.Run(), "prod", env={})
        err = io.StringIO()
        with redirect_stderr(err), redirect_stdout(io.StringIO()):
            self.assertEqual(platform_up.main(["up", "prod"]), 2)
        self.assertIn("PROFILE=prod", err.getvalue())

    def test_a_missing_context_is_exit_3(self):
        with tempfile.TemporaryDirectory() as d:
            path = os.path.join(d, "kubeconfig")
            with open(path, "w") as f:
                f.write("contexts:\n- name: kind-andara\n")
            fake = types.SimpleNamespace(kubectl_path="bin/kubectl", context="kind-nope", env={"KUBECONFIG": path})
            with self.assertRaises(platform_up.Missing) as e:
                platform_up.require_context(fake)
        self.assertIn("ANDARA_KUBE_CONTEXT=kind-nope: the kubeconfig has no context kind-nope", str(e.exception))

    def test_the_context_is_andara_kube_context(self):
        seen = []

        def cluster(context, env=None):
            seen.append(context)
            raise platform_up.Missing("stop here")
        with mock.patch.object(platform_up, "Cluster", cluster):
            for env, want in (({}, "kind-andara"), ({"ANDARA_KUBE_CONTEXT": "kind-ci"}, "kind-ci")):
                env["ANDARA_BOX_ENV"] = "/nonexistent/box.env"
                with self.assertRaises(platform_up.Missing):
                    platform_up.up(platform_up.Run(), "ci", env=env)
                self.assertEqual(seen[-1], want)


class Versions(unittest.TestCase):

    def doc(self):
        return platform_up.load_versions()

    def test_the_committed_file_is_valid(self):
        """AC-10."""
        self.assertEqual(platform_up.validate(self.doc()), [])

    def test_todays_pins_moved_over_unchanged(self):
        doc = self.doc()
        self.assertEqual({k: doc[k]["version"] for k in platform_up.CHARTS},
                         {"certManager": "v1.21.1", "traefik": "41.4.0", "strimzi": "1.2.0", "argocd": "10.9.2",
                      "collectors": "4.5.3"})

    def broken(self, mutate):
        doc = json.loads(json.dumps(self.doc()))
        mutate(doc)
        return platform_up.validate(doc)

    def test_an_item_without_a_version_fails(self):
        errors = self.broken(lambda d: d["traefik"].pop("version"))
        self.assertIn("traefik.version is missing", errors)

    def test_an_image_without_a_digest_fails(self):
        errors = self.broken(lambda d: d["kind"].update(nodeImage="kindest/node:v1.36.4"))
        self.assertTrue([e for e in errors if "kind.nodeImage" in e and "digest" in e], errors)

    def test_an_image_updater_key_fails(self):
        for key in ("imageUpdater", "argocdImageUpdater", "image_updater"):
            with self.subTest(key=key):
                errors = self.broken(lambda d: d.update({key: {"repo": "r", "chart": "c", "version": "1.3.1"}}))
                self.assertTrue([e for e in errors if "Image Updater" in e], errors)

    def test_an_unknown_item_or_field_fails(self):
        self.assertTrue(self.broken(lambda d: d.update(redis={"version": "7.0.0"})))
        self.assertTrue(self.broken(lambda d: d["strimzi"].update(values={})))

    def test_argocd_reads_its_version_from_the_file(self):
        import argocd
        self.assertEqual(argocd.argocd_chart(), ("argo-cd", self.doc()["argocd"]["version"]))


class PinCheck(unittest.TestCase):
    """AC-10: a pin of a listed item outside versions.yaml fails, matched by item."""

    def setUp(self):
        self.d = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.d)

    def write(self, rel, text):
        path = os.path.join(self.d, rel)
        os.makedirs(os.path.dirname(path), exist_ok=True)
        with open(path, "w") as f:
            f.write(text)

    def test_the_repo_is_clean(self):
        self.assertEqual(platform_up.pin_findings(REPO), [])

    def test_a_pin_outside_versions_yaml_is_found(self):
        self.write("scripts/kafka.sh", 'STRIMZI_VERSION="1.2.0"\n')
        self.write("scripts/x.py", 'TRAEFIK_CHART = "41.4.0"\nhelm install cert-manager --version v1.21.1\n')
        self.write(".github/workflows/kind.yaml", "node_image: kindest/node:v1.36.1\n")
        found = platform_up.pin_findings(self.d)
        self.assertEqual(sorted(found), sorted([
            ".github/workflows/kind.yaml:1: kind.nodeImage (a kindest/node image)",
            "scripts/kafka.sh:1: strimzi beside a version",
            "scripts/x.py:1: traefik beside a version",
            "scripts/x.py:2: certManager beside a version",
        ]))

    def test_pins_split_across_lines_are_found(self):
        self.write("scripts/install.sh", "helm upgrade --install traefik traefik \\\n  --namespace traefik \\\n  --version 41.4.0\n")
        self.write(".github/workflows/w.yaml", "with:\n  chart: argo-cd\n  version: 10.9.2\n")
        self.write("scripts/values.yaml", "- chart: strimzi-kafka-operator\n  repo: https://strimzi.io/charts/\n  version: '1.2.0'\n")
        self.write("scripts/both.sh", "ARGOCD=10.9.2 IMAGE_UPDATER=1.3.1\n")
        self.assertEqual(sorted(platform_up.pin_findings(self.d)), sorted([
            ".github/workflows/w.yaml:2: argocd chart with a version: in its entry",
            "scripts/both.sh:1: argocd beside a version",
            "scripts/install.sh:3: traefik beside a version",
            "scripts/values.yaml:1: strimzi chart with a version: in its entry",
        ]))

    def test_a_python_call_split_inside_its_brackets_is_found(self):
        self.write("scripts/a.py", 'run(["helm", "upgrade", "--install", "traefik", "traefik",\n'
                                   '     "--version", "41.4.0"])\nx = 1\n')
        self.assertEqual(platform_up.pin_findings(self.d), ["scripts/a.py:2: traefik beside a version"])

    def test_an_unbalanced_bracket_joins_at_most_a_few_lines(self):
        lines = ["s = '('"] + ["x = %d" % i for i in range(20)] + ['TRAEFIK = "41.4.0"']
        joined = platform_up.logical_lines(lines)
        self.assertEqual(joined[-1], [(22, 'TRAEFIK = "41.4.0"')])

    def test_an_oci_chart_reference_is_found(self):
        self.write(".github/workflows/b.yaml", "- chart: oci://quay.io/jetstack/charts/cert-manager\n  version: v1.21.1\n")
        self.assertEqual(platform_up.pin_findings(self.d),
                         [".github/workflows/b.yaml:1: certManager chart with a version: in its entry"])

    def test_a_neighbouring_entry_s_version_is_not_this_chart_s(self):
        self.write("scripts/c.yaml", "- chart: redis\n  version: 20.1.0\n"
                                     "- chart: argo-cd\n  repo: https://argoproj.github.io/argo-helm\n")
        self.assertEqual(platform_up.pin_findings(self.d), [])

    def test_a_next_entry_s_version_is_not_this_chart_s(self):
        self.write("scripts/d.yaml", "- chart: argo-cd\n  repo: x\n- chart: other\n  version: 1.0.0\n")
        self.assertEqual(platform_up.pin_findings(self.d), [])

    def test_a_version_above_chart_in_the_same_entry_is_found(self):
        self.write("scripts/e.yaml", "- name: edge\n  version: 41.4.0\n  args:\n    - x\n  chart: traefik\n")
        self.assertEqual(platform_up.pin_findings(self.d), ["scripts/e.yaml:5: traefik chart with a version: in its entry"])

    def test_a_version_below_a_nested_list_is_found(self):
        self.write("scripts/f.yaml", "- chart: traefik\n  valuesFiles:\n    - a.yaml\n  version: 41.4.0\n")
        self.assertEqual(platform_up.pin_findings(self.d), ["scripts/f.yaml:1: traefik chart with a version: in its entry"])

    def test_a_nested_version_is_not_the_chart_s(self):
        self.write("scripts/g.yaml", "chart: traefik\nimages:\n  - version: 1.2.3\n")
        self.assertEqual(platform_up.pin_findings(self.d), [])

    def test_a_long_backslash_command_is_one_line(self):
        sets = "".join("  --set a%d=b \\\n" % i for i in range(9))
        self.write("scripts/h.sh", "helm upgrade --install traefik traefik/traefik \\\n" + sets + "  --version 41.4.0\n")
        self.assertEqual(platform_up.pin_findings(self.d), ["scripts/h.sh:11: traefik beside a version"])

    def test_a_shell_function_body_is_not_joined(self):
        """`{` never joins: a pin inside a function is found on its own line, and an unrelated
        version elsewhere in the body doesn't pair with an item named on another line."""
        self.write("scripts/g.sh", "deploy() {\n  echo a\n  echo b\n  echo c\n  echo d\n  echo e\n"
                                   "  helm upgrade --install traefik traefik/traefik \\\n    --namespace edge \\\n"
                                   "    --wait \\\n    --version 41.4.0\n}\n"
                                   "install() {\n  echo installing traefik\n  KIND_VERSION=v0.33.0\n}\n")
        self.assertEqual(platform_up.pin_findings(self.d), ["scripts/g.sh:10: traefik beside a version"])

    def test_a_bracket_in_a_comment_does_not_join(self):
        self.write("scripts/k.sh", "# traefik (see versions.yaml\nKUBECTL_VERSION=v1.36.4\n")
        self.assertEqual(platform_up.pin_findings(self.d), [])

    def test_a_chart_key_without_a_version_is_not_a_pin(self):
        self.write("scripts/values.yaml", "chart: traefik\nrepo: https://traefik.github.io/charts\n")
        self.assertEqual(platform_up.pin_findings(self.d), [])

    def test_tests_and_non_items_are_exempt(self):
        self.write("scripts/tests/fixture.py", 'echo strimzi-kafka-operator-1.2.0\n')
        self.write("scripts/bootstrap.sh", 'KUBECTL_VERSION="v1.36.4"\nKIND_VERSION="v0.33.0"\n')
        self.write("scripts/argocd.py", 'IMAGE_UPDATER_CHART_VERSION = "1.3.1"    # Argo CD Image Updater v1.3.0\n')
        self.write("scripts/name_only.sh", "helm list -n traefik  # no version here\n")
        self.assertEqual(platform_up.pin_findings(self.d), [])

    def test_check_exits_1_listing_each_problem(self):
        self.write("scripts/kafka.sh", 'STRIMZI_VERSION="1.2.0"\n')
        lines = []
        self.assertEqual(platform_up.check(say=lines.append, repo=self.d), 1)
        self.assertTrue([l for l in lines if "scripts/kafka.sh:1" in l], lines)
        lines = []
        self.assertEqual(platform_up.check(say=lines.append), 0)
        self.assertEqual(lines, ["platform-check: ok"])



class MakeTarget(unittest.TestCase):
    """`make platform-up` passes PROFILE through, and defaults to box rather than the compose `full`."""

    def recipe(self, *args):
        r = subprocess.run(["make", "-n", "-s", "-C", REPO, "platform-up", *args], capture_output=True, text=True)
        self.assertEqual(r.returncode, 0, r.stderr)
        return r.stdout

    def test_profile_ci_reaches_the_script(self):
        self.assertIn('platform_up.py up "ci"', self.recipe("PROFILE=ci"))

    def test_no_profile_is_box(self):
        env_free = self.recipe()
        self.assertIn('platform_up.py up "box"', env_free)


if __name__ == "__main__":
    unittest.main()
