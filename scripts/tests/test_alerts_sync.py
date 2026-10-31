# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""`make alerts-sync` and `make alerts-diff` (AW-INF-009) against a stand-in ruler."""
import contextlib
import http.server
import io
import os
import shutil
import subprocess
import sys
import tempfile
import threading
import unittest
from unittest import mock

import yaml

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
sys.path.insert(0, os.path.join(REPO, "scripts"))
import alerts_sync  # noqa: E402

TOOL = os.path.join(REPO, "bin", "mimirtool")


class Ruler(http.server.BaseHTTPRequestHandler):
    """Keeps the groups POSTed to it and lists them back the way Mimir's ruler API does."""
    groups = {}
    posts = []
    calls = []  # (method, path, basic-auth password)
    identities = []  # (method, basic-auth user, X-Scope-OrgID)

    def log_message(self, *a):
        pass

    def reply(self, body=b""):
        self.send_response(200)
        self.end_headers()
        self.wfile.write(body)

    def note(self):
        import base64
        auth = self.headers.get("authorization", "")
        user, pw = base64.b64decode(auth.split()[-1]).decode().split(":", 1) if auth else ("", "")
        self.calls.append((self.command, self.path, pw))
        self.identities.append((self.command, user, self.headers.get("x-scope-orgid")))

    def do_GET(self):
        self.note()
        listing = {ns: list(gs.values()) for ns, gs in self.groups.items()}
        self.reply(yaml.safe_dump(listing).encode())

    def do_POST(self):
        self.note()
        body = self.rfile.read(int(self.headers.get("content-length") or 0))
        ns = self.path.rsplit("/", 1)[1]
        doc = yaml.safe_load(body)
        self.groups.setdefault(ns, {})[doc["name"]] = doc
        self.posts.append(self.path)
        self.reply()

    def do_DELETE(self):
        self.note()
        parts = self.path.split("/")
        self.groups.get(parts[-2], {}).pop(parts[-1], None)
        self.reply()


def serve():
    Ruler.groups, Ruler.posts, Ruler.calls, Ruler.identities = {}, [], [], []
    srv = http.server.HTTPServer(("127.0.0.1", 0), Ruler)
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    return srv


def unmarked():
    """The chart's rule file without its `# env:` markers: what the ruler mechanics below run on, since
    sync refuses the marked file itself (AW-INF-047). Written once, removed at exit."""
    with open(alerts_sync.ALERTS) as f:
        text = alerts_sync.ENV_MARKER.sub("", f.read())
    fd, path = tempfile.mkstemp(suffix=".yaml", prefix="alerts-unmarked-")
    with os.fdopen(fd, "w") as f:
        f.write(text)
    __import__("atexit").register(os.unlink, path)
    return path


UNMARKED = unmarked()


# The last alerts.yaml the legacy ruler synced (AW-INF-047). CI's check job clones with fetch-depth: 0.
FROZEN_REF = "eb42e4c"


def shown(ref):
    return subprocess.run(["git", "-C", REPO, "show", "%s:deploy/helm/andara/files/alerts.yaml" % ref],
                          capture_output=True, text=True, check=True).stdout


def run_recording(cmd, drift=(0, 0, 0), **env):
    """alerts_sync.run in-process with plan stubbed to report drift: (exit code, the content of each
    file planned, stderr)."""
    planned = []

    def plan(tool, rules_file, e):
        with open(rules_file) as f:
            planned.append(f.read())
        return drift, [], ""

    keys = {k: "x" for k in alerts_sync.SECRETS}
    err = io.StringIO()
    with mock.patch.object(alerts_sync, "plan", plan), contextlib.redirect_stderr(err), \
            contextlib.redirect_stdout(io.StringIO()):
        code = alerts_sync.run(cmd, env=dict(keys, **env), find_tool=lambda: "/bin/true")
    return code, planned, err.getvalue()


def script(cmd, env=None, rules=None):
    # A sync always gets the unmarked copy; a diff does unless the test names its file in ALERTS_FILE
    # or its ref in ALERTS_REF.
    if rules is None and (cmd == "sync" or not {"ALERTS_FILE", "ALERTS_REF"} & set(env or {})):
        rules = UNMARKED
    full = dict(os.environ, PATH="/usr/bin:/bin")
    for k in alerts_sync.SECRETS:
        full.pop(k, None)
    full.update(env or {})
    code = "import sys; sys.path.insert(0, %r); import alerts_sync; sys.exit(alerts_sync.run(%r%s))" % (
        os.path.join(REPO, "scripts"), cmd, ", rules_file=%r" % rules if rules else "")
    return subprocess.run([sys.executable, "-c", code], capture_output=True, text=True, env=full)


class Frozen(unittest.TestCase):
    """AW-INF-047 AC-8: the ruler is frozen. No workflow syncs it, and sync refuses the marked file
    before any credential or network call (ADR-0012 decision 7, amendment of 2026-10-19)."""

    def test_no_workflow_syncs_or_diffs_the_ruler(self):
        self.assertFalse(os.path.exists(os.path.join(REPO, ".github", "workflows", "alerts.yaml")))
        workflows = os.path.join(REPO, ".github", "workflows")
        for name in os.listdir(workflows):
            with open(os.path.join(workflows, name)) as f:
                text = f.read()
            for call in ("alerts-sync", "alerts-diff", "alerts_sync.py"):
                self.assertNotIn(call, text, "%s calls %s" % (name, call))

    def test_the_chart_file_is_marked(self):
        self.assertEqual(sorted(set(alerts_sync.env_markers(alerts_sync.ALERTS))), ["dev", "prod"])

    def test_sync_of_the_marked_file_exits_1_naming_the_marker_with_no_keys(self):
        r = script("sync", {}, rules=alerts_sync.ALERTS)
        self.assertEqual(r.returncode, 1, r.stderr)
        self.assertIn("`# env:` markers (dev, prod)", r.stderr)
        self.assertNotIn("unset", r.stderr)

    def test_the_default_file_is_refused_too(self):
        code = "import sys; sys.path.insert(0, %r); import alerts_sync; sys.exit(alerts_sync.run('sync', env={}))" % (
            os.path.join(REPO, "scripts"))
        r = subprocess.run([sys.executable, "-c", code], capture_output=True, text=True)
        self.assertEqual(r.returncode, 1, r.stderr)
        self.assertIn("`# env:` markers", r.stderr)

    @unittest.skipUnless(os.access(TOOL, os.X_OK), "bin/mimirtool absent; run `make bootstrap`")
    def test_sync_of_the_marked_file_never_reaches_the_ruler_even_with_keys(self):
        srv = serve()
        self.addCleanup(srv.server_close)
        self.addCleanup(srv.shutdown)
        env = {"MIMIR_ADDRESS": "http://127.0.0.1:%d" % srv.server_address[1], "MIMIR_TENANT_ID": "t1",
               "MIMIR_API_KEY": "read-key", "MIMIR_API_KEY_WRITE": "write-key"}
        r = script("sync", env, rules=alerts_sync.ALERTS)
        self.assertEqual(r.returncode, 1, r.stderr)
        self.assertEqual(Ruler.calls, [])

    def test_a_marker_mid_line_or_in_a_name_is_not_one(self):
        with tempfile.NamedTemporaryFile("w", suffix=".yaml", delete=False) as f:
            f.write("groups:\n- name: g  # env:dev is a comment here\n  rules: []\n")
        self.addCleanup(os.unlink, f.name)
        self.assertEqual(alerts_sync.env_markers(f.name), [])

    @unittest.skipUnless(os.access(TOOL, os.X_OK), "bin/mimirtool absent; run `make bootstrap`")
    def test_diff_still_reads_a_marked_file(self):
        srv = serve()
        self.addCleanup(srv.server_close)
        self.addCleanup(srv.shutdown)
        env = {"MIMIR_ADDRESS": "http://127.0.0.1:%d" % srv.server_address[1], "MIMIR_TENANT_ID": "t1",
               "MIMIR_API_KEY": "read-key"}
        r = script("diff", env, rules=alerts_sync.ALERTS)
        self.assertEqual(r.returncode, 1, r.stderr)
        self.assertIn("differs from files/alerts.yaml", r.stderr)
        self.assertTrue(Ruler.calls)


class Secrets(unittest.TestCase):

    def test_unset_secrets_exit_3_and_are_named(self):
        r = script("sync", {"MIMIR_ADDRESS": "http://x"})
        self.assertEqual(r.returncode, 3, r.stderr)
        self.assertIn("MIMIR_TENANT_ID, MIMIR_API_KEY unset", r.stderr)
        self.assertNotIn("MIMIR_ADDRESS,", r.stderr)

    def test_diff_exits_3_too(self):
        self.assertEqual(script("diff").returncode, 3)

    def test_an_empty_secret_counts_as_unset(self):
        r = script("diff", {"MIMIR_ADDRESS": "", "MIMIR_TENANT_ID": "t", "MIMIR_API_KEY": "k"})
        self.assertEqual(r.returncode, 3)
        self.assertIn("MIMIR_ADDRESS unset", r.stderr)


class Defaults(unittest.TestCase):

    def test_the_address_and_tenant_come_from_the_grafana_cloud_pair(self):
        env = alerts_sync.resolve_env({"GRAFANA_CLOUD_PROM_URL": "https://prom.example.net/api/prom/",
                                       "GRAFANA_CLOUD_PROM_USER": "800950"})
        self.assertEqual(env["MIMIR_ADDRESS"], "https://prom.example.net")
        self.assertEqual(env["MIMIR_TENANT_ID"], "800950")

    def test_an_empty_mimir_value_falls_through_to_the_pair(self):
        env = alerts_sync.resolve_env({"MIMIR_ADDRESS": "", "MIMIR_TENANT_ID": "",
                                       "GRAFANA_CLOUD_PROM_URL": "https://p/api/prom",
                                       "GRAFANA_CLOUD_PROM_USER": "9"})
        self.assertEqual((env["MIMIR_ADDRESS"], env["MIMIR_TENANT_ID"]), ("https://p", "9"))

    def test_an_explicit_mimir_value_wins(self):
        env = alerts_sync.resolve_env({"MIMIR_ADDRESS": "http://x", "MIMIR_TENANT_ID": "t",
                                       "GRAFANA_CLOUD_PROM_URL": "https://p/api/prom",
                                       "GRAFANA_CLOUD_PROM_USER": "9"})
        self.assertEqual((env["MIMIR_ADDRESS"], env["MIMIR_TENANT_ID"]), ("http://x", "t"))

    def test_a_url_without_the_suffix_is_kept(self):
        env = alerts_sync.resolve_env({"GRAFANA_CLOUD_PROM_URL": "https://p.example.net"})
        self.assertEqual(env["MIMIR_ADDRESS"], "https://p.example.net")

    def test_only_the_key_missing_names_only_the_key(self):
        r = script("sync", {"GRAFANA_CLOUD_PROM_URL": "https://p/api/prom", "GRAFANA_CLOUD_PROM_USER": "9"})
        self.assertEqual(r.returncode, 3)
        self.assertIn("MIMIR_API_KEY unset", r.stderr)
        self.assertNotIn("MIMIR_ADDRESS", r.stderr)


class Guards(unittest.TestCase):
    """The paths that must not report success, in-process with a stand-in mimirtool."""
    ENV = {"MIMIR_ADDRESS": "http://x", "MIMIR_TENANT_ID": "t", "MIMIR_API_KEY": "k"}

    def fake_tool(self, body):
        d = tempfile.mkdtemp()
        self.addCleanup(__import__("shutil").rmtree, d)
        path = os.path.join(d, "mimirtool")
        with open(path, "w") as f:
            f.write("#!/bin/sh\n" + body + "\n")
        os.chmod(path, 0o755)
        return lambda: path

    def run_quiet(self, cmd, find_tool):
        import contextlib
        import io
        err, out = io.StringIO(), io.StringIO()
        with contextlib.redirect_stderr(err), contextlib.redirect_stdout(out):
            rc = alerts_sync.run(cmd, rules_file=UNMARKED, env=self.ENV, find_tool=find_tool)
        return rc, out.getvalue(), err.getvalue()

    def test_a_tool_that_exits_0_without_a_summary_is_a_failure(self):
        rc, _, err = self.run_quiet("sync", self.fake_tool("echo whatever"))
        self.assertEqual(rc, 1)
        self.assertIn("printed no summary", err)

    def test_a_missing_tool_is_a_failure(self):
        rc, _, err = self.run_quiet("sync", lambda: None)
        self.assertEqual(rc, 1)
        self.assertIn("mimirtool not found", err)

    def test_mimirtools_stderr_is_part_of_the_output(self):
        tool = self.fake_tool('echo "level=info msg=syncing group namespace=andara" >&2; '
                              'echo "Diff Summary: 0 Groups Created, 0 Groups Updated, 0 Groups Deleted"')
        rc, out, _ = self.run_quiet("sync", tool)
        self.assertEqual(rc, 0)
        self.assertIn("syncing group namespace=andara", out)
        self.assertIn("wrote 0 created, 0 updated, 0 deleted", out)

    def test_usage_is_exit_2(self):
        import contextlib
        import io
        with contextlib.redirect_stderr(io.StringIO()):
            self.assertEqual(alerts_sync.main([]), 2)
            self.assertEqual(alerts_sync.main(["push"]), 2)
            self.assertEqual(alerts_sync.main(["sync", "extra"]), 2)


class Secure(unittest.TestCase):

    def test_only_https_or_loopback_may_carry_the_write_key(self):
        self.assertTrue(alerts_sync.secure("https://prom.example.net"))
        self.assertTrue(alerts_sync.secure("http://127.0.0.1:9"))
        self.assertTrue(alerts_sync.secure("http://localhost:9"))
        self.assertFalse(alerts_sync.secure("http://prom.example.net"))
        self.assertFalse(alerts_sync.secure("ftp://x"))


class ClearText(unittest.TestCase):

    def test_apply_refuses_to_send_the_write_key_over_http(self):
        import contextlib, io
        env = {"MIMIR_ADDRESS": "http://prom.example.net", "MIMIR_TENANT_ID": "t", "MIMIR_API_KEY_WRITE": "w"}
        with mock.patch.object(alerts_sync.urllib.request.OpenerDirector, "open") as opened, \
                contextlib.redirect_stderr(io.StringIO()) as err:
            rc = alerts_sync.apply(alerts_sync.ALERTS, [], env)
        self.assertEqual(rc, 1)
        self.assertIn("not https", err.getvalue())
        opened.assert_not_called()


class Summary(unittest.TestCase):

    def test_it_reads_the_last_summary_line(self):
        out = "Sync Summary: 8 Groups Created, 2 Groups Updated, 1 Groups Deleted\n"
        self.assertEqual(alerts_sync.summary_counts(out), (8, 2, 1))

    def test_the_last_summary_wins(self):
        out = ("Diff Summary: 1 Groups Created, 0 Groups Updated, 0 Groups Deleted\n"
               "Sync Summary: 4 Groups Created, 5 Groups Updated, 6 Groups Deleted\n")
        self.assertEqual(alerts_sync.summary_counts(out), (4, 5, 6))

    def test_a_clean_diff_has_no_summary_and_means_zero(self):
        self.assertEqual(alerts_sync.summary_counts("no changes detected\n"), (0, 0, 0))

    def test_no_summary_is_none(self):
        self.assertIsNone(alerts_sync.summary_counts("error: nope"))


@unittest.skipUnless(os.access(TOOL, os.X_OK), "bin/mimirtool absent; run `make bootstrap`")
class AgainstARuler(unittest.TestCase):

    def setUp(self):
        self.srv = serve()
        self.addCleanup(self.srv.server_close)
        self.addCleanup(self.srv.shutdown)
        self.env = {"MIMIR_ADDRESS": "http://127.0.0.1:%d" % self.srv.server_address[1],
                    "MIMIR_TENANT_ID": "t1", "MIMIR_API_KEY": "read-key",
                    "MIMIR_API_KEY_WRITE": "write-key"}

    def test_sync_lands_in_namespace_andara_not_alerts(self):
        r = script("sync", self.env)
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertTrue(Ruler.posts)
        self.assertTrue(all(p.endswith("/rules/andara") for p in Ruler.posts), Ruler.posts)
        self.assertIn("andara-edge", Ruler.groups["andara"])
        self.assertNotRegex(r.stdout, r"0 created, 0 updated, 0 deleted")

    def test_the_read_key_plans_and_the_write_key_writes(self):
        r = script("sync", self.env)
        self.assertEqual(r.returncode, 0, r.stderr)
        reads = {c[2] for c in Ruler.calls if c[0] == "GET"}
        writes = {c[2] for c in Ruler.calls if c[0] in ("POST", "DELETE")}
        self.assertEqual(reads, {"read-key"})
        self.assertEqual(writes, {"write-key"})
        with open(os.path.join(REPO, "deploy", "helm", "andara", "files", "alerts.yaml")) as f:
            groups = len(yaml.safe_load(f)["groups"])
        self.assertIn("wrote %d created, 0 updated, 0 deleted" % groups, r.stdout)

    def test_a_sync_with_no_drift_never_uses_the_write_key(self):
        script("sync", self.env)
        Ruler.calls.clear()
        r = script("sync", dict(self.env, MIMIR_API_KEY_WRITE=""))
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual([c for c in Ruler.calls if c[0] != "GET"], [])
        self.assertIn("wrote 0 created, 0 updated, 0 deleted", r.stdout)

    def test_drift_without_a_write_key_is_exit_3_and_writes_nothing(self):
        r = script("sync", dict(self.env, MIMIR_API_KEY_WRITE=""))
        self.assertEqual(r.returncode, 3)
        self.assertIn("MIMIR_API_KEY_WRITE unset", r.stderr)
        self.assertEqual(Ruler.posts, [])

    def test_writes_carry_the_tenant_as_user_and_as_the_org_header(self):
        script("sync", self.env)
        Ruler.groups["andara"]["x"] = {"name": "x", "rules": [{"alert": "X", "expr": "vector(1)"}]}
        script("sync", self.env)
        writes = [i for i in Ruler.identities if i[0] in ("POST", "DELETE")]
        self.assertTrue(any(i[0] == "DELETE" for i in writes) and any(i[0] == "POST" for i in writes))
        self.assertEqual({(u, o) for _, u, o in writes}, {("t1", "t1")})

    def test_a_group_name_is_escaped_in_the_delete_path(self):
        script("sync", self.env)
        name = "a b/c(d)?"
        Ruler.groups["andara"][name] = {"name": name, "rules": [{"alert": "X", "expr": "vector(1)"}]}
        script("sync", self.env)
        paths = [p for m, p, _ in Ruler.calls if m == "DELETE"]
        self.assertEqual(paths, ["/prometheus/config/v1/rules/andara/a%20b%2Fc%28d%29%3F"])

    def test_a_group_name_with_trailing_spaces_is_deleted_whole(self):
        self.assertEqual(alerts_sync.DELETED.findall("  - Group: trail  \n"), ["trail  "])

    def test_the_plan_never_hands_mimirtool_the_write_key(self):
        d = tempfile.mkdtemp()
        self.addCleanup(__import__("shutil").rmtree, d)
        dump = os.path.join(d, "env.txt")
        tool = os.path.join(d, "mimirtool")
        with open(tool, "w") as f:
            f.write('#!/bin/sh\nenv > %s\necho "Diff Summary: 0 Groups Created, 0 Groups Updated, 0 Groups Deleted"\n' % dump)
        os.chmod(tool, 0o755)
        import contextlib, io
        with contextlib.redirect_stdout(io.StringIO()):
            rc = alerts_sync.run("sync", rules_file=UNMARKED, env=dict(self.env), find_tool=lambda: tool)
        self.assertEqual(rc, 0)
        seen = open(dump).read()
        self.assertIn("MIMIR_API_KEY=read-key", seen)
        self.assertNotIn("MIMIR_API_KEY_WRITE", seen)
        self.assertNotIn("write-key", seen)

    def test_a_redirect_on_a_write_is_an_error_not_a_silent_no_op(self):
        orig = Ruler.do_POST
        def redirect(h):
            h.note(); h.rfile.read(int(h.headers.get("content-length") or 0))
            h.send_response(302); h.send_header("Location", "http://127.0.0.1:1/elsewhere"); h.end_headers()
        Ruler.do_POST = redirect
        self.addCleanup(setattr, Ruler, "do_POST", orig)
        r = script("sync", self.env)
        self.assertEqual(r.returncode, 1)
        self.assertIn("HTTP 302", r.stderr)
        self.assertIn("after 0 of", r.stderr)

    def test_diff_does_not_need_the_write_key(self):
        r = script("diff", dict(self.env, MIMIR_API_KEY_WRITE=""))
        self.assertEqual(r.returncode, 1)  # drift on an empty ruler, not exit 3
        self.assertNotIn("WRITE", r.stderr)

    def test_a_group_the_file_lacks_is_deleted(self):
        script("sync", self.env)
        Ruler.groups["andara"]["stale-group"] = {"name": "stale-group", "rules": [
            {"alert": "Stale", "expr": "vector(1)"}]}
        r = script("sync", self.env)
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertNotIn("stale-group", Ruler.groups["andara"])
        self.assertIn("wrote 0 created, 0 updated, 1 deleted", r.stdout)
        self.assertTrue(any(c[0] == "DELETE" and c[1].endswith("/andara/stale-group") for c in Ruler.calls))

    def test_a_ruler_that_refuses_the_write_is_an_error_naming_the_scope(self):
        orig = Ruler.do_POST
        def deny(h):
            h.note(); h.send_response(401); h.end_headers()
        Ruler.do_POST = deny
        self.addCleanup(setattr, Ruler, "do_POST", orig)
        r = script("sync", self.env)
        self.assertEqual(r.returncode, 1)
        self.assertIn("rules:write", r.stderr)
        self.assertIn("HTTP 401", r.stderr)

    def test_a_write_that_does_not_take_is_an_error(self):
        orig = Ruler.do_POST
        def ignore(h):
            h.note(); h.rfile.read(int(h.headers.get("content-length") or 0)); h.send_response(200); h.end_headers()
        Ruler.do_POST = ignore
        self.addCleanup(setattr, Ruler, "do_POST", orig)
        r = script("sync", self.env)
        self.assertEqual(r.returncode, 1)
        self.assertIn("still differs after the write", r.stderr)

    def test_a_second_sync_changes_nothing(self):
        self.assertEqual(script("sync", self.env).returncode, 0)
        before = len(Ruler.posts)
        r = script("sync", self.env)
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertIn("wrote 0 created, 0 updated, 0 deleted", r.stdout)
        self.assertEqual(len(Ruler.posts), before)

    def test_diff_on_an_empty_ruler_is_drift(self):
        r = script("diff", self.env)
        self.assertEqual(r.returncode, 1)
        self.assertIn("differs from files/alerts.yaml", r.stderr)
        self.assertEqual(Ruler.posts, [])

    def test_alerts_file_names_the_file_that_is_read(self):
        script("sync", self.env)
        with open(UNMARKED) as f:
            doc = yaml.safe_load(f)
        doc["groups"][0]["rules"][0]["for"] = "7m"
        with tempfile.NamedTemporaryFile("w", suffix=".yaml", delete=False) as f:
            yaml.safe_dump(doc, f)
        self.addCleanup(os.unlink, f.name)
        r = script("diff", dict(self.env, ALERTS_FILE=f.name))
        self.assertEqual(r.returncode, 1)
        self.assertIn(doc["groups"][0]["name"], r.stdout)

    def test_a_sync_ignores_alerts_file(self):
        with tempfile.NamedTemporaryFile("w", suffix=".yaml", delete=False) as f:
            yaml.safe_dump({"groups": [{"name": "stray", "rules": [
                {"alert": "Stray", "expr": "vector(1)"}]}]}, f)
        self.addCleanup(os.unlink, f.name)
        r = script("sync", dict(self.env, ALERTS_FILE=f.name))
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertNotIn("stray", Ruler.groups["andara"])
        self.assertIn("andara-edge", Ruler.groups["andara"])

    def test_diff_after_a_sync_is_clean(self):
        script("sync", self.env)
        r = script("diff", self.env)
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)

    def test_an_edited_rule_names_its_group_and_exits_1(self):
        script("sync", self.env)
        with open(UNMARKED) as f:
            doc = yaml.safe_load(f)
        doc["groups"][0]["rules"][0]["for"] = "7m"
        edited = doc["groups"][0]["name"]
        with tempfile.NamedTemporaryFile("w", suffix=".yaml", delete=False) as f:
            yaml.safe_dump(doc, f)
        self.addCleanup(os.unlink, f.name)
        r = script("diff", self.env, rules=f.name)
        self.assertEqual(r.returncode, 1)
        self.assertIn(edited, r.stdout)

    def test_a_ruler_that_refuses_is_an_error(self):
        env = dict(self.env, MIMIR_ADDRESS="http://127.0.0.1:1")
        r = script("sync", env)
        self.assertEqual(r.returncode, 1)
        self.assertIn("mimirtool rules diff failed", r.stderr)


class ByRef(unittest.TestCase):
    """The frozen ruler is compared with alerts.yaml at eb42e4c (alert-routing.md) by ref, not a
    hand-extracted file (AW-INF-047). In-process with plan stubbed, so no mimirtool is needed."""

    def test_rules_at_writes_the_refs_file_to_a_temporary_directory(self):
        path = alerts_sync.rules_at(FROZEN_REF)
        # Checked before anything is removed: a rules_at that returned the chart's own path would
        # otherwise have this test delete the chart's files directory.
        d = os.path.dirname(path)
        self.assertEqual(os.path.dirname(d), tempfile.gettempdir())
        self.assertTrue(os.path.basename(d).startswith("alerts-ref-"), d)
        self.addCleanup(shutil.rmtree, d)
        with open(path) as f:
            got = f.read()
        self.assertEqual(got, shown(FROZEN_REF))
        with open(alerts_sync.ALERTS) as f:
            self.assertNotEqual(got, f.read())

    def test_a_ref_is_never_read_as_an_option(self):
        out = os.path.join(tempfile.mkdtemp(), "written")
        self.addCleanup(shutil.rmtree, os.path.dirname(out))
        # git would write `--output=<out>:<chart path>`; make that directory exist, so only
        # --end-of-options stands between the ref and a written file.
        target = out + ":" + os.path.dirname(os.path.relpath(alerts_sync.ALERTS, REPO))
        os.makedirs(target)
        with self.assertRaises(subprocess.CalledProcessError):
            alerts_sync.rules_at("--output=" + out)
        self.assertEqual(os.listdir(target), [])

    def test_a_diff_by_ref_plans_that_refs_file(self):
        code, planned, _ = run_recording("diff", ALERTS_REF=FROZEN_REF)
        self.assertEqual(code, 0)
        self.assertEqual(planned, [shown(FROZEN_REF)])

    def test_alerts_file_wins_over_alerts_ref(self):
        with tempfile.NamedTemporaryFile("w", suffix=".yaml", delete=False) as f:
            f.write("groups: []\n")
        self.addCleanup(os.unlink, f.name)
        code, planned, _ = run_recording("diff", ALERTS_FILE=f.name, ALERTS_REF="no-such-ref-aw-inf-009")
        self.assertEqual(code, 0)
        self.assertEqual(planned, ["groups: []\n"])

    def test_a_sync_ignores_alerts_ref_and_stays_frozen(self):
        """eb42e4c's file has no `# env:` markers; a sync that read it would get past the freeze."""
        code, planned, err = run_recording("sync", ALERTS_REF=FROZEN_REF)
        self.assertEqual((code, planned), (1, []))
        self.assertIn("carries `# env:` markers", err)

    def test_a_sync_ignores_alerts_file_and_stays_frozen(self):
        """The same for ALERTS_FILE: the unmarked copy named there must not be what a sync reads."""
        code, planned, err = run_recording("sync", ALERTS_FILE=UNMARKED)
        self.assertEqual((code, planned), (1, []))
        self.assertIn("carries `# env:` markers", err)

    def test_an_unknown_alerts_ref_exits_1_before_any_key_is_read(self):
        r = script("diff", {"ALERTS_REF": "no-such-ref-aw-inf-009"})
        self.assertEqual(r.returncode, 1, r.stderr)
        self.assertIn("ALERTS_REF=no-such-ref-aw-inf-009", r.stderr)
        self.assertNotIn("unset", r.stderr)

    def test_drift_names_the_file_it_was_measured_against(self):
        with tempfile.NamedTemporaryFile("w", suffix=".yaml", delete=False) as f:
            f.write("groups: []\n")
        self.addCleanup(os.unlink, f.name)
        for env, want in (({"ALERTS_REF": FROZEN_REF}, "differs from alerts.yaml at %s\n" % FROZEN_REF),
                          ({"ALERTS_FILE": f.name, "ALERTS_REF": "no-such-ref"}, "differs from %s\n" % f.name),
                          ({}, "differs from files/alerts.yaml; `make alerts-sync` on main delivers it")):
            code, _, err = run_recording("diff", drift=(1, 0, 0), **env)
            self.assertEqual(code, 1, env)
            self.assertIn(want, err, env)


if __name__ == "__main__":
    unittest.main()
