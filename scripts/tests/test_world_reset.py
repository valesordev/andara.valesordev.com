# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""`make world-reset` refuses before touching the cluster (AW-INF-021, AC-10)."""
import json
import os
import subprocess
import sys
import types
import unittest
from unittest import mock

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
sys.path.insert(0, os.path.join(REPO, "scripts"))
import world_reset  # noqa: E402


def make(*args):
    # PATH without kubectl: a refusal that reached the cluster would fail differently.
    env = dict(os.environ, PATH="/usr/bin:/bin")
    return subprocess.run(["make", "-s", "-C", REPO, "world-reset", *args],
                          capture_output=True, text=True, env=env)


class Refusals(unittest.TestCase):

    def assertRefused(self, r, text):
        self.assertEqual(r.returncode, 2, r.stderr)
        self.assertIn(text, r.stderr)

    def test_prod_is_refused(self):
        self.assertRefused(make("ENV=prod", "CONFIRM=andara-prod"), "world-reset: prod is never reset")

    def test_local_is_refused(self):
        self.assertRefused(make("ENV=local", "CONFIRM=andara-local"), "make down VOLUMES=1")

    def test_a_missing_confirm_is_refused(self):
        self.assertRefused(make("ENV=dev"), "CONFIRM is missing; it must be the namespace, andara-dev")

    def test_a_confirm_that_is_not_the_namespace_is_refused(self):
        self.assertRefused(make("ENV=dev", "CONFIRM=dev"), "CONFIRM=dev is not the namespace andara-dev")

    def test_no_env_is_usage(self):
        self.assertRefused(make("CONFIRM=andara-dev"), "usage: make world-reset")

    # AW-INF-055 AC-7: local on the shared broker is reset by name; on compose it isn't.
    def test_local_on_compose_is_still_refused(self):
        self.assertRefused(make("ENV=local", "CONFIRM=andara-local", "BROKER=compose"), "make down VOLUMES=1")

    def test_broker_with_dev_is_refused(self):
        self.assertRefused(make("ENV=dev", "CONFIRM=andara-dev", "BROKER=cluster"), "only for local")

    def test_an_unknown_broker_is_refused(self):
        self.assertRefused(make("ENV=local", "CONFIRM=andara-local", "BROKER=laptop"), "compose or cluster")

    def test_local_on_the_cluster_passes_the_refusals(self):
        self.assertEqual(world_reset.check_args(["local", "andara-local", "--broker", "cluster"]),
                         ("local", "andara-local", "cluster"))
        self.assertEqual(world_reset.check_args(["dev", "andara-dev"]), ("dev", "andara-dev", "cluster"))


class FailsClosed(unittest.TestCase):
    """Preconditions stop the reset before anything on the cluster changes (Codex on #165)."""

    NOT_FOUND = 'Error from server (NotFound): statefulsets.apps "andara" not found'

    def fake(self, argo_err=None, store="", server_err=None, projector_err=None):
        calls = []

        def kubectl(ns, *args, check=True):
            calls.append(args)
            if server_err and args[:2] == ("get", world_reset.SERVER):
                p = types.SimpleNamespace(returncode=1, stdout="", stderr=server_err)
            elif projector_err and args[:2] == ("get", world_reset.PROJECTOR):
                p = types.SimpleNamespace(returncode=1, stdout="", stderr=projector_err)
            elif args[:2] == ("get", "application"):
                if argo_err:
                    p = types.SimpleNamespace(returncode=1, stdout="", stderr=argo_err)
                else:
                    p = types.SimpleNamespace(returncode=0, stderr="",
                                              stdout='{"spec": {"syncPolicy": {"automated": {}}}}')
            elif args[:2] == ("get", "configmap"):
                p = types.SimpleNamespace(returncode=0, stdout=store, stderr="")
            else:
                p = types.SimpleNamespace(returncode=0, stdout="", stderr="")
            if check and p.returncode != 0:
                raise world_reset.StepFailed(p.stderr)
            return p
        return calls, kubectl

    def run_reset(self, env="dev", **kw):
        calls, kubectl = self.fake(**kw)
        with mock.patch.object(world_reset, "kubectl", kubectl), \
                mock.patch.object(world_reset.topics, "rpk_runner", lambda env, broker=None: None):
            with self.assertRaises(world_reset.StepFailed) as e:
                world_reset.reset(env, "andara-" + env)
        changed = [c for c in calls if c[0] in ("patch", "scale", "delete")]
        return str(e.exception), changed

    def test_an_unreadable_application_stops_before_any_change(self):
        msg, changed = self.run_reset(argo_err='Error from server (Forbidden): applications is forbidden')
        self.assertIn("only NotFound counts as none", msg)
        self.assertEqual(changed, [])

    def test_a_namespace_with_no_release_names_helm_install_before_any_change(self):
        # Brian on kind-andara, 2026-10-23: right after shared-up, andara-local has no release, and
        # the reset failed on `kubectl get configmap andara-config` NotFound.
        msg, changed = self.run_reset(env="local", server_err=self.NOT_FOUND, store="fs")
        self.assertIn("no server release in andara-local (statefulset/andara not found)", msg)
        self.assertIn("Install it first (`make helm-install ENV=local`); nothing was changed", msg)
        self.assertEqual(changed, [])
        # dev's release is Argo CD's, from the promoted tag; a bare helm-install refuses without TAG=.
        msg, changed = self.run_reset(server_err=self.NOT_FOUND, store="fs")
        self.assertIn("`make argocd-install ENV=dev`, after `make promote ENV=dev SHA=<main sha>` if the `dev` tag", msg)
        self.assertNotIn("helm-install", msg)
        self.assertEqual(changed, [])

    def test_an_unreadable_projector_stops_before_the_server_scales(self):
        # Read as "no projector", a Forbidden would skip projector-stop and reset under a live projector.
        msg, changed = self.run_reset(projector_err="Error from server (Forbidden): deployments.apps is forbidden",
                                      store="fs")
        self.assertIn("read %s in andara-dev (only NotFound counts as none)" % world_reset.PROJECTOR, msg)
        self.assertFalse([c for c in changed if c[0] == "scale"])

    def test_an_unreadable_server_is_reported_as_it_is_not_as_no_release(self):
        msg, changed = self.run_reset(server_err="Error from server (Forbidden): statefulsets.apps is forbidden",
                                      store="fs")
        self.assertIn("only NotFound counts as none", msg)
        self.assertIn("Forbidden", msg)
        self.assertNotIn("no server release", msg)
        self.assertEqual(changed, [])

    def test_an_unknown_snapshot_store_stops_before_any_change(self):
        msg, changed = self.run_reset(store="gcs")
        self.assertIn("snapshot.store=gcs", msg)
        self.assertEqual(changed, [])

    def test_a_missing_application_is_not_an_error(self):
        _, kubectl = self.fake(argo_err='Error from server (NotFound): applications "andara-dev" not found')
        with mock.patch.object(world_reset, "kubectl", kubectl):
            self.assertEqual(world_reset.argo_policy("andara-dev"), (False, None))


class WaitUp(unittest.TestCase):
    """After scaling back up: Ready, or started and waiting for content (AW-SRV-042), counts."""

    def pod(self, ready=False, started=False):
        return json.dumps({"status": {
            "conditions": [{"type": "Ready", "status": "True" if ready else "False"}],
            "containerStatuses": [{"name": "server", "started": started,
                                   "state": {"running": {"startedAt": "2026-10-01T00:00:00Z"}}}]}})

    def wait(self, pods, logs="", deadline=30):
        seq = list(pods)
        calls = []

        def kubectl(ns, *args, check=True):
            calls.append(args)
            if args[:2] == ("get", "pod"):
                return types.SimpleNamespace(returncode=0, stdout=seq.pop(0) if len(seq) > 1 else seq[0], stderr="")
            if args[:1] == ("logs",):
                return types.SimpleNamespace(returncode=0, stdout=logs, stderr="")
            return types.SimpleNamespace(returncode=0, stdout="", stderr="")
        t = [0.0]
        with mock.patch.object(world_reset, "kubectl", kubectl):
            got = world_reset.wait_up("andara-dev", deadline=deadline,
                                      sleep=lambda n: t.__setitem__(0, t[0] + n), clock=lambda: t[0])
        return got, calls

    def test_ready_is_ready(self):
        got, _ = self.wait([self.pod(), self.pod(started=True), self.pod(ready=True, started=True)])
        self.assertEqual(got, "ready")

    def test_started_and_waiting_for_content_counts(self):
        line = '{"level":"WARN","msg":"waiting for content: no Zones in effect; publish and activate a pack"}'
        got, calls = self.wait([self.pod(started=True)], logs=line)
        self.assertEqual(got, "waiting")
        logs = [c for c in calls if c[:1] == ("logs",)][0]
        self.assertIn("--since-time=2026-10-01T00:00:00Z", logs)

    def test_a_wait_the_container_already_left_does_not_count(self):
        # Seeded, then unready again (a drain or a rollout) in the same container: the old
        # wait line is still in its logs, followed by the leave line (Codex on #353).
        lines = "\n".join([
            '{"level":"WARN","msg":"waiting for content: no Zones in effect; publish and activate a pack"}',
            '{"level":"INFO","msg":"content in effect: leaving the wait","zones":4}',
        ])
        with self.assertRaises(world_reset.StepFailed):
            self.wait([self.pod(started=True)], logs=lines, deadline=10)

    def test_a_wait_entered_again_after_leaving_counts(self):
        lines = "\n".join([
            '{"level":"WARN","msg":"waiting for content: no Zones in effect; publish and activate a pack"}',
            '{"level":"INFO","msg":"content in effect: leaving the wait","zones":4}',
            '{"level":"WARN","msg":"waiting for content: no Zones in effect; publish and activate a pack"}',
        ])
        got, _ = self.wait([self.pod(started=True)], logs=lines)
        self.assertEqual(got, "waiting")

    def test_started_unready_without_the_wait_line_keeps_waiting_then_fails(self):
        with self.assertRaises(world_reset.StepFailed) as e:
            self.wait([self.pod(started=True)], logs="recovering from the log", deadline=20)
        self.assertIn("neither Ready nor waiting for content within 20s", str(e.exception))

    def test_not_started_fails_at_the_deadline(self):
        with self.assertRaises(world_reset.StepFailed):
            self.wait([self.pod()], deadline=10)


class Scope(unittest.TestCase):

    def test_it_resets_the_world_and_never_content_or_audit(self):
        self.assertEqual(sorted(world_reset.RESET_TOPICS), sorted(
            ["andara.commands.v1", "andara.events.v1", "andara.state.v1", "andara.accounts.v1"]))
        for t in world_reset.RESET_TOPICS:
            self.assertFalse(t.startswith("andara.content.") or t == "andara.audit.v1", t)

    def test_the_names_are_the_environments_own(self):
        self.assertEqual(world_reset.reset_names("dev")[0], "dev.andara.commands.v1")
        self.assertEqual(world_reset.reset_names("local")[0], "local.andara.commands.v1")
        for env in ("dev", "local"):
            for name in world_reset.reset_names(env):
                self.assertTrue(name.startswith(env + ".andara."), name)

    def test_a_reset_of_one_environment_never_touches_the_other(self):
        # AW-INF-055 AC-7, against a broker that holds both environments' topics.
        names = ["%s.%s" % (p, t) for p in ("dev", "local")
                 for t in world_reset.RESET_TOPICS + ["andara.audit.v1", "andara.content.active.v1"]]
        for env, other in (("local", "dev"), ("dev", "local")):
            with self.subTest(env=env):
                live, calls = set(names), []

                def run(args, live=live, calls=calls):
                    calls.append(list(args))
                    if args[:2] == ["topic", "list"]:
                        return types.SimpleNamespace(returncode=0, stderr="",
                                                     stdout=json.dumps([{"name": n, "partitions": 1} for n in live]))
                    if args[:2] == ["topic", "delete"]:
                        live.discard(args[2])
                    return types.SimpleNamespace(returncode=0, stdout="", stderr="")

                with mock.patch.object(world_reset.topics, "run_action", return_value=0) as apply:
                    world_reset.reset_topics(env, run, sleep=lambda s: None)
                deleted = [c[2] for c in calls if c[:2] == ["topic", "delete"]]
                self.assertEqual(sorted(deleted), sorted(world_reset.reset_names(env)))
                self.assertFalse([c for c in calls if any(str(a).startswith(other + ".") for a in c)])
                self.assertEqual(apply.call_args.args[:2], ("apply", env))
                self.assertIs(apply.call_args.kwargs["rp"], False)


if __name__ == "__main__":
    unittest.main()
