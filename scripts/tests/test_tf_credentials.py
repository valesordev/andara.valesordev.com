# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""scripts/tf_credentials.py (AW-INF-076): the credentials file (AC-2), the expiry check (AC-3, AC-4) and the
credentials-expiry job's issue, with gh stubbed (AC-5)."""
import contextlib
import datetime
import io
import json
import os
import socket
import sys
import tempfile
import unittest
from unittest import mock

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
sys.path.insert(0, os.path.join(REPO, "scripts"))
import tf_credentials  # noqa: E402

TODAY = datetime.date(2026, 10, 20)


def text(*entries):
    return "credentials:\n" + "".join(entries)


def entry(name, expires, warn=None):
    return "  - name: %s\n    expires: %s\n%s" % (name, expires, "    warn_days: %s\n" % warn if warn is not None else "")


def days(n):
    return (TODAY + datetime.timedelta(days=n)).isoformat()


class Base(unittest.TestCase):

    def write(self, body):
        d = tempfile.TemporaryDirectory()
        self.addCleanup(d.cleanup)
        path = os.path.join(d.name, "credentials.yaml")
        with open(path, "w") as f:
            f.write(body)
        return path

    def main(self, body, *args, env=None):
        out, err = io.StringIO(), io.StringIO()
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            rc = tf_credentials.main(["--file", self.write(body), "--today", TODAY.isoformat(), *args], env or {})
        return rc, out.getvalue() + err.getvalue()


class File(Base):
    """AC-2: name and ISO expires, integer warn_days, no other field, no value."""

    def test_the_committed_file_parses(self):
        entries = tf_credentials.load(tf_credentials.FILE)
        self.assertTrue(entries)
        for name, expires, warn in entries:
            self.assertIsInstance(expires, datetime.date)
            if name.endswith("_TELEMETRY_TOKEN"):
                self.assertEqual(warn, 30, "%s: a lapsed telemetry token blinds every SLO (AW-INF-065)" % name)

    def test_the_committed_file_holds_no_value(self):
        with open(tf_credentials.FILE) as f:
            self.assertIsNone(tf_credentials.SECRET.search(f.read()))

    def test_entries(self):
        got = tf_credentials.parse(text(entry("A_TOKEN", "2027-01-15"), entry("B_TOKEN", "2027-02-01", 30)))
        self.assertEqual(got, [("A_TOKEN", datetime.date(2027, 1, 15), 14), ("B_TOKEN", datetime.date(2027, 2, 1), 30)])

    def test_refusals_name_the_line(self):
        cases = {
            "unknown field": (text(entry("A_TOKEN", "2027-01-15") + "    value: x\n"), 4),
            "no expires": ("credentials:\n  - name: A_TOKEN\n", 2),
            "no name": ("credentials:\n  - expires: 2027-01-15\n", 2),
            "not a date": (text(entry("A_TOKEN", "2027-13-01")), 3),
            "not ISO": (text(entry("A_TOKEN", "15/01/2027")), 3),
            "date and time": (text(entry("A_TOKEN", "2027-01-15T08:00:00Z")), 3),
            "quoted warn_days": (text(entry("A_TOKEN", "2027-01-15", '"14"')), 4),
            "fractional warn_days": (text(entry("A_TOKEN", "2027-01-15", "1.5")), 4),
            "negative warn_days": (text(entry("A_TOKEN", "2027-01-15", "-3")), 4),
            "lower-case name": (text(entry("a_token", "2027-01-15")), 2),
            "twice": (text(entry("A_TOKEN", "2027-01-15"), entry("A_TOKEN", "2027-02-15")), 4),
            "a list value": ("credentials:\n  - name: [A]\n    expires: 2027-01-15\n", 2),
            "not a list": ("credentials: A_TOKEN\n", 1),
            "another top-level key": ("tokens: []\n", 1),
            "not YAML": ("credentials:\n  - name: A\n   expires: [\n", 3),
            "a stack token": (text(entry("A_TOKEN", "2027-01-15")) + "# glsa_abc\n", 4),
            "an access-policy token": (text(entry("A_TOKEN", "2027-01-15")) + "# glc_abc\n", 4),
            "a webhook": ("# https://hooks.slack.com/services/T/B/x\ncredentials: []\n", 1),
        }
        for why, (body, line) in cases.items():
            with self.subTest(why), self.assertRaises(tf_credentials.Exit) as e:
                tf_credentials.parse(body)
            self.assertEqual(e.exception.code, 1)
            self.assertTrue(str(e.exception).startswith("line %d:" % line), str(e.exception))

    def test_unreadable_file_exits_1_naming_the_line(self):
        rc, out = self.main(text(entry("A_TOKEN", "2027-01-15") + "    note: x\n"))
        self.assertEqual(rc, 1)
        self.assertIn("line 4: unknown field 'note'", out)

    def test_a_missing_file_exits_1(self):
        out = io.StringIO()
        with contextlib.redirect_stderr(out):
            rc = tf_credentials.main(["--file", "/nonexistent/credentials.yaml", "--today", "2026-10-20"], {})
        self.assertEqual(rc, 1)


class Check(Base):
    """AC-3, AC-4 and the Observability lines."""

    def test_nothing_in_its_window_exits_0(self):
        rc, out = self.main(text(entry("A_TOKEN", days(15)), entry("B_TOKEN", days(31), 30)))
        self.assertEqual(rc, 0)
        self.assertIn("A_TOKEN", out)
        self.assertIn("15 days left", out)
        self.assertIn("0 of 2 within its window or expired", out)

    def test_within_its_window_exits_1_naming_it(self):
        rc, out = self.main(text(entry("A_TOKEN", days(14)), entry("B_TOKEN", days(90))))
        self.assertEqual(rc, 1)
        line = [l for l in out.splitlines() if "A_TOKEN" in l][0]
        self.assertIn(days(14), line)
        self.assertIn("WITHIN 14-day window", line)
        self.assertNotIn("WITHIN", [l for l in out.splitlines() if "B_TOKEN" in l][0])
        self.assertIn("1 of 2 within its window or expired", out)

    def test_expired_prints_expired_not_a_negative_count(self):
        for n in (0, -1, -40):
            with self.subTest(n):
                rc, out = self.main(text(entry("A_TOKEN", days(n))))
                self.assertEqual(rc, 1)
                self.assertIn("expired", out)
                self.assertNotRegex(out, r"-\d+ days")

    def test_per_entry_windows(self):
        """AC-4: a telemetry token and a read token, both 20 days out: only the telemetry token is listed."""
        entries = tf_credentials.parse(text(entry("GRAFANA_SOLO7DEV_TELEMETRY_TOKEN", days(20), 30),
                                            entry("GRAFANA_SOLO7DEV_READ_TOKEN", days(20))))
        self.assertEqual([n for n, _, _ in tf_credentials.due(entries, TODAY)], ["GRAFANA_SOLO7DEV_TELEMETRY_TOKEN"])
        rc, out = self.main(text(entry("GRAFANA_SOLO7DEV_TELEMETRY_TOKEN", days(20), 30),
                                 entry("GRAFANA_SOLO7DEV_READ_TOKEN", days(20))))
        self.assertEqual(rc, 1)
        self.assertIn("WITHIN 30-day window", out)
        self.assertNotIn("WITHIN", [l for l in out.splitlines() if "READ_TOKEN" in l][0])

    def test_window_edges(self):
        entries = [("A", TODAY + datetime.timedelta(days=15), 14), ("B", TODAY + datetime.timedelta(days=14), 14),
                   ("C", TODAY + datetime.timedelta(days=1), 0), ("D", TODAY, 0)]
        self.assertEqual(tf_credentials.due(entries, TODAY),
                         [("B", entries[1][1], 14), ("D", TODAY, None)])

    def test_no_network(self):
        def refuse(*a, **k):
            raise AssertionError("tf-credentials-check opened a socket")
        with mock.patch.object(socket, "socket", refuse), mock.patch.object(socket, "create_connection", refuse), \
                mock.patch.object(tf_credentials.subprocess, "run", refuse):
            rc, _ = self.main(text(entry("A_TOKEN", days(3))))
        self.assertEqual(rc, 1)

    def test_usage(self):
        with contextlib.redirect_stderr(io.StringIO()):
            self.assertEqual(tf_credentials.main(["--today", "tomorrow"], {}), 2)
            self.assertEqual(tf_credentials.main(["--nope"], {}), 2)

    def test_today_defaults_to_utc(self):
        """01:00 UTC on the 21st is still the 20th at UTC-5: the check counts from the UTC date."""
        instant = datetime.datetime(2026, 10, 21, 1, 0, tzinfo=datetime.timezone.utc)

        class Clock(datetime.datetime):
            @classmethod
            def now(cls, tz=None):
                return instant if tz else instant.astimezone(datetime.timezone(datetime.timedelta(hours=-5))).replace(tzinfo=None)

            @classmethod
            def today(cls):
                return cls.now()

        class Day(datetime.date):
            @classmethod
            def today(cls):
                return datetime.date(2026, 10, 20)

        body = text(entry("A_TOKEN", "2026-11-04"))
        with mock.patch.object(tf_credentials.datetime, "datetime", Clock), \
                mock.patch.object(tf_credentials.datetime, "date", Day), \
                contextlib.redirect_stdout(io.StringIO()) as out:
            rc = tf_credentials.main(["--file", self.write(body)], {})
        self.assertEqual(rc, 1)
        self.assertIn("14 days left", out.getvalue())


class Issue(Base):
    """AC-5: the credentials-expiry job's issue, against a fake gh that holds issues in memory."""

    def setUp(self):
        self.issues = {}
        self.calls = []
        real = tf_credentials.gh
        tf_credentials.gh = self.gh
        self.addCleanup(setattr, tf_credentials, "gh", real)
        self.out = os.path.join(tempfile.mkdtemp(), "output")
        self.env = {"GITHUB_RUN_ID": "7", "GITHUB_REPOSITORY": "o/r", "GITHUB_OUTPUT": self.out}

    def gh(self, *args, input_text=None):
        self.calls.append(args)
        if args[:2] == ("issue", "list"):
            return json.dumps([{"number": n} for n, i in sorted(self.issues.items()) if i["state"] == "open"][:1])
        if args[:2] in (("issue", "create"), ("issue", "edit")) and len(args[args.index("--title") + 1]) > 256:
            raise tf_credentials.Exit(1, "gh %s failed: title is too long (maximum is 256 characters)" % args[1])
        if args[:2] == ("issue", "create"):
            n = len(self.issues) + 1
            self.issues[n] = {"state": "open", "title": args[args.index("--title") + 1], "body": input_text,
                              "label": args[args.index("--label") + 1]}
            return "https://github.com/o/r/issues/%d\n" % n
        if args[:2] == ("issue", "edit"):
            i = self.issues[int(args[2])]
            i["title"], i["body"] = args[args.index("--title") + 1], input_text
            return ""
        if args[:2] == ("issue", "close"):
            self.issues[int(args[2])]["state"] = "closed"
            return ""
        return ""

    def reported(self):
        with open(self.out) as f:
            return "reported=true" in f.read()

    def test_opens_then_updates_one_issue(self):
        rc, _ = self.main(text(entry("A_TOKEN", days(5)), entry("B_TOKEN", days(80))), "--issue", env=self.env)
        self.assertEqual(rc, 1)
        self.assertEqual(len(self.issues), 1)
        issue = self.issues[1]
        self.assertEqual(issue["label"], "credentials-expiry")
        self.assertIn("`A_TOKEN`", issue["body"])
        self.assertIn(days(5), issue["body"])
        self.assertNotIn("B_TOKEN", issue["body"])
        self.assertIn("/o/r/actions/runs/7", issue["body"])
        self.assertTrue(self.reported())
        rc, _ = self.main(text(entry("A_TOKEN", days(5)), entry("B_TOKEN", days(3))), "--issue", env=self.env)
        self.assertEqual(rc, 1)
        self.assertEqual(len(self.issues), 1, "a second run opened a duplicate")
        self.assertIn("`B_TOKEN`", self.issues[1]["body"])
        self.assertIn("2 Grafana Cloud credentials", self.issues[1]["title"])

    def test_many_due_at_once_still_fit_the_title(self):
        """Tokens made in one session expire together; GitHub refuses a title over 256 characters."""
        names = ["GRAFANA_SOLO7DEV_FLEET_APPLY_TOKEN_%02d" % i for i in range(12)]
        rc, _ = self.main(text(*[entry(n, days(3)) for n in names]), "--issue", env=self.env)
        self.assertEqual(rc, 1)
        self.assertEqual(len(self.issues), 1)
        for n in names:
            self.assertIn("`%s`" % n, self.issues[1]["body"])
        self.assertTrue(self.reported())

    def test_the_parse_error_is_printed_even_when_the_issue_fails(self):
        def broken(*args, input_text=None):
            raise tf_credentials.Exit(1, "gh label create failed: boom")
        tf_credentials.gh = broken
        rc, out = self.main(text(entry("A_TOKEN", "2027-01-15") + "    note: x\n"), "--issue", env=self.env)
        self.assertEqual(rc, 1)
        self.assertIn("line 4: unknown field 'note'", out)

    def test_a_failed_check_opens_or_updates_the_same_issue(self):
        rc, _ = self.main("credentials: [\n", "--issue", env=self.env)
        self.assertEqual(rc, 1)
        self.assertEqual(len(self.issues), 1)
        self.assertIn("could not read", self.issues[1]["body"])
        self.assertIn("line", self.issues[1]["body"])
        self.assertTrue(self.reported())
        rc, _ = self.main(text(entry("A_TOKEN", days(2))), "--issue", env=self.env)
        self.assertEqual(len(self.issues), 1)
        self.assertIn("`A_TOKEN`", self.issues[1]["body"])

    def test_a_clean_check_closes_the_issue(self):
        self.main(text(entry("A_TOKEN", days(2))), "--issue", env=self.env)
        rc, out = self.main(text(entry("A_TOKEN", days(90))), "--issue", env=self.env)
        self.assertEqual(rc, 0)
        self.assertEqual(self.issues[1]["state"], "closed")
        self.assertIn("closed issue #1", out)
        rc, _ = self.main(text(entry("A_TOKEN", days(90))), "--issue", env=self.env)
        self.assertEqual(rc, 0)
        self.assertEqual(len(self.issues), 1, "a clean check opened an issue")

    def test_no_issue_without_the_flag(self):
        self.main(text(entry("A_TOKEN", days(2))), env=self.env)
        self.assertEqual(self.calls, [])

    def test_a_gh_failure_while_closing_does_not_fail_a_clean_check(self):
        def broken(*args, input_text=None):
            raise tf_credentials.Exit(1, "gh issue list failed: boom")
        tf_credentials.gh = broken
        rc, out = self.main(text(entry("A_TOKEN", days(90))), "--issue", env=self.env)
        self.assertEqual(rc, 0)
        self.assertIn("closing the issue failed", out)


if __name__ == "__main__":
    unittest.main()
