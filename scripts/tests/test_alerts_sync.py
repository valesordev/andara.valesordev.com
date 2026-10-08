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

    def log_message(self, *a):
        pass

    def reply(self, body=b""):
        self.send_response(200)
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        listing = {ns: list(gs.values()) for ns, gs in self.groups.items()}
        self.reply(yaml.safe_dump(listing).encode())

    def do_POST(self):
        body = self.rfile.read(int(self.headers.get("content-length") or 0))
        ns = self.path.rsplit("/", 1)[1]
        doc = yaml.safe_load(body)
        self.groups.setdefault(ns, {})[doc["name"]] = doc
        self.posts.append(self.path)
        self.reply()

    def do_DELETE(self):
        parts = self.path.split("/")
        self.groups.get(parts[-2], {}).pop(parts[-1], None)
        self.reply()


def serve():
    Ruler.groups, Ruler.posts = {}, []
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


class Summary(unittest.TestCase):

    def test_it_reads_the_last_summary_line(self):
        out = "Sync Summary: 8 Groups Created, 2 Groups Updated, 1 Groups Deleted\n"
        self.assertEqual(alerts_sync.summary_counts(out), (8, 2, 1))

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
                    "MIMIR_TENANT_ID": "t1", "MIMIR_API_KEY": "k"}

    def test_sync_lands_in_namespace_andara_not_alerts(self):
        r = script("sync", self.env)
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertTrue(Ruler.posts)
        self.assertTrue(all(p.endswith("/rules/andara") for p in Ruler.posts), Ruler.posts)
        self.assertIn("andara-edge", Ruler.groups["andara"])
        self.assertNotRegex(r.stdout, r"0 created, 0 updated, 0 deleted")

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
        self.assertIn("mimirtool rules sync failed", r.stderr)


if __name__ == "__main__":
    unittest.main()
