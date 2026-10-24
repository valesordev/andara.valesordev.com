# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""AW-INF-039 AC-7: make slo-report renders the 28-day SLIs into the SLO documents' tables, with ruling
5's note, and exits 3 naming the read token before any query when it is unset."""
import contextlib
import datetime
import io
import os
import re
import shutil
import sys
import tempfile
import unittest
from unittest import mock

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
import grafana_stack  # noqa: E402
import slo_report  # noqa: E402

NO_BOX = {"ANDARA_BOX_ENV": os.path.join(tempfile.gettempdir(), "andara-no-box.env")}
NOW = datetime.datetime(2026, 10, 24, 18, 5, tzinfo=datetime.timezone.utc)


class FakeGcx:
    def __init__(self, results=None, fail_on=None):
        self.calls = []
        self.results = results or {}
        self.fail_on = fail_on

    def write_config(self, env=None):
        pass

    def metrics_query(self, ctx, expr, since=None):
        self.calls.append((ctx, expr))
        if self.fail_on and self.fail_on in expr:
            raise slo_report.gcx.GcxError("gcx: solo7dev metrics query: exit 1: boom")
        for needle, res in self.results.items():
            if needle in expr:
                return res
        return []


def series(ns, v):
    return {"metric": {"namespace": ns}, "value": [1761328800, str(v)]}


class Base(unittest.TestCase):
    def setUp(self):
        self.dir = tempfile.mkdtemp()
        for d in slo_report.DOCS:
            shutil.copy(os.path.join(slo_report.SLO_DIR, d["file"]), self.dir)
        self.addCleanup(shutil.rmtree, self.dir)
        env = {k: v for k, v in os.environ.items() if not k.startswith("GRAFANA_")}
        env.update(NO_BOX)
        self.env = env

    def run_main(self, fake, token="t"):
        env = dict(self.env)
        if token:
            env["GRAFANA_SOLO7DEV_READ_TOKEN"] = token
        out = io.StringIO()
        with mock.patch.dict(os.environ, env, clear=True), \
                mock.patch.object(slo_report, "SLO_DIR", self.dir), contextlib.redirect_stdout(out):
            try:
                code = slo_report.main([], q=fake, now=NOW)
            except grafana_stack.Exit as e:
                code = e.code
                out.write(str(e))
        return code, out.getvalue()

    def doc(self, name):
        with open(os.path.join(self.dir, name)) as f:
            return f.read()


class Report(Base):
    def test_rows_land_in_each_table_with_the_note(self):
        fake = FakeGcx({"andara_ingress_degraded": [series("andara-dev", 0.9975), series("andara-local", 1)],
                        "andara_consumer_lag": [series("andara-dev", 0.9995)]})
        code, out = self.run_main(fake)
        self.assertEqual(code, 0, out)
        w = self.doc("world-write-availability.md")
        # 0.25% of a 0.5% budget spent: half left.
        self.assertIn("| 2026-10-24 18:05 | andara-dev | 99.7500% | 50.0% | — |", w)
        self.assertIn("| 2026-10-24 18:05 | andara-local | 100.0000% | 100.0% | — |", w)
        k = self.doc("kafka-availability.md")
        self.assertIn("| 2026-10-24 18:05 | andara-dev | 99.9500% | 50.0% | — |", k)
        for text in (w, k):
            section = text.split("## Measurement", 1)[1].split("\n## ", 1)[0]
            self.assertIn(slo_report.NOTE, section)
            self.assertLess(section.index(slo_report.NOTE), section.index(slo_report.HEADER))
        self.assertEqual({c[0] for c in fake.calls}, {"solo7dev"})

    def test_a_second_run_appends_below_the_first(self):
        fake = FakeGcx({"andara_ingress_degraded": [series("andara-dev", 1)]})
        self.run_main(fake)
        later = NOW + datetime.timedelta(days=1)
        with mock.patch.dict(os.environ, dict(self.env, GRAFANA_SOLO7DEV_READ_TOKEN="t"), clear=True), \
                mock.patch.object(slo_report, "SLO_DIR", self.dir), contextlib.redirect_stdout(io.StringIO()):
            slo_report.main([], q=fake, now=later)
        w = self.doc("world-write-availability.md")
        self.assertLess(w.index("| 2026-10-24 18:05 | andara-dev"), w.index("| 2026-10-25 18:05 | andara-dev"))

    def test_no_series_is_a_row_not_a_failure(self):
        code, out = self.run_main(FakeGcx())
        self.assertEqual(code, 0, out)
        self.assertIn("| 2026-10-24 18:05 | (no series) | — | — | — |", self.doc("kafka-availability.md"))

    def test_a_missing_note_is_put_back(self):
        path = os.path.join(self.dir, "kafka-availability.md")
        with open(path) as f:
            text = f.read().replace(slo_report.NOTE + "\n", "")
        with open(path, "w") as f:
            f.write(text)
        self.run_main(FakeGcx())
        self.assertIn(slo_report.NOTE, self.doc("kafka-availability.md"))


class Failures(Base):
    def test_unset_token_exits_3_naming_it_before_any_query(self):
        fake = FakeGcx()
        code, out = self.run_main(fake, token=None)
        self.assertEqual(code, 3)
        self.assertIn("GRAFANA_SOLO7DEV_READ_TOKEN is unset", out)
        self.assertEqual(fake.calls, [])

    def test_a_failed_query_writes_nothing(self):
        before = {d["file"]: self.doc(d["file"]) for d in slo_report.DOCS}
        with self.assertRaises(slo_report.gcx.GcxError):
            self.run_main(FakeGcx({"andara_ingress_degraded": [series("andara-dev", 1)]},
                                  fail_on="andara_consumer_lag"))
        for name, text in before.items():
            self.assertEqual(self.doc(name), text, "%s changed although a later query failed" % name)

    def test_usage(self):
        with self.assertRaises(grafana_stack.Exit) as e:
            slo_report.main(["dev"], q=FakeGcx())
        self.assertEqual(e.exception.code, 2)


class Documents(unittest.TestCase):
    """The expression this queries is the one each document defines, and its target is the one in force."""

    def test_each_document_states_the_expression_queried(self):
        for d in slo_report.DOCS:
            with open(os.path.join(slo_report.SLO_DIR, d["file"])) as f:
                text = f.read()
            sli = text.split("## SLI", 1)[1].split("\n## ", 1)[0]
            blocks = re.findall(r"```promql\n(.*?)```", sli, re.S)
            self.assertIn(slo_report.squash(d["expr"]), [slo_report.squash(b) for b in blocks], d["file"])

    def test_each_target_appears_in_its_document(self):
        for d in slo_report.DOCS:
            with open(os.path.join(slo_report.SLO_DIR, d["file"])) as f:
                self.assertIn("**%g%%**" % (d["target"] * 100), f.read(), d["file"])

    def test_not_on_the_gcx_exception_list(self):
        with open(os.path.join(slo_report.REPO, "scripts", "gcx_only_exceptions.txt")) as f:
            self.assertNotIn("scripts/slo_report.py", f.read())


if __name__ == "__main__":
    unittest.main()
