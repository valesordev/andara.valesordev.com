# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""`make local-up`, `local-down`, `local-reset` (AW-INF-060 AC-1, AC-2, AC-3): the steps and their order,
the refusals, and that a reset deletes nothing outside local's prefix."""
import io
import json
import os
import subprocess
import sys
import tempfile
import types
import unittest

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
sys.path.insert(0, os.path.join(REPO, "scripts"))
import local  # noqa: E402
import topics  # noqa: E402


def res(rc=0, out="", err=""):
    return types.SimpleNamespace(returncode=rc, stdout=out, stderr=err)


def log():
    out = io.StringIO()
    return local.Log("r1", out), out


def make(*args):
    """The target on a PATH with no kubectl, helm, kind or docker, and a kubeconfig with no cluster:
    a refusal that regressed would fail differently, never act."""
    env = dict(os.environ, PATH="/usr/bin:/bin", KUBECONFIG=os.path.join(REPO, "scripts", "tests", "fixtures", "kubeconfig"))
    env.pop("ANDARA_KUBE_CONTEXT", None)
    return subprocess.run(["make", "-s", "-C", REPO, *args], capture_output=True, text=True, env=env)


class Refusals(unittest.TestCase):

    def test_another_env_is_refused(self):
        for target in ("local-up", "local-down", "local-reset"):
            with self.subTest(target=target):
                r = make(target, "ENV=dev")
                self.assertEqual(r.returncode, 2, r.stderr)  # make's own code for a recipe that failed
                self.assertIn("acts on andara-local only, not ENV=dev", r.stderr)
                self.assertIn("Error 1", r.stderr)

    def test_local_up_takes_no_profile(self):
        r = make("local-up", "PROFILE=ci")
        self.assertIn("takes no PROFILE", r.stderr)
        self.assertIn("Error 1", r.stderr)

    def test_usage(self):
        for argv in ([], ["up", ""], ["down"], ["reset", "", ""], ["nuke", ""]):
            with self.subTest(argv=argv):
                self.assertEqual(local.main(argv, log=log()[0]), 2)


class Up(unittest.TestCase):

    def run_up(self, codes=None, token="dG9r"):
        calls = []

        def step(target, args):
            calls.append((target, args))
            return (codes or {}).get(target, 0)
        lg, out = log()
        kubectl = lambda *a: res(0, token) if token is not None else res(1, "", "NotFound")  # noqa: E731
        try:
            local.up(lg, step=step, kubectl=kubectl)
            err = None
        except (local.Failed, local.Missing) as e:
            err = e
        return calls, out.getvalue(), err

    def test_the_steps_in_order(self):
        calls, out, err = self.run_up()
        self.assertIsNone(err)
        self.assertEqual(calls, [("cluster-up", []), ("platform-up", []), ("shared-up", []),
                                 ("topics-apply", ["ENV=local", "BROKER=cluster"]), ("kafka-creds", ["ENV=local"]),
                                 ("image", []), ("kind-load", []), ("helm-install", ["ENV=local"])])
        # kafka-creds after topics-apply and before the install (architecture, 2026-10-28).
        names = [c[0] for c in calls]
        self.assertLess(names.index("topics-apply"), names.index("kafka-creds"))
        self.assertLess(names.index("kafka-creds"), names.index("helm-install"))
        for target, _ in calls:
            self.assertIn("local: %s run=r1" % target, out)

    def test_a_failed_step_stops_the_run_and_is_named(self):
        calls, out, err = self.run_up({"shared-up": 1})
        self.assertEqual([c[0] for c in calls], ["cluster-up", "platform-up", "shared-up"])
        self.assertIsInstance(err, local.Failed)
        self.assertIn("make shared-up", str(err))

    def test_a_missing_credential_is_exit_3(self):
        calls, out, err = self.run_up({"kafka-creds": 3})
        self.assertIsInstance(err, local.Missing)
        self.assertEqual(err.code, 3)
        self.assertNotIn("helm-install", [c[0] for c in calls])

    def test_it_says_whether_the_collectors_have_a_credential(self):
        _, out, _ = self.run_up(token="c2VjcmV0LXRva2Vu")
        self.assertIn("the collectors have a telemetry credential", out)
        self.assertNotIn("c2VjcmV0LXRva2Vu", out)
        for token in ("", None):
            with self.subTest(token=token):
                _, out, err = self.run_up(token=token)
                self.assertIsNone(err)
                self.assertIn("have no telemetry credential", out)
                self.assertIn("make platform-up", out)


class MakeStep(unittest.TestCase):
    """GNU make exits 2 for any failed recipe: the recipe's own code is read from its error line."""

    def test_the_recipes_code(self):
        with tempfile.TemporaryDirectory() as d:
            with open(os.path.join(d, "Makefile"), "w") as f:
                f.write("ok:\n\t@true\nthree:\n\t@exit 3\none:\n\t@exit 1\n")
            mk = ["make", "-C", d]
            self.assertEqual(local.make_step("ok", [], make=mk), 0)
            self.assertEqual(local.make_step("three", [], make=mk), 3)
            self.assertEqual(local.make_step("one", [], make=mk), 1)

    def test_a_sub_makes_line(self):
        self.assertEqual(local.MAKE_ERROR.match("make[1]: *** [Makefile:470: kafka-creds] Error 3\n").group(1), "3")
        self.assertIsNone(local.MAKE_ERROR.match("helm-install: Error 3 in a message"))


class Helm:
    def __init__(self, status=res(0, "STATUS: deployed")):
        self.status, self.calls = status, []

    def __call__(self, *args):
        self.calls.append(args)
        return self.status if args[0] == "status" else res()


class Down(unittest.TestCase):

    def test_it_uninstalls_the_release_only(self):
        helm, (lg, out) = Helm(), log()
        local.down(lg, helm=helm)
        self.assertEqual(helm.calls[1][:4], ("uninstall", "andara", "-n", "andara-local"))
        self.assertEqual(len(helm.calls), 2)
        self.assertIn("local: down run=r1", out.getvalue())

    def test_no_release_is_nothing_to_do(self):
        helm, (lg, out) = Helm(res(1, "", "Error: release: not found")), log()
        local.down(lg, helm=helm)
        self.assertEqual([c[0] for c in helm.calls], ["status"])
        self.assertIn("nothing to remove", out.getvalue())

    def test_an_unreadable_release_fails(self):
        with self.assertRaises(local.Failed):
            local.down(log()[0], helm=Helm(res(1, "", "Kubernetes cluster unreachable")))


LIVE = ["local.andara.commands.v1", "local.andara.events.v1", "local.andara.content.v1",
        "dev.andara.commands.v1", "andara.test.x", "it123.andara.commands.v1", "_schemas"]


class Broker:
    def __init__(self, live=LIVE):
        self.live, self.calls = list(live), []

    def __call__(self, args):
        self.calls.append(list(args))
        if args[:2] == ["topic", "list"]:
            return res(0, json.dumps([{"name": n, "partitions": 1} for n in self.live]))
        if args[:2] == ["topic", "delete"]:
            self.live.remove(args[2])
        return res()


def decl_with(old, new):
    with open(topics.DECL) as f:
        text = f.read()
    assert old in text, old
    fd, path = tempfile.mkstemp(suffix=".yaml")
    with os.fdopen(fd, "w") as f:
        f.write(text.replace(old, new, 1))
    return path


class Reset(unittest.TestCase):

    def run_reset(self, decl=topics.DECL, bucket=None):
        broker, helm, emptied, (lg, out) = Broker(), Helm(), [], log()
        try:
            local.reset(lg, run=broker, helm=helm, empty=emptied.append, decl=decl, bucket=bucket)
            err = None
        except local.Failed as e:
            err = e
        return broker, helm, emptied, out.getvalue(), err

    def test_it_deletes_local_only(self):
        broker, helm, emptied, out, err = self.run_reset()
        self.assertIsNone(err)
        deleted = [c[2] for c in broker.calls if c[:2] == ["topic", "delete"]]
        self.assertEqual(deleted, ["local.andara.commands.v1", "local.andara.content.v1", "local.andara.events.v1"])
        groups = [c[2] for c in broker.calls if c[:2] == ["group", "delete"]]
        self.assertTrue(groups)
        self.assertTrue(all(g.endswith("-local") for g in groups), groups)
        self.assertIn("andara-sim-local", groups)
        self.assertEqual(emptied, ["local"])
        for name in deleted + ["andara-snapshots-local"]:
            self.assertIn(name, out)
        self.assertIn("andara-sim-local", out)
        # The release goes first, so nothing writes to what's being deleted.
        self.assertEqual(helm.calls[1][0], "uninstall")

    def assertNothingDeleted(self, broker, helm, emptied):
        self.assertEqual([c for c in broker.calls if c[1:2] == ["delete"]], [])
        self.assertEqual([c for c in helm.calls if c[0] == "uninstall"], [])
        self.assertEqual(emptied, [])

    def test_a_prefix_other_than_local_deletes_nothing(self):
        for prefix in ('""', "dev"):
            with self.subTest(prefix=prefix):
                path = decl_with("  local:\n    prefix: local\n", "  local:\n    prefix: %s\n" % prefix)
                try:
                    broker, helm, emptied, _, err = self.run_reset(decl=path)
                finally:
                    os.unlink(path)
                self.assertIsInstance(err, local.Failed)
                self.assertIn("nothing deleted", str(err))
                self.assertNothingDeleted(broker, helm, emptied)

    def test_a_group_outside_local_deletes_nothing(self):
        path = decl_with("  - andara-sim-<env>", "  - andara-sim-dev")
        try:
            broker, helm, emptied, _, err = self.run_reset(decl=path)
        finally:
            os.unlink(path)
        self.assertIn("andara-sim-dev", str(err))
        self.assertNothingDeleted(broker, helm, emptied)

    def test_another_bucket_deletes_nothing(self):
        broker, helm, emptied, _, err = self.run_reset(bucket="andara-snapshots-dev")
        self.assertIn("andara-snapshots-dev", str(err))
        self.assertNothingDeleted(broker, helm, emptied)


if __name__ == "__main__":
    unittest.main()
