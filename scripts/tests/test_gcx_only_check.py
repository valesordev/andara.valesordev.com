# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""make gcx-only-check (AW-INF-075 AC-6): direct Grafana API paths and Grafana bearer tokens fail outside the
per-file exception list, and a stale list line fails too."""
import os
import subprocess
import sys
import unittest

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
sys.path.insert(0, os.path.join(REPO, "scripts"))
import gcx_only_check as goc  # noqa: E402

LISTED = """\
# comment
scripts/legacy.py   AW-INF-069  # still direct
"""
LEGACY = 'body = prom.get("/api/v1/query", {"query": q})\n'


class Patterns(unittest.TestCase):

    def assertFails(self, text, what, path="scripts/new.py"):
        errors = goc.check({path: text, "scripts/legacy.py": LEGACY}, LISTED)
        self.assertEqual(len(errors), 1, errors)
        self.assertIn(path + ":", errors[0])
        self.assertIn(what, errors[0])
        return errors[0]

    def test_query_path_names_file_and_line(self):
        err = self.assertFails("import os\n\nurl = base + '/api/v1/query_range'\n", "path /api/v1/query")
        self.assertTrue(err.startswith("scripts/new.py:3:"), err)

    def test_every_path(self):
        for text, what in (("u = '/api/prom'", "path /api/prom"),
                           ("u = '/api/prometheus/grafana/api/v1/rules'", "path /api/prometheus/"),
                           ("u = base + '/prometheus/config/v1/rules/andara'", "path /prometheus/config/v1/rules"),
                           ("curl \"$LOKI/loki/api/v1/labels\"", "path /loki/api"),
                           ("URL=https://logs-prod-012.grafana.net", "a *.grafana.net host"),
                           ("# e.g. https://prometheus-prod-NN-….grafana.net/x", "a *.grafana.net host")):
            with self.subTest(what=what):
                self.assertFails(text + "\n", what)

    def test_grafana_bearer(self):
        self.assertFails('h = {"Authorization": "Bearer " + os.environ["GRAFANA_SOLO7_READ_TOKEN"]}\n', "bearer")
        self.assertFails('  -H "Authorization: Bearer ${GRAFANA_SOLO7DEV_READ_TOKEN}" \\\n', "bearer",
                         path="scripts/x.sh")
        self.assertFails('h = "%s %s" % (os.environ["GRAFANA_SOLO7_READ_TOKEN"], "Bearer")\n', "bearer")

    def test_other_bearers_pass(self):
        # argocd.py's and image_digest.sh's headers, verbatim in shape.
        for text in ('headers={"Authorization": "Bearer " + tok, "Accept": accept})\n',
                     '  -H "Authorization: Bearer ${token}" \\\n',
                     'headers={"Authorization": "Bearer " + bearer})\n'):
            self.assertEqual(goc.check({"scripts/a.py": text, "scripts/legacy.py": LEGACY}, LISTED), [], text)

    def test_listed_file_passes(self):
        self.assertEqual(goc.check({"scripts/legacy.py": LEGACY}, LISTED), [])

    def test_listed_file_that_is_gone_fails(self):
        errors = goc.check({}, LISTED)
        self.assertEqual(len(errors), 1)
        self.assertIn("scripts/legacy.py is listed (AW-INF-069) but is not a tracked, scanned file", errors[0])

    def test_listed_file_with_no_match_fails(self):
        errors = goc.check({"scripts/legacy.py": "print('moved to gcx')\n"}, LISTED)
        self.assertEqual(len(errors), 1)
        self.assertIn("no pattern matches it any more", errors[0])

    def test_malformed_line_fails(self):
        errors = goc.check({"scripts/legacy.py": LEGACY}, LISTED + "scripts/other.py\n")
        self.assertIn("not `<path> <AW-story>`", errors[0])


class Tree(unittest.TestCase):
    """The merged branch: clean, with exactly the story's exception lines."""

    def test_scope_excludes_tests_itself_and_tf_policy(self):
        files = goc.tracked()
        self.assertIn("Makefile", files)
        self.assertIn("scripts/gcx.py", files)
        self.assertIn(".github/workflows/terraform.yaml", files)
        for path in ("scripts/gcx_only_check.py", "scripts/tf_policy.py"):
            self.assertNotIn(path, files)
        self.assertFalse([f for f in files if f.startswith("scripts/tests/")])

    def test_tf_policy_and_own_patterns_would_match(self):
        """Why they are excluded rather than listed: each holds pattern text and calls no API."""
        with open(os.path.join(REPO, "scripts", "tf_policy.py")) as f:
            self.assertIn("/api/prom/push", f.read())
        with open(os.path.join(REPO, "scripts", "gcx_only_check.py")) as f:
            self.assertTrue(goc.matches(f.read()))

    def test_exception_lines(self):
        with open(os.path.join(REPO, goc.EXCEPTIONS)) as f:
            allowed, errors = goc.parse_exceptions(f.read())
        self.assertEqual(errors, [])
        self.assertEqual(allowed, {
            "scripts/alerts_sync.py": "AW-INF-077",
            "scripts/grafana_http.py": "AW-INF-069",
            "scripts/env_recover.py": "AW-INF-069",
            "scripts/stack_smoke.sh": "AW-INF-064",
            "scripts/stack_recover.sh": "AW-INF-064",
            "scripts/stack_recover_mismatch.sh": "AW-INF-064",
        })

    def test_a_non_utf8_file_is_scanned_not_skipped(self):
        import tempfile
        path = os.path.join(tempfile.mkdtemp(), "x.sh")
        with open(path, "wb") as f:
            f.write(b"\xff\xfe latin junk\ncurl \"$P/api/v1/query\"\n")
        self.assertEqual(goc.matches(goc.read_text(path)), [(2, "path /api/v1/query")])

    def test_tree_is_clean_with_untracked_pycache(self):
        cache = os.path.join(REPO, "scripts", "__pycache__")
        os.makedirs(cache, exist_ok=True)
        planted = os.path.join(cache, "gcx_only_planted.cpython-0.pyc")
        with open(planted, "w") as f:
            f.write("/api/v1/query https://x.grafana.net\n")
        self.addCleanup(os.remove, planted)
        p = subprocess.run([sys.executable, os.path.join(REPO, "scripts", "gcx_only_check.py")],
                           capture_output=True, text=True)
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertIn("gcx-only-check: ok", p.stdout)


if __name__ == "__main__":
    unittest.main()
