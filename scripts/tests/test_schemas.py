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
    """The Confluent subject API as schemas.py calls it, as a registry behaves: an identical schema
    under a subject returns its existing id and adds no version; a lookup finds a version by its
    payload. incompatible: subjects whose compatibility check fails. strict: like Karapace, a schema
    whose import has no reference is a 422, on register and on a compatibility question alike."""

    def __init__(self, subjects=(), incompatible=(), strict=False, payloads=None):
        self.subjects = {s: [{"version": 1, "id": 100 + i, "payload": (payloads or {}).get(s)}]
                         for i, s in enumerate(subjects)}
        self.incompatible = set(incompatible)
        self.strict = strict
        self.calls, self.next_id = [], 1
        self.config = "BACKWARD"

    def unresolved(self, path, payload):
        imports = set(re.findall(r'^import "([^"]+)";', (payload or {}).get("schema", ""), re.M))
        named = {r["name"] for r in (payload or {}).get("references", [])}
        if self.strict and imports - named:
            raise RuntimeError("POST %s -> HTTP 422 unresolved import %s" % (path, sorted(imports - named)))

    def __call__(self, url, path, method="GET", payload=None, params=""):
        self.calls.append((method, path + params, payload))
        p = path.split("/")
        if path == "/subjects" and method == "GET":
            return list(self.subjects)
        if path.startswith("/config"):
            if method == "PUT" and path == "/config":
                self.config = payload["compatibility"]
            return {"compatibilityLevel": self.config} if method == "GET" else {}
        if p[1] == "compatibility":
            self.unresolved(path, payload)
            subject = schemas.urllib.parse.unquote(p[3])
            if subject in self.incompatible:
                return {"is_compatible": False, "messages": ["Incompatible modification FIELD_KIND_ALTER"]}
            return {"is_compatible": True}
        subject = schemas.urllib.parse.unquote(p[2])
        if method == "DELETE" and len(p) == 5 and p[3] == "versions":
            version = next(v for v in self.subjects[subject] if v["version"] == int(p[4]))
            if "permanent" not in params:
                version["deleted"] = True
                return {}
            if not version.get("deleted"):
                raise RuntimeError("DELETE %s -> HTTP 404 not soft-deleted first" % path)
            self.subjects[subject].remove(version)
            if not self.subjects[subject]:
                del self.subjects[subject]
            return {}
        if path.endswith("/versions/latest"):
            if subject not in self.subjects:
                raise RuntimeError("GET %s -> HTTP 404" % path)
            live = [v for v in self.subjects[subject] if not v.get("deleted")]
            if not live:
                raise RuntimeError("GET %s -> HTTP 404" % path)
            return dict(live[-1], subject=subject)
        if path.endswith("/versions") and method == "POST":
            self.unresolved(path, payload)
            versions = self.subjects.setdefault(subject, [])
            same = [v for v in versions if v.get("payload") == payload and not v.get("deleted")]
            if same:
                return {"id": same[-1]["id"]}
            versions.append({"version": len(versions) + 1, "id": self.next_id, "payload": payload})
            self.next_id += 1
            return {"id": self.next_id - 1}
        if method == "POST":  # lookup
            for v in self.subjects.get(subject, []):
                if v.get("payload") == payload and not v.get("deleted"):
                    return dict(v)
            raise RuntimeError("POST %s -> HTTP 404" % path)
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

    def test_an_unreachable_registry_is_an_error_the_run_can_report(self):
        """Pre-review P2: unreachable raises, so apply's rollback and reporting paths see it, as
        they see an HTTP error, rather than call() exiting under them."""
        for failure in (urllib.error.URLError("refused"), TimeoutError("timed out"), ConnectionResetError("reset")):
            with mock.patch("urllib.request.urlopen", side_effect=failure):
                with self.assertRaises(schemas.Unreachable) as e:
                    schemas.call("http://nowhere:1", "/subjects")
            self.assertIsInstance(e.exception, RuntimeError)
            self.assertIn("no schema registry at http://nowhere:1", str(e.exception))

    def test_an_unreachable_registry_is_never_read_as_an_absent_subject(self):
        """A lost registry is not 'declared but not registered', 'new subject' or 'reference not
        registered yet': each of those reads would act on an answer the registry never gave."""
        reg = FakeRegistry(strict=True)
        run(schemas.apply_action, "dev", "http://r", registry=reg)

        def gone(url, path, method="GET", payload=None, params=""):
            raise schemas.Unreachable("no schema registry at %s (refused)" % url)
        with mock.patch.object(schemas, "call", gone), mock.patch.object(schemas, "descriptor", files_from_protos), \
                contextlib.redirect_stdout(io.StringIO()):
            with self.assertRaises(schemas.Unreachable):
                schemas.diff_action("dev", "http://r")
            with self.assertRaises(schemas.Unreachable):
                schemas.incompatible("http://r", "dev.x", "src", [], "BACKWARD")
            importer = next(r for r in DECL["subjects"] if files_from_protos()[r["file"]]["deps"])
            with self.assertRaises(schemas.Unreachable):
                schemas.reference_list("http://r", files_from_protos(), importer["file"], "dev")

    def test_main_exits_1_on_an_unreachable_registry(self):
        with mock.patch.object(schemas, "registry_url", return_value=("http://nowhere:1", None)), \
                mock.patch("urllib.request.urlopen", side_effect=urllib.error.URLError("refused")), \
                mock.patch.object(sys, "argv", ["schemas.py", "diff", "--env", "dev"]), \
                contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()) as err:
            with self.assertRaises(SystemExit) as e:
                schemas.main()
        self.assertEqual(e.exception.code, 1)
        self.assertIn("no schema registry at http://nowhere:1", err.getvalue())
        self.assertNotIn("Traceback", err.getvalue())


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
        versions = {s: len(v) for s, v in reg.subjects.items()}
        code, out, err = run(schemas.apply_action, "dev", "http://r", registry=reg)
        self.assertEqual(code, 0, err)
        self.assertIn("0 newly registered", out)
        self.assertEqual({s: len(v) for s, v in reg.subjects.items()}, versions)

    def test_a_new_import_is_registered_reference_first_not_a_crash(self):
        """Pre-review P1: the compatibility pre-pass can't ask about an importer whose reference isn't
        registered yet (the registry can't resolve the import); it asks after the reference is in."""
        reg = FakeRegistry(strict=True)
        run(schemas.apply_action, "dev", "http://r", registry=reg)
        ref = "dev." + DECL["references"][0]["file"]
        # The reference is lost (or changes): every importer now has an import its registry can't resolve.
        del reg.subjects[ref]
        code, out, err = run(schemas.apply_action, "dev", "http://r", registry=reg)
        self.assertEqual(code, 0, err)
        self.assertNotIn("Traceback", err)
        self.assertIn(ref, reg.subjects)

    def test_a_deferred_importer_refused_leaves_the_registry_as_it_was(self):
        """Pre-review round 2 P1: a reference and its importer changed together. The reference has to
        be registered before the importer can be asked; the importer is refused; the reference's new
        version is rolled back, so AC-4's 'registers nothing' holds."""
        reg = FakeRegistry(strict=True)
        run(schemas.apply_action, "dev", "http://r", registry=reg)
        ref = "dev." + DECL["references"][0]["file"]
        importer = next(s for s, v in reg.subjects.items() if (v[-1].get("payload") or {}).get("references"))
        # The reference's registered version differs from the file now (it changed in this commit).
        reg.subjects[ref][-1]["payload"] = dict(reg.subjects[ref][-1]["payload"], schema="old")
        reg.incompatible = {importer}
        before = {s: [v["version"] for v in vs] for s, vs in reg.subjects.items()}
        code, _, err = run(schemas.apply_action, "dev", "http://r", registry=reg)
        self.assertEqual(code, 1)
        self.assertIn("the registry refuses %s under compatibility BACKWARD; nothing was registered (rolled back %s@2)"
                      % (importer, ref), err)
        self.assertEqual({s: [v["version"] for v in vs] for s, vs in reg.subjects.items()}, before)
        self.assertTrue([c for c in reg.calls if c[0] == "DELETE"])

    def flaky_reference(self):
        reg = FakeRegistry(strict=True)
        run(schemas.apply_action, "dev", "http://r", registry=reg)
        real = reg.__call__
        ref_path = "/subjects/%s" % schemas.q("dev." + DECL["references"][0]["file"])

        def flaky(url, path, method="GET", payload=None, params=""):
            if method == "POST" and path == ref_path:
                raise RuntimeError("POST %s -> HTTP 500 boom" % path)
            return real(url, path, method, payload, params)
        return flaky

    def test_a_reference_lookup_that_fails_is_not_ready(self):
        """refs_ready's error branch: a lookup answering 500 defers the importer."""
        flaky = self.flaky_reference()
        importer = next(r for r in DECL["subjects"] if files_from_protos()[r["file"]]["deps"])
        with mock.patch.object(schemas, "call", flaky):
            self.assertFalse(schemas.refs_ready("http://r", files_from_protos(), importer["file"], "dev"))

    def test_a_registry_failing_mid_apply_exits_1_naming_the_subject(self):
        code, _, err = run(schemas.apply_action, "dev", "http://r", registry=self.flaky_reference())
        self.assertEqual(code, 1)
        self.assertNotIn("Traceback", err)
        self.assertIn("registering dev.andara/content/v1/zone.proto failed: POST", err)
        self.assertIn("registered by this run: nothing", err)

    def two_references(self):
        """A declaration with references a.proto and b.proto, imported by subjects sa and sb."""
        src = {"a.proto": 'syntax = "proto3";\npackage a;\nmessage A { string x = 1; }\n',
               "b.proto": 'syntax = "proto3";\npackage b;\nmessage B { string x = 1; }\n',
               "sa.proto": 'syntax = "proto3";\nimport "a.proto";\npackage sa;\nmessage SA { a.A a = 1; }\n',
               "sb.proto": 'syntax = "proto3";\nimport "b.proto";\npackage sb;\nmessage SB { b.B b = 1; }\n'}
        doc = {"compatibility": "BACKWARD",
               "references": [{"file": "a.proto", "subject": "a.proto"}, {"file": "b.proto", "subject": "b.proto"}],
               "subjects": [{"subject": "sa-value", "file": "sa.proto"}, {"subject": "sb-value", "file": "sb.proto"}]}
        files = {"a.proto": {"deps": []}, "b.proto": {"deps": []},
                 "sa.proto": {"deps": ["a.proto"]}, "sb.proto": {"deps": ["b.proto"]}}
        return src, doc, files

    def run_two(self, reg, src, doc, files):
        out, err = io.StringIO(), io.StringIO()
        with mock.patch.object(schemas, "call", reg), mock.patch.object(schemas, "descriptor", lambda: files), \
                mock.patch.object(schemas, "parse_schema_declaration", lambda p: doc), \
                mock.patch.object(schemas, "source_of", lambda rel: src[rel]), \
                contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            try:
                schemas.apply_action("dev", "http://r")
                return 0, out.getvalue(), err.getvalue()
            except SystemExit as e:
                return e.code, out.getvalue(), err.getvalue()

    def test_a_rollback_never_touches_a_version_this_run_didnt_create(self):
        """Pre-review round 3 P1: a reference reverted to its v1 source while v2 is latest creates
        nothing; when another importer is refused, only b's new version is rolled back."""
        src, doc, files = self.two_references()
        reg = FakeRegistry(strict=True)
        self.run_two(reg, src, doc, files)
        a_payload = reg.subjects["dev.a.proto"][0]["payload"]
        # a.proto gained a v2 since (someone else's), and the file is back at v1's source.
        reg.subjects["dev.a.proto"].append({"version": 2, "id": 900, "payload": dict(a_payload, schema="A v2")})
        # b.proto changed in this commit, and its importer is refused.
        src["b.proto"] = src["b.proto"].replace("string x = 1;", "string x = 1; string y = 2;")
        reg.incompatible = {"dev.sb-value"}
        before = {s: [v["version"] for v in vs] for s, vs in reg.subjects.items()}
        calls_before = len(reg.calls)
        code, _, err = self.run_two(reg, src, doc, files)
        self.assertEqual(code, 1)
        self.assertIn("nothing was registered (rolled back dev.b.proto@2)", err)
        self.assertEqual({s: [v["version"] for v in vs] for s, vs in reg.subjects.items()}, before)
        # Nothing is configured on a refused run.
        self.assertFalse([c for c in reg.calls[calls_before:] if c[0] == "PUT"])
        # Soft, then permanent.
        deletes = [c[1] for c in reg.calls[calls_before:] if c[0] == "DELETE"]
        self.assertEqual(deletes, ["/subjects/dev.b.proto/versions/2", "/subjects/dev.b.proto/versions/2?permanent=true"])

    def test_a_rollback_that_fails_names_what_it_left(self):
        src, doc, files = self.two_references()
        reg = FakeRegistry(strict=True)
        self.run_two(reg, src, doc, files)
        src["b.proto"] = src["b.proto"].replace("string x = 1;", "string x = 1; string y = 2;")
        reg.incompatible = {"dev.sb-value"}
        real = reg.__call__

        def no_delete(url, path, method="GET", payload=None, params=""):
            if method == "DELETE":
                raise RuntimeError("DELETE %s -> HTTP 422 references exist" % path)
            return real(url, path, method, payload, params)
        code, _, err = self.run_two(no_delete, src, doc, files)
        self.assertEqual(code, 1)
        self.assertNotIn("Traceback", err)
        self.assertIn("refuses dev.sb-value under compatibility BACKWARD; rollback incomplete: still registered dev.b.proto@2", err)

    def test_a_failed_question_about_a_deferred_importer_rolls_back(self):
        """Pre-review round 4 P2: a registry error (not a refusal) asking a deferred importer undoes
        the references phase 2 registered, as a refusal does."""
        src, doc, files = self.two_references()
        reg = FakeRegistry(strict=True)
        self.run_two(reg, src, doc, files)
        src["b.proto"] = src["b.proto"].replace("string x = 1;", "string x = 1; string y = 2;")
        real = reg.__call__

        def boom(url, path, method="GET", payload=None, params=""):
            if path.startswith("/compatibility/subjects/dev.sb-value"):
                raise RuntimeError("POST %s -> HTTP 500 boom" % path)
            return real(url, path, method, payload, params)
        code, _, err = self.run_two(boom, src, doc, files)
        self.assertEqual(code, 1)
        self.assertIn("asking the registry about dev.sb-value failed: POST", err)
        self.assertIn("nothing was registered (rolled back dev.b.proto@2)", err)
        self.assertEqual([v["version"] for v in reg.subjects["dev.b.proto"]], [1])

    def test_a_registry_lost_asking_a_deferred_importer_rolls_back(self):
        src, doc, files = self.two_references()
        reg = FakeRegistry(strict=True)
        self.run_two(reg, src, doc, files)
        src["b.proto"] = src["b.proto"].replace("string x = 1;", "string x = 1; string y = 2;")
        real = reg.__call__

        def lost(url, path, method="GET", payload=None, params=""):
            if path.startswith("/compatibility/subjects/dev.sb-value"):
                raise schemas.Unreachable("no schema registry at %s (timed out)" % url)
            return real(url, path, method, payload, params)
        code, _, err = self.run_two(lost, src, doc, files)
        self.assertEqual(code, 1)
        self.assertIn("asking the registry about dev.sb-value failed: no schema registry at http://r", err)
        self.assertIn("nothing was registered (rolled back dev.b.proto@2)", err)
        self.assertEqual([v["version"] for v in reg.subjects["dev.b.proto"]], [1])

    def test_a_soft_delete_whose_purge_fails_is_named_as_such(self):
        src, doc, files = self.two_references()
        reg = FakeRegistry(strict=True)
        self.run_two(reg, src, doc, files)
        src["b.proto"] = src["b.proto"].replace("string x = 1;", "string x = 1; string y = 2;")
        reg.incompatible = {"dev.sb-value"}
        real = reg.__call__

        def no_purge(url, path, method="GET", payload=None, params=""):
            if method == "DELETE" and "permanent" in params:
                raise RuntimeError("DELETE %s -> HTTP 500 boom" % path)
            return real(url, path, method, payload, params)
        code, _, err = self.run_two(no_purge, src, doc, files)
        self.assertEqual(code, 1)
        self.assertIn("rollback incomplete: still registered dev.b.proto@2 (soft-deleted, not purged)", err)

    def test_a_failed_question_exits_1_naming_the_subject(self):
        reg = FakeRegistry(["dev." + s for s in DECLARED])
        real = reg.__call__

        def failing(url, path, method="GET", payload=None, params=""):
            if path.startswith("/compatibility/") and "audit" in path:
                raise RuntimeError("POST %s -> HTTP 500 boom" % path)
            return real(url, path, method, payload, params)
        code, _, err = run(schemas.apply_action, "dev", "http://r", registry=failing)
        self.assertEqual(code, 1)
        self.assertIn("asking the registry about dev.andara.audit.v1-value failed", err)

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


class Check(unittest.TestCase):
    # files_from_protos carries no message lists, so check() reports those too; these tests look only
    # at the line under test, with and without the import that causes it.
    def errors(self, files):
        with mock.patch.object(schemas, "descriptor", lambda: files), \
                contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()) as err:
            try:
                schemas.check()
            except SystemExit:
                pass
        return err.getvalue()

    def test_a_reference_that_imports_something_is_refused(self):
        files = files_from_protos()
        ref = DECL["references"][0]["file"]
        self.assertNotIn("a reference must import nothing", self.errors(files))
        files[ref] = dict(files[ref], deps=["x.proto"])
        self.assertIn("reference '%s' imports x.proto; a reference must import nothing" % ref, self.errors(files))


class Diff(unittest.TestCase):
    def test_a_matching_registry_is_clean(self):
        reg = FakeRegistry()
        run(schemas.apply_action, "dev", "http://r", registry=reg)
        run(schemas.apply_action, "local", "http://r", registry=reg)
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


class DiffBranches(unittest.TestCase):
    """Each drift diff exists to catch (pre-review P2)."""

    def applied(self):
        reg = FakeRegistry()
        run(schemas.apply_action, "dev", "http://r", registry=reg)
        return reg

    def test_declared_but_not_registered(self):
        reg = self.applied()
        del reg.subjects["dev.andara.audit.v1-value"]
        code, _, err = run(schemas.diff_action, "dev", "http://r", registry=reg)
        self.assertEqual(code, 1)
        self.assertIn("dev.andara.audit.v1-value: declared but not registered", err)

    def test_registered_but_never_with_the_declared_source(self):
        reg = self.applied()
        reg.subjects["dev.andara.audit.v1-value"][0]["payload"] = {"schemaType": "PROTOBUF", "schema": "other"}
        code, _, err = run(schemas.diff_action, "dev", "http://r", registry=reg)
        self.assertEqual(code, 1)
        self.assertIn("dev.andara.audit.v1-value: andara/audit/v1/audit.proto is not registered under this subject", err)

    def test_something_registered_after_the_declared_version(self):
        reg = self.applied()
        reg.subjects["dev.andara.audit.v1-value"].append({"version": 2, "id": 999, "payload": {"schema": "newer"}})
        code, _, err = run(schemas.diff_action, "dev", "http://r", registry=reg)
        self.assertEqual(code, 1)
        self.assertIn("is registered as version 1 but the latest is 2", err)

    def test_global_compatibility_drift(self):
        reg = self.applied()
        reg.config = "NONE"
        code, _, err = run(schemas.diff_action, "dev", "http://r", registry=reg)
        self.assertEqual(code, 1)
        self.assertIn("global compatibility is NONE, declared BACKWARD", err)

    def test_apply_sets_each_subjects_compatibility(self):
        reg = self.applied()
        subjects_configured = {schemas.urllib.parse.unquote(c[1].split("/", 2)[2])
                               for c in reg.calls if c[0] == "PUT" and c[1].startswith("/config/")}
        self.assertEqual(subjects_configured, {"dev." + s for s in DECLARED})


class PortForward(unittest.TestCase):
    """schemas.py's port-forward: the port from kubectl's first line, a failure that exits 1 and
    kills it, and the forward ended when the run ends (pre-review P2)."""

    class FakeProc:
        def __init__(self, lines, stderr=""):
            self.stdout = iter(lines)
            self.stderr = io.StringIO(stderr)
            self.killed = self.terminated = False

        def kill(self):
            self.killed = True

        def terminate(self):
            self.terminated = True

    def forward(self, proc):
        import kube
        self.popen_kwargs = {}

        def fake_popen(*a, **k):
            self.popen_kwargs = k
            return proc
        with mock.patch.object(kube, "require", lambda *a, **k: "kind-andara"), \
                mock.patch.object(kube, "kubectl", lambda *a, **k: ["kubectl"] + list(a)), \
                mock.patch("shutil.which", return_value="/usr/bin/kubectl"), \
                mock.patch.object(schemas, "popen", fake_popen), \
                mock.patch.object(schemas, "PORT_FORWARD_TIMEOUT", 1):
            return schemas.port_forward()

    def test_the_url_comes_from_the_first_line(self):
        proc = self.FakeProc(["Forwarding from 127.0.0.1:41234 -> 8081\n", "Handling connection for 41234\n"])
        self.assertEqual(self.forward(proc), ("http://127.0.0.1:41234", proc))
        # One drained pipe: kubectl's stderr can't fill an undrained one and stall the forward.
        self.assertEqual(self.popen_kwargs.get("stderr"), subprocess.STDOUT)

    def test_no_forwarding_line_exits_1_at_once_and_kills_it(self):
        # stderr is merged into stdout: kubectl's error is a line, and its exit ends the wait at once.
        import time
        proc = self.FakeProc(['error: services "andara-schema-registry" not found\n'])
        start = time.monotonic()
        with contextlib.redirect_stderr(io.StringIO()) as err, self.assertRaises(SystemExit) as e:
            self.forward(proc)
        self.assertEqual(e.exception.code, 1)
        self.assertLess(time.monotonic() - start, 0.9, "kubectl ended; the deadline isn't waited out")
        self.assertTrue(proc.killed)
        self.assertIn("not found", err.getvalue())

    def test_a_line_before_the_forwarding_line_is_skipped(self):
        proc = self.FakeProc(["W1024 some warning\n", "Forwarding from 127.0.0.1:5000 -> 8081\n"])
        self.assertEqual(self.forward(proc)[0], "http://127.0.0.1:5000")

    def test_main_ends_the_forward_even_when_the_action_dies(self):
        proc = self.FakeProc([])
        with mock.patch.object(schemas, "registry_url", return_value=("http://127.0.0.1:1", proc)), \
                mock.patch.object(schemas, "diff_action", side_effect=SystemExit(1)), \
                mock.patch.object(sys, "argv", ["schemas.py", "diff", "--env", "dev"]):
            with self.assertRaises(SystemExit):
                schemas.main()
        self.assertTrue(proc.terminated)


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

    def test_a_double_slash_inside_a_string_is_not_a_comment(self):
        with contextlib.redirect_stderr(io.StringIO()), self.assertRaises(SystemExit) as e:
            schemas.ascii_comments('option x = "http://\u00e9";', "f.proto")
        self.assertEqual(e.exception.code, 1)
        self.assertEqual(schemas.ascii_comments('option x = "http://a"; // \u2014'), 'option x = "http://a"; // ?')

    def test_escaped_quotes_and_single_quotes(self):
        self.assertEqual(schemas.ascii_comments('option x = "a\\"b"; // \u2014'), 'option x = "a\\"b"; // ?')
        self.assertEqual(schemas.ascii_comments("option y = 'x//y'; // \u00e9"), "option y = 'x//y'; // ?")
        # A // inside single quotes is not a comment: what follows it is code.
        with contextlib.redirect_stderr(io.StringIO()), self.assertRaises(SystemExit) as e:
            schemas.ascii_comments("option y = '//\u00e9';")
        self.assertEqual(e.exception.code, 1)

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
