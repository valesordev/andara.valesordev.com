# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""make observe-unavailable through gcx (AW-INF-069 AC-3, AC-4, AC-6): the drill against recorded fire-and-clear
sequences, its refusals before anything is scaled, and its exit 3s."""
import contextlib
import copy
import io
import itertools
import json
import os
import subprocess
import sys
import unittest
from unittest import mock

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
sys.path.insert(0, os.path.join(REPO, "scripts"))
sys.path.insert(0, os.path.join(REPO, "scripts", "tests"))
import gcx  # noqa: E402
import observe_unavailable as ou  # noqa: E402
import rule_backend  # noqa: E402
from test_rule_backend import CREDS, FakeGcx, fixture, ruler_yaml  # noqa: E402

DEV_ABSENCE = '    or absent(up{job="andara-server", environment="dev", namespace="andara-dev"})    # env:dev\n'


def live_server_rules(with_absence=True):
    """The recorded andara-server group, un-paused (step 4(ii-b)) and, with_absence, with step 4(v)'s operand."""
    doc = copy.deepcopy(fixture("alert_rules_list_server_paused.json"))
    now = rule_backend.now_utc().strftime("%Y-%m-%dT%H:%M:%SZ")
    for r in doc[0]["rules"]:
        r.update(isPaused=False, lastEvaluation=now)
        if with_absence and r["name"] == ou.RULE:
            r["query"] = r["query"].replace("       vector(0) < 0\n", "       vector(0) < 0\n" + DEV_ABSENCE)
    return doc


def instances(dev_state):
    """The recorded instance list, as AndaraServerUnavailable's, dev's state set (None: dev has no instance)."""
    doc = copy.deepcopy(fixture("alert_instances_json.json"))
    for i in doc:
        i["ruleName"] = i["labels"]["alertname"] = ou.RULE
    dev = [i for i in doc if i["labels"]["environment"] == "dev"][0]
    if dev_state is None:
        doc.remove(dev)
    else:
        dev["state"] = dev_state
    return doc


EMPTY = fixture("metrics_query_vector_empty.json")
NAMES_DEV = fixture("metrics_query_vector.json")   # up{… namespace="andara-dev"}: the expr names dev


class Sequenced(FakeGcx):
    """gcx answering a recorded sequence: each poll is one (expr result, instances) pair, the last repeating."""

    def __init__(self, rules, polls):
        super().__init__(rules=rules)
        self.polls, self.n = polls, 0

    def __call__(self, context, *args, **kw):
        self.calls.append((context,) + args)
        if args[:3] == ("alert", "rules", "list"):
            return self.rules
        step = self.polls[min(self.n, len(self.polls) - 1)]
        if args[:2] == ("metrics", "query"):
            return step[0]
        if args[:3] == ("alert", "instances", "list"):
            self.n += 1
            return step[1]
        raise AssertionError(args)


FIRE_AND_CLEAR = [
    (EMPTY, instances("Normal")),                 # before
    (EMPTY, instances("Normal")),                 # scaled to 0, the series not yet absent
    (NAMES_DEV, instances("Pending")),            # absent: the expr names dev, `for: 2m` running
    (NAMES_DEV, instances("Alerting")),           # fired
    (NAMES_DEV, instances("Alerting")),           # scaled back; still firing
    (EMPTY, instances("Normal")),                 # cleared
]


class Drill(unittest.TestCase):

    def setUp(self):
        self.kubectl = []
        self.clock = itertools.count(0, 10)
        for target, value in ((ou.time, "sleep"), (ou.time, "monotonic")):
            p = mock.patch.object(target, value, (lambda s: None) if value == "sleep" else (lambda: next(self.clock)))
            p.start()
            self.addCleanup(p.stop)
        p = mock.patch.object(ou, "kubectl", lambda *a: self.kubectl.append(a) or "")
        p.start()
        self.addCleanup(p.stop)
        p = mock.patch.object(ou, "restore", lambda ns, d: self.kubectl.append(("restore", ns)))
        p.start()
        self.addCleanup(p.stop)

    def main(self, rules, polls, ruler=None, env=None):
        f = Sequenced(rules, polls)
        out = io.StringIO()
        with mock.patch.object(gcx, "run", f), mock.patch.object(gcx, "write_config", lambda e=None: []), \
                mock.patch.object(rule_backend.ruler_read, "fetch", lambda e: ruler), \
                mock.patch.dict(os.environ, env if env is not None else CREDS, clear=True), \
                mock.patch.object(sys, "argv", ["observe_unavailable.py", "dev"]), contextlib.redirect_stdout(out):
            try:
                ou.main()
                code = 0
            except SystemExit as e:
                code = e.code
        return code, out.getvalue(), f

    def test_a_recorded_fire_and_clear_passes(self):
        code, out, _ = self.main(live_server_rules(), FIRE_AND_CLEAR)
        self.assertEqual(code, 0, out)
        self.assertIn("observe-unavailable: backend: grafana solo7dev", out)
        self.assertIn('filter environment="dev",namespace="andara-dev", poll 10s, ALERT_SETTLE 130s', out)
        for step in ("drill: start", "drill: server-restored", "drill: end"):
            self.assertRegex(out, r"observe-unavailable: %s \d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ" % step)
        self.assertRegex(out, r"record \S+ AndaraServerUnavailable firing \(gcx: Alerting\) expr: \['andara-dev'\]")
        self.assertRegex(out, r"record \S+ AndaraServerUnavailable pending \(gcx: Pending\)")
        self.assertEqual(self.kubectl, [("-n", "andara-dev", "scale", "statefulset", "andara", "--replicas=0"),
                                        ("restore", "andara-dev")])

    def test_never_fires_fails_and_still_restores(self):
        polls = FIRE_AND_CLEAR[:3]                # pending to the deadline
        code, out, _ = self.main(live_server_rules(), polls)
        self.assertEqual(code, 1)
        self.assertIn("FAIL AndaraServerUnavailable never fired for andara-dev", out)
        self.assertEqual(self.kubectl[-1], ("restore", "andara-dev"))

    def test_never_clears_fails(self):
        code, out, _ = self.main(live_server_rules(), FIRE_AND_CLEAR[:5])
        self.assertEqual(code, 1)
        self.assertIn("fired for andara-dev but never cleared", out)

    def test_another_namespace_moving_in_the_expr_fails(self):
        other = copy.deepcopy(NAMES_DEV)
        other["data"]["result"][0]["metric"]["namespace"] = "andara-staging"
        both = copy.deepcopy(NAMES_DEV)
        both["data"]["result"].append(other["data"]["result"][0])
        polls = list(FIRE_AND_CLEAR)
        polls[3] = (both, instances("Alerting"))
        code, out, _ = self.main(live_server_rules(), polls)
        self.assertEqual(code, 1)
        self.assertIn("another environment or namespace moved: andara-staging", out)

    def test_another_environment_instance_moving_fails(self):
        moved = instances("Alerting")
        [i for i in moved if i["labels"]["environment"] == "staging"][0]["state"] = "Alerting"
        polls = list(FIRE_AND_CLEAR)
        polls[4] = (NAMES_DEV, moved)
        code, out, _ = self.main(live_server_rules(), polls)
        self.assertEqual(code, 1)
        self.assertIn("staging/andara-staging pending→firing", out)

    def test_already_firing_before_fails_without_scaling(self):
        code, out, _ = self.main(live_server_rules(), [(EMPTY, instances("Alerting"))])
        self.assertEqual((code, self.kubectl), (1, []))
        self.assertIn("already firing", out)

    def test_legacy_backend_refuses_without_scaling(self):
        code, out, f = self.main(fixture("alert_rules_list_server_paused.json"), FIRE_AND_CLEAR,
                                 ruler=ruler_yaml(ou.RULE))
        self.assertEqual((code, self.kubectl), (1, []))
        self.assertIn("backend: ruler solo7", out)
        self.assertIn("runs after cutover step 4(v)", out)
        self.assertFalse([c for c in f.calls if c[1:3] == ("metrics", "query")])

    def test_live_rule_without_the_dev_absence_operand_refuses_without_scaling(self):
        code, out, _ = self.main(live_server_rules(with_absence=False), FIRE_AND_CLEAR)
        self.assertEqual((code, self.kubectl), (1, []))
        self.assertIn("runs after cutover step 4(v)", out)
        self.assertIn("no dev absence operand", out)

    def test_inconclusive_selection_exits_1_without_scaling(self):
        code, out, _ = self.main(fixture("alert_rules_list_server_paused.json"), FIRE_AND_CLEAR, ruler=None)
        self.assertEqual((code, self.kubectl), (1, []))
        self.assertIn("live in neither backend: grafana solo7dev: paused", out)

    def test_unset_dev_token_exits_3_before_any_gcx_call(self):
        env = {k: v for k, v in CREDS.items() if k != "GRAFANA_SOLO7DEV_READ_TOKEN"}
        code, out, f = self.main(live_server_rules(), FIRE_AND_CLEAR, env=env)
        self.assertEqual((code, f.calls, self.kubectl), (3, [], []))
        self.assertIn("GRAFANA_SOLO7DEV_READ_TOKEN", out)


class Pure(unittest.TestCase):

    def test_dev_absence_operand(self):
        self.assertTrue(ou.has_dev_absence(ou.rule_expr()))      # alerts.yaml, every marked line kept
        self.assertTrue(ou.has_dev_absence(live_server_rules()[0]["rules"][0]["query"]))
        self.assertFalse(ou.has_dev_absence(fixture("alert_rules_list_server_paused.json")[0]["rules"][0]["query"]))
        self.assertFalse(ou.has_dev_absence('absent(up{job="andara-server", environment="prod", namespace="andara-prod"})'))
        self.assertFalse(ou.has_dev_absence(None))

    def test_rule_for(self):
        self.assertEqual(ou.rule_for(), 120)


class Guard(unittest.TestCase):
    """AC-4: anything but dev exits 3 and touches nothing (no kubectl or gcx on PATH to reach)."""

    def test_prod_exits_3(self):
        env = {"PATH": "/usr/bin:/bin", "ANDARA_BOX_ENV": "/nonexistent", "HOME": os.environ.get("HOME", "/")}
        for arg in ("prod", "local", ""):
            p = subprocess.run([sys.executable, os.path.join(REPO, "scripts", "observe_unavailable.py"), arg],
                               capture_output=True, text=True, env=env)
            self.assertEqual(p.returncode, 3, p.stdout + p.stderr)
            self.assertIn("only dev", p.stdout)
        # make reports any failed recipe as its own exit 2; the script's refusal reaches the operator.
        p = subprocess.run(["make", "-s", "-C", REPO, "observe-unavailable", "ENV=prod"], capture_output=True,
                           text=True, env=env)
        self.assertNotEqual(p.returncode, 0)
        self.assertIn("observe-unavailable: ENV=prod: only dev", p.stdout)


if __name__ == "__main__":
    unittest.main()
