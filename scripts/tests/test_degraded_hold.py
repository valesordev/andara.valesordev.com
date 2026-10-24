# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""AW-INF-039 AC-5: WorldReadOnly's for: and ingress.degraded_hold change together."""
import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
import degraded_hold  # noqa: E402

ALERTS = """
groups:
  - name: andara-kafka
    rules:
      - alert: WorldReadOnly
        expr: max by (namespace) (andara_ingress_degraded) == 1
        for: %s
"""
CONFIG = """
const (
	DefaultIngressDegradedHold = %d * time.Second
	MinIngressDegradedHold     = %d * time.Second
)
"""
KEYS = """
keys:
  - {key: ingress.degraded_hold, env: ANDARA_INGRESS_DEGRADED_HOLD, type: duration, default: %s, story: AW-SRV-052}
"""


def check(for_="30s", default=120, minimum=90, keys="120s"):
    return degraded_hold.check(ALERTS % for_, CONFIG % (default, minimum), KEYS % keys)


class Floor(unittest.TestCase):
    def test_the_repo_holds(self):
        self.assertEqual(degraded_hold.check_repo(), [])

    def test_the_contracted_values_hold(self):
        self.assertEqual(check(), [])

    def test_a_default_below_the_floor_fails(self):
        errors = check(default=89, keys="89s")  # the 90 s minimum still clears the 90 s floor
        self.assertEqual(len(errors), 1, errors)
        self.assertIn("DefaultIngressDegradedHold is 89s, below 2 x WorldReadOnly's for: (30s) + 30 = 90s",
                      errors[0])

    def test_raising_for_without_the_hold_fails(self):
        # for: 46 s needs a 122 s hold: the 120 s default and the 90 s minimum both fall short.
        errors = check(for_="46s")
        self.assertEqual([e.split(" is ")[0] for e in errors],
                         ["DefaultIngressDegradedHold", "MinIngressDegradedHold"])

    def test_a_minimum_below_the_floor_fails_alone(self):
        errors = check(for_="45s")  # floor 120: the default clears it, the 90 s minimum doesn't
        self.assertEqual(len(errors), 1)
        self.assertTrue(errors[0].startswith("MinIngressDegradedHold is 90s"))

    def test_keys_yaml_must_agree_with_the_server(self):
        errors = check(keys="150s")
        self.assertEqual(errors, ["keys.yaml's ingress.degraded_hold default is 150s, "
                                  "the server's DefaultIngressDegradedHold 120s"])

    def test_minute_durations(self):
        self.assertEqual(degraded_hold.seconds("2m"), 120)
        self.assertEqual(degraded_hold.seconds("1m30s"), 90)
        with self.assertRaises(ValueError):
            degraded_hold.seconds("30 s")

    def test_a_missing_rule_names_it(self):
        with self.assertRaisesRegex(ValueError, "no rule WorldReadOnly"):
            degraded_hold.check("groups: []", CONFIG % (120, 90), KEYS % "120s")


if __name__ == "__main__":
    unittest.main()
