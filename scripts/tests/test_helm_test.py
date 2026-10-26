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


if __name__ == "__main__":
    unittest.main()
