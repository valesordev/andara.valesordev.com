# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""`make alerts-sync` and `make alerts-diff` (AW-INF-009) against a stand-in ruler."""
import http.server
import os
import subprocess
import sys
import tempfile
import threading
import unittest

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

    def log_message(self, *a):
        pass

    def reply(self, body=b""):
        self.send_response(200)
        self.end_headers()
        self.wfile.write(body)

    def note(self):
        import base64
        auth = self.headers.get("authorization", "")
        pw = base64.b64decode(auth.split()[-1]).decode().split(":", 1)[1] if auth else ""
        self.calls.append((self.command, self.path, pw))

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
    Ruler.groups, Ruler.posts, Ruler.calls = {}, [], []
    srv = http.server.HTTPServer(("127.0.0.1", 0), Ruler)
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    return srv


def script(cmd, env=None, rules=None):
    full = dict(os.environ, PATH="/usr/bin:/bin")
    for k in alerts_sync.SECRETS:
        full.pop(k, None)
    full.update(env or {})
    code = "import sys; sys.path.insert(0, %r); import alerts_sync; sys.exit(alerts_sync.run(%r%s))" % (
        os.path.join(REPO, "scripts"), cmd, ", rules_file=%r" % rules if rules else "")
    return subprocess.run([sys.executable, "-c", code], capture_output=True, text=True, env=full)


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
            rc = alerts_sync.run(cmd, env=self.ENV, find_tool=find_tool)
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
        self.assertIn("wrote 8 created, 0 updated, 0 deleted", r.stdout)

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
        with open(alerts_sync.ALERTS) as f:
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
        with open(alerts_sync.ALERTS) as f:
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


if __name__ == "__main__":
    unittest.main()
