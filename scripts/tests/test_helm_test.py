# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""scripts/helm_test.py's rule-coverage check (AW-INF-047): every rule has a promtool case whose
expected alert carries both environment and namespace."""
import os
import sys
import tempfile
import unittest

import yaml

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
sys.path.insert(0, os.path.join(REPO, "scripts"))
import helm_test  # noqa: E402

RULES = {"groups": [{"name": "g", "rules": [{"alert": "A", "expr": "up == 0"}, {"alert": "B", "expr": "up == 0"}]}]}


def case(alert, labels):
    return {"name": alert, "input_series": [], "alert_rule_test": [
        {"eval_time": "1m", "alertname": alert, "exp_alerts": [{"exp_labels": labels}]}]}


class RulesWithoutLabelledCase(unittest.TestCase):

    def check(self, tests):
        d = tempfile.mkdtemp()
        self.addCleanup(__import__("shutil").rmtree, d)
        rules, cases = os.path.join(d, "alerts.yaml"), os.path.join(d, "alerts_test.yaml")
        with open(rules, "w") as f:
            yaml.safe_dump(RULES, f)
        with open(cases, "w") as f:
            yaml.safe_dump({"tests": tests}, f)
        return helm_test.rules_without_labelled_case(rules, cases)

    def test_a_case_with_both_labels_covers_its_rule(self):
        both = {"environment": "dev", "namespace": "andara-dev"}
        self.assertEqual(self.check([case("A", both), case("B", both)]), [])

    def test_namespace_alone_does_not_cover_a_rule(self):
        self.assertEqual(self.check([case("A", {"environment": "dev", "namespace": "andara-dev"}),
                                     case("B", {"namespace": "andara-dev"})]), ["B"])

    def test_environment_alone_does_not_cover_a_rule(self):
        self.assertEqual(self.check([case("A", {"environment": "dev"})]), ["A", "B"])

    def test_a_case_expecting_no_alert_covers_nothing(self):
        silent = {"name": "s", "input_series": [], "alert_rule_test": [
            {"eval_time": "1m", "alertname": "A", "exp_alerts": []}]}
        self.assertEqual(self.check([silent]), ["A", "B"])

    def test_the_committed_file_is_covered(self):
        self.assertEqual(helm_test.rules_without_labelled_case(), [])


def group(name, *evals):
    """One test group: each (eval_time, alertname, fires) is a case expecting the alert or none."""
    return {"name": name, "input_series": [], "alert_rule_test": [
        {"eval_time": t, "alertname": a, "exp_alerts": [{"exp_labels": {}}] if fires else []}
        for t, a, fires in evals]}


class RulesWithoutResolvingCase(unittest.TestCase):
    """AW-INF-009's every-rule line: each rule has a group in which it fires and later clears."""

    def check(self, tests):
        d = tempfile.mkdtemp()
        self.addCleanup(__import__("shutil").rmtree, d)
        rules, cases = os.path.join(d, "alerts.yaml"), os.path.join(d, "alerts_test.yaml")
        with open(rules, "w") as f:
            yaml.safe_dump(RULES, f)
        with open(cases, "w") as f:
            yaml.safe_dump({"tests": tests}, f)
        return helm_test.rules_without_resolving_case(rules, cases)

    def test_fire_then_clear_in_one_group_covers_its_rule(self):
        self.assertEqual(self.check([group("a", ("5m", "A", True), ("10m", "A", False)),
                                     group("b", ("2m", "B", True), ("1h", "B", False))]), [])

    def test_clear_before_fire_does_not_cover(self):
        self.assertEqual(self.check([group("a", ("5m", "A", False), ("10m", "A", True)),
                                     group("b", ("2m", "B", True), ("3m", "B", False))]), ["A"])

    def test_fire_and_clear_in_different_groups_do_not_cover(self):
        self.assertEqual(self.check([group("a1", ("5m", "A", True)), group("a2", ("10m", "A", False)),
                                     group("b", ("2m", "B", True), ("3m", "B", False))]), ["A"])

    def test_another_rules_clear_does_not_cover(self):
        self.assertEqual(self.check([group("a", ("5m", "A", True), ("10m", "B", False)),
                                     group("b", ("2m", "B", True), ("3m", "B", False))]), ["A"])

    def test_durations_compare_as_times_not_strings(self):
        # "10m" sorts before "9m" as text; 90s is 1m30s.
        self.assertEqual(self.check([group("a", ("9m", "A", True), ("10m", "A", False)),
                                     group("b", ("1m30s", "B", True), ("90s", "B", False), ("2m", "B", False))]), [])

    def test_the_committed_file_is_covered(self):
        self.assertEqual(helm_test.rules_without_resolving_case(), [])


if __name__ == "__main__":
    unittest.main()
