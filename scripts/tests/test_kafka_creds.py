# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""AW-INF-056 AC-4 and AC-5: each environment's credentials are copied into its own namespace only,
rotation writes a new password and re-copies it, and no value is ever printed. Also the KafkaUser
apply `make shared-up` runs."""
import base64
import contextlib
import io
import json
import os
import sys
import unittest
from types import SimpleNamespace
from unittest import mock

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
import kafka_creds  # noqa: E402
import kafka_users  # noqa: E402

FIXTURE = os.path.join(os.path.dirname(os.path.abspath(__file__)), "fixtures", "kubeconfig")
REAL = kafka_users.load()


def b64(s):
    return base64.b64encode(s.encode()).decode()


class FakeKube:
    """kubectl over {ns: {secret name: {"data": .., "labels": ..}}}, plus KafkaUser annotations."""

    def __init__(self):
        self.secrets = {"andara-shared": {}, "andara-dev": {}, "andara-local": {}}
        for env in ("dev", "local"):
            for p in ("andara-server", "andara-projector-state"):
                self.secrets["andara-shared"]["%s-%s" % (p, env)] = {"data": {"password": b64("pw-%s-%s" % (p, env)),
                                                                              "sasl.jaas.config": b64("jaas")}}
        self.calls = []
        self.regenerate = True

    def __call__(self, argv, capture_output=True, text=True, input=None):
        args = argv[argv.index("-n") + 2:]
        ns = argv[argv.index("-n") + 1]
        self.calls.append((ns, args))
        ok = lambda out="": SimpleNamespace(returncode=0, stdout=out, stderr="")  # noqa: E731
        miss = SimpleNamespace(returncode=1, stdout="", stderr="NotFound")
        store = self.secrets[ns]
        if args[:2] == ["get", "secret"] and len(args) > 2 and not args[2].startswith("-"):
            s = store.get(args[2])
            if s is None:
                return miss
            if "jsonpath={.data.password}" in args:
                return ok(s["data"].get("password", ""))
            return ok(json.dumps({"data": s["data"]}))
        if args[:2] == ["get", "secret"] and "-l" in args:
            return ok("\n".join("secret/" + n for n, s in store.items() if s.get("labels", {}).get(kafka_creds.LABEL)))
        if args[:2] == ["apply", "-f"]:
            doc = json.loads(input)
            store[doc["metadata"]["name"]] = {"data": doc["data"], "labels": doc["metadata"]["labels"]}
            return ok()
        if args[0] == "delete":
            name = args[-1].split("/", 1)[-1]
            store.pop(name, None)
            return ok()
        if args[0] == "annotate":
            name = args[-2].split("/", 1)[1]
            if self.regenerate:
                store[name] = {"data": {"password": b64("new-" + name)}}
            return ok()
        raise AssertionError("unexpected kubectl %r" % (args,))


class Base(unittest.TestCase):
    def setUp(self):
        env = {k: v for k, v in os.environ.items() if k not in ("ANDARA_KUBE_CONTEXT", "ANDARA_KAFKA_NAMESPACE")}
        p = mock.patch.dict(os.environ, dict(env, KUBECONFIG=FIXTURE), clear=True)
        p.start()
        self.addCleanup(p.stop)
        self.kube = FakeKube()

    def run_main(self, fn, *args, **kw):
        out, err = io.StringIO(), io.StringIO()
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            try:
                fn(*args, run=self.kube, doc=REAL, **kw)
                code = 0
            except kafka_creds.Failed as e:
                print("make: kafka-creds: %s" % e, file=sys.stderr)
                code = e.code
        return code, out.getvalue(), err.getvalue()


class Copy(Base):
    def test_dev_gets_its_own_pair_only_and_local_nothing(self):
        code, out, _ = self.run_main(kafka_creds.copy, "dev")
        self.assertEqual(code, 0)
        self.assertEqual(sorted(self.kube.secrets["andara-dev"]), ["andara-projector-state-dev", "andara-server-dev"])
        self.assertEqual(self.kube.secrets["andara-local"], {})
        # The password only, never the JAAS line that also carries it.
        self.assertEqual(self.kube.secrets["andara-dev"]["andara-server-dev"]["data"],
                         {"password": b64("pw-andara-server-dev")})
        self.assertIn("kafka-creds: andara-server-dev -> andara-dev created", out)
        # Nothing is written outside andara-dev.
        self.assertFalse([c for c in self.kube.calls if c[1][0] in ("apply", "delete") and c[0] != "andara-dev"])

    def test_a_second_run_changes_nothing(self):
        self.run_main(kafka_creds.copy, "dev")
        code, out, _ = self.run_main(kafka_creds.copy, "dev")
        self.assertEqual(code, 0)
        self.assertIn("andara-server-dev -> andara-dev unchanged", out)
        self.assertNotIn("created", out)

    def test_no_value_is_printed(self):
        _, out, err = self.run_main(kafka_creds.copy, "local")
        for v in ("pw-andara-server-local", b64("pw-andara-server-local")):
            self.assertNotIn(v, out + err)

    def test_a_copy_whose_principal_is_gone_is_removed_and_others_kept(self):
        self.run_main(kafka_creds.copy, "dev")
        self.kube.secrets["andara-dev"]["andara-old-dev"] = {"data": {}, "labels": {kafka_creds.LABEL: "true"}}
        self.kube.secrets["andara-dev"]["andara-token-key"] = {"data": {}}
        _, out, _ = self.run_main(kafka_creds.copy, "dev")
        self.assertNotIn("andara-old-dev", self.kube.secrets["andara-dev"])
        self.assertIn("andara-token-key", self.kube.secrets["andara-dev"])
        self.assertIn("andara-old-dev -> andara-dev removed", out)

    def test_a_missing_source_exits_1_naming_it(self):
        del self.kube.secrets["andara-shared"]["andara-server-dev"]
        code, _, err = self.run_main(kafka_creds.copy, "dev")
        self.assertEqual(code, 1)
        self.assertIn("no Secret andara-shared/andara-server-dev", err)

    def test_an_unknown_environment_exits_2(self):
        code, _, err = self.run_main(kafka_creds.copy, "prod")
        self.assertEqual(code, 2)
        self.assertIn("ENV=prod", err)
        self.assertFalse(self.kube.calls)


class Rotate(Base):
    def test_a_new_password_reaches_the_copy(self):
        self.run_main(kafka_creds.copy, "dev")
        code, out, _ = self.run_main(kafka_creds.rotate, "dev", "andara-server", sleep=lambda s: None)
        self.assertEqual(code, 0)
        self.assertEqual(self.kube.secrets["andara-dev"]["andara-server-dev"]["data"]["password"],
                         b64("new-andara-server-dev"))
        self.assertIn("andara-server-dev -> andara-dev updated", out)
        verbs = [c[1][0] for c in self.kube.calls if c[0] == "andara-shared" and c[1][0] in ("delete", "annotate")]
        self.assertEqual(verbs, ["delete", "annotate"])

    def test_no_new_secret_in_time_fails_without_copying(self):
        self.kube.regenerate = False
        clock = iter(range(0, 10000, 100))
        code, _, err = self.run_main(kafka_creds.rotate, "dev", "andara-server", sleep=lambda s: None,
                                     now=lambda: next(clock))
        self.assertEqual(code, 1)
        self.assertIn("wrote no new Secret andara-shared/andara-server-dev", err)
        self.assertEqual(self.kube.secrets["andara-dev"], {})

    def test_another_environments_principal_is_refused(self):
        code, _, err = self.run_main(kafka_creds.rotate, "dev", "andara-server-local", sleep=lambda s: None)
        self.assertEqual(code, 2)
        self.assertFalse([c for c in self.kube.calls if c[1][0] in ("delete", "annotate")])


class Apply(unittest.TestCase):
    """`kafka_users.py apply`, from `make shared-up`."""

    def setUp(self):
        env = {k: v for k, v in os.environ.items() if k != "ANDARA_KUBE_CONTEXT"}
        p = mock.patch.dict(os.environ, dict(env, KUBECONFIG=FIXTURE), clear=True)
        p.start()
        self.addCleanup(p.stop)

    def test_it_applies_deletes_the_undeclared_and_waits_on_each(self):
        calls = []

        def run(argv, capture_output=True, text=True, input=None):
            args = argv[argv.index("-n") + 2:]
            calls.append((args, input))
            out = ""
            if args[:2] == ["get", "kafkausers"]:
                out = "kafkauser.kafka.strimzi.io/andara-server-dev\nkafkauser.kafka.strimzi.io/andara-gone-dev\n"
            return SimpleNamespace(returncode=0, stdout=out, stderr="")
        users = kafka_users.render(REAL)
        with contextlib.redirect_stdout(io.StringIO()) as out:
            kafka_users.apply("andara-shared", users, run=run)
        self.assertEqual(calls[0][0], ["apply", "-f", "-"])
        self.assertEqual(len(list(kafka_users.yaml.safe_load_all(calls[0][1]))), len(users))
        self.assertIn((["delete", "kafkauser.kafka.strimzi.io/andara-gone-dev"], None), calls)
        waited = {a[1] for a, _ in calls if a[0] == "wait"}
        self.assertEqual(waited, {"kafkauser/" + u["metadata"]["name"] for u in users})
        self.assertIn("deleted KafkaUser andara-gone-dev", out.getvalue())

    def test_a_failed_step_exits_1(self):
        run = lambda argv, **kw: SimpleNamespace(returncode=1, stdout="", stderr="denied")  # noqa: E731
        with contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()) as err, \
                self.assertRaises(SystemExit) as e:
            kafka_users.apply("andara-shared", kafka_users.render(REAL), run=run)
        self.assertEqual(e.exception.code, 1)
        self.assertIn("denied", err.getvalue())


if __name__ == "__main__":
    unittest.main()
