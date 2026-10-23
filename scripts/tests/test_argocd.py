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
from unittest import mock

HERE = os.path.dirname(os.path.abspath(__file__))
SCRIPTS = os.path.dirname(HERE)
REPO = os.path.dirname(SCRIPTS)
sys.path.insert(0, SCRIPTS)  # argocd.py imports kube

# Every call argocd.py builds names a context checked against the kubeconfig (AW-INF-053): the
# fixture's, with ANDARA_KUBE_CONTEXT unset, whatever the shell running the tests has.
_ENV = mock.patch.dict(os.environ, dict({k: v for k, v in os.environ.items() if k != "ANDARA_KUBE_CONTEXT"},
                                        KUBECONFIG=os.path.join(HERE, "fixtures", "kubeconfig")), clear=True)


def setUpModule():
    _ENV.start()


def tearDownModule():
    _ENV.stop()


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
            self.assertIn("only dev is on Argo CD", err)
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


def AGREED(ns):
    """agreement() with the three answers agreeing, for status tests about something else."""
    return {"tag": "abc123", "argo": "abc123", "pinned": "", "running": "", "mismatch": []}


class Application(unittest.TestCase):
    """AW-INF-059: the Application follows the tag `dev`, which must exist before it's applied, and
    carries no helm.parameters, so nothing outside the tagged commit decides the render."""

    def mod(self, cluster, tag="c0ffee" * 6 + "abcd"):
        mod = with_cluster(load_argocd(), cluster)
        mod.tag_commit = lambda: tag
        return mod

    def test_no_tag_refuses_and_applies_nothing(self):
        c = Cluster(app=None)
        code, _, err = quiet(self.mod(c, tag="").application, "andara-dev")
        self.assertEqual(code, 1)
        self.assertIn("make promote ENV=dev SHA=<main sha>", err)
        self.assertEqual(c.made("apply"), [])

    def test_a_new_application_is_the_file_alone(self):
        c = Cluster(app=None)
        code, out, _ = quiet(self.mod(c).application, "andara-dev")
        self.assertEqual(code, 0)
        self.assertEqual(c.made("patch"), [])
        self.assertEqual(c.made("create"), [])
        applies = c.made("apply", "--server-side")
        self.assertEqual(len(applies), 1)
        self.assertIn(os.path.join(REPO, "deploy", "argocd", "andara-dev.yaml"), applies[0][0])
        self.assertIn("follows tag dev", out)
        self.assertEqual([a for a, _ in c.calls if a[0].endswith("image_digest.sh")], [])

    def test_image_updater_s_parameter_is_removed_before_the_file(self):
        app = {"spec": {"source": {"helm": {"parameters": [{"name": "image.tag", "value": "dev@sha256:" + "cd" * 32}]}}}}
        c = Cluster(app=app)
        quiet(self.mod(c).application, "andara-dev")
        patches = [i for i, (a, _) in enumerate(c.calls) if "patch" in a]
        applies = [i for i, (a, _) in enumerate(c.calls) if "--server-side" in a]
        self.assertTrue(patches and applies and max(patches) < min(applies), c.calls)
        self.assertIn("/spec/source/helm/parameters", c.calls[patches[0]][0][-1])


class RecoverAndBroker(unittest.TestCase):
    """AW-INF-059 review: recover reads image.tag from the synced commit, which a promotion made
    from another clone may not be in this one; preconditions look for the broker dev.yaml names."""

    def test_recover_names_a_synced_commit_it_cannot_read(self):
        mod = load_argocd()
        sts = {"status": {"updateRevision": "new"}}
        pod = {"metadata": {"labels": {"controller-revision-hash": "old"}}}
        mod.kubectl_json = lambda *a: sts if "statefulset" in a else pod if "pod" in a else {}
        mod.stalled = lambda ns: "server: CrashLoopBackOff"
        mod.app_state = lambda name: ("Synced", "Degraded", "f" * 40, {})
        mod.have_commit = lambda c: False
        mod.run = lambda *a, **k: self.fail("recover deleted something: %r" % (a,))
        code, _, err = quiet(mod.recover, "dev")
        self.assertEqual(code, 1)
        self.assertIn("the synced commit ffffffffffff isn't in this clone or on origin", err)

    def test_have_commit_fetches_a_commit_only_origin_has(self):
        d = tempfile.mkdtemp()
        self.addCleanup(lambda: subprocess.run(["rm", "-rf", d]))
        ident = ["-c", "user.name=t", "-c", "user.email=t@example.invalid"]

        def g(cwd, *a):
            return subprocess.run(["git", *ident, *a], cwd=cwd, capture_output=True, text=True, check=True).stdout.strip()
        origin, work, other = (os.path.join(d, n) for n in ("origin.git", "work", "other"))
        g(d, "init", "-q", "--bare", origin)
        g(d, "init", "-q", work)
        g(d, "init", "-q", other)
        empty = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"
        only_origin = g(other, "commit-tree", empty, "-m", "promotion made from another clone")
        g(other, "push", "-q", origin, only_origin + ":refs/tags/dev")
        g(work, "remote", "add", "origin", origin)
        mod = load_argocd()
        mod.REPO = work
        self.assertTrue(mod.have_commit(only_origin))
        self.assertFalse(mod.have_commit("f" * 40))
        self.assertFalse(mod.have_commit(""))

    def test_run_names_a_call_that_doesnt_answer(self):
        mod = load_argocd()
        with self.assertRaises(RuntimeError) as e:
            mod.run(["sleep", "5"], timeout=0.2)
        self.assertIn("no answer in", str(e.exception))

    def test_uninstall_names_the_tag_dev_needs(self):
        c = Cluster(app=None)
        mod = with_cluster(load_argocd(), c)
        mod.run = lambda *a, **k: SimpleNamespace(returncode=0, stdout="", stderr="")
        code, out, _ = quiet(mod.uninstall, "dev")
        self.assertEqual(code, 0)
        self.assertIn("`make helm-install ENV=dev TAG=<the build make argocd-status showed>` takes them back", out)

    def test_preconditions_look_for_the_shared_broker(self):
        mod = load_argocd()
        asked = []
        ready = {"status": {"conditions": [{"type": "Ready", "status": "True"}]}}

        def kj(*a):
            if "kafka" in a:
                asked.append(a[a.index("-n") + 1])
                return {}
            return ready
        mod.kubectl_json = kj
        mod.run = lambda *a, **k: SimpleNamespace(returncode=0, stdout="", stderr="")
        code, _, err = quiet(mod.preconditions, "andara-dev")
        self.assertEqual(code, 1)
        self.assertEqual(asked, ["andara-shared"])
        self.assertIn("no Ready kafka/andara-log in andara-shared; it comes from the shared broker (AW-INF-055)", err)


class Agreement(unittest.TestCase):
    """AW-INF-059 Observability: argocd-status's three answers (the tag's commit, Argo's synced
    revision, andara-0's digest against the digest that commit pins) and exit 1 on any mismatch."""

    TAG = "a" * 40
    DIGEST = "sha256:" + "ab" * 32

    def mod(self, argo, running, pinned="sha-0123456789ab@" + DIGEST, tag=TAG):
        mod = load_argocd()
        mod.tag_commit = lambda: tag
        mod.image_tag_at = lambda c: pinned if c == tag else ""
        mod.app_state = lambda name: ("Synced", "Healthy", argo, {"x": 1})
        mod.running_digest = lambda ns: running
        return mod

    def test_one_answer(self):
        a = self.mod(self.TAG, self.DIGEST).agreement("andara-dev")
        self.assertEqual(a["mismatch"], [])
        self.assertEqual(a["pinned"], self.DIGEST)

    def test_argo_behind_the_tag(self):
        a = self.mod("b" * 40, self.DIGEST).agreement("andara-dev")
        self.assertEqual(len(a["mismatch"]), 1)
        self.assertIn("argo synced bbbbbbbbbbbb, tag dev is aaaaaaaaaaaa", a["mismatch"][0])

    def test_running_another_digest(self):
        a = self.mod(self.TAG, "sha256:" + "cd" * 32).agreement("andara-dev")
        self.assertIn("andara-0 runs", a["mismatch"][0])

    def test_an_unpinned_tag_disagrees(self):
        a = self.mod(self.TAG, self.DIGEST, pinned="dev").agreement("andara-dev")
        self.assertIn("pins no digest", a["mismatch"][0])

    def test_no_tag(self):
        a = self.mod(self.TAG, self.DIGEST, tag="").agreement("andara-dev")
        self.assertEqual(a["mismatch"], ["no tag dev on origin"])

    def test_status_exits_1_on_a_mismatch_while_synced_and_healthy(self):
        mod = self.mod("b" * 40, self.DIGEST)
        mod.kubectl_json = lambda *a: {}
        mod.active_packs = lambda ns: [("andara.core", 1)]
        mod.stalled = lambda ns: None
        code, out, _ = quiet(mod.status, "dev")
        self.assertEqual(code, 1)
        self.assertIn("disagree     argo synced", out)

    def test_running_digest_reads_the_server_container_s_image_id(self):
        mod = load_argocd()
        mod.kubectl_json = lambda *a: {"status": {"containerStatuses": [
            {"name": "sidecar", "imageID": "x@sha256:" + "11" * 32},
            {"name": "server", "imageID": "ghcr.io/valesordev/andara-server@" + self.DIGEST}]}}
        self.assertEqual(mod.running_digest("andara-dev"), self.DIGEST)


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
        mod.agreement = AGREED
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
        mod.agreement = AGREED
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
        mod.agreement = AGREED
        mod.kubectl_json = lambda *a: {}
        mod.active_packs = lambda ns: [("andara.core", 1)]
        mod.stalled = lambda ns: None
        mod.content_waiting = lambda ns: asked.append(ns) or False
        code, out, _ = quiet(mod.status, "dev", False)
        self.assertEqual((code, asked), (0, []))
        self.assertNotIn("stalled", out)


class HelmInstallRefusal(unittest.TestCase):
    """AC-7: once the Application exists, helm-install exits 1, naming it, and changes nothing."""

    def test_dev_without_a_tag_refuses_before_any_call(self):
        """AW-INF-059: :dev no longer moves, so installing dev by hand names its build."""
        with tempfile.TemporaryDirectory() as d:
            log = os.path.join(d, "calls")
            env = stub_bin(d, log, {"kubectl": "exit 0", "helm": "exit 0"})
            p = subprocess.run([os.path.join(SCRIPTS, "helm_install.sh"), "dev", "andara-server", "dev", "", ""],
                               capture_output=True, text=True, env=env)
            self.assertEqual(p.returncode, 2, p.stderr)
            self.assertIn("ENV=dev needs TAG=sha-<12>@sha256:<digest>", p.stderr)
            self.assertFalse(os.path.exists(log) and open(log).read().strip())
            # A TAG given, but the frozen :dev, is refused the same way.
            p = subprocess.run([os.path.join(SCRIPTS, "helm_install.sh"), "dev", "andara-server", "dev", "", "1"],
                               capture_output=True, text=True, env=env)
            self.assertEqual(p.returncode, 2, p.stderr)
            self.assertFalse(os.path.exists(log) and open(log).read().strip())
            # A digest-pinned build with trailing junk is not a build.
            p = subprocess.run([os.path.join(SCRIPTS, "helm_install.sh"), "dev", "andara-server",
                                "sha-0123456789ab@sha256:" + "ab" * 32 + "x", "", "1"],
                               capture_output=True, text=True, env=env)
            self.assertEqual(p.returncode, 2, p.stderr)

    def test_dev_with_a_pinned_build_passes_the_guard_and_finds_the_shared_broker(self):
        """The TAG every message names (sha-<12>@sha256:<64 hex>) gets past the guard, and the broker
        is looked for where values/dev.yaml says it is: andara-shared, not andara-dev."""
        with tempfile.TemporaryDirectory() as d:
            log = os.path.join(d, "calls")
            env = stub_bin(d, log, {
                "kubectl": 'case "$*" in "get crd applications.argoproj.io") exit 1;; '
                           '"-n andara-shared get kafka andara-log"*) echo True; exit 0;; esac\nexit 1',
                "helm": "exit 0"})
            for tag in ("sha-0123456789ab@sha256:" + "ab" * 32, "sha-0123456789ab"):
                p = subprocess.run([os.path.join(SCRIPTS, "helm_install.sh"), "dev", "andara-server", tag, "", "1"],
                                   capture_output=True, text=True, env=env)
                self.assertNotEqual(p.returncode, 2, p.stderr)
                self.assertNotIn("ENV=dev needs TAG", p.stderr)
                self.assertIn("helm-install: kafka/andara-log in andara-shared is Ready", p.stdout, p.stderr)
            self.assertFalse([c for c in calls(log) if "-n andara-dev get kafka" in c])

    def test_prod_checks_its_own_namespace_s_broker(self):
        """prod's values name a bare broker, so the check stays in andara-prod, with its own hint."""
        with tempfile.TemporaryDirectory() as d:
            log = os.path.join(d, "calls")
            env = stub_bin(d, log, {
                "kubectl": 'case "$*" in "get crd applications.argoproj.io") exit 1;; '
                           '"-n andara-prod get kafka andara-log"*) echo True; exit 0;; esac\nexit 1',
                "helm": "exit 0"})
            p = subprocess.run([os.path.join(SCRIPTS, "helm_install.sh"), "prod", "andara-server", "v1.0.0", "", "1"],
                               capture_output=True, text=True, env=env)
            self.assertIn("helm-install: kafka/andara-log in andara-prod is Ready", p.stdout, p.stderr)
            self.assertFalse([c for c in calls(log) if "-n andara-shared" in c])
        with tempfile.TemporaryDirectory() as d:
            log = os.path.join(d, "calls")
            env = stub_bin(d, log, {"kubectl": 'case "$*" in "get crd applications.argoproj.io") exit 1;; esac\nexit 1',
                                    "helm": "exit 0"})
            p = subprocess.run([os.path.join(SCRIPTS, "helm_install.sh"), "prod", "andara-server", "v1.0.0", "", "1"],
                               capture_output=True, text=True, env=env)
            self.assertEqual(p.returncode, 1)
            self.assertIn("no Ready kafka/andara-log in andara-prod; run `make kafka-install ENV=prod` first", p.stderr)

    def test_dev_without_the_shared_broker_names_aw_inf_055(self):
        with tempfile.TemporaryDirectory() as d:
            log = os.path.join(d, "calls")
            env = stub_bin(d, log, {"kubectl": 'case "$*" in "get crd applications.argoproj.io") exit 1;; esac\nexit 1',
                                    "helm": "exit 0"})
            p = subprocess.run([os.path.join(SCRIPTS, "helm_install.sh"), "dev", "andara-server", "sha-0123456789ab", "", "1"],
                               capture_output=True, text=True, env=env)
            self.assertEqual(p.returncode, 1)
            self.assertIn("no Ready kafka/andara-log in andara-shared; it comes from the shared broker (AW-INF-055)", p.stderr)

    def test_refuses_and_touches_nothing(self):
        with tempfile.TemporaryDirectory() as d:
            log = os.path.join(d, "calls")
            env = stub_bin(d, log, {
                "kubectl": 'case "$*" in "get crd applications.argoproj.io"|"-n argocd get application andara-dev") exit 0;; esac\nexit 0',
                "helm": "exit 0"})
            p = subprocess.run([os.path.join(SCRIPTS, "helm_install.sh"), "dev", "andara-server", "sha-0123456789ab", "", "1"],
                               capture_output=True, text=True, env=env)
            self.assertEqual(p.returncode, 1, p.stderr)
            self.assertIn("make: helm-install: andara-dev is deployed by Argo CD (application andara-dev); see make argocd-status",
                          p.stderr)
            with open(log) as f:
                calls = f.read().splitlines()
            # The refusal is the last thing it does: the two probes, then exit.
            self.assertEqual(calls, ["kubectl --context kind-andara get crd applications.argoproj.io",
                                     "kubectl --context kind-andara -n argocd get application andara-dev"])
            self.assertEqual(p.stderr.strip().splitlines()[0], "context: kind-andara")
            self.assertEqual(len(p.stderr.strip().splitlines()), 2, p.stderr)



def stub_bin(d, log, tools):
    """Shell stubs on PATH that log every call to log, whole; tools maps a name to its body, which
    sees the arguments after the context flag (AW-INF-053), so its patterns name the command alone."""
    for tool, body in tools.items():
        p = os.path.join(d, tool)
        with open(p, "w") as f:
            f.write('#!/bin/sh\necho "%s $*" >> "%s"\n'
                    'case "$1" in --context|--kube-context) shift 2;; esac\n%s\n' % (tool, log, body))
        os.chmod(p, os.stat(p).st_mode | stat.S_IEXEC)
    # The fixture kubeconfig and no ANDARA_KUBE_CONTEXT, whatever the caller's shell has (AW-INF-053).
    env = {k: v for k, v in os.environ.items() if k != "ANDARA_KUBE_CONTEXT"}
    return dict(env, PATH=d + os.pathsep + os.environ["PATH"],
                KUBECONFIG=os.path.join(REPO, "scripts", "tests", "fixtures", "kubeconfig"))


def calls(log):
    if not os.path.exists(log):
        return []
    with open(log) as f:
        return f.read().splitlines()


class IssuerRefusal(unittest.TestCase):
    """AW-INF-054 AC-13: with andara-ca absent, helm-install and argocd-install exit 1 naming `make
    platform-up`, and apply nothing under deploy/k8s/cert-manager/."""

    # Traefik and cert-manager's CRDs are there; no ClusterIssuer is, so the Ready read prints nothing.
    KUBECTL = 'case "$*" in "get crd applications.argoproj.io") exit 1;; esac\nexit 0'

    def test_helm_install_refuses_and_applies_nothing(self):
        with tempfile.TemporaryDirectory() as d:
            log = os.path.join(d, "calls")
            env = stub_bin(d, log, {"kubectl": self.KUBECTL, "helm": "exit 0"})
            p = subprocess.run([os.path.join(SCRIPTS, "helm_install.sh"), "local", "andara-server", "dev", "", ""],
                               capture_output=True, text=True, env=env)
            self.assertEqual(p.returncode, 1, p.stderr)
            self.assertIn("ClusterIssuer andara-ca is not Ready; run `make platform-up`", p.stderr)
            made = calls(log)
            self.assertFalse([c for c in made if " apply " in c or " create " in c or "cert-manager/" in c], made)
            self.assertFalse([c for c in made if c.startswith("helm ")], made)

    def test_helm_install_checks_the_edge_issuer_too(self):
        """dev's edge is letsencrypt: andara-ca Ready isn't enough."""
        kubectl = ('case "$*" in "get crd applications.argoproj.io") exit 1;;\n'
                   '  *"kafka andara-log"*) echo True;;\n'
                   '  *"clusterissuer andara-ca"*) echo True;;\nesac\nexit 0')
        with tempfile.TemporaryDirectory() as d:
            log = os.path.join(d, "calls")
            env = stub_bin(d, log, {"kubectl": kubectl, "helm": "exit 0"})
            p = subprocess.run([os.path.join(SCRIPTS, "helm_install.sh"), "dev", "andara-server", "sha-0123456789ab", "", "1"],
                               capture_output=True, text=True, env=env)
            self.assertEqual(p.returncode, 1, p.stderr)
            self.assertIn("ClusterIssuer letsencrypt is not Ready; run `make platform-up`", p.stderr)
            self.assertFalse([c for c in calls(log) if " apply " in c or "create namespace" in c])

    def test_argocd_install_refuses_and_applies_nothing(self):
        c = Cluster()
        mod = with_cluster(load_argocd(), c)
        code, _, err = quiet(mod.preconditions, "andara-dev")
        self.assertEqual(code, 1)
        self.assertIn("ClusterIssuer andara-ca is not Ready; run `make platform-up`", err)
        self.assertEqual(c.made("apply"), [])
        self.assertFalse([a for a, _ in c.calls if any("k8s/cert-manager" in str(x) for x in a)])

    def test_argocd_install_wants_dev_s_edge_issuer(self):
        c = Cluster()
        mod = with_cluster(load_argocd(), c)
        ready = {"status": {"conditions": [{"type": "Ready", "status": "True"}]}}
        mod.kubectl_json = lambda *a: ready if a[-1] == "andara-ca" else None
        code, _, err = quiet(mod.preconditions, "andara-dev")
        self.assertEqual(code, 1)
        self.assertIn("ClusterIssuer letsencrypt is not Ready", err)


class KafkaOperator(unittest.TestCase):
    """AW-INF-054 AC-5: kafka-install never installs Strimzi, and refuses a namespace it doesn't watch."""

    def run_install(self, watching):
        d = tempfile.mkdtemp()
        self.addCleanup(lambda: subprocess.run(["rm", "-rf", d]))
        log = os.path.join(d, "calls")
        values = json.dumps({"watchNamespaces": watching}) if watching else ""
        helm = 'case "$1" in get) [ -n \'%s\' ] && echo \'%s\' && exit 0; exit 1;; esac\nexit 0' % (values, values)
        env = stub_bin(d, log, {"helm": helm, "kubectl": "exit 0"})
        p = subprocess.run([os.path.join(SCRIPTS, "kafka.sh"), "install", "dev"], capture_output=True, text=True, env=env)
        return p, calls(log)

    def test_an_operator_watching_andara_shared_refuses_dev(self):
        p, made = self.run_install(["andara-shared"])
        self.assertEqual(p.returncode, 1)
        self.assertIn("Strimzi watches andara-shared, not andara-dev", p.stderr)
        self.assertFalse([c for c in made if c.startswith("helm upgrade") or c.startswith("helm install") or " apply " in c], made)

    def test_no_operator_names_platform_up(self):
        p, made = self.run_install(None)
        self.assertEqual(p.returncode, 1)
        self.assertIn("run `make platform-up`", p.stderr)
        self.assertFalse([c for c in made if c.startswith("helm upgrade") or " apply " in c], made)

    def test_bounce_refuses_dev_before_any_call(self):
        """AW-INF-059 moved dev's broker to andara-shared; there's no broker in andara-dev to bounce."""
        d = tempfile.mkdtemp()
        self.addCleanup(lambda: subprocess.run(["rm", "-rf", d]))
        log = os.path.join(d, "calls")
        env = stub_bin(d, log, {"helm": "exit 0", "kubectl": "exit 0"})
        p = subprocess.run([os.path.join(SCRIPTS, "kafka.sh"), "bounce", "dev"], capture_output=True, text=True, env=env)
        self.assertEqual(p.returncode, 2, p.stderr)
        self.assertIn("its bounce is AW-INF-055's", p.stderr)
        self.assertEqual(calls(log), [])

    def test_no_install_path_carries_another_watch_list(self):
        """AC-5's grep: kafka-operator and kafka.sh's WATCHED list are gone."""
        for path in ("Makefile", os.path.join("scripts", "kafka.sh")):
            with open(os.path.join(REPO, path)) as f:
                text = f.read()
            self.assertNotIn("kafka-operator", text, path)
            self.assertNotIn("WATCHED=", text, path)


if __name__ == "__main__":
    unittest.main()
