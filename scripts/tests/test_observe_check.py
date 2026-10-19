# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""make observe-check through gcx (AW-INF-075 AC-11), and observe_unavailable.py still loading its helpers from
scripts/grafana_http.py (AC-12)."""
import contextlib
import io
import json
import os
import sys
import tempfile
import unittest
from unittest import mock

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
sys.path.insert(0, os.path.join(REPO, "scripts"))
import gcx  # noqa: E402
import grafana_stack  # noqa: E402
import observe_check  # noqa: E402

FIXTURES = os.path.join(REPO, "scripts", "tests", "fixtures", "gcx")
NO_BOX = {"ANDARA_BOX_ENV": os.path.join(tempfile.gettempdir(), "andara-no-box.env")}


def fixture(name):
    with open(os.path.join(FIXTURES, name)) as f:
        return json.load(f)


class FakeGcx:
    """observe_check's gcx surface, answering from the recorded fixtures; every call recorded."""

    def __init__(self, metrics=True, logs=True, trace=True, up="1", span=None):
        self.calls = []
        self.metrics, self.logs, self.trace, self.up, self.span = metrics, logs, trace, up, span

    def metrics_query(self, ctx, expr, since=None):
        self.calls.append(("metrics", ctx, expr))
        if not self.metrics:
            return gcx.normalise(fixture("metrics_query_vector_empty.json"))
        res = gcx.normalise(fixture("metrics_query_vector.json"))
        res[0]["value"][1] = self.up
        return res

    def logs_query(self, ctx, expr, since="1h", limit=5):
        self.calls.append(("logs", ctx, expr))
        doc = fixture("logs_query.json" if self.logs else "logs_query_empty.json")
        return [{"stream": s["stream"], "timestamp": v["timestamp"], "line": v["line"]}
                for s in gcx.normalise(doc) for v in s["values"]]

    def trace_get(self, ctx, trace_id, since=None):
        self.calls.append(("trace", ctx, trace_id))
        found = gcx.normalise(fixture("traces_get.json" if self.trace else "traces_get_missing.json"))
        if self.span and found:
            found[0]["resourceSpans"][0]["scopeSpans"][0]["spans"][0]["name"] = self.span
        return found


def run_check(fake, env="dev"):
    out = io.StringIO()
    with mock.patch.dict(os.environ, {"PROJECTORS": "state"}), contextlib.redirect_stdout(out):
        absent = observe_check.check(env, grafana_stack.stack(env), "1h", q=fake)
    return absent, out.getvalue()


class Check(unittest.TestCase):

    def test_every_signal_present(self):
        fake = FakeGcx()
        absent, out = run_check(fake)
        self.assertEqual(absent, [])
        self.assertIn("ok     trace 9f330ddce795582aca223dff19488478 carries andara.game.v1.Game/OpenSession", out)
        self.assertEqual({c[1] for c in fake.calls}, {"solo7dev"})
        self.assertEqual({c[0] for c in fake.calls}, {"metrics", "logs", "trace"})

    def test_queries_carry_env_selectors(self):
        fake = FakeGcx()
        run_check(fake)
        exprs = [c[2] for c in fake.calls if c[0] in ("metrics", "logs")]
        self.assertIn('up{job="andara-server", environment="dev", namespace="andara-dev"}', exprs)
        self.assertIn('up{job="andara-projector-state", environment="dev", namespace="andara-dev"}', exprs)
        self.assertTrue(any(e.startswith('{environment="dev", namespace="andara-dev", container="server"}')
                            for e in exprs))
        fake = FakeGcx()
        run_check(fake, "prod")
        self.assertEqual({c[1] for c in fake.calls}, {"solo7"})
        self.assertIn('up{job="andara-server", environment="prod", namespace="andara-prod"}', [c[2] for c in fake.calls])

    def test_absent_signals(self):
        self.assertIn("up", run_check(FakeGcx(metrics=False))[0])
        self.assertIn("up", run_check(FakeGcx(up="0"))[0])
        self.assertEqual(run_check(FakeGcx(logs=False))[0], ["session opened log line", "trace"])
        self.assertEqual(run_check(FakeGcx(trace=False))[0], ["trace"])
        absent, out = run_check(FakeGcx(span="andara.game.v1.Game/Subscribe"))
        self.assertEqual(absent, ["trace"], "a trace without the OpenSession span is absent")
        self.assertIn("ABSENT trace 9f330ddce795582aca223dff19488478 with a andara.game.v1.Game/OpenSession span", out)

    def test_main_reads_through_gcx_against_solo7dev(self):
        """AC-11: the token for solo7dev and no GRAFANA_CLOUD_* variable is enough; every call goes through gcx."""
        env = {k: v for k, v in os.environ.items() if not k.startswith("GRAFANA_")}
        env.update(NO_BOX, GRAFANA_SOLO7DEV_READ_TOKEN="t", PROJECTORS="none",
                   GCX_CONFIG=os.path.join(tempfile.mkdtemp(), "gcx.yaml"))
        fake = FakeGcx()
        with mock.patch.dict(os.environ, env, clear=True), \
                mock.patch.object(observe_check, "gcx", mock.Mock(wraps=gcx)) as wrapped, \
                contextlib.redirect_stdout(io.StringIO()) as out:
            wrapped.metrics_query.side_effect = fake.metrics_query
            wrapped.logs_query.side_effect = fake.logs_query
            wrapped.trace_get.side_effect = fake.trace_get
            orig = observe_check.check
            with mock.patch.object(observe_check, "check", lambda e, c, s, q=None: orig(e, c, s, q=wrapped)):
                code = observe_check.main(["dev"])
        self.assertEqual(code, 0, out.getvalue())
        self.assertIn("every signal present for andara-dev in solo7dev", out.getvalue())
        self.assertEqual({c[1] for c in fake.calls}, {"solo7dev"})

    def test_not_on_the_exception_list(self):
        with open(os.path.join(REPO, "scripts", "gcx_only_exceptions.txt")) as f:
            self.assertNotIn("scripts/observe_check.py", f.read())
        with open(os.path.join(REPO, "scripts", "observe_check.py")) as f:
            self.assertNotIn("GRAFANA_CLOUD_", f.read())

    def test_projectors_unknown_is_a_failure(self):
        with mock.patch.dict(os.environ, {"PROJECTORS": "", "PATH": "/nonexistent"}):
            with self.assertRaises(observe_check.Failed):
                observe_check.projector_jobs("andara-dev")

    def test_since_is_usage(self):
        with self.assertRaises(grafana_stack.Exit) as e:
            observe_check.since_ok("an hour")
        self.assertEqual(e.exception.code, 2)


class Unavailable(unittest.TestCase):
    """AC-12: observe_unavailable.py loads scripts/grafana_http.py and still refuses anything but dev."""

    def test_prod_exits_3_with_the_module_loaded(self):
        import observe_unavailable
        self.assertEqual(observe_unavailable.oc.__name__, "grafana_http")
        for name in ("Backend", "BackendError", "instant", "need", "ALERTS"):
            self.assertTrue(hasattr(observe_unavailable.oc, name), name)
        with mock.patch.object(sys, "argv", ["observe_unavailable.py", "prod"]), \
                contextlib.redirect_stdout(io.StringIO()):
            with self.assertRaises(SystemExit) as e:
                observe_unavailable.main()
        self.assertEqual(e.exception.code, 3)

    def test_env_recover_builds_its_backend_from_grafana_http(self):
        import env_recover
        with mock.patch.dict(os.environ, {"GRAFANA_CLOUD_READ_TOKEN": "t", "GRAFANA_CLOUD_PROM_URL": "https://p.invalid",
                                          "GRAFANA_CLOUD_PROM_USER": "1"}):
            g = env_recover.Grafana()
        self.assertEqual(g.oc.__name__, "grafana_http")
        self.assertIsInstance(g.prom, g.oc.Backend)
        self.assertTrue(callable(g.oc.instant))


if __name__ == "__main__":
    unittest.main()
