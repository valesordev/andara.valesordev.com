# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""`make local-up`, `local-down`, `local-reset` (AW-INF-060 AC-1, AC-2, AC-3): the steps and their order,
the refusals, and that a reset deletes nothing outside local's prefix."""
import io
import json
import os
import shutil
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
    """The target with kubectl, helm, kind and docker stubbed to log and exit 97 ahead of the real
    ones (which /usr/bin has on the box), and a scratch copy of the fixture kubeconfig: a refusal
    that regressed fails differently and never acts. Returns (result, the stubs' calls)."""
    with tempfile.TemporaryDirectory() as d:
        calls = os.path.join(d, "calls")
        for tool in ("kubectl", "helm", "kind", "docker"):
            p = os.path.join(d, tool)
            with open(p, "w") as f:
                f.write('#!/bin/sh\necho "%s $*" >> "%s"\nexit 97\n' % (tool, calls))
            os.chmod(p, 0o755)
        kubeconfig = os.path.join(d, "kubeconfig")
        shutil.copy(os.path.join(REPO, "scripts", "tests", "fixtures", "kubeconfig"), kubeconfig)
        env = dict(os.environ, PATH=d + os.pathsep + "/usr/bin:/bin", KUBECONFIG=kubeconfig)
        env.pop("ANDARA_KUBE_CONTEXT", None)
        r = subprocess.run(["make", "-s", "-C", REPO, *args], capture_output=True, text=True, env=env)
        made = open(calls).read().splitlines() if os.path.exists(calls) else []
    return r, made


class Refusals(unittest.TestCase):

    def test_another_env_is_refused(self):
        for target in ("local-up", "local-down", "local-reset"):
            with self.subTest(target=target):
                r, made = make(target, "ENV=dev")
                self.assertEqual(r.returncode, 2, r.stderr)  # make's own code for a recipe that failed
                self.assertIn("acts on andara-local only, not ENV=dev", r.stderr)
                self.assertIn("Error 1", r.stderr)
                self.assertEqual(made, [])

    def test_local_up_takes_no_profile(self):
        r, made = make("local-up", "PROFILE=ci")
        self.assertIn("takes no PROFILE", r.stderr)
        self.assertIn("Error 1", r.stderr)
        self.assertEqual(made, [])

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

    def test_an_unreadable_secret_is_not_called_missing(self):
        lg, out = log()
        local.up(lg, step=lambda t, a: 0, kubectl=lambda *a: res(1, "", "Unable to connect to the server"))
        self.assertIn("could not read observability/grafana-telemetry", out.getvalue())
        self.assertNotIn("no telemetry credential", out.getvalue())

    def test_the_refusal_names_the_env(self):
        with self.assertRaises(local.Failed) as e:
            local.check_env("prod", "up")
        self.assertIn("andara-prod", str(e.exception))
        self.assertNotIn("dev", str(e.exception))


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
        self.assertEqual(local.MAKE_ERROR.match("gmake: *** [Makefile:470: kafka-creds] Error 3\n").group(1), "3")
        self.assertIsNone(local.MAKE_ERROR.match("helm-install: Error 3 in a message"))


class Helm:
    def __init__(self, status=res(0, "STATUS: deployed"), events=None):
        self.status, self.calls = status, []
        self.events = events if events is not None else []

    def __call__(self, *args):
        self.calls.append(args)
        self.events.append("helm " + args[0])
        return self.status if args[0] == "status" else res()


class Down(unittest.TestCase):

    def test_it_uninstalls_the_release_only(self):
        helm, (lg, out) = Helm(), log()
        local.down(lg, helm=helm)
        self.assertEqual(helm.calls[1][:4], ("uninstall", "andara", "-n", "andara-local"))
        # Waited on: a reset deletes the topics next, and nothing may still be writing to them.
        self.assertIn("--wait", helm.calls[1])
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


# `local` and `localx.…` share local's prefix as a string, not as a segment: never local's.
LIVE = ["local.andara.commands.v1", "local.andara.events.v1", "local.andara.content.v1",
        "dev.andara.commands.v1", "andara.test.x", "it123.andara.commands.v1", "_schemas",
        "local", "localx.andara.commands.v1"]


class Broker:
    """rpk as the shared Kafka answers: `group delete` exits 0 and reports each group's outcome in
    its STATUS column, GROUP_ID_NOT_FOUND for one that isn't there. A deleted topic stays listed
    for `linger` listings, as an asynchronous delete does."""

    def __init__(self, live=LIVE, groups=("andara-sim-local",), group_status=None, linger=0, events=None):
        self.live, self.groups, self.calls = list(live), set(groups), []
        self.group_status, self.linger, self.pending = group_status or {}, linger, {}
        self.events = events if events is not None else []

    def __call__(self, args):
        self.calls.append(list(args))
        if args[:2] == ["topic", "list"]:
            for name, left in list(self.pending.items()):
                if left == 0:
                    self.live.remove(name)
                    del self.pending[name]
                else:
                    self.pending[name] = left - 1
            return res(0, json.dumps([{"name": n, "partitions": 1} for n in self.live]))
        if args[:2] in (["topic", "delete"], ["group", "delete"]):
            self.events.append(" ".join(args[:2]))
        if args[:2] == ["topic", "delete"]:
            self.pending[args[2]] = self.linger
        if args[:2] == ["group", "delete"]:
            g = args[2]
            status = self.group_status.get(g) or ("OK" if g in self.groups else
                                                  "GROUP_ID_NOT_FOUND: The group id does not exist.")
            return res(0, "GROUP%sSTATUS\n%s  %s\n" % (" " * (len(g) - 3), g, status))
        return res()


class Groups(unittest.TestCase):

    def test_each_groups_outcome_is_read_from_its_status(self):
        lg, out = log()
        local.delete_groups(lg, Broker(), ["andara-sim-local", "andara-content-local"])
        self.assertIn("deleted consumer group andara-sim-local", out.getvalue())
        self.assertIn("consumer group andara-content-local was not there", out.getvalue())
        self.assertNotIn("deleted consumer group andara-content-local", out.getvalue())

    def test_a_refused_delete_fails_though_rpk_exits_0(self):
        for status in ("NON_EMPTY_GROUP: The group is not empty.",
                       "GROUP_AUTHORIZATION_FAILED: Not authorized to access group."):
            with self.subTest(status=status):
                broker = Broker(group_status={"andara-sim-local": status})
                with self.assertRaises(local.Failed) as e:
                    local.delete_groups(log()[0], broker, ["andara-sim-local"])
                self.assertIn(status.split(":")[0], str(e.exception))


def decl_with(old, new):
    with open(topics.DECL) as f:
        text = f.read()
    assert old in text, old
    fd, path = tempfile.mkstemp(suffix=".yaml")
    with os.fdopen(fd, "w") as f:
        f.write(text.replace(old, new, 1))
    return path


class Reset(unittest.TestCase):

    def run_reset(self, decl=topics.DECL, bucket=None, empty=None, broker=None):
        events = []
        broker = broker or Broker(events=events)
        broker.events = events
        helm, emptied, (lg, out) = Helm(events=events), [], log()

        def record(env):
            events.append("empty " + env)
            emptied.append(env)
        try:
            local.reset(lg, run=broker, helm=helm, empty=empty or record, decl=decl, bucket=bucket,
                        sleep=lambda s: None)
            err = None
        except local.Failed as e:
            err = e
        self.events = events
        return broker, helm, emptied, out.getvalue(), err

    def test_it_deletes_local_only(self):
        broker, helm, emptied, out, err = self.run_reset()
        self.assertIsNone(err)
        deleted = [c[2] for c in broker.calls if c[:2] == ["topic", "delete"]]
        self.assertEqual(deleted, ["local.andara.commands.v1", "local.andara.content.v1", "local.andara.events.v1"])
        self.assertIn("local", broker.live)
        self.assertIn("localx.andara.commands.v1", broker.live)
        groups = [c[2] for c in broker.calls if c[:2] == ["group", "delete"]]
        self.assertTrue(groups)
        self.assertTrue(all(g.endswith("-local") for g in groups), groups)
        self.assertIn("andara-sim-local", groups)
        self.assertEqual(emptied, ["local"])
        for name in deleted + ["andara-snapshots-local"]:
            self.assertIn(name, out)
        self.assertIn("andara-sim-local", out)

    def test_the_order(self):
        """The release first, so nothing writes to what's being deleted; the bucket before the log,
        so a failure part-way never leaves a snapshot whose log is gone."""
        self.run_reset()
        first = {e: self.events.index(e) for e in reversed(self.events)}
        self.assertLess(first["helm uninstall"], first["empty local"])
        self.assertLess(first["empty local"], first["topic delete"])
        self.assertLess(first["empty local"], first["group delete"])

    def test_a_failed_empty_leaves_the_log_and_says_to_rerun(self):
        def fail(env):
            raise local.objectstore.Failed("bucket andara-snapshots-local still holds 3 object(s) after delete")
        broker, helm, _, _, err = self.run_reset(empty=fail)
        self.assertIsInstance(err, local.Failed)
        self.assertIn("make local-reset", str(err))
        self.assertEqual([c for c in broker.calls if c[1:2] == ["delete"]], [])

    def test_it_waits_until_the_topics_are_gone(self):
        broker, _, _, _, err = self.run_reset(broker=Broker(linger=3))
        self.assertIsNone(err)
        lists = [c for c in broker.calls if c[:2] == ["topic", "list"]]
        self.assertGreaterEqual(len(lists), 1 + 4)  # the plan's, then until the last delete lands

    def test_topics_that_never_go_fail(self):
        broker = Broker(linger=10 ** 6)
        ticks = iter(range(0, 10 ** 6, 30))
        with self.assertRaises(local.Failed) as e:
            local.delete_topics(log()[0], broker, ["local.andara.commands.v1"], sleep=lambda s: None,
                                clock=lambda: next(ticks))
        self.assertIn("still listed", str(e.exception))

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
