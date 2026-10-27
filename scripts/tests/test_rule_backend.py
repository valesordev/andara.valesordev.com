# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""The drills' backend selection and rule-state reads, against recorded gcx fixtures (AW-INF-069 AC-2, AC-5,
AC-6, AC-7), and the ruler read's 404 / error mapping (scripts/ruler_read.py)."""
import copy
import datetime
import io
import json
import os
import socket
import sys
import unittest
import urllib.error
from unittest import mock

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
sys.path.insert(0, os.path.join(REPO, "scripts"))
import gcx  # noqa: E402
import grafana_stack  # noqa: E402
import rule_backend  # noqa: E402
import ruler_read  # noqa: E402

FIXTURES = os.path.join(REPO, "scripts", "tests", "fixtures", "gcx")
NOW = datetime.datetime(2026, 10, 27, 15, 0, 0, tzinfo=datetime.timezone.utc)
RULE = "RecoveryStateMismatch"
CREDS = {"GRAFANA_SOLO7DEV_READ_TOKEN": "dev-t", "GRAFANA_SOLO7_READ_TOKEN": "prod-t",
         "GRAFANA_CLOUD_PROM_URL": "https://prom.example/api/prom", "GRAFANA_CLOUD_PROM_USER": "123",
         "GRAFANA_CLOUD_READ_TOKEN": "cloud-t", "ANDARA_BOX_ENV": "/nonexistent"}
RULER_ENV = {"MIMIR_ADDRESS": "https://prom.example", "MIMIR_TENANT_ID": "123", "MIMIR_API_KEY": "k"}


def fixture(name):
    with open(os.path.join(FIXTURES, name)) as f:
        return json.load(f)


def live(doc, **change):
    """The recorded paused group, made live as the cutover's un-pause makes it: evaluated 30 s ago."""
    doc = copy.deepcopy(doc)
    for g in doc:
        g["lastEvaluation"] = "2026-10-27T14:59:30Z"
        for r in g["rules"]:
            r.update({"isPaused": False, "lastEvaluation": "2026-10-27T14:59:30Z"}, **change)
    return doc


def ruler_yaml(*rules):
    return "andara:\n  - name: andara-recovery\n    rules:\n" + "".join(
        "      - alert: %s\n        expr: vector(1)\n" % r for r in rules)


class FakeGcx:
    """gcx.run answering from fixture documents by command, every call logged."""

    def __init__(self, rules=None, instances=None, legacy=None, query=None):
        self.rules, self.instances, self.legacy, self.query = rules, instances, legacy, query
        self.calls = []

    def __call__(self, context, *args, **kw):
        self.calls.append((context,) + args)
        if args[:3] == ("alert", "rules", "list"):
            return self.rules
        if args[:3] == ("alert", "instances", "list"):
            return self.legacy if "--datasource" in args else self.instances
        if args[:2] == ("metrics", "query"):
            return self.query
        raise AssertionError("unexpected gcx call %s" % (args,))


class Base(unittest.TestCase):

    def fake(self, **kw):
        f = FakeGcx(**kw)
        p = mock.patch.object(gcx, "run", f)
        p.start()
        self.addCleanup(p.stop)
        return f

    def select(self, ruler, rule=RULE):
        def fetch(env):
            if isinstance(ruler, Exception):
                raise ruler
            return ruler
        return rule_backend.select("dev", rule, "solo7dev", RULER_ENV, {}, now=NOW, fetch=fetch)


class Selection(Base):
    """AC-2, AC-7: each fixture maps to its documented backend or to an inconclusive run naming why."""

    def test_legacy_live_and_grafana_paused_selects_the_ruler(self):
        self.fake(rules=fixture("alert_rules_list_paused.json"))
        b = self.select(ruler_yaml(RULE))
        self.assertEqual((str(b), b.interval, b.selectors), ("ruler solo7", 60, {"namespace": "andara-dev"}))
        self.assertEqual(b.header()[0], "backend: ruler solo7")
        self.assertEqual(b.settle, 130)

    def test_grafana_live_and_ruler_gone_selects_grafana(self):
        # Cutover step 4(ii) deletes the ruler namespace: a 404, "not live in the ruler".
        self.fake(rules=live(fixture("alert_rules_list_paused.json")))
        b = self.select(None)
        self.assertEqual(str(b), "grafana solo7dev")
        self.assertEqual(b.selectors, {"environment": "dev", "namespace": "andara-dev"})
        self.assertEqual(b.matchers, 'environment="dev",namespace="andara-dev"')
        self.assertIn("andara_recovery_state_hash_match", b.query)

    def test_grafana_live_and_ruler_without_the_rule_selects_grafana(self):
        self.fake(rules=fixture("alert_rules_list.json"))  # recorded live and firing, evaluated 2026-10-19
        b =rule_backend.select("dev", RULE, "solo7dev", RULER_ENV, {},
                                now=datetime.datetime(2026, 10, 19, 21, 39, 0, tzinfo=datetime.timezone.utc),
                                fetch=lambda env: ruler_yaml("Other"))
        self.assertEqual(str(b), "grafana solo7dev")

    def test_paused_and_no_live_ruler_is_inconclusive_naming_the_rule_and_reason(self):
        self.fake(rules=fixture("alert_rules_list_paused.json"))
        for ruler, why in ((None, "namespace andara is gone (404)"), (ruler_yaml("Other"), "not in namespace andara")):
            with self.assertRaises(rule_backend.Inconclusive) as e:
                self.select(ruler)
            self.assertIn("RecoveryStateMismatch is live in neither backend", str(e.exception))
            self.assertIn("grafana solo7dev: paused", str(e.exception))
            self.assertIn(why, str(e.exception))

    def test_live_in_both_is_inconclusive(self):
        self.fake(rules=live(fixture("alert_rules_list_paused.json")))
        with self.assertRaisesRegex(rule_backend.Inconclusive, "live in both backends"):
            self.select(ruler_yaml(RULE))

    def test_unhealthy_rule_is_inconclusive(self):
        self.fake(rules=live(fixture("alert_rules_list_paused.json"), health="error"))
        with self.assertRaisesRegex(rule_backend.Inconclusive, "neither.*health 'error', not ok"):
            self.select(None)

    def test_stale_evaluation_is_inconclusive_with_its_age(self):
        self.fake(rules=live(fixture("alert_rules_list_paused.json"), lastEvaluation="2026-10-27T14:57:00Z"))
        with self.assertRaisesRegex(rule_backend.Inconclusive, r"180s old, over twice the group's 60s interval"):
            self.select(None)

    def test_unloaded_rule_is_inconclusive(self):
        for doc in (fixture("alert_rules_list_empty.json"), fixture("alert_rules_list.json")):
            self.fake(rules=doc)
            with self.assertRaisesRegex(rule_backend.Inconclusive, "grafana solo7dev: not defined"):
                self.select(ruler_yaml("Other"), rule="SnapshotStale")

    def test_ruler_errors_are_inconclusive(self):
        self.fake(rules=fixture("alert_rules_list_paused.json"))
        for err in (ruler_read.RulerError("the ruler refused the rules read for namespace andara: HTTP 500"),
                    ruler_read.RulerError("could not reach the ruler (namespace andara): timed out")):
            with self.assertRaisesRegex(rule_backend.Inconclusive, "backend selection: (the ruler refused|could not reach)"):
                self.select(err)


class States(Base):
    """AC-5, AC-7: one vocabulary, the filter per backend, and "none" only once the rule is known to exist."""

    def grafana(self):
        return rule_backend.Backend(rule_backend.GRAFANA, "solo7dev", RULE, "andara-recovery", 60,
                                    {"environment": "dev", "namespace": "andara-dev"})

    def ruler(self, fetch=lambda env: ruler_yaml(RULE)):
        return rule_backend.Backend(rule_backend.RULER, "solo7", RULE, "andara-recovery", 60,
                                    {"namespace": "andara-dev"}, fetch=fetch)

    def test_a_firing_grafana_instance(self):
        f = self.fake(instances=fixture("alert_instances_json.json"))
        s = self.grafana().sample(lambda: 1.0)
        self.assertEqual((s.state, s.raw), ("firing", "Alerting"))
        self.assertEqual(f.calls[0][:5], ("solo7dev", "alert", "instances", "list", "--name"))
        self.assertNotIn("--datasource", f.calls[0])

    def test_another_environment_is_filtered_out(self):
        self.fake(instances=fixture("alert_instances_json.json"))
        b = self.grafana()
        b.selectors = {"environment": "staging", "namespace": "andara-staging"}
        self.assertEqual(b.sample().state, "pending")

    def test_a_ruler_firing_instance_with_only_namespace_counts_as_firing(self):
        f = self.fake(legacy=fixture("alert_instances_legacy_firing.json"))
        s = self.ruler().sample()
        self.assertEqual((s.state, s.raw), ("firing", "firing"))
        self.assertEqual(f.calls[0][0], "solo7")
        self.assertIn("--datasource", f.calls[0])
        self.assertIn(gcx.PROM, f.calls[0])

    def test_an_environment_filter_on_the_ruler_would_match_nothing(self):
        # Why the legacy backend filters on namespace only: the same instance, filtered as grafana is, vanishes.
        self.fake(legacy=fixture("alert_instances_legacy_firing.json"))
        b = self.ruler()
        b.selectors = {"environment": "dev", "namespace": "andara-dev"}
        self.assertEqual(b.sample().state, "normal")

    def test_empty_results_are_normal_and_shown_as_such(self):
        for name in ("alert_instances_json_empty.json", "alert_instances_name_empty.json",
                     "alert_instances_legacy_empty.json"):
            self.fake(instances=fixture(name), legacy=fixture(name))
            for b in (self.grafana(), self.ruler()):
                s = b.sample()
                self.assertEqual((s.state, s.raw), ("normal", "—"), name)

    def test_none_is_accepted_only_when_the_rule_exists(self):
        self.fake(rules=fixture("alert_rules_list_paused.json"))
        self.grafana().confirm_exists()
        self.ruler().confirm_exists()
        self.fake(rules=fixture("alert_rules_list_empty.json"))
        with self.assertRaisesRegex(rule_backend.Inconclusive, "not defined in grafana solo7dev, so an empty read proves nothing"):
            self.grafana().confirm_exists()
        for fetch in (lambda env: None, lambda env: ruler_yaml("Other")):
            with self.assertRaisesRegex(rule_backend.Inconclusive, "not defined in ruler solo7"):
                self.ruler(fetch).confirm_exists()

    def test_vocabulary(self):
        self.assertEqual(rule_backend.fold([]), ("normal", "—"))
        for raw, want in (("Alerting", "firing"), ("firing", "firing"), ("Recovering", "firing"),
                          ("Pending", "pending"), ("Normal", "normal"), ("Normal (NoData)", "normal")):
            self.assertEqual(rule_backend.fold([{"state": gcx.state(raw), "raw_state": raw}]), (want, raw))
        mixed = [{"state": "normal", "raw_state": "Normal"}, {"state": "firing", "raw_state": "Alerting"}]
        self.assertEqual(rule_backend.fold(mixed), ("firing", "Normal, Alerting"))
        self.assertEqual(rule_backend.fold([{"state": "error", "raw_state": "Error"}])[0], "error")

    def test_series_reads_follow_the_backend(self):
        f = self.fake(query=fixture("metrics_query_vector.json"))
        self.assertEqual(len(self.ruler().instant("up{%s}" % self.ruler().matchers)), 1)
        self.assertEqual(f.calls[-1][0], "solo7")
        self.assertEqual(f.calls[-1][-1], 'up{namespace="andara-dev"}')
        self.grafana().instant("up")
        self.assertEqual(f.calls[-1][0], "solo7dev")


class Watching(unittest.TestCase):
    """The (time, state) record a run asserts over."""

    def backend(self, states):
        it = iter(states)

        class B:
            rule, interval = RULE, 60

            def sample(self, clock):
                st = next(it)
                if isinstance(st, Exception):
                    raise st
                return rule_backend.Sample(clock(), st, st.title())
        return B()

    def test_a_record_with_no_firing_and_no_gap_passes(self):
        w = rule_backend.Watch(self.backend(["normal", "pending", "normal"]), print, poll=0)
        w.samples = [rule_backend.Sample(t, s, s) for t, s in ((10, "normal"), (20, "pending"), (30, "normal"))]
        w.started, w.stopped = 5, 35
        self.assertEqual((w.problems(), w.firing()), ([], []))

    def test_a_gap_over_the_interval_and_an_odd_state_are_problems(self):
        w = rule_backend.Watch(self.backend([]), print)
        w.samples = [rule_backend.Sample(10, "normal", "Normal"), rule_backend.Sample(100, "error", "Error")]
        w.started, w.stopped = 5, 110
        p = w.problems()
        self.assertEqual(len(p), 2)
        self.assertIn("'error' is not firing, pending or normal", p[0])
        self.assertIn("(90s, over the 60s interval)", p[1])

    def test_the_thread_records_every_poll_and_prints_each(self):
        lines = []
        w = rule_backend.Watch(self.backend(["normal", gcx.GcxError("boom"), "firing"] + ["normal"] * 1000),
                               lines.append, poll=0.001)
        w.start()
        while len(w.samples) < 3:
            pass
        w.stop()
        self.assertEqual([s.state for s in w.samples[:2]], ["normal", "firing"])
        self.assertEqual(len(w.errors), 1)
        self.assertTrue(lines[0].startswith("record ") and lines[0].endswith("RecoveryStateMismatch normal (gcx: Normal)"))
        self.assertIn("read failed: boom", lines[1])
        self.assertEqual(len(w.firing()), 1)


class Credentials(unittest.TestCase):
    """AC-6: exit 3 naming the variable, before any gcx call."""

    def test_each_unset_credential_is_named(self):
        for drop, name in (("GRAFANA_SOLO7DEV_READ_TOKEN", "GRAFANA_SOLO7DEV_READ_TOKEN"),
                           ("GRAFANA_SOLO7_READ_TOKEN", "GRAFANA_SOLO7_READ_TOKEN"),
                           ("GRAFANA_CLOUD_PROM_URL", "MIMIR_ADDRESS"),
                           ("GRAFANA_CLOUD_READ_TOKEN", "MIMIR_API_KEY")):
            env = {k: v for k, v in CREDS.items() if k != drop}
            with self.assertRaises(grafana_stack.Exit) as e:
                rule_backend.credentials("dev", env)
            self.assertEqual(e.exception.code, 3, drop)
            self.assertIn(name, str(e.exception))

    def test_all_set(self):
        stack, ruler_env, merged = rule_backend.credentials("dev", CREDS)
        self.assertEqual((stack, ruler_env["MIMIR_ADDRESS"], ruler_env["MIMIR_TENANT_ID"], ruler_env["MIMIR_API_KEY"]),
                         ("solo7dev", "https://prom.example", "123", "cloud-t"))

    def test_an_explicit_ruler_key_wins(self):
        _, ruler_env, _ = rule_backend.credentials("dev", dict(CREDS, MIMIR_API_KEY="k"))
        self.assertEqual(ruler_env["MIMIR_API_KEY"], "k")


class RulerRead(unittest.TestCase):
    """The one direct read: 200 is the YAML, 404 is "gone", anything else is an error."""

    def opener(self, result):
        class O:
            def open(self, req, timeout):
                O.req = req
                if isinstance(result, BaseException):
                    raise result
                return io.BytesIO(result.encode())
        return O()

    def http(self, code):
        return urllib.error.HTTPError("u", code, "x", {}, None)

    def test_200_is_the_body_and_the_request_is_the_tenants(self):
        o = self.opener(ruler_yaml(RULE))
        self.assertTrue(ruler_read.rule_loaded(ruler_read.fetch(RULER_ENV, opener=o), RULE))
        self.assertEqual(o.req.full_url, "https://prom.example/prometheus/config/v1/rules/andara")
        self.assertEqual(o.req.get_header("X-scope-orgid"), "123")

    def test_404_is_not_live(self):
        self.assertIsNone(ruler_read.fetch(RULER_ENV, opener=self.opener(self.http(404))))
        self.assertFalse(ruler_read.rule_loaded(None, RULE))

    def test_other_errors_raise(self):
        for err, msg in ((self.http(500), "HTTP 500"), (self.http(403), "HTTP 403"),
                         (socket.timeout("timed out"), "timed out"), (TimeoutError("t"), "could not reach"),
                         (urllib.error.URLError("refused"), "could not reach")):
            with self.assertRaisesRegex(ruler_read.RulerError, msg):
                ruler_read.fetch(RULER_ENV, opener=self.opener(err))

    def test_rule_loaded(self):
        self.assertTrue(ruler_read.rule_loaded(ruler_yaml(RULE), RULE))
        self.assertFalse(ruler_read.rule_loaded(ruler_yaml("Other"), RULE))
        self.assertFalse(ruler_read.rule_loaded(ruler_yaml(RULE).replace("andara:", "other:"), RULE))
        self.assertFalse(ruler_read.rule_loaded("", RULE))
        self.assertFalse(ruler_read.rule_loaded("andara: []", RULE))


if __name__ == "__main__":
    unittest.main()
