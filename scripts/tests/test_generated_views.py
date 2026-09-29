# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""BACKLOG.md and docs/status.md are rendered per clone, not committed (AW-INF-026)."""
import contextlib
import io
import os
import sys
import tempfile
import unittest
from unittest import mock

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
import gen_backlog  # noqa: E402
import gen_status  # noqa: E402


class Views(unittest.TestCase):
    """The repository's own stories, with both output paths moved to a scratch directory."""

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.backlog = os.path.join(self.tmp.name, "BACKLOG.md")
        self.status = os.path.join(self.tmp.name, "status.md")
        for p in (mock.patch.object(gen_backlog, "BACKLOG", self.backlog),
                  mock.patch.object(gen_status, "STATUS", self.status)):
            p.start()
            self.addCleanup(p.stop)

    def run_main(self, fn, *argv):
        out, err = io.StringIO(), io.StringIO()
        with mock.patch.object(sys, "argv", ["gen", *argv]), \
                contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            rc = fn()
        return rc, out.getvalue(), err.getvalue()

    def test_check_renders_with_neither_file_present(self):
        rc, out, _ = self.run_main(gen_backlog.main, "--check")
        self.assertEqual(rc, 0)
        self.assertEqual(out, "")
        self.run_main(gen_status.main, "--check")
        self.assertFalse(os.path.exists(self.backlog))
        self.assertFalse(os.path.exists(self.status))

    def test_status_prints_what_it_writes(self):
        _, out, err = self.run_main(gen_status.main)
        with open(self.status, encoding="utf-8") as f:
            self.assertEqual(out, f.read())
        self.assertIn("status: wrote", err)
        self.assertNotIn("status: wrote", out)

    def test_backlog_prints_what_it_writes(self):
        rc, out, err = self.run_main(gen_backlog.main)
        self.assertEqual(rc, 0)
        with open(self.backlog, encoding="utf-8") as f:
            self.assertEqual(out, f.read())
        self.assertIn("backlog: wrote", err)

    def test_check_over_the_line_budget_fails_with_no_file(self):
        with mock.patch.object(gen_status, "MAX_LINES", 3), self.assertRaises(SystemExit) as e:
            self.run_main(gen_status.main, "--check")
        self.assertEqual(e.exception.code, 1)
        self.assertFalse(os.path.exists(self.status))

    def test_check_over_the_column_budget_fails_with_no_file(self):
        with mock.patch.object(gen_status, "MAX_COLS", 20), self.assertRaises(SystemExit) as e:
            self.run_main(gen_status.main, "--check")
        self.assertEqual(e.exception.code, 1)


if __name__ == "__main__":
    unittest.main()
