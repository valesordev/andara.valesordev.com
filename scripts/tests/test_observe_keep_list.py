# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""observe-check KEEP_LIST=1 (AW-INF-075 AC-8 to AC-10, ADR-0012 decision 13): the names come from the rules and
the dashboard, each is classified, every always-present row needs a series, and the unknown guard."""
import contextlib
import io
import os
import re
import sys
import tempfile
import unittest

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
sys.path.insert(0, os.path.join(REPO, "scripts"))
import observe_keep_list as kl  # noqa: E402

MATCHER = re.compile(r'(\w+)\s*(=~|!=|!~|=)\s*"((?:[^"\\]|\\.)*)"')


def selects(selector, labels):
    """Prometheus series-selector semantics, enough for the selectors the check sends."""
    for name, op, value in MATCHER.findall(selector):
        have = labels.get(name, "")
        full = re.fullmatch(value, have) is not None
        if (op == "=" and have != value) or (op == "!=" and have == value) or \
                (op == "=~" and not full) or (op == "!~" and full):
            return False
    return True


class Store:
    """A fake stack: metrics_series answers from a list of label sets, like `gcx metrics series`."""

    def __init__(self, series):
        self.series = series
        self.selectors = []

    def metrics_series(self, ctx, selector, since="10m"):
        self.selectors.append((ctx, selector))
        return [s for s in self.series if selects(selector, s)]

    def metrics_labels(self, ctx, label, match=None):
        # Stack-wide, with no window: values whose samples are older than the series window still show.
        self.selectors.append((ctx, "labels:" + label))
        return sorted({s[label] for s in self.series + getattr(self, "stale", []) if label in s})


def healthy(env="dev", names=None):
    """One series per always-present row for ENV, labelled as the stamp pipeline labels them."""
    ns = "andara-" + env
    out = []
    for name in sorted(names or kl.source_names()):
        if name in kl.FAULT_ONLY or kl.not_shipped(name):
            continue
        for _, tmpl in kl.rows_for(name):
            labels = {k: v for k, _, v in MATCHER.findall(tmpl.format(m=name, env=env, ns=ns))}
            labels = {k: (v.replace(".*", "x") if k in ("name", "router") else v) for k, v in labels.items()}
            out.append(labels)
    return out


def run(store, env="dev"):
    out = io.StringIO()
    with contextlib.redirect_stdout(out):
        code = kl.run(env, "solo7dev", q=store)
    return code, out.getvalue()


class Sources(unittest.TestCase):

    def test_names_in(self):
        self.assertEqual(kl.names_in('max by (namespace) (up{job="andara-server"}) == 0'), {"up"})
        self.assertEqual(kl.names_in('increase(kube_pod_container_status_restarts_total{container="server"}[10m]) > 3'),
                         {"kube_pod_container_status_restarts_total"})
        self.assertEqual(kl.names_in('label_replace(certmanager_certificate_expiration_timestamp_seconds{name=~"andara-.*"},'
                                     ' "namespace", "$1", "exported_namespace", "(.+)") - time() < 7 * 24 * 3600'),
                         {"certmanager_certificate_expiration_timestamp_seconds"})
        self.assertEqual(kl.names_in("(a > b) and on(namespace) (sum by (namespace) (rate(c[5m])) > 0)"),
                         {"a", "b", "c"})
        self.assertEqual(kl.names_in("histogram_quantile(0.99, sum by (le) (rate(x_bucket[1m])))"), {"x_bucket"})

    def test_the_rules_and_dashboard_names(self):
        names = kl.source_names()
        for n in ("up", "andara_ticks_total", "kube_pod_container_status_ready", "traefik_router_requests_total",
                  "andara_recovery_state_hash_match", "andara_tick_duration_seconds_bucket"):
            self.assertIn(n, names)

    def test_every_name_is_classified(self):
        """Coverage: a metric the rules or dashboard name is in a row, FAULT_ONLY or NOT_SHIPPED."""
        self.assertEqual(kl.unclassified(kl.source_names()), [])

    def test_a_new_rule_metric_is_unclassified(self):
        tmp = tempfile.mkdtemp()
        rules = os.path.join(tmp, "alerts.yaml")
        with open(rules, "w") as f:
            f.write("groups:\n- name: g\n  rules:\n  - alert: New\n    expr: brand_new_series > 0\n")
        names = kl.source_names(alerts=rules, dashboard=os.path.join(tmp, "absent.json"))
        self.assertEqual(kl.unclassified(names), ["brand_new_series"])

    def test_no_rule_names_a_not_shipped_series(self):
        tmp = tempfile.mkdtemp()
        rules_only = kl.source_names(dashboard=os.path.join(tmp, "absent.json"))
        self.assertEqual([n for n in rules_only if kl.not_shipped(n)], [])

    def test_fault_only_are_named_by_the_rules(self):
        self.assertTrue(set(kl.FAULT_ONLY) <= kl.source_names())

    def test_keep_pipeline_names_the_fault_only_series(self):
        """The allow-list half: runs once AW-INF-051's pipeline source exists."""
        if not os.path.exists(kl.KEEP_PIPELINE):
            self.skipTest("notice: %s does not exist yet (AW-INF-051); the allow-list half is skipped"
                          % os.path.relpath(kl.KEEP_PIPELINE, REPO))
        with open(kl.KEEP_PIPELINE) as f:
            text = f.read()
        for name in kl.FAULT_ONLY:
            self.assertIn(name, text)


class KeepList(unittest.TestCase):

    def test_every_row_present_passes(self):
        """AC-8."""
        store = Store(healthy())
        code, out = run(store)
        self.assertEqual(code, 0, out)
        self.assertIn("always-present", out)
        self.assertRegex(out, r"summary: always-present \d+ pass, 0 fail; environment 0 fail; fault-only 2 named, "
                              r"not queried")
        self.assertEqual({c for c, _ in store.selectors}, {"solo7dev"})
        presence = [s for _, s in store.selectors if 'environment="unknown"' not in s]
        self.assertFalse([s for s in presence if any(f in s for f in kl.FAULT_ONLY)],
                         "fault-only series are not required to exist")
        guarded = [s for _, s in store.selectors if 'environment="unknown"' in s]
        self.assertTrue(any(kl.FAULT_ONLY[0] in s for s in guarded), "but an unknown-labelled one still fails")

    def test_one_row_empty_names_the_metric(self):
        series = [s for s in healthy() if s.get("__name__") != "andara_simulation_lag_seconds"]
        code, out = run(Store(series))
        self.assertEqual(code, 1)
        self.assertRegex(out, r"FAIL always-present andara_simulation_lag_seconds +0 series")
        self.assertIn("unknown guard skipped", out)

    def test_every_missing_row_is_named_in_one_run(self):
        series = [s for s in healthy() if s.get("__name__") not in ("andara_ticks_total", "kube_pod_container_status_ready")]
        code, out = run(Store(series))
        self.assertEqual(code, 1)
        self.assertIn("FAIL always-present andara_ticks_total", out)
        self.assertIn("FAIL always-present kube_pod_container_status_ready", out)
        self.assertIn(", 2 fail;", out)

    def test_projector_up_is_its_own_row(self):
        series = [s for s in healthy() if s.get("job") != "andara-projector-state"]
        code, out = run(Store(series))
        self.assertEqual(code, 1)
        self.assertIn("FAIL always-present up andara-projector-state", out)

    def test_another_environment_does_not_satisfy_a_row(self):
        code, out = run(Store(healthy(env="local")), env="dev")
        self.assertEqual(code, 1)
        self.assertNotIn("ok   always-present", out)

    def test_no_series_at_all_fails(self):
        """AC-10: an empty stack is a failure, not a pass."""
        code, out = run(Store([]))
        self.assertEqual(code, 1)
        self.assertRegex(out, r"summary: always-present 0 pass, \d+ fail")

    def test_unknown_outside_andara_namespaces_passes(self):
        """AC-9, first half: kube-system's unknown series are not this check's."""
        series = healthy() + [{"__name__": "kube_pod_container_status_ready", "environment": "unknown",
                               "namespace": "kube-system", "container": "coredns"},
                              {"__name__": "andara_ticks_total", "environment": "unknown", "namespace": "andara-shared"},
                              {"__name__": "traefik_router_requests_total", "environment": "unknown",
                               "namespace": "andara-dev", "router": "andara-dev-andara-x"}]
        code, out = run(Store(series))
        self.assertEqual(code, 0, out)

    def test_unknown_in_an_andara_namespace_fails(self):
        """AC-9, second half."""
        series = healthy() + [{"__name__": "kube_pod_container_status_ready", "environment": "unknown",
                               "namespace": "andara-dev", "container": "server"}]
        code, out = run(Store(series))
        self.assertEqual(code, 1)
        self.assertIn('FAIL unknown         kube_pod_container_status_ready in namespace andara-dev carries '
                      'environment="unknown"', out)

    def test_an_environment_value_outside_the_five_fails(self):
        """ADR-0012 decision 9: a sender that sets environment outside local/dev/staging/prod/unknown is reported."""
        stray = healthy() + [{"__name__": "up", "environment": "development", "namespace": "andara-dev",
                              "job": "andara-server"}]
        code, out = run(Store(stray))
        self.assertEqual(code, 1)
        self.assertIn('FAIL environment     value "development" is not one of local, dev, staging, prod, unknown: '
                      'up{job="andara-server", namespace="andara-dev"}', out)
        self.assertRegex(out, r"environment 1 fail")
        self.assertRegex(out, r"always-present \d+ pass, 0 fail", "a label defect is counted on its own")
        # Reported even when the presence rows fail.
        code, out = run(Store([{"__name__": "up", "environment": "development", "namespace": "andara-dev"}]))
        self.assertEqual(code, 1)
        self.assertIn('FAIL environment     value "development"', out)
        ok = healthy() + [{"__name__": "up", "environment": "unknown", "namespace": "kube-system"},
                          {"__name__": "up", "environment": "local", "namespace": "andara-local"}]
        self.assertEqual(run(Store(ok))[0], 0)

    def test_a_stray_value_outside_the_window_is_reported_not_failed(self):
        store = Store(healthy())
        store.stale = [{"__name__": "up", "environment": "development", "namespace": "andara-dev"}]
        code, out = run(store)
        self.assertEqual(code, 0, out)
        self.assertIn('--   environment    value "development" has no series in the last 10m (stale; ages out)', out)

    def test_an_unclassified_name_fails_the_run(self):
        out = io.StringIO()
        with contextlib.redirect_stdout(out):
            code = kl.run("dev", "solo7dev", q=Store(healthy()), names=kl.source_names() | {"brand_new"})
        self.assertEqual(code, 1)
        self.assertIn("FAIL brand_new: in neither", out.getvalue())
        self.assertIn("unknown guard skipped", out.getvalue())

    def test_not_shipped_is_reported_not_queried(self):
        store = Store(healthy())
        code, out = run(store)
        self.assertIn("not-shipped    redpanda_kafka_consumer_group_lag_sum", out)
        self.assertFalse([s for _, s in store.selectors if "redpanda_" in s])


if __name__ == "__main__":
    unittest.main()
