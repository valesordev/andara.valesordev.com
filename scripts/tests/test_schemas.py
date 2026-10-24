# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""AW-INF-057: schemas.py against the shared registry, over a fake Confluent-API registry: ENV
handling (AC-7), environment-prefixed subjects and references (AC-2, AC-3), an incompatible change
registering nothing (AC-4), diff's scope, the registry URL, the ASCII rule Karapace needs (AC-8),
and that nothing at runtime reads the registry (AC-5)."""
import contextlib
import io
import json
import os
import re
import subprocess
import sys
import unittest
import urllib.error
from unittest import mock

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
import schemas  # noqa: E402

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))


def files_from_protos():
    """descriptor()'s shape from the real .proto files' imports, so the tests need no buf."""
    out = {}
    for root, _, names in os.walk(schemas.PROTO_DIR):
        for n in names:
            if n.endswith(".proto"):
                rel = os.path.relpath(os.path.join(root, n), schemas.PROTO_DIR)
                with open(os.path.join(root, n)) as f:
                    text = f.read()
                out[rel] = {"package": "", "messages": [],
                            "deps": re.findall(r'^import "([^"]+)";', text, re.M)}
    return out


class FakeRegistry:
    """The Confluent subject API as schemas.py calls it. incompatible: subjects whose compatibility
    check fails."""

    def __init__(self, subjects=(), incompatible=()):
        self.subjects = {s: [{"version": 1, "id": 100 + i}] for i, s in enumerate(subjects)}
        self.incompatible = set(incompatible)
        self.calls, self.next_id = [], 1

    def __call__(self, url, path, method="GET", payload=None, params=""):
        self.calls.append((method, path, payload))
        p = path.split("/")
        if path == "/subjects" and method == "GET":
            return list(self.subjects)
        if path.startswith("/config"):
            return {"compatibilityLevel": "BACKWARD"} if method == "GET" else {}
        if p[1] == "compatibility":
            subject = schemas.urllib.parse.unquote(p[3])
            if subject in self.incompatible:
                return {"is_compatible": False, "messages": ["Incompatible modification FIELD_KIND_ALTER"]}
            return {"is_compatible": True}
        subject = schemas.urllib.parse.unquote(p[2])
        if path.endswith("/versions/latest"):
            if subject not in self.subjects:
                raise RuntimeError("GET %s -> HTTP 404" % path)
            return dict(self.subjects[subject][-1], subject=subject)
        if path.endswith("/versions") and method == "POST":
            self.subjects.setdefault(subject, []).append({"version": len(self.subjects.get(subject, [])) + 1,
                                                          "id": self.next_id, "payload": payload})
            self.next_id += 1
            return {"id": self.next_id - 1}
        if method == "POST":  # lookup
            if subject not in self.subjects:
                raise RuntimeError("POST %s -> HTTP 404" % path)
            return dict(self.subjects[subject][-1])
        raise AssertionError("unexpected registry call %s %s" % (method, path))


def run(fn, *args, registry):
    out, err = io.StringIO(), io.StringIO()
    with mock.patch.object(schemas, "call", registry), \
            mock.patch.object(schemas, "descriptor", files_from_protos), \
            contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
        try:
            fn(*args)
            code = 0
        except SystemExit as e:
            code = e.code
    return code, out.getvalue(), err.getvalue()


DECL = schemas.parse_schema_declaration(schemas.DECL)
DECLARED = [r["subject"] for r in DECL["references"] + DECL["subjects"]]


class Env(unittest.TestCase):
    """AC-7: ENV is required and known; --broker is local's only."""

    def main(self, *argv):
        # PATH without kubectl: a usage error must come before any cluster call.
        r = subprocess.run([sys.executable, os.path.join(REPO, "scripts", "schemas.py"), *argv],
                           capture_output=True, text=True, env=dict(os.environ, PATH="/usr/bin:/bin"))
        return r.returncode, r.stderr

    def test_env_unset_exits_2_naming_it(self):
        for action in ("apply", "diff"):
            code, err = self.main(action)
            self.assertEqual(code, 2, err)
            self.assertIn("ENV is required", err)

    def test_an_unknown_env_exits_2(self):
        for env in ("staging", "prod"):
            code, err = self.main("apply", "--env", env)
            self.assertEqual(code, 2, err)

    def test_broker_means_something_only_for_local(self):
        code, err = self.main("apply", "--env", "dev", "--broker", "compose")
        self.assertEqual(code, 2, err)

    def test_make_passes_env(self):
        r = subprocess.run(["make", "-s", "-C", REPO, "-n", "schemas-apply", "ENV=dev"], capture_output=True, text=True)
        self.assertIn("apply --env \"dev\"", r.stdout)


class Url(unittest.TestCase):
    def test_an_explicit_url_wins(self):
        with mock.patch.dict(os.environ, {"ANDARA_SCHEMA_REGISTRY_URL": "http://r:1/"}):
            self.assertEqual(schemas.registry_url("dev", "cluster"), ("http://r:1", None))

    def test_local_on_compose_is_localhost(self):
        env = {k: v for k, v in os.environ.items() if k != "ANDARA_SCHEMA_REGISTRY_URL"}
        env["ANDARA_SCHEMA_REGISTRY_PORT"] = "18081"
        with mock.patch.dict(os.environ, env, clear=True):
            self.assertEqual(schemas.registry_url("local", "compose"), ("http://localhost:18081", None))

    def test_the_cluster_is_a_port_forward(self):
        env = {k: v for k, v in os.environ.items() if k != "ANDARA_SCHEMA_REGISTRY_URL"}
        with mock.patch.dict(os.environ, env, clear=True), \
                mock.patch.object(schemas, "port_forward", return_value=("http://127.0.0.1:5", "p")):
            self.assertEqual(schemas.registry_url("dev", "cluster"), ("http://127.0.0.1:5", "p"))
            self.assertEqual(schemas.registry_url("local", "cluster"), ("http://127.0.0.1:5", "p"))

    def test_an_unreachable_registry_exits_1(self):
        with mock.patch("urllib.request.urlopen", side_effect=urllib.error.URLError("refused")), \
                contextlib.redirect_stderr(io.StringIO()) as err:
            with self.assertRaises(SystemExit) as e:
                schemas.call("http://nowhere:1", "/subjects")
        self.assertEqual(e.exception.code, 1)
        self.assertIn("no schema registry at http://nowhere:1", err.getvalue())


class Apply(unittest.TestCase):
    def test_every_subject_and_reference_is_registered_under_the_prefix(self):
        reg = FakeRegistry()
        code, out, err = run(schemas.apply_action, "dev", "http://r", registry=reg)
        self.assertEqual(code, 0, err)
        self.assertEqual(sorted(reg.subjects), sorted("dev." + s for s in DECLARED))
        self.assertIn("schemas-apply: %d subject(s), %d newly registered (env=dev, prefix=dev)"
                      % (len(DECLARED), len(DECLARED)), out)
        self.assertRegex(out, r"schemas: dev\.andara\.commands\.v1-value registered id=\d+ run=\w+")

    def test_a_reference_is_named_by_import_path_and_found_by_prefixed_subject(self):
        reg = FakeRegistry()
        run(schemas.apply_action, "local", "http://r", registry=reg)
        ref_file = DECL["references"][0]["file"]
        users = [v[-1]["payload"] for s, v in reg.subjects.items() if v[-1].get("payload", {}).get("references")]
        self.assertTrue(users)
        for payload in users:
            self.assertEqual(payload["references"][0]["name"], ref_file)
            self.assertEqual(payload["references"][0]["subject"], "local." + ref_file)

    def test_a_second_run_registers_nothing_new(self):
        reg = FakeRegistry()
        run(schemas.apply_action, "dev", "http://r", registry=reg)
        with mock.patch.object(schemas, "register", lambda url, s, src, refs: (1, False)):
            code, out, _ = run(schemas.apply_action, "dev", "http://r", registry=reg)
        self.assertIn("0 newly registered", out)

    def test_an_incompatible_change_registers_nothing_anywhere(self):
        existing = ["dev." + s for s in DECLARED]
        reg = FakeRegistry(existing, incompatible={"dev.andara.audit.v1-value"})
        code, _, err = run(schemas.apply_action, "dev", "http://r", registry=reg)
        self.assertEqual(code, 1)
        self.assertIn("schemas: error dev.andara.audit.v1-value refused under compatibility BACKWARD", err)
        self.assertIn("nothing was registered", err)
        self.assertFalse([c for c in reg.calls if c[0] == "POST" and c[1].endswith("/versions")])
        self.assertFalse([c for c in reg.calls if c[0] == "PUT"], "not even the compatibility config")

    def test_one_environment_never_writes_the_others_subjects(self):
        reg = FakeRegistry(["dev." + s for s in DECLARED])
        before = {s: list(v) for s, v in reg.subjects.items()}
        run(schemas.apply_action, "local", "http://r", registry=reg)
        self.assertEqual({s: v for s, v in reg.subjects.items() if s.startswith("dev.")}, before)


class Diff(unittest.TestCase):
    def test_a_matching_registry_is_clean(self):
        reg = FakeRegistry(["dev." + s for s in DECLARED] + ["local." + s for s in DECLARED])
        code, out, err = run(schemas.diff_action, "dev", "http://r", registry=reg)
        self.assertEqual(code, 0, err)
        self.assertIn("match the registry (env=dev, prefix=dev)", out)

    def test_an_unprefixed_subject_is_drift(self):
        reg = FakeRegistry(["dev." + s for s in DECLARED] + ["andara.commands.v1-value"])
        code, _, err = run(schemas.diff_action, "dev", "http://r", registry=reg)
        self.assertEqual(code, 1)
        self.assertIn("andara.commands.v1-value: carries no environment prefix", err)

    def test_its_own_undeclared_subject_is_drift_the_others_are_not(self):
        reg = FakeRegistry(["dev." + s for s in DECLARED] + ["dev.stray-value", "local.stray-value"])
        code, _, err = run(schemas.diff_action, "dev", "http://r", registry=reg)
        self.assertEqual(code, 1)
        self.assertIn("dev.stray-value: registered but not declared", err)
        self.assertNotIn("local.stray-value", err)


class Ascii(unittest.TestCase):
    """AC-8's new finding: Karapace refuses non-ASCII; schemas.py sends comments as ASCII."""

    def test_non_ascii_in_a_comment_becomes_ascii(self):
        self.assertEqual(schemas.ascii_comments('message M { // a — b\n  string a = 1;\n}'),
                         'message M { // a ? b\n  string a = 1;\n}')

    def test_non_ascii_outside_a_comment_is_refused(self):
        with contextlib.redirect_stderr(io.StringIO()) as err, self.assertRaises(SystemExit) as e:
            schemas.ascii_comments('option x = "é";', "f.proto")
        self.assertEqual(e.exception.code, 1)
        self.assertIn("f.proto:1: a non-ASCII character outside a // comment", err.getvalue())

    def test_every_declared_proto_goes_out_ascii(self):
        for r in DECL["references"] + DECL["subjects"]:
            self.assertTrue(schemas.source_of(r["file"]).isascii(), r["file"])


class NoRuntimeReader(unittest.TestCase):
    """AC-5: nothing at runtime reads the registry, so its NetworkPolicy may admit no workload."""

    def test_no_source_reads_the_registry_url(self):
        hits = []
        for top in ("server", "internal", "cmd", "admin"):
            for root, _, names in os.walk(os.path.join(REPO, top)):
                for n in names:
                    if n.endswith((".go", ".py", ".ts")):
                        with open(os.path.join(root, n), errors="replace") as f:
                            if "ANDARA_SCHEMA_REGISTRY" in f.read():
                                hits.append(os.path.relpath(os.path.join(root, n), REPO))
        self.assertEqual(hits, [], "a runtime reader needs the registry's NetworkPolicy reopened (AW-INF-057)")


if __name__ == "__main__":
    unittest.main()
