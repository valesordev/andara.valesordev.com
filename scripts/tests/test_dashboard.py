# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""The tick-health dashboard source (AW-INF-049 AC-4, AC-5) and make dashboard-check (AC-3), gcx stubbed."""
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
import dashboard_check as dc  # noqa: E402
import gcx  # noqa: E402
import grafana_stack  # noqa: E402
import observe_keep_list as kl  # noqa: E402

FIXTURE = os.path.join(REPO, "scripts", "tests", "fixtures", "tick-health-panels.json")
GCX_FIXTURES = os.path.join(REPO, "scripts", "tests", "fixtures", "gcx")
NO_BOX = {"ANDARA_BOX_ENV": os.path.join(tempfile.gettempdir(), "andara-no-box.env")}
# The one change AC-5 allows against the compose file: the broker view of consumer lag, compose's Redpanda
# series, which no stack receives (environment="unknown" in andara-shared, ADR-0014 §4; never kept).
REMOVED = {"Per-partition consumer lag": {"redpanda_kafka_consumer_group_lag_sum"}}


def gcx_fixture(name):
    with open(os.path.join(GCX_FIXTURES, name)) as f:
        return gcx.normalise(json.load(f))


class Source(unittest.TestCase):

    def setUp(self):
        self.d = dc.load()
        self.vars = dc.variables(self.d)

    def test_identity_is_unchanged(self):
        self.assertEqual(self.d["uid"], "andara-tick-health")
        self.assertEqual(self.d["title"], "Andara — tick health and log lag")

    def test_ds_is_a_prometheus_datasource_defaulting_to_grafanacloud_prom(self):
        ds = self.vars["ds"]
        self.assertEqual((ds["type"], ds["query"]), ("datasource", "prometheus"))
        self.assertEqual(ds["current"]["value"], "grafanacloud-prom")

    def test_environment_excludes_unknown(self):
        """AC-4, the unit half."""
        env = self.vars["environment"]
        self.assertEqual(dc.variable_query(env),
                         'label_values(up{job="andara-server", environment!="unknown"}, environment)')
        self.assertEqual(dc.label_values(dc.variable_query(env)),
                         ("environment", 'up{job="andara-server", environment!="unknown"}'))
        self.assertFalse(env["multi"])
        self.assertFalse(env.get("includeAll"))
        # The first listed value is the default (the [ASSUMPTION]): no value is pinned.
        self.assertEqual(env["current"], {})

    def test_namespace_depends_on_environment(self):
        ns = self.vars["namespace"]
        self.assertEqual(dc.variable_query(ns),
                         'label_values(up{job="andara-server", environment="$environment"}, namespace)')
        self.assertFalse(ns["multi"])

    def test_variables_read_through_ds(self):
        for name in ("environment", "namespace"):
            self.assertEqual(self.vars[name]["datasource"]["uid"], "${ds}", name)

    def test_panels_and_metrics_match_the_compose_file(self):
        """AC-5: titles and the metrics each panel plots, against the fixture recorded before the move."""
        with open(FIXTURE) as f:
            want = json.load(f)["panels"]
        got = []
        for p in self.d["panels"]:
            names = set()
            for t in p.get("targets", []):
                names |= kl.names_in(t["expr"])
            got.append({"title": p["title"], "metrics": sorted(names)})
        expected = [{"title": w["title"], "metrics": sorted(set(w["metrics"]) - REMOVED.get(w["title"], set()))}
                    for w in want]
        self.assertEqual(got, expected)

    def test_the_allowed_removal_is_one_the_fixture_records(self):
        with open(FIXTURE) as f:
            want = {w["title"]: set(w["metrics"]) for w in json.load(f)["panels"]}
        for title, names in REMOVED.items():
            self.assertTrue(names <= want[title], title)

    def test_no_panel_reads_a_series_no_stack_receives(self):
        names = set()
        for _, _, expr in dc.targets(self.d):
            names |= kl.names_in(expr)
        self.assertEqual([n for n in names if kl.not_shipped(n)], [])

    def test_the_keep_list_reads_this_file(self):
        self.assertEqual(os.path.realpath(kl.DASHBOARD), os.path.realpath(dc.DASHBOARD))
        self.assertTrue(os.path.exists(kl.DASHBOARD))

    def test_fill(self):
        self.assertEqual(dc.fill('a{environment="$environment", namespace="$namespace"}[$__rate_interval]',
                                 "dev", "andara-dev"),
                         'a{environment="dev", namespace="andara-dev"}[5m]')


class FakeGcx:
    """dashboard_check's gcx surface: metrics_query answers from the recorded fixtures."""

    def __init__(self, empty=(), late=None, options=("dev", "local")):
        self.empty, self.late, self.options = set(empty), dict(late or {}), list(options)
        self.queries, self.label_calls = [], []

    def write_config(self):
        pass

    def metrics_query(self, ctx, expr, since=None):
        self.queries.append((ctx, expr))
        metric = next(iter(kl.names_in(expr)))
        if self.late.get(metric, 0) > 0:
            self.late[metric] -= 1
            return gcx_fixture("metrics_query_vector_empty.json")
        if metric in self.empty:
            return gcx_fixture("metrics_query_vector_empty.json")
        return gcx_fixture("metrics_query_vector.json")

    def metrics_labels(self, ctx, label, match=None):
        self.label_calls.append((ctx, label, match))
        if self.options and isinstance(self.options[0], (list, tuple)):
            return list(self.options.pop(0) if len(self.options) > 1 else self.options[0])
        return list(self.options)


class Clock:
    def __init__(self):
        self.t = 0.0

    def __call__(self):
        return self.t

    def sleep(self, s):
        self.t += s


def run_main(fake, argv=("dev",), env=None):
    clock = Clock()
    environ = dict(NO_BOX, GRAFANA_SOLO7DEV_READ_TOKEN="t", GRAFANA_SOLO7_READ_TOKEN="t", DEADLINE="60")
    environ.update(env or {})
    out = io.StringIO()
    with mock.patch.dict(os.environ, environ, clear=True), \
            mock.patch.object(dc.time, "monotonic", clock), mock.patch.object(dc.time, "sleep", clock.sleep), \
            contextlib.redirect_stdout(out):
        try:
            code = dc.main(list(argv), q=fake)
        except grafana_stack.Exit as e:
            print(str(e))
            code = e.code
    return code, out.getvalue()


class DashboardCheck(unittest.TestCase):

    def test_every_panel_with_data_exits_0(self):
        """AC-3."""
        fake = FakeGcx()
        code, out = run_main(fake)
        self.assertEqual(code, 0, out)
        self.assertIn("every panel query returned a series for environment=dev in solo7dev", out)

    def test_queries_carry_env_and_its_namespace_and_go_to_its_stack(self):
        fake = FakeGcx()
        run_main(fake)
        self.assertTrue(fake.queries)
        for ctx, expr in fake.queries:
            self.assertEqual(ctx, "solo7dev")
            self.assertIn('environment="dev", namespace="andara-dev"', expr)
            self.assertNotIn("$", expr)

    def test_local_reads_the_kind_clusters_namespace_on_the_box_cluster(self):
        """ADR-0014 A3: CI's lifecycle job ships andara-local too, on cluster ci; a box check adds cluster="andara"."""
        fake = FakeGcx()
        run_main(fake, argv=("local",))
        self.assertTrue(fake.queries)
        for _, e in fake.queries:
            self.assertIn('namespace="andara-local", cluster="andara"', e)

    def test_dev_does_not_select_on_cluster(self):
        fake = FakeGcx()
        run_main(fake)
        self.assertFalse([e for _, e in fake.queries if "cluster=" in e])

    def test_every_series_reading_target_is_queried_and_vector_is_not(self):
        fake = FakeGcx()
        run_main(fake)
        series = [e for _, _, e in dc.targets(dc.load()) if kl.names_in(e)]
        self.assertEqual(len(fake.queries), len(series))
        self.assertFalse([e for _, e in fake.queries if e.startswith("vector(")])

    def test_a_panel_without_data_exits_1_naming_it(self):
        fake = FakeGcx(empty={"andara_handoff_placed_entries"})
        code, out = run_main(fake)
        self.assertEqual(code, 1)
        self.assertIn("ABSENT Handoff marks (Placed), against the runbook thresholds [A]", out)
        self.assertIn("1 panel(s) without data for environment=dev: Handoff marks (Placed)", out)

    def test_one_empty_target_fails_its_panel(self):
        """A panel with several queries needs every one: another target's series does not stand in."""
        fake = FakeGcx(empty={"andara_tick_input_starved_total"})
        code, out = run_main(fake)
        self.assertEqual(code, 1)
        self.assertIn("Tick overruns and input starvation [B]", out)

    def test_a_late_series_is_polled_for(self):
        fake = FakeGcx(late={"andara_simulation_lag_seconds": 2})
        code, out = run_main(fake)
        self.assertEqual(code, 0, out)
        lag = [e for _, e in fake.queries if "andara_simulation_lag_seconds" in e]
        self.assertEqual(len(lag), 3)

    def test_the_deadline_bounds_the_poll(self):
        fake = FakeGcx(empty={"andara_simulation_lag_seconds"})
        code, out = run_main(fake, env={"DEADLINE": "30"})
        self.assertEqual(code, 1)
        lag = [e for _, e in fake.queries if "andara_simulation_lag_seconds" in e]
        self.assertEqual(len(lag), 3)  # t=0, 15, 30
        self.assertIn("no series within 30s", out)

    def test_options_are_read_with_the_variables_query(self):
        """AC-4, the live half: the stack lists local and dev, not prod."""
        fake = FakeGcx()
        run_main(fake)
        self.assertEqual(fake.label_calls,
                         [("solo7dev", "environment", 'up{job="andara-server", environment!="unknown"}')])

    def test_prod_in_solo7dev_fails(self):
        code, out = run_main(FakeGcx(options=("dev", "local", "prod")))
        self.assertEqual(code, 1)
        self.assertIn("FAIL options: solo7dev carries prod", out)

    def test_options_are_polled_until_env_appears(self):
        fake = FakeGcx(options=[("local",), ("local",), ("dev", "local")])
        code, out = run_main(fake)
        self.assertEqual(code, 0, out)
        self.assertEqual(len(fake.label_calls), 3)

    def test_env_missing_from_the_options_fails(self):
        code, out = run_main(FakeGcx(options=("local",)))
        self.assertEqual(code, 1)
        self.assertIn("FAIL options: dev is not an option", out)

    def test_no_read_token_exits_3_before_any_query(self):
        fake = FakeGcx()
        code, out = run_main(fake, env={"GRAFANA_SOLO7DEV_READ_TOKEN": ""})
        self.assertEqual(code, 3)
        self.assertIn("GRAFANA_SOLO7DEV_READ_TOKEN is unset", out)
        self.assertEqual(fake.queries, [])

    def test_usage(self):
        for argv in ((), ("",), ("dev", "x"), ("qa",)):
            code, _ = run_main(FakeGcx(), argv=argv)
            self.assertEqual(code, 2, argv)

    def test_a_bad_deadline_is_usage(self):
        code, _ = run_main(FakeGcx(), env={"DEADLINE": "5m"})
        self.assertEqual(code, 2)


if __name__ == "__main__":
    unittest.main()
