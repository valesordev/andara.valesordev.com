# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""scripts/topics.py picks its broker by environment (AW-INF-014 AC-3)."""
import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
import topics  # noqa: E402


class RpkPrefix(unittest.TestCase):
    def test_local_prefers_the_compose_broker(self):
        p = topics.rpk_prefix("local", compose_redpanda_running=True, host_rpk=True)
        self.assertEqual(p[:2], ["docker", "compose"])
        self.assertEqual(p[-2:], ["redpanda", "rpk"])

    def test_local_falls_back_to_host_rpk(self):
        self.assertEqual(topics.rpk_prefix("local", False, True), ["rpk"])

    def test_local_with_neither_has_no_broker(self):
        self.assertIsNone(topics.rpk_prefix("local", False, False))

    def test_cluster_env_never_reaches_compose(self):
        # The defect this story fixes: with `make up` running, dev went to the laptop.
        for env in ("dev", "prod"):
            with self.subTest(env=env):
                p = topics.rpk_prefix(env, compose_redpanda_running=True, host_rpk=True)
                self.assertNotIn("docker", p)
                self.assertEqual(p[:3], ["kubectl", "-n", "andara-" + env])
                self.assertIn(topics.CLUSTER_TOOLBOX, p)
                self.assertEqual(p[-3:], ["rpk", "-X", "brokers=" + topics.CLUSTER_BOOTSTRAP])


if __name__ == "__main__":
    unittest.main()
