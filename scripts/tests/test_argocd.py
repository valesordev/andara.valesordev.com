# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""AW-INF-019's unit half: scripts/argocd.py and helm_install.sh's refusal, against fake
kubectl and helm, so nothing here needs a cluster or the registry."""
import importlib.util
import io
import json
import os
import stat
import subprocess
import sys
import tempfile
import unittest
from contextlib import redirect_stderr, redirect_stdout
from types import SimpleNamespace

HERE = os.path.dirname(os.path.abspath(__file__))
SCRIPTS = os.path.dirname(HERE)
REPO = os.path.dirname(SCRIPTS)


def load_argocd():
    spec = importlib.util.spec_from_file_location("argocd", os.path.join(SCRIPTS, "argocd.py"))
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


class Cluster:
    """What `run` and `kubectl_json` see: which objects exist, and every call made."""

    def __init__(self, secrets=(), app=None):
        self.secrets = set(secrets)
        self.app = app
        self.calls = []

    def run(self, args, check=True, input=None):
        self.calls.append((args, input))
        rc, out = 0, ""
        if args[:1] == ["kubectl"] and "get" in args and "secret" in args:
            rc = 0 if args[-1] in self.secrets else 1
        if args[:1] == ["kubectl"] and "create" in args and "secret" in args:
            self.secrets.add(args[args.index("generic") + 1])
        if args[0].endswith("image_digest.sh"):
            out = "sha256:" + "ab" * 32 + "\n"
        return SimpleNamespace(returncode=rc, stdout=out, stderr="")

    def kubectl_json(self, *args):
        if "application" in args:
            return self.app
        return None

    def made(self, *words):
        return [c for c in self.calls if all(w in c[0] for w in words)]


def with_cluster(mod, cluster):
    mod.run, mod.kubectl_json = cluster.run, cluster.kubectl_json
    return mod


def quiet(fn, *a):
    out, err = io.StringIO(), io.StringIO()
    with redirect_stdout(out), redirect_stderr(err):
        try:
            fn(*a)
            code = 0
        except SystemExit as e:
            code = e.code
    return code, out.getvalue(), err.getvalue()


class EnvGuard(unittest.TestCase):
    """Only dev follows main, and a refused ENV installs nothing."""

    def test_local_and_prod_are_refused_before_anything_runs(self):
        for env in ("local", "prod"):
            c = Cluster()
            mod = with_cluster(load_argocd(), c)
            code, _, err = quiet(mod.install, env)
            self.assertEqual(code, 2, env)
            self.assertIn("only dev follows main", err)
            self.assertEqual(c.calls, [], "%s: %s" % (env, c.calls))

    def test_every_env_command_refuses_prod(self):
        for cmd in ("recover", "uninstall"):
            c = Cluster()
            mod = with_cluster(load_argocd(), c)
            code, _, _ = quiet(getattr(mod, cmd), "prod")
            self.assertEqual(code, 2, cmd)
            self.assertEqual(c.calls, [], cmd)


class Secrets(unittest.TestCase):
    """AC-9: the bootstrap operator is required only while its Secret is absent."""

    def setUp(self):
        self.saved = os.environ.pop("ANDARA_BOOTSTRAP_OPERATOR", None)

    def tearDown(self):
        os.environ.pop("ANDARA_BOOTSTRAP_OPERATOR", None)
        if self.saved is not None:
            os.environ["ANDARA_BOOTSTRAP_OPERATOR"] = self.saved

    def test_absent_and_unset_exits_1_with_helm_installs_message(self):
        c = Cluster(secrets={"andara-server-token-key"})
        mod = with_cluster(load_argocd(), c)
        code, _, err = quiet(mod.secrets, "andara-dev")
        self.assertEqual(code, 1)
        self.assertIn("ENV=dev needs ANDARA_BOOTSTRAP_OPERATOR=<user>:<password>; the local default is public", err)
        self.assertEqual(c.made("create", "andara-server-bootstrap"), [])

    def test_existing_secrets_are_left_as_they_are_without_the_variable(self):
        c = Cluster(secrets={"andara-server-token-key", "andara-server-bootstrap"})
        mod = with_cluster(load_argocd(), c)
        code, out, _ = quiet(mod.secrets, "andara-dev")
        self.assertEqual(code, 0)
        self.assertEqual(c.made("create"), [])
        self.assertIn("andara-server-bootstrap exists; left as it is", out)

    def test_the_operator_goes_in_on_stdin_never_argv(self):
        os.environ["ANDARA_BOOTSTRAP_OPERATOR"] = "op:s3cret-value"
        c = Cluster(secrets={"andara-server-token-key"})
        mod = with_cluster(load_argocd(), c)
        code, _, _ = quiet(mod.secrets, "andara-dev")
        self.assertEqual(code, 0)
        made = c.made("create", "andara-server-bootstrap")
        self.assertEqual(len(made), 1)
        args, stdin = made[0]
        self.assertEqual(stdin, "op:s3cret-value")
        self.assertFalse(any("s3cret" in a for a in args), args)


class Seed(unittest.TestCase):
    """The first render is already pinned; an image.tag Image Updater set is never overwritten."""

    def test_a_new_application_is_created_seeded_and_without_sync(self):
        c = Cluster(app=None)
        mod = with_cluster(load_argocd(), c)
        quiet(mod.application, "andara-dev")
        created = c.made("kubectl", "create", "-f", "-")
        self.assertEqual(len(created), 1)
        first = json.loads(created[0][1])
        self.assertNotIn("syncPolicy", first["spec"])
        self.assertEqual(first["spec"]["source"]["helm"]["parameters"],
                         [{"name": "image.tag", "value": "dev@sha256:" + "ab" * 32}])
        # The file turns sync on only after the create.
        order = [i for i, (a, _) in enumerate(c.calls) if "create" in a or "--server-side" in a]
        self.assertEqual(order, sorted(order))
        self.assertTrue(c.made("apply", "--server-side"))

    def test_an_existing_parameter_is_kept(self):
        app = {"spec": {"source": {"helm": {"parameters": [{"name": "image.tag", "value": "dev@sha256:" + "cd" * 32}]}}}}
        c = Cluster(app=app)
        mod = with_cluster(load_argocd(), c)
        quiet(mod.application, "andara-dev")
        self.assertEqual(c.made("create"), [])
        self.assertEqual(c.made("patch"), [])
        self.assertEqual([a for a, _ in c.calls if a[0].endswith("image_digest.sh")], [])

    def test_an_application_found_without_the_parameter_is_seeded_before_the_file(self):
        c = Cluster(app={"spec": {"source": {"helm": {}}}, "metadata": {"name": "andara-dev"}})
        mod = with_cluster(load_argocd(), c)
        quiet(mod.application, "andara-dev")
        patches = [i for i, (a, _) in enumerate(c.calls) if "patch" in a]
        applies = [i for i, (a, _) in enumerate(c.calls) if "--server-side" in a]
        self.assertTrue(patches and applies and max(patches) < min(applies), c.calls)


class Stalled(unittest.TestCase):
    """AC-8: argocd-status names the pod's reason for the stall, not the symptom."""

    def stalled(self, pod_status):
        mod = load_argocd()
        pod = {"status": dict({"conditions": [{"type": "Ready", "status": "False"}]}, **pod_status)}
        mod.kubectl_json = lambda *a: pod if "pod" in a else {"status": {}}
        return mod.stalled("andara-dev")

    def test_an_init_container_that_cannot_pull_is_the_reason(self):
        # What the box showed on 2026-09-27: the server waits on PodInitializing because the
        # init container, on the same image, is in ImagePullBackOff.
        self.assertEqual(self.stalled({
            "initContainerStatuses": [{"name": "partitions", "state": {"waiting": {"reason": "ImagePullBackOff"}}}],
            "containerStatuses": [{"name": "server", "state": {"waiting": {"reason": "PodInitializing"}}}],
        }), "partitions: ImagePullBackOff")

    def test_a_crashing_server_is_the_reason_after_a_completed_init(self):
        self.assertEqual(self.stalled({
            "initContainerStatuses": [{"name": "partitions", "state": {"terminated": {"reason": "Completed"}}}],
            "containerStatuses": [{"name": "server", "state": {"waiting": {"reason": "CrashLoopBackOff"}}}],
        }), "server: CrashLoopBackOff")

    def test_a_running_server_that_is_not_ready_says_so(self):
        self.assertEqual(self.stalled({
            "containerStatuses": [{"name": "server", "state": {"running": {}}}],
        }), "andara-0 not Ready")


class ContentLines(unittest.TestCase):
    """AW-INF-021: argocd-status adds one `content <pack>@<version>` line per active pack."""

    def packs(self, rc, out, err=""):
        mod = load_argocd()

        def metrics(ns):
            if rc:
                raise RuntimeError(err)
            return out
        mod.pod_metrics = metrics
        return mod.active_packs("andara-dev")

    def test_one_entry_per_pack_sorted(self):
        metrics = "\n".join([
            "# TYPE andara_content_active_version gauge",
            'andara_content_active_version{pack="town"} 3',
            'andara_content_active_version{pack="andara.core"} 1',
            'andara_content_active_version{pack="brian"} 1',
            'andara_content_pending_seconds{pack="town"} 0',
        ])
        self.assertEqual(self.packs(0, metrics), [("andara.core", 1), ("brian", 1), ("town", 3)])

    def test_an_unreadable_pod_is_a_status_line_not_a_failure(self):
        got = self.packs(1, "", "Error from server (NotFound): pods \"andara-0\" not found")
        self.assertTrue(got.startswith("? ("), got)
        self.assertIn("not found", got)

    def test_no_series_says_so(self):
        self.assertEqual(self.packs(0, "andara_build_info 1\n"), "? (no andara_content_active_version series)")


class WaitSyncedHealthy(unittest.TestCase):
    """AW-INF-021 AC-7: on a rebuild from nothing the store has no pack with Zones, so the
    Application is Synced but can't be Healthy before the seed; that counts as done."""

    def run_wait(self, states, waiting):
        mod = load_argocd()
        seq = list(states)
        mod.app_state = lambda name: (*(seq.pop(0) if len(seq) > 1 else seq[0]), "rev", {"x": 1})
        mod.time = SimpleNamespace(monotonic=iter(range(0, 10**6, 5)).__next__, sleep=lambda n: None)
        mod.SYNC_DEADLINE = 60
        out = io.StringIO()
        with redirect_stdout(out):
            return mod.wait_synced_healthy("andara-dev", waiting=waiting)

    def test_healthy_is_done(self):
        self.assertIs(self.run_wait([("OutOfSync", "Missing"), ("Synced", "Healthy")], lambda: False), True)

    def test_synced_and_waiting_for_content_is_done(self):
        self.assertEqual(self.run_wait([("Synced", "Progressing")], lambda: True), "waiting")

    def test_waiting_doesnt_count_before_the_sync(self):
        calls = []
        got = self.run_wait([("OutOfSync", "Progressing")], lambda: calls.append(1) or True)
        self.assertIs(got, False)
        self.assertEqual(calls, [])

    def test_progressing_without_the_wait_times_out(self):
        self.assertIs(self.run_wait([("Synced", "Progressing")], lambda: False), False)


class StatusWaitingOk(unittest.TestCase):
    """argocd-install's closing status exits 0 for an Application that's Synced while andara-0
    waits for content; make argocd-status (strict) still exits 1 for it (Codex on #327)."""

    def run_status(self, waiting_ok, sync="Synced", health="Progressing"):
        mod = load_argocd()
        mod.app_state = lambda name: (sync, health, "abc123", {"x": 1})
        mod.kubectl_json = lambda *a: {}
        mod.active_packs = lambda ns: [("andara.core", 1)]
        mod.stalled = lambda ns: None
        return quiet(mod.status, "dev", waiting_ok)

    def test_install_after_a_wait_exits_zero(self):
        code, out, _ = self.run_status(True)
        self.assertEqual(code, 0)
        self.assertIn("next: make content-seed ENV=dev", out)

    def test_plain_status_stays_strict(self):
        self.assertEqual(self.run_status(False)[0], 1)

    def test_waiting_ok_still_needs_the_sync(self):
        self.assertEqual(self.run_status(True, sync="OutOfSync")[0], 1)


class StatusStalledOrWaiting(unittest.TestCase):
    """A server waiting for its first content is not Ready, which stalled() alone can't tell
    from a stuck rollout. The closing status printed `stalled … argocd-recover` beside
    `waiting` on an empty store (AW-INF-021's §8 close); argocd-recover would refuse there."""

    def run_status(self, waiting_ok, stuck, content_waiting):
        mod = load_argocd()
        mod.app_state = lambda name: ("Synced", "Progressing", "abc123", {"x": 1})
        mod.kubectl_json = lambda *a: {}
        mod.active_packs = lambda ns: [("andara.core", 1)]
        mod.stalled = lambda ns: stuck
        mod.content_waiting = lambda ns: content_waiting
        return quiet(mod.status, "dev", waiting_ok)

    def test_install_on_an_empty_store_says_waiting_not_stalled(self):
        code, out, _ = self.run_status(True, "andara-0 not Ready", True)
        self.assertEqual(code, 0)
        self.assertNotIn("stalled", out)
        self.assertNotIn("argocd-recover", out)
        self.assertIn("waiting      andara-0 is up and waiting for content", out)

    def test_plain_status_on_an_empty_store_says_waiting_and_stays_strict(self):
        code, out, _ = self.run_status(False, "andara-0 not Ready", True)
        self.assertEqual(code, 1)
        self.assertNotIn("stalled", out)
        self.assertIn("next: make content-seed ENV=dev", out)

    def test_a_stuck_rollout_still_says_stalled(self):
        code, out, _ = self.run_status(False, "server: CrashLoopBackOff", False)
        self.assertEqual(code, 1)
        self.assertIn("stalled      server: CrashLoopBackOff", out)
        self.assertNotIn("waiting      ", out)

    def test_install_that_stalls_after_its_wait_still_says_stalled(self):
        # waiting_ok was decided before the closing status; the pod may have rolled since.
        code, out, _ = self.run_status(True, "init: ImagePullBackOff", False)
        self.assertEqual(code, 0)
        self.assertIn("stalled      init: ImagePullBackOff", out)
        self.assertNotIn("waiting      ", out)

    def test_content_waiting_asks_world_reset_for_the_namespace(self):
        mod = load_argocd()
        asked = []
        fake = SimpleNamespace(waiting_for_content=lambda ns: asked.append(ns) or True)
        saved = sys.modules.get("world_reset")
        sys.modules["world_reset"] = fake
        try:
            self.assertIs(mod.content_waiting("andara-dev"), True)
        finally:
            if saved is None:
                sys.modules.pop("world_reset", None)
            else:
                sys.modules["world_reset"] = saved
        self.assertEqual(asked, ["andara-dev"])

    def test_ready_asks_nothing_about_content(self):
        asked = []
        mod = load_argocd()
        mod.app_state = lambda name: ("Synced", "Healthy", "abc123", {"x": 1})
        mod.kubectl_json = lambda *a: {}
        mod.active_packs = lambda ns: [("andara.core", 1)]
        mod.stalled = lambda ns: None
        mod.content_waiting = lambda ns: asked.append(ns) or False
        code, out, _ = quiet(mod.status, "dev", False)
        self.assertEqual((code, asked), (0, []))
        self.assertNotIn("stalled", out)


class HelmInstallRefusal(unittest.TestCase):
    """AC-7: once the Application exists, helm-install exits 1, naming it, and changes nothing."""

    def test_refuses_and_touches_nothing(self):
        with tempfile.TemporaryDirectory() as d:
            log = os.path.join(d, "calls")
            for tool, body in (("kubectl", 'case "$*" in "get crd applications.argoproj.io"|"-n argocd get application andara-dev") exit 0;; esac\nexit 0'),
                               ("helm", "exit 0")):
                p = os.path.join(d, tool)
                with open(p, "w") as f:
                    f.write('#!/bin/sh\necho "%s $*" >> "%s"\n%s\n' % (tool, log, body))
                os.chmod(p, os.stat(p).st_mode | stat.S_IEXEC)
            env = dict(os.environ, PATH=d + os.pathsep + os.environ["PATH"])
            p = subprocess.run([os.path.join(SCRIPTS, "helm_install.sh"), "dev", "andara-server", "dev", "", ""],
                               capture_output=True, text=True, env=env)
            self.assertEqual(p.returncode, 1, p.stderr)
            self.assertIn("make: helm-install: andara-dev is deployed by Argo CD (application andara-dev); see make argocd-status",
                          p.stderr)
            with open(log) as f:
                calls = f.read().splitlines()
            # The refusal is the last thing it does: the two probes, then exit.
            self.assertEqual(calls, ["kubectl get crd applications.argoproj.io",
                                     "kubectl -n argocd get application andara-dev"])
            self.assertEqual(p.stderr.strip().count("\n"), 0, p.stderr)


if __name__ == "__main__":
    unittest.main()
